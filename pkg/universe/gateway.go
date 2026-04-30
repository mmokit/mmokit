// Package universe — gateway.go
//
// Gateway is the worker type responsible for terminating WebSocket
// connections, running LoginHandler inline, and proxying client I/O to
// authoritative nodes via MeshData streams. It owns the login service,
// the set of active localSession records, and the cell→host topology
// snapshot needed for routing.
//
// # Embedded vs standalone modes
//
// Embedded — the Process constructs the Gateway in Build() and
// shares its own ConnManager and coord reference. cachedTopology reads
// live from coord state, announceSession writes directly to
// coord.sessionRoutes, and dispatchPlayerAssignment delivers to the
// target cell's Inbox channel. isLocalShortcut returns true for every
// session whose host appears in coord.Hosts (classic `all` preset).
//
// Standalone — `--mode=gateway` runs in its own process. cachedTopology
// is populated via PeerList broadcasts on the MeshControl stream,
// sessions are announced via SessionAnnounce, and both PlayerAssignment
// and ClientInput forwarding ride MeshData streams to the authoritative
// node via HostNetwork. The gateway has its own hostNetwork gRPC server
// so nodes can push ClientFrame responses back.

package universe

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// Gateway terminates WebSocket connections, runs login, and routes authenticated
// players to the correct cell. In embedded mode it runs inside the Process
// process and dispatches directly to cell inboxes. In standalone mode it runs
// as a separate process and forwards traffic via MeshData streams.
type Gateway struct {
	id      string
	connMgr *net.ConnManager // real WebSocket server (concrete)
	loginSvc  *loginService
	log       *logger.Logger

	mu       sync.RWMutex
	sessions map[uint32]*localSession

	topology *cachedTopology // cellID → hostID

	// Only non-nil when running standalone (standalone):
	controlClient *meshGatewayClient
	hostNetwork   *HostNetwork // gRPC server + peer streams for MeshData (standalone only)

	// wsAddr is the WebSocket listen address advertised to the coordinator.
	// Set by standalone gateway mode; empty in embedded mode.
	wsAddr string

	// defaultSpawn is the fallback spawn position when no resolver is registered
	// or the resolver returns ok=false. Copied from Config.DefaultSpawn at
	// Gateway construction time (standalone mode) or read via coord.cfg (embedded).
	defaultSpawn coords.Location

	// spawnOrch tracks in-flight ResolveSpawn RPC requests (standalone mode only).
	spawnOrch *spawnOrchestrator

	// Embedded mode: coordinator reference for direct access to sessionRoutes
	// and cell Inbox. nil when standalone.
	coord *Process

	// tickRate is copied from Config.TickRate at construction time and sent
	// to every newly connected client via SE_SERVER_CONFIG so the client
	// knows the server tick cadence (used for interpolation math). Must be
	// set in BOTH embedded and standalone modes — the client relies on it
	// regardless of where the gateway lives.
	tickRate uint32

	// Gateway-plane state. Populated during Build() from Process's
	// corresponding fields. Phase 2 migration makes these authoritative;
	// Phase 6 drops Process's copies.
	// loginSvc is reused from the existing field above — not duplicated here.
	// httpServer is nil at mirror time — startHTTPListener() runs in Start()
	// after Build(). Phase 2 must re-mirror post-Start() or relocate the
	// httpServer lifecycle onto Gateway directly.
	spawnResolver SpawnResolver
	sessionRoutes *sessionRoutes
	httpServer    *http.Server
}

// localSession records the gateway-side routing state for one authenticated player.
type localSession struct {
	connID   uint32
	username string
	hostID   string // current authoritative host; "local" sentinel in single-host mode
	cellID   string
	epoch    uint64
	spawnLoc coords.Location // resolved at login; forwarded in PlayerAssignment
}

// handleEvent handles a net.PlayerEvent (connect or disconnect) from ConnManager.Events().
// For connect events it registers the connection as pending in the loginService.
// For disconnect events it delegates to handleDisconnect.
func (g *Gateway) handleEvent(evt net.PlayerEvent) {
	if evt.Connected {
		g.loginSvc.addPending(evt.ConnID)
		// Send SE_SERVER_CONFIG immediately so the client has the tick
		// rate before any world update arrives. The client divides
		// elapsed frame time by state.tickMs for interpolation, so a
		// missing config silently produces interp = Infinity (clamped
		// to 2.0, forcing extrapolation) or NaN at the exact tick-
		// arrival instant — causing entities to snap in 50ms steps
		// instead of interpolating smoothly, and to flicker to
		// (NaN, NaN) for one frame on every tick boundary.
		g.sendServerConfig(evt.ConnID)
		g.log.Log(CatNetConn, "gateway: conn %d pending login", evt.ConnID)
		return
	}
	g.handleDisconnect(evt)
}

