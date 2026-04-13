// Package universe — gateway.go
//
// Gateway is the worker type responsible for terminating WebSocket connections,
// running LoginHandler inline, and (in T6+) proxying client I/O to authoritative
// nodes via MeshData streams. It owns the login service, the set of active
// localSession records, and the cell→host topology snapshot needed for routing.
//
// # Embedded vs standalone modes
//
// In T5 the Gateway only runs in embedded mode: the Coordinator constructs it in
// Build() and shares its own ConnManager and coord reference. In this mode:
//   - cachedTopology reads live from coord.cellToHostMap (no independent snapshot).
//   - announceSession writes directly to coord.sessionRoutes (no MeshData round-trip).
//   - dispatchPlayerAssignment sends to the target cell's Inbox channel directly.
//   - isLocalShortcut always returns true (every session is local).
//
// Standalone gateway mode (T9) will populate topology from PeerList broadcasts,
// announce sessions via SessionAnnounce MeshControl messages, and forward
// player assignments over MeshData streams.
//
// # Deferred work
//
// T6: per-session drain goroutine + VirtualConnManager + MeshData forward path.
// T9: standalone binary, PeerList-driven cachedTopology.applyPeerList, SessionAnnounce.

package universe

import (
	"fmt"
	"sync"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// Gateway terminates WebSocket connections, runs login, and routes authenticated
// players to the correct cell. In embedded mode it runs inside the Coordinator
// process and dispatches directly to cell inboxes. In standalone mode (T9) it
// is a separate process that forwards traffic via MeshData streams.
type Gateway struct {
	id      string
	connMgr *net.ConnManager // real WebSocket server (concrete)
	loginSvc  *loginService
	log       *logger.Logger

	mu       sync.RWMutex
	sessions map[uint32]*localSession

	topology *cachedTopology // cellID → hostID

	// Only non-nil when running standalone (T9):
	controlClient *meshControlClient

	// Embedded mode: coordinator reference for direct access to sessionRoutes
	// and cell Inbox. nil when standalone.
	coord *Coordinator
}

// localSession records the gateway-side routing state for one authenticated player.
type localSession struct {
	connID   uint32
	username string
	hostID   string // current authoritative host; "local" sentinel in single-host mode
	cellID   string
	epoch    uint64
}

// handleEvent handles a net.PlayerEvent (connect or disconnect) from ConnManager.Events().
// For connect events it registers the connection as pending in the loginService.
// Disconnect events are intentionally not handled here — the Coordinator's routeEvents
// goroutine handles disconnects by forwarding the event to the owning cell. T8 will
// move disconnect handling into the Gateway.
func (g *Gateway) handleEvent(evt net.PlayerEvent) {
	if evt.Connected {
		g.loginSvc.addPending(evt.ConnID)
		g.coord.sendServerConfig(evt.ConnID)
		g.log.Log(CatNetConn, "gateway: conn %d pending login", evt.ConnID)
	}
	// Disconnect events: fall through — handled by Coordinator.routeEvents.
}

// processLogins processes all pending login attempts. Called on the routeEvents
// goroutine at game-tick rate. On success, routes the authenticated player.
func (g *Gateway) processLogins() {
	results, timedOut := g.loginSvc.processLogins(g.connMgr)

	for _, connID := range timedOut {
		g.log.Log(CatNetConn, "gateway: login timeout conn=%d", connID)
		g.connMgr.Remove(connID)
	}

	for _, r := range results {
		if err := g.processLogin(r.connID, r.username, r.data); err != nil {
			g.log.Log(CatNetConn, "gateway: login error conn=%d: %v", r.connID, err)
			g.connMgr.Remove(r.connID)
		}
	}
}

// processLogin routes a successfully authenticated player to the correct cell.
// In embedded mode this mirrors coordinator.routeAuthenticatedPlayer.
//
// TODO(T6): start per-session pump goroutine for non-local hosts.
func (g *Gateway) processLogin(connID uint32, username string, data any) error {
	cellID := g.topology.cellForPlayer(username, g.coord)
	hostID := g.topology.HostForCell(cellID)
	if hostID == "" {
		return fmt.Errorf("no host for cell %s", cellID)
	}

	sess := &localSession{
		connID:   connID,
		username: username,
		hostID:   hostID,
		cellID:   cellID,
		epoch:    1,
	}
	g.mu.Lock()
	g.sessions[connID] = sess
	g.mu.Unlock()

	// Announce the session to the coordinator's routing table (embedded: direct write).
	g.announceSession(sess)

	// Forward the PlayerAssignment to the target cell (embedded: direct Inbox write).
	return g.dispatchPlayerAssignment(sess, data)
}

// announceSession registers the session in coord.sessionRoutes.
// In embedded mode this is a direct write. In standalone mode (T9) it will
// emit a SessionAnnounce message over the MeshControl stream.
func (g *Gateway) announceSession(sess *localSession) {
	if g.coord != nil {
		// Embedded mode: write directly into the coordinator's routing table.
		g.coord.sessionRoutes.Set(&SessionRoute{
			Key:      SessionKey{GatewayID: g.id, ConnID: sess.connID},
			Username: sess.username,
			HostID:   sess.hostID,
			CellID:   sess.cellID,
			Epoch:    sess.epoch,
		})
		return
	}
	// TODO(T9): emit SessionAnnounce via controlClient.
	g.log.Log(CatNetConn, "gateway: announceSession no-op in standalone mode (T9)")
}

// dispatchPlayerAssignment sends a PlayerAssignment to the target cell.
// In embedded mode it checks for reconnect state first (mirrors routeAuthenticatedPlayer)
// then writes directly to the cell's Inbox. In standalone mode (T9) it will
// forward via MeshData.
func (g *Gateway) dispatchPlayerAssignment(sess *localSession, data any) error {
	if g.coord == nil {
		// TODO(T9): forward via MeshData.
		return fmt.Errorf("gateway: dispatchPlayerAssignment not implemented in standalone mode")
	}

	// Embedded mode: mirror coordinator.routeAuthenticatedPlayer logic.

	// 1. Check for reconnection (lingering disconnected session).
	var reconnectNodeID, existingNodeID string
	g.coord.mu.RLock()
	if loc := g.coord.players[sess.username]; loc != nil {
		if loc.Active {
			existingNodeID = loc.NodeID
		} else {
			reconnectNodeID = loc.NodeID
		}
	}
	g.coord.mu.RUnlock()

	if existingNodeID != "" {
		// Duplicate username — reject.
		g.log.Log(CatNetConn, "gateway: duplicate username %q conn=%d (active on %s)", sess.username, sess.connID, existingNodeID)
		if g.loginSvc.onRejected != nil {
			g.loginSvc.onRejected(sess.connID, "Username already connected")
		}
		// Clean up the session we just added.
		g.mu.Lock()
		delete(g.sessions, sess.connID)
		g.mu.Unlock()
		g.coord.sessionRoutes.Remove(SessionKey{GatewayID: g.id, ConnID: sess.connID})
		return fmt.Errorf("duplicate username %q", sess.username)
	}

	if reconnectNodeID != "" {
		if node, ok := g.coord.getCell(reconnectNodeID); ok {
			// Update session route to the reconnect cell.
			g.coord.sessionRoutes.Set(&SessionRoute{
				Key:      SessionKey{GatewayID: g.id, ConnID: sess.connID},
				Username: sess.username,
				HostID:   sess.hostID,
				CellID:   reconnectNodeID,
				Epoch:    sess.epoch,
			})
			node.Inbox <- CellMessage{
				Type: MsgPlayerAssignment,
				Assignment: &PlayerAssignment{
					ConnID:      sess.connID,
					Username:    sess.username,
					IsReconnect: true,
				},
			}
			g.log.Log(CatNetConn, "gateway: reconnect conn=%d user=%s -> %s", sess.connID, sess.username, reconnectNodeID)
			return nil
		}
		// Node gone (e.g., merged) — fall through to fresh login.
	}

	// 2. Route via playerRouter result (already resolved in processLogin via topology).
	targetNodeID := sess.cellID

	// Validate the target cell still exists.
	node, ok := g.coord.getCell(targetNodeID)
	if !ok {
		g.log.Log(CatNetConn, "gateway: no node %s for conn=%d user=%s", targetNodeID, sess.connID, sess.username)
		// Clean up.
		g.mu.Lock()
		delete(g.sessions, sess.connID)
		g.mu.Unlock()
		g.coord.sessionRoutes.Remove(SessionKey{GatewayID: g.id, ConnID: sess.connID})
		return fmt.Errorf("no node %s for user %s", targetNodeID, sess.username)
	}

	node.Inbox <- CellMessage{
		Type: MsgPlayerAssignment,
		Assignment: &PlayerAssignment{
			ConnID:   sess.connID,
			Username: sess.username,
			Data:     data,
		},
	}
	g.log.Log(CatNetConn, "gateway: conn=%d user=%s -> %s", sess.connID, sess.username, targetNodeID)
	return nil
}

// isLocalShortcut returns true when the gateway can dispatch directly to a cell
// Inbox without going through MeshData. In embedded mode, every host that appears
// in coord.Hosts is colocated, so all sessions qualify.
func (g *Gateway) isLocalShortcut(hostID string) bool {
	if g.coord == nil {
		return false // standalone mode: never local
	}
	// Empty string or "local" sentinel → always local (single-host all-in-one).
	if hostID == "" || hostID == "local" {
		return true
	}
	// Multi-host all-in-one: the host is local if it appears in coord.Hosts.
	g.coord.mu.RLock()
	_, ok := g.coord.Hosts[hostID]
	g.coord.mu.RUnlock()
	return ok
}

// lookupSession returns the localSession for the given connID, or nil if absent.
func (g *Gateway) lookupSession(connID uint32) *localSession {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.sessions[connID]
}

// removeSession removes the session record for the given connID.
func (g *Gateway) removeSession(connID uint32) {
	g.mu.Lock()
	delete(g.sessions, connID)
	g.mu.Unlock()
}

// cachedTopology is the gateway's snapshot of cell → host ownership.
//
// In embedded mode (coord != nil) HostForCell reads live from
// coord.cellToHostMap under coord.mu.RLock. In standalone mode (T9) the
// internal cells map is populated by applyPeerList from PeerList broadcasts.
//
// The "local" sentinel is returned when no host mapping exists (e.g.
// single-host all-in-one where cellToHostMap is empty). Gateway.isLocalShortcut
// treats "local" as an always-local host so dispatch still reaches the cell Inbox.
type cachedTopology struct {
	// Embedded-mode reference. When non-nil, HostForCell reads live from
	// coord.cellToHostMap. When nil (standalone T9), the cells map below is used.
	coord *Coordinator

	mu    sync.RWMutex
	cells map[string]string // cellID -> hostID (standalone mode only)
}

func newCachedTopology(coord *Coordinator) *cachedTopology {
	return &cachedTopology{coord: coord}
}

// HostForCell returns the hostID that owns cellID.
// Returns the "local" sentinel when no mapping exists (single-host mode).
//
// Note: returning "local" has different meanings per mode — in embedded mode it
// means single-host all-in-one (always safe); in standalone mode (T9) it means
// the PeerList has not yet arrived (empty snapshot). The inconsistency is harmless
// because isLocalShortcut returns false when g.coord == nil, so a standalone
// gateway will never treat "local" as an in-process shortcut.
func (t *cachedTopology) HostForCell(cellID string) string {
	if t.coord != nil {
		// Embedded mode: read live from coordinator state.
		t.coord.mu.RLock()
		hostID := t.coord.cellToHostMap[cellID]
		t.coord.mu.RUnlock()
		if hostID == "" {
			return "local" // single-host all-in-one sentinel
		}
		return hostID
	}
	// Standalone mode: read from internal snapshot.
	t.mu.RLock()
	hostID := t.cells[cellID]
	t.mu.RUnlock()
	if hostID == "" {
		return "local"
	}
	return hostID
}

// cellForPlayer resolves which cell should host the player, using the
// coordinator's playerRouter (embedded mode only). This mirrors the
// PlayerRouter call in coordinator.routeAuthenticatedPlayer.
// Returns "" if no cell can be found.
func (t *cachedTopology) cellForPlayer(username string, coord *Coordinator) string {
	if coord == nil {
		return ""
	}
	var cellID string
	if coord.playerRouter != nil {
		cellID = coord.playerRouter(username)
	}
	if cellID == "" {
		// Fallback: pick any cell.
		coord.mu.RLock()
		for id := range coord.Cells {
			cellID = id
			break
		}
		coord.mu.RUnlock()
	}
	return cellID
}

// applyPeerList updates the topology snapshot from a PeerList broadcast.
// Called by the standalone gateway (T9) when the coordinator pushes ownership changes.
func (t *cachedTopology) applyPeerList(cells []*meshpb.CellOwnership) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cells == nil {
		t.cells = make(map[string]string, len(cells))
	}
	for _, co := range cells {
		t.cells[co.CellId] = co.HostId
	}
}
