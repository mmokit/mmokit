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
	"time"

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
	controlClient *meshGatewayClient
	hostNetwork   *HostNetwork // gRPC server + peer streams for MeshData (standalone only)

	// wsAddr is the WebSocket listen address advertised to the coordinator.
	// Set by standalone gateway mode; empty in embedded mode.
	wsAddr string

	// playerRouter resolves the destination cellID for a newly authenticated
	// player. Required in standalone mode. In embedded mode, topology.cellForPlayer
	// uses the coordinator reference instead.
	playerRouter PlayerRouter

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
// For disconnect events it delegates to handleDisconnect.
func (g *Gateway) handleEvent(evt net.PlayerEvent) {
	if evt.Connected {
		g.loginSvc.addPending(evt.ConnID)
		if g.coord != nil {
			g.coord.sendServerConfig(evt.ConnID)
		}
		g.log.Log(CatNetConn, "gateway: conn %d pending login", evt.ConnID)
		return
	}
	g.handleDisconnect(evt)
}

// handleDisconnect owns the full disconnect cleanup for a connection:
//   1. If the connection was still in pending login, remove it from the login queue.
//   2. If authenticated, remove the session record and clean up sessionRoutes.
//   3. Forward the disconnect event to the owning cell's Events channel so the
//      engine's grace-period state machine fires unchanged.
//
// In embedded (in-process) mode the cell is looked up directly. In standalone
// mode (T9) a ClientDisconnect MeshFrame would be sent to the remote node;
// that path is stubbed here pending T9 wiring.
func (g *Gateway) handleDisconnect(evt net.PlayerEvent) {
	connID := evt.ConnID

	g.mu.Lock()
	sess, found := g.sessions[connID]
	if found {
		delete(g.sessions, connID)
	}
	g.mu.Unlock()

	if !found {
		// Connection was still in the login pipeline — remove from pending queue.
		g.loginSvc.removePending(connID)
		g.log.Log(CatNetConn, "gateway: conn %d disconnected before login complete", connID)
		return
	}

	g.log.Log(CatNetConn, "gateway: conn %d user=%s disconnected from cell=%s host=%s",
		connID, sess.username, sess.cellID, sess.hostID)

	// Always clean up the coordinator's session route table.
	if g.coord != nil {
		g.coord.sessionRoutes.Remove(SessionKey{GatewayID: g.id, ConnID: connID})
		g.coord.removePlayerNode(connID)
	}

	if g.isLocalShortcut(sess.hostID) {
		// In-process path: push the disconnect event directly to the owning cell.
		if g.coord != nil {
			if cell, ok := g.coord.getCell(sess.cellID); ok {
				select {
				case cell.Events <- evt:
				default:
					g.log.Log(CatNetConn, "gateway: cell %s events channel full, dropping disconnect conn=%d", sess.cellID, connID)
				}
			}
		}
	} else {
		// Standalone mode: send ClientDisconnect to the remote node via MeshData.
		if g.hostNetwork != nil {
			frame := &meshpb.MeshFrame{
				Msg: &meshpb.MeshFrame_ClientDisconnect{
					ClientDisconnect: &meshpb.ClientDisconnect{
						GatewayId: g.id,
						ConnId:    connID,
						Reason:    "client disconnected",
					},
				},
			}
			if err := g.hostNetwork.SendReliable(sess.hostID, frame); err != nil {
				g.log.Log(CatNetConn, "gateway: ClientDisconnect to host=%s conn=%d failed: %v", sess.hostID, connID, err)
			}
		} else {
			g.log.Log(CatNetConn, "gateway: conn %d cross-process disconnect to host=%s — no hostNetwork", connID, sess.hostID)
		}

		// Notify the coordinator to remove the session from sessionRoutes.
		// A SessionAnnounce with empty target_host_id is the tombstone convention:
		// the coordinator removes the route rather than setting it.
		if g.controlClient != nil {
			msg := &meshpb.HostMessage{
				Msg: &meshpb.HostMessage_SessionAnnounce{
					SessionAnnounce: &meshpb.SessionAnnounce{
						GatewayId:    g.id,
						ConnId:       connID,
						Username:     sess.username,
						TargetHostId: "", // empty = removal tombstone
						TargetCellId: "",
					},
				},
			}
			if err := g.controlClient.send(msg); err != nil {
				g.log.Log(CatNetConn, "gateway: session removal announce conn=%d failed: %v", connID, err)
			}
		}
	}
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
func (g *Gateway) processLogin(connID uint32, username string, data any) error {
	var cellID string
	if g.coord != nil {
		cellID = g.topology.cellForPlayer(username, g.coord)
	} else if g.playerRouter != nil {
		cellID = g.playerRouter(username)
	}
	if cellID == "" {
		return fmt.Errorf("no cell resolved for user %s", username)
	}
	hostID := g.topology.HostForCell(cellID)
	if hostID == "" || hostID == "local" {
		if g.coord == nil {
			return fmt.Errorf("no host for cell %s (topology not yet populated)", cellID)
		}
		// Embedded mode "local" sentinel is acceptable — isLocalShortcut handles it.
		hostID = "local"
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
// In embedded mode this is a direct write. In standalone mode it emits a
// SessionAnnounce message over the MeshControl stream.
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
	// Standalone mode: emit SessionAnnounce via the gateway control client.
	if g.controlClient == nil {
		g.log.Log(CatNetConn, "gateway: announceSession — no controlClient in standalone mode")
		return
	}
	msg := &meshpb.HostMessage{
		Msg: &meshpb.HostMessage_SessionAnnounce{
			SessionAnnounce: &meshpb.SessionAnnounce{
				GatewayId:    g.id,
				ConnId:       sess.connID,
				Username:     sess.username,
				TargetHostId: sess.hostID,
				TargetCellId: sess.cellID,
			},
		},
	}
	if err := g.controlClient.send(msg); err != nil {
		g.log.Log(CatNetConn, "gateway: SessionAnnounce send failed conn=%d user=%s: %v", sess.connID, sess.username, err)
	}
}

// dispatchPlayerAssignment sends a PlayerAssignment to the target cell.
// In embedded mode it checks for reconnect state first (mirrors routeAuthenticatedPlayer)
// then writes directly to the cell's Inbox. In standalone mode it forwards via MeshData.
func (g *Gateway) dispatchPlayerAssignment(sess *localSession, data any) error {
	if g.coord == nil {
		return g.dispatchPlayerAssignmentRemote(sess, data)
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
//
// The always-proxy override: when Config.GatewayMode == "always-proxy" this
// function always returns false, forcing the MeshData codec path even for
// colocated destinations. Used by integration tests that need to exercise the
// full wire format end-to-end.
func (g *Gateway) isLocalShortcut(hostID string) bool {
	if g.coord != nil && g.coord.cfg.GatewayMode == "always-proxy" {
		// Force the MeshData codec path even when the destination is colocated.
		// Used for integration tests that need to exercise the wire format.
		return false
	}
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

// OnUpstreamSwitch updates the session's authoritative host and epoch after a
// cross-host entity handoff. Called by the coordinator (embedded mode) or by
// the standalone gateway's meshControlClient dispatch (T9) when a
// CoordMessage.UpstreamSwitch arrives.
func (g *Gateway) OnUpstreamSwitch(connID uint32, newHost string, newEpoch uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	sess, ok := g.sessions[connID]
	if !ok {
		g.log.Log(CatNetConn, "gateway: UpstreamSwitch for unknown conn %d", connID)
		return
	}
	sess.hostID = newHost
	sess.epoch = newEpoch
	g.log.Log(CatNetConn, "gateway: upstream switched conn=%d -> host=%s epoch=%d", connID, newHost, newEpoch)
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

// dispatchPlayerAssignmentRemote forwards a PlayerAssignment to the target node
// via MeshData. Used in standalone gateway mode where there is no direct cell Inbox.
func (g *Gateway) dispatchPlayerAssignmentRemote(sess *localSession, data any) error {
	if g.hostNetwork == nil {
		return fmt.Errorf("gateway: standalone dispatchPlayerAssignment — no hostNetwork")
	}

	var dataBytes []byte
	if b, ok := data.([]byte); ok {
		dataBytes = b
	}

	frame := &meshpb.MeshFrame{
		DestCellId: sess.cellID,
		Msg: &meshpb.MeshFrame_PlayerAssignment{
			PlayerAssignment: &meshpb.PlayerAssignment{
				ConnId:    sess.connID,
				GatewayId: g.id,
				Username:  sess.username,
				ToCellId:  sess.cellID,
				Data:      dataBytes,
			},
		},
	}
	if err := g.hostNetwork.SendReliable(sess.hostID, frame); err != nil {
		// Clean up the session we just added.
		g.mu.Lock()
		delete(g.sessions, sess.connID)
		g.mu.Unlock()
		return fmt.Errorf("gateway: PlayerAssignment to host=%s cell=%s: %w", sess.hostID, sess.cellID, err)
	}
	g.log.Log(CatNetConn, "gateway: conn=%d user=%s -> host=%s cell=%s (MeshData)", sess.connID, sess.username, sess.hostID, sess.cellID)

	// Start the per-session pump goroutine to forward input from this client.
	go g.runSessionPump(sess.connID)
	return nil
}

// runSessionPump is the per-session goroutine for standalone gateway mode.
// It polls ConnManager for pending input from the client and forwards each
// drained byte slice to the authoritative node via a MeshData ClientInput frame.
//
// The pump exits when:
//   - The session is removed from g.sessions (client disconnected or transferred).
//   - g.hostNetwork is nil or closed.
//
// In embedded mode isLocalShortcut returns true and the pump is never started.
// 1ms poll is acceptable for now; channel-driven is a future optimisation.
//
// NOTE (T11): In embedded always-proxy mode (Config.GatewayMode == "always-proxy")
// isLocalShortcut returns false, so the pump IS started for colocated sessions.
// The pump requires g.hostNetwork to forward ClientInput frames; however
// hostNetwork is currently only constructed in standalone (--mode=gateway) mode
// (T9). T11 must arrange hostNetwork construction for embedded always-proxy mode
// before the integration test fixture will work end-to-end.
func (g *Gateway) runSessionPump(connID uint32) {
	ticker := time.NewTicker(1 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		sess := g.lookupSession(connID)
		if sess == nil {
			return // session removed
		}
		if g.hostNetwork == nil {
			return
		}

		msgs := g.connMgr.DrainInput(connID)
		for _, raw := range msgs {
			frame := &meshpb.MeshFrame{
				Msg: &meshpb.MeshFrame_ClientInput{
					ClientInput: &meshpb.ClientInput{
						GatewayId: g.id,
						ConnId:    connID,
						Data:      raw,
					},
				},
			}
			if err := g.hostNetwork.SendReliable(sess.hostID, frame); err != nil {
				g.log.Log(CatNetConn, "gateway: ClientInput forward conn=%d host=%s: %v", connID, sess.hostID, err)
			}
		}

		opMsgs := g.connMgr.DrainOpInput(connID)
		for _, raw := range opMsgs {
			frame := &meshpb.MeshFrame{
				Msg: &meshpb.MeshFrame_ClientInput{
					ClientInput: &meshpb.ClientInput{
						GatewayId: g.id,
						ConnId:    connID,
						Data:      raw,
					},
				},
			}
			if err := g.hostNetwork.SendReliable(sess.hostID, frame); err != nil {
				g.log.Log(CatNetConn, "gateway: ClientInput (op) forward conn=%d host=%s: %v", connID, sess.hostID, err)
			}
		}
	}
}