// sendServerConfig writes SE_SERVER_CONFIG with the cached tick rate to
// the given connection via the local ConnManager. Works identically in
// embedded and standalone modes because g.connMgr is always the process's
// real WebSocket-serving ConnManager. Falls back silently when tickRate
// is zero (tests / misconfigured setup).
func (g *Gateway) sendServerConfig(connID uint32) {
	if g.tickRate == 0 {
		return
	}
	msg := &enginepb.ServerConfigMsg{TickRate: g.tickRate}
	inner, err := proto.Marshal(msg)
	if err != nil {
		return
	}
	evt := &enginepb.ServerEvent{
		Code: uint32(enginepb.ServerEventCode_SE_SERVER_CONFIG),
		Data: inner,
	}
	evtData, err := proto.Marshal(evt)
	if err != nil {
		return
	}
	frame := make([]byte, 1+len(evtData))
	frame[0] = 0x00 // event channel
	copy(frame[1:], evtData)
	g.connMgr.SendReliable(connID, frame)
}

// handleDisconnect owns the full disconnect cleanup for a connection:
//   1. If the connection was still in pending login, remove it from the login queue.
//   2. If authenticated, remove the session record and clean up sessionRoutes.
//   3. Forward the disconnect event to the owning cell's Events channel so the
//      engine's grace-period state machine fires unchanged.
//
// In embedded mode the owning cell is looked up directly; in standalone mode
// a ClientDisconnect MeshFrame is sent to the remote node via hostNetwork
// and the coordinator is notified via a tombstone SessionAnnounce so
// sessionRoutes drops the entry.
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
		if g.coord.opsSessions != nil {
			g.coord.opsSessions.Remove(connID)
		}
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
	loc := g.resolveSpawn(context.Background(), username)

	cellID := g.topology.cellAtPosition(loc.X, loc.Y)
	if cellID == "" {
		return fmt.Errorf("spawn point outside world bounds for user %s: loc=(%f,%f)",
			username, loc.X, loc.Y)
	}
	hostID := g.topology.HostForCell(cellID)
	if hostID == "" || hostID == "local" {
		if g.coord == nil {
			return fmt.Errorf("no host for cell %s (topology not yet populated)", cellID)
		}
		hostID = "local"
	}

	sess := &localSession{
		connID:   connID,
		username: username,
		hostID:   hostID,
		cellID:   cellID,
		epoch:    1,
		spawnLoc: loc,
	}
	g.mu.Lock()
	g.sessions[connID] = sess
	g.mu.Unlock()

	// Service framework: populate the process-level connID→username map
	// so the OpRouter can dispatch service handlers with a non-empty
	// username on opCtx. No-op when the engine didn't auto-create the
	// sessions handle (game supplied its own OpRouter with its own
	// PlayerSessions).
	if g.coord != nil && g.coord.opsSessions != nil {
		g.coord.opsSessions.Set(connID, username)
	}

	g.announceSession(sess)
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

	// 1. Check for reconnection (lingering disconnected session). The cell
	// ID — not the host ID — is what the cell-routing path needs: getCell()
	// looks up *Cell by cellID, and a host can own many cells. Using HostID
	// here would silently fail every reconnect (g.coord.getCell("local")
	// returns false because "local" isn't a cell name) and quietly drop
	// the player into the fresh-login path, spawning a duplicate entity
	// alongside the lingering disconnected one for the entire grace period.
	var reconnectCellID, existingHostID string
	g.coord.mu.RLock()
	if loc := g.coord.players[sess.username]; loc != nil {
		if loc.Active {
			existingHostID = loc.HostID
		} else {
			reconnectCellID = loc.CellID
		}
	}
	g.coord.mu.RUnlock()

	if existingHostID != "" {
		// Duplicate username — reject.
		g.log.Log(CatNetConn, "gateway: duplicate username %q conn=%d (active on host %s)", sess.username, sess.connID, existingHostID)
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

	if reconnectCellID != "" {
		if node, ok := g.coord.getCell(reconnectCellID); ok {
			// Update session route to the reconnect cell.
			g.coord.sessionRoutes.Set(&SessionRoute{
				Key:      SessionKey{GatewayID: g.id, ConnID: sess.connID},
				Username: sess.username,
				HostID:   sess.hostID,
				CellID:   reconnectCellID,
				Epoch:    sess.epoch,
			})
			node.Inbox <- CellMessage{
				Type: MsgPlayerAssignment,
				Assignment: &PlayerAssignment{
					ConnID:        sess.connID,
					Username:      sess.username,
					IsReconnect:   true,
					SpawnLocation: sess.spawnLoc,
				},
			}
			g.log.Log(CatNetConn, "gateway: reconnect conn=%d user=%s -> %s", sess.connID, sess.username, reconnectCellID)
			return nil
		}
		// Cell gone (e.g., merged) — fall through to fresh login.
	}

	// 2. Route via playerRouter result (already resolved in processLogin via topology).
	targetNodeID := sess.cellID

	// Remote host path: the target cell lives on a remote `--mode=host`
	// process, not in this one. Happens in coord+gateway-without-host mode.
	// Delegate to the MeshData dispatch path — identical wire format to
	// standalone gateway mode, just with coord != nil.
	if !g.isLocalShortcut(sess.hostID) {
		return g.dispatchPlayerAssignmentRemote(sess, data)
	}

	// Validate the target cell still exists locally.
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
			ConnID:        sess.connID,
			Username:      sess.username,
			Data:          data,
			SpawnLocation: sess.spawnLoc,
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
	// Empty string or "local" sentinel → always local (single-host `all` preset).
	if hostID == "" || hostID == "local" {
		return true
	}
	// Multi-host `all` preset: the host is local if it appears in coord.Hosts.
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

// OnUpstreamSwitch updates the session's authoritative host, cell, and epoch
// after a cross-host entity handoff or cell-structure change (split/merge).
// Called by the coordinator (embedded mode) or by the standalone gateway's
// meshControlClient dispatch (standalone) when a CoordMessage.UpstreamSwitch
// arrives. newCell may be empty when only the host changes (migrate keeps
// the same cellID).
func (g *Gateway) OnUpstreamSwitch(connID uint32, newHost, newCell string, newEpoch uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	sess, ok := g.sessions[connID]
	if !ok {
		g.log.Log(CatNetConn, "gateway: UpstreamSwitch for unknown conn %d", connID)
		return
	}
	sess.hostID = newHost
	if newCell != "" {
		sess.cellID = newCell
	}
	sess.epoch = newEpoch
	g.log.Log(CatNetConn, "gateway: upstream switched conn=%d -> host=%s cell=%s epoch=%d", connID, newHost, sess.cellID, newEpoch)
}

// reconcileRemotePeers opens MeshData streams to every host listed in pl
// and tears down streams to hosts no longer present. Mirror of the
// applyPeerList peer-reconcile loop in mesh_gateway_client.go, but callable
// directly from the embedded-gateway path where there is no MeshControl
// stream to the coordinator (because the coordinator is in-process).
//
// Called by assignmentEngine.broadcastPeerList after rebalance/register so
// the embedded gateway's outbound peer set stays in sync with the live
// cluster topology. Safe to call with hostNetwork == nil — this is the
// classic `all` preset path where every cell is local and no remote peers
// exist.
func (g *Gateway) reconcileRemotePeers(pl *meshpb.PeerList) {
	if g == nil || g.hostNetwork == nil || pl == nil {
		return
	}
	// Keep the cached topology in sync with the live cluster view. The
	// standalone gateway path (meshGatewayClient.applyPeerList) does this
	// unconditionally; the embedded-coord+gateway-without-host variant needs
	// the same so Gateway.cellAtPosition can resolve a world coord to a cell
	// ID when coord.CellOwner is empty (the coord process owns no cells of
	// its own). No-op in the all-preset colocated path because hostNetwork is
	// nil and we return above.
	if g.topology != nil {
		g.topology.applyPeerList(pl.Cells)
	}
	wanted := make(map[string]string, len(pl.Hosts))
	for _, hr := range pl.Hosts {
		if hr.HostId == g.id {
			continue // don't dial ourselves
		}
		if hr.GrpcAddr == "" {
			continue
		}
		wanted[hr.HostId] = hr.GrpcAddr
	}
	for hid, addr := range wanted {
		if err := g.hostNetwork.ConnectPeer(hid, addr, peerKindNode); err != nil {
			g.log.Log(CatMeshCell, "gateway: ConnectPeer node %s (%s) failed: %v", hid, addr, err)
		}
	}
	// Drop outbound streams to hosts no longer present.
	g.hostNetwork.mu.RLock()
	existing := make([]string, 0, len(g.hostNetwork.peers))
	for hid, peer := range g.hostNetwork.peers {
		if peer.kind == peerKindNode {
			existing = append(existing, hid)
		}
	}
	g.hostNetwork.mu.RUnlock()
	for _, hid := range existing {
		if _, keep := wanted[hid]; !keep {
			g.hostNetwork.DisconnectPeer(hid)
			g.log.Log(CatMeshCell, "gateway: disconnected from node %s", hid)
		}
	}
}

// cachedTopology is the gateway's snapshot of cell → host ownership.
//
// In embedded mode (coord != nil) HostForCell reads live from
// coord.cellToHostMap under coord.mu.RLock. In standalone mode (standalone) the
// internal cells map is populated by applyPeerList from PeerList broadcasts.
//
// The "local" sentinel is returned when no host mapping exists (e.g.
// single-host `all` preset where cellToHostMap is empty). Gateway.isLocalShortcut
// treats "local" as an always-local host so dispatch still reaches the cell Inbox.
type cachedTopology struct {
	// Embedded-mode reference. When non-nil, HostForCell reads live from
	// coord.cellToHostMap. When nil (standalone T9), the cells map below is used.
	coord *Process

	mu    sync.RWMutex
	cells map[string]string // cellID -> hostID (standalone mode only)
}

func newCachedTopology(coord *Process) *cachedTopology {
	return &cachedTopology{coord: coord}
}

// HostForCell returns the hostID that owns cellID.
// Returns the "local" sentinel when no mapping exists (single-host mode).
//
// Note: returning "local" has different meanings per mode — in embedded mode it
// means single-host `all` preset (always safe); in standalone mode (standalone) it means
// the PeerList has not yet arrived (empty snapshot). The inconsistency is harmless
// because isLocalShortcut returns false when g.coord == nil, so a standalone
// gateway will never treat "local" as an in-process shortcut.
func (t *cachedTopology) HostForCell(cellID string) string {
	if t.coord != nil {
		// Embedded mode: read live from coordinator state. OwnerOf unifies
		// hostRegistry (authoritative for coord+gateway-without-host mode)
		// and cellToHostMap (populated via PeerList on node and all-preset
		// processes), locking as needed.
		if hostID, ok := t.coord.Control.OwnerOf(cellID); ok {
			return hostID
		}
		return "local" // single-host `all` preset sentinel
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

// cellAtPosition returns the cell ID currently owning world position
// (worldX, worldY). Walks the snapshot and parses each cell ID into its
// quadtree coordinate so arbitrary split depths resolve correctly. Used by
// the gateway's standalone login path where coord.CellAtPosition can't see
// remote cells. Returns "" when no cell owns the point or the snapshot is
// empty.
func (t *cachedTopology) cellAtPosition(worldX, worldY float32) string {
	if t.coord != nil {
		// Embedded mode: delegate to the live coordinator state.
		if id := t.coord.CellAtPosition(worldX, worldY); id != "" {
			return id
		}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for cellID := range t.cells {
		cell, err := ParseCellID(cellID)
		if err != nil {
			continue
		}
		minX, minY, maxX, maxY := cell.WorldBounds(coords.CellSize)
		if worldX >= minX && worldX < maxX && worldY >= minY && worldY < maxY {
			return cellID
		}
	}
	return ""
}

// applyPeerList updates the topology snapshot from a PeerList broadcast.
// Called by the standalone gateway (standalone) when the coordinator pushes ownership changes.
//
// PeerList carries the FULL cell-to-host ownership table, so we replace the
// map atomically rather than upserting. Otherwise cells removed by a merge
// (e.g. d1_* children collapsed back into a depth-0 parent) linger in the
// snapshot, and cellAtPosition can return a non-existent cell to processLogin
// — clients then get assigned to a missing cell and can never reconnect.
func (t *cachedTopology) applyPeerList(cells []*meshpb.CellOwnership) {
	newMap := make(map[string]string, len(cells))
	for _, co := range cells {
		newMap[co.CellId] = co.HostId
	}
	t.mu.Lock()
	t.cells = newMap
	t.mu.Unlock()
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
				ConnId:        sess.connID,
				GatewayId:     g.id,
				Username:      sess.username,
				ToCellId:      sess.cellID,
				Data:          dataBytes,
				SpawnLocation: locationToProto(sess.spawnLoc),
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
// (standalone). T11 must arrange hostNetwork construction for embedded always-proxy mode
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
						Epoch:     sess.epoch,
						Data:      raw,
					},
				},
			}
			if err := g.hostNetwork.SendOrdered(sess.hostID, frame); err != nil {
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
						Epoch:     sess.epoch,
						Data:      raw,
					},
				},
			}
			if err := g.hostNetwork.SendOrdered(sess.hostID, frame); err != nil {
				g.log.Log(CatNetConn, "gateway: ClientInput (op) forward conn=%d host=%s: %v", connID, sess.hostID, err)
			}
		}
	}
}
