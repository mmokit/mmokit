// Package universe — gateway.go
//
// Gateway is the worker type responsible for terminating WebSocket
// connections, holding per-connection authentication state, and proxying
// client I/O to authoritative nodes via MeshData streams. Login itself
// is now a regular service op (handled by pkg/services/auth); the gateway only
// gates non-auth ops on authentication state and dispatches a
// PlayerAssignment to the target cell after the auth response succeeds.
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

	"github.com/google/uuid"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/service"
	"github.com/zenion/mmoserver/pkg/services/auth"
)

// Gateway terminates WebSocket connections, gates ops on per-connection
// authentication state, and routes authenticated players to the correct cell.
// In embedded mode it runs inside the Process process and dispatches directly
// to cell inboxes; in standalone mode it runs as a separate process and
// forwards traffic via MeshData streams.
type Gateway struct {
	id      string
	connMgr *net.ConnManager // real WebSocket server (concrete)
	log     *logger.Logger

	mu       sync.RWMutex
	sessions map[uint32]*localSession

	topology *cachedTopology // cellID → hostID

	// Only non-nil when running standalone (standalone):
	controlClient *meshGatewayClient
	hostNetwork   *HostNetwork // gRPC server + peer streams for MeshData (standalone only)

	// wsAddr is the WebSocket listen address advertised to the coordinator.
	// Set by standalone gateway mode; empty in embedded mode.
	wsAddr string

	// spawnOrch tracks in-flight ResolveSpawn RPC requests (standalone mode only).
	spawnOrch *spawnOrchestrator

	// Embedded mode: coordinator reference for direct access to sessionRoutes
	// and cell Inbox. nil when standalone.
	coord *Process

	// process is the local *Process that owns this Gateway. Set in BOTH
	// embedded and standalone build paths (in embedded mode it equals
	// coord). Used to reach process-local infrastructure that exists in
	// both modes — e.g. the cmdsys dispatcher and transport for service-
	// routed commands arriving on the gateway control stream, and
	// serviceRouting for PeerList-driven service announcement updates.
	process *Process

	// cfg points at the LOCAL Process's Config — set in BOTH embedded and
	// standalone build paths. Used for late-bound config that gets populated
	// after Build() (notably AuthResolver / AuthHTTPOpts, stamped by the auth
	// service's OnReady during startServices). Don't substitute g.coord.cfg
	// here: g.coord is nil for standalone gateways.
	cfg *Config

	// tickRate is copied from Config.TickRate at construction time and sent
	// to every newly connected client via the typed ServerConfig event so
	// the client knows the server tick cadence (used for interpolation
	// math). Must be set in BOTH embedded and standalone modes — the
	// client relies on it regardless of where the gateway lives.
	tickRate uint32

	// Gateway-plane state. Populated during Build() from Process's
	// corresponding fields.
	sessionRoutes *sessionRoutes
	httpServer    *http.Server

	// authMu guards authStates.
	authMu     sync.Mutex
	authStates map[uint32]connAuthState
}

// connAuthState is the gateway's authoritative per-connection auth record.
// Populated by the bus subscriber (subscribeToAuthEvents) once an
// AUTH_LOGIN / AUTH_REGISTER / AUTH_VALIDATE_TOKEN response succeeds.
type connAuthState struct {
	authenticated bool
	userID        uuid.UUID
	username      string
	sessionToken  string
	expiresAtMs   int64
	authedAt      time.Time
}

// localSession records the gateway-side routing state for one authenticated player.
type localSession struct {
	connID   uint32
	userID   uuid.UUID
	username string
	token    string // session token bound to the auth login
	hostID   string // current authoritative host; "local" sentinel in single-host mode
	cellID   MeshCellID
	epoch    uint64
	spawnLoc coords.Location // resolved at login; forwarded in PlayerAssignment

	// isReconnect is set when the standalone gateway's resolveSpawn RPC
	// returned reconnect routing for this user. Forwarded onto the remote
	// PlayerAssignment so the destination cell reattaches to the lingering
	// entity instead of spawning a fresh one. Embedded mode runs the same
	// detection inline at dispatchPlayerAssignment and ignores this field.
	isReconnect bool
}

// handleEvent handles a net.PlayerEvent (connect or disconnect) from ConnManager.Events().
// For connect events it sends the server-config tick rate; the connection
// remains unauthenticated until pkg/services/auth completes a successful auth op.
// For disconnect events it delegates to handleDisconnect.
func (g *Gateway) handleEvent(evt net.PlayerEvent) {
	if evt.Connected {
		// Send the typed ServerConfig event immediately so the client
		// has the tick rate before any world update arrives. The client divides
		// elapsed frame time by state.tickMs for interpolation, so a
		// missing config silently produces interp = Infinity (clamped
		// to 2.0, forcing extrapolation) or NaN at the exact tick-
		// arrival instant — causing entities to snap in 50ms steps
		// instead of interpolating smoothly, and to flicker to
		// (NaN, NaN) for one frame on every tick boundary.
		g.sendServerConfig(evt.ConnID)
		g.log.Log(CatNetConn, "gateway: conn %d connected (unauthenticated)", evt.ConnID)
		// Start the per-conn pump immediately on processes that route via
		// MeshData. The pump owns BOTH the pre-auth window (RouteGatewayLocal
		// auth ops dispatched inline) AND post-auth forwarding. Embedded
		// local-shortcut processes (g.hostNetwork == nil) keep using
		// OpRouter — there's no MeshData hop to fork on.
		if g.hostNetwork != nil {
			go g.runSessionPump(evt.ConnID)
		}
		return
	}
	g.handleDisconnect(evt)
}

// subscribeToAuthEvents wires the bus subscribers that observe auth
// login/logout events. Called from coordinator.Build after the bus is
// constructed and the gateway is wired. The subscribers run synchronously
// inline on Publish (process-local Phase 1 contract) so authStates[connID]
// is populated BEFORE the typed-op response leaves the gateway.
func (g *Gateway) subscribeToAuthEvents(bus *service.Bus) {
	if bus == nil {
		return
	}
	service.Subscribe(bus, func(ev service.AuthLoginSucceededEvent) {
		uid, err := uuid.Parse(ev.UserID)
		if err != nil {
			g.log.Log(CatNetConn, "gateway: AuthLoginSucceededEvent: bad user_id %q: %v", ev.UserID, err)
			return
		}
		g.onAuthSuccess(ev.ConnID, uid, ev.Username, ev.SessionToken, ev.ExpiresAtMs)
	})
	service.Subscribe(bus, func(ev service.AuthLogoutEvent) {
		g.onAuthLogout(ev.ConnID)
	})
}

// isAuthenticated reports whether connID is in the authenticated set.
func (g *Gateway) isAuthenticated(connID uint32) bool {
	g.authMu.Lock()
	defer g.authMu.Unlock()
	return g.authStates[connID].authenticated
}

// authStateOf returns a copy of the auth state for connID.
func (g *Gateway) authStateOf(connID uint32) (connAuthState, bool) {
	g.authMu.Lock()
	defer g.authMu.Unlock()
	s, ok := g.authStates[connID]
	return s, ok
}

// clearAuthState removes any auth state for connID. Called on disconnect
// and after a successful logout.
func (g *Gateway) clearAuthState(connID uint32) {
	g.authMu.Lock()
	defer g.authMu.Unlock()
	delete(g.authStates, connID)
}

// onAuthSuccess is invoked when an AuthLoginSucceededEvent fires on the bus
// (or synchronously from onWSUpgrade for cookie-based reattach). It commits
// the per-connection auth record and then dispatches the downstream
// PlayerAssignment to the appropriate cell.
func (g *Gateway) onAuthSuccess(connID uint32, userID uuid.UUID, username, token string, expiresAtMs int64) {
	g.authMu.Lock()
	prev := g.authStates[connID]
	if token == "" {
		// AUTH_VALIDATE_TOKEN response doesn't carry the token — the
		// client already has it; preserve the request-side value.
		token = prev.sessionToken
	}
	g.authStates[connID] = connAuthState{
		authenticated: true,
		userID:        userID,
		username:      username,
		sessionToken:  token,
		expiresAtMs:   expiresAtMs,
		authedAt:      time.Now(),
	}
	g.authMu.Unlock()
	g.dispatchPostAuthAssignment(connID, userID, username, token)
}

// onWSUpgrade fires synchronously after a WebSocket upgrade succeeds,
// before any frames flow. If the request carries a valid auth cookie,
// the gateway binds the session at upgrade time — same path
// onAuthSuccess takes after an op-channel AUTH_LOGIN. If no cookie
// or the cookie is invalid, the connection upgrades unauthenticated;
// the web client's /auth/me or /auth/login round-trip will clear
// any stale cookie.
//
// Config.AnonymousAuth is the dev/example escape hatch: when no
// AuthResolver is registered, the gateway synthesizes a fresh session
// per upgrade so OnPlayerJoin fires without any login plumbing.
func (g *Gateway) onWSUpgrade(uc net.UpgradeContext) {
	if g.cfg == nil {
		return
	}
	if g.cfg.AuthResolver == nil {
		if g.cfg.AnonymousAuth {
			g.onAuthSuccess(uc.ConnID, uuid.New(), fmt.Sprintf("anon-%d", uc.ConnID), "", 0)
		}
		return
	}
	opts := g.cfg.AuthHTTPOpts
	if opts.CookieName == "" {
		opts = auth.DefaultHTTPOpts()
	}
	cookie, err := uc.Request.Cookie(opts.CookieName)
	if err != nil || cookie.Value == "" {
		return
	}
	resolved, err := g.cfg.AuthResolver.Resolve(uc.Request.Context(), cookie.Value)
	if err != nil {
		g.log.Log(CatNetConn, "ws upgrade: cookie validate failed conn=%d: %v", uc.ConnID, err)
		return
	}
	g.onAuthSuccess(uc.ConnID, resolved.UserID, resolved.Username, cookie.Value, resolved.ExpiresAt.UnixMilli())
}

// onAuthLogout fires after a successful AUTH_LOGOUT response. The auth
// service has already revoked the session row; the gateway closes the WS.
func (g *Gateway) onAuthLogout(connID uint32) {
	g.clearAuthState(connID)
	g.connMgr.Remove(connID)
}

// kickConn tears down an existing connection — used by the duplicate-active
// rejection path in dispatchPostAuthAssignment when a second window logs in
// for an already-online user_id. Reason is logged; no SE_KICKED event is
// sent today (gateway-crash transparent reconnect will land in a follow-up
// phase). The client sees a clean WebSocket close and surfaces the rejection
// via the existing onClose → onLoginRejected handler.
func (g *Gateway) kickConn(connID uint32, reason string) {
	g.clearAuthState(connID)
	g.mu.Lock()
	delete(g.sessions, connID)
	g.mu.Unlock()
	g.log.Log(CatNetConn, "gateway: kicking conn=%d (%s)", connID, reason)
	g.connMgr.Remove(connID)
}

// dispatchPostAuthAssignment is the Tasks-20–22 replacement for the
// LoginHandler-driven processLogin path. It runs after auth succeeds, looks
// up the spawn location, resolves the target cell via PlayerRouter (or the
// gateway's cached topology), records the localSession, and sends the
// PlayerAssignment to the destination cell.
func (g *Gateway) dispatchPostAuthAssignment(connID uint32, userID uuid.UUID, username, token string) {
	res := g.resolveSpawn(context.Background(), userID, username)

	// Duplicate-active rejection. Another window is already online for this
	// user_id; close the new WS without dispatching a PlayerAssignment so
	// the original session keeps playing uninterrupted. Standalone gateway
	// gets the flag piggybacked on SpawnResolved (set by
	// applyResolveSpawnReconnect on the coord); embedded gateway runs the
	// same check inline via activeUserLocked. Both paths converge here.
	rejected := res.Rejected
	if !rejected && g.coord != nil {
		g.coord.mu.RLock()
		if loc := g.coord.activeUserLocked(userID); loc != nil && loc.Active {
			rejected = true
		}
		g.coord.mu.RUnlock()
	}
	if rejected {
		g.log.Log(CatNetConn, "gateway: rejecting duplicate login conn=%d user=%s — another session is active", connID, username)
		g.kickConn(connID, "another session is active for this user")
		return
	}

	loc := res.Location

	var cellID string
	var hostID string
	isReconnect := false

	// Standalone gateway: the coordinator may have piggybacked reconnect
	// routing onto the SpawnResolved response. Embedded mode runs the same
	// detection inline at dispatchPlayerAssignment via activeUserLocked, so
	// the override only fires for the standalone (g.coord == nil) path.
	if g.coord == nil && res.IsReconnect && res.TargetCellID != "" {
		cellID = res.TargetCellID
		hostID = res.TargetHostID
		isReconnect = true
	}

	if cellID == "" {
		if g.coord != nil && g.coord.cfg.PlayerRouter != nil {
			cellID = g.coord.cfg.PlayerRouter(userID, username)
		}
		if cellID == "" {
			cellID = g.topology.cellAtPosition(loc.X, loc.Y)
		}
		if cellID == "" {
			g.log.Log(CatNetConn, "gateway: no cell for user=%s loc=(%f,%f)", username, loc.X, loc.Y)
			return
		}
	}

	if hostID == "" {
		hostID = g.topology.HostForCell(MeshCellID(cellID))
	}
	if hostID == "" || hostID == "local" {
		if g.coord == nil {
			g.log.Log(CatNetConn, "gateway: no host for cell %s (topology not yet populated)", cellID)
			return
		}
		hostID = "local"
	}

	sess := &localSession{
		connID:      connID,
		userID:      userID,
		username:    username,
		token:       token,
		hostID:      hostID,
		cellID:      MeshCellID(cellID),
		epoch:       1,
		spawnLoc:    loc,
		isReconnect: isReconnect,
	}
	g.mu.Lock()
	g.sessions[connID] = sess
	g.mu.Unlock()

	// Service framework: populate the process-level connID→username map
	// so the OpRouter can dispatch service handlers with a non-empty
	// username on opCtx.
	if g.coord != nil && g.coord.opsSessions != nil {
		g.coord.opsSessions.Set(connID, username)
	}

	g.announceSession(sess)
	if err := g.dispatchPlayerAssignment(sess); err != nil {
		g.log.Log(CatNetConn, "gateway: dispatchPlayerAssignment conn=%d user=%s: %v",
			connID, username, err)
		return
	}

	// Stamp the UUID-keyed activeUsers index so reconnect detection
	// (activeUserLocked) and duplicate-active rejection can find this
	// session on a subsequent auth attempt for the same user_id.
	// Without this, activeUsers stays empty and every login creates a
	// fresh entity instead of reattaching to the lingering session.
	//
	// In standalone mode the gateway can't reach activeUsers directly;
	// announceSession carries user_id on SessionAnnounce and the coord-side
	// handler runs the same registration there.
	if g.coord != nil {
		g.coord.registerAuthenticatedSession(userID, username, g.id, connID, sess.hostID, sess.cellID)
	}

	// Publish the session-enter event on the per-process bus. Any service
	// (chat presence, future presence service, achievements) subscribed
	// at Init time receives the event synchronously. No-op if Bus is nil
	// (defensive — Bus is always constructed in Process.New, but standalone
	// fixture tests may bypass that path).
	if g.process != nil && g.process.bus != nil {
		service.Publish(g.process.bus, service.SessionEnterEvent{
			ConnID:    connID,
			UserID:    userID.String(),
			Username:  username,
			GatewayID: g.id,
		})
		g.log.Log(CatServicesBus, "publish SessionEnterEvent conn=%d user=%s gw=%s",
			connID, username, g.id)
	}
}

// sendServerConfig writes the typed ServerConfig event with the cached
// tick rate to the given connection via the local ConnManager. Works
// identically in embedded and standalone modes because g.connMgr is
// always the process's real WebSocket-serving ConnManager. Falls back
// silently when tickRate is zero (tests / misconfigured setup) or when
// the EngineDefaultFrameHooks builder is unwired (test paths that never
// import mmokit).
func (g *Gateway) sendServerConfig(connID uint32) {
	if g.tickRate == 0 {
		return
	}
	if EngineDefaultFrameHooks.ServerConfig == nil {
		return
	}
	frame := EngineDefaultFrameHooks.ServerConfig(g.tickRate)
	if frame == nil {
		// Encoder guard rejected the payload — already counted and logged.
		return
	}
	g.connMgr.SendReliable(connID, frame)
}

// handleDisconnect owns the full disconnect cleanup for a connection:
//  1. If the connection was still in pending login, remove it from the login queue.
//  2. If authenticated, remove the session record and clean up sessionRoutes.
//  3. Forward the disconnect event to the owning cell's Events channel so the
//     engine's grace-period state machine fires unchanged.
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

	// Always drop any auth state for this connection.
	g.clearAuthState(connID)

	if !found {
		g.log.Log(CatNetConn, "gateway: conn %d disconnected before authentication", connID)
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

	// Publish the session-leave event on the per-process bus. Subscribers
	// (chat presence, future presence service, etc) drop per-conn state.
	// Idempotent on the chat-side handler.
	if g.process != nil && g.process.bus != nil {
		userIDStr := ""
		if sess.userID != uuid.Nil {
			userIDStr = sess.userID.String()
		}
		service.Publish(g.process.bus, service.SessionLeaveEvent{
			ConnID:    connID,
			UserID:    userIDStr,
			GatewayID: g.id,
		})
		g.log.Log(CatServicesBus, "publish SessionLeaveEvent conn=%d gw=%s",
			connID, g.id)
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
	var userIDStr string
	if sess.userID != uuid.Nil {
		userIDStr = sess.userID.String()
	}
	msg := &meshpb.HostMessage{
		Msg: &meshpb.HostMessage_SessionAnnounce{
			SessionAnnounce: &meshpb.SessionAnnounce{
				GatewayId:    g.id,
				ConnId:       sess.connID,
				Username:     sess.username,
				TargetHostId: sess.hostID,
				TargetCellId: string(sess.cellID),
				UserId:       userIDStr,
			},
		},
	}
	if err := g.controlClient.send(msg); err != nil {
		g.log.Log(CatNetConn, "gateway: SessionAnnounce send failed conn=%d user=%s: %v", sess.connID, sess.username, err)
	}
}

// dispatchPlayerAssignment sends a PlayerAssignment to the target cell.
// In embedded mode it checks for reconnect state first (lingering
// disconnected session in the same coord process) then writes directly to
// the cell's Inbox. In standalone mode it forwards via MeshData.
//
// Duplicate-active rejection happens upstream in dispatchPostAuthAssignment
// (single early-exit before any session state is mutated). By the time we
// reach this method the user is either fresh-login or grace-period reconnect.
func (g *Gateway) dispatchPlayerAssignment(sess *localSession) error {
	if g.coord == nil {
		return g.dispatchPlayerAssignmentRemote(sess)
	}

	// Embedded mode.

	// Look up the user's lingering disconnected session (grace period). If
	// one exists, route the assignment to its current cell with
	// IsReconnect=true so the cell rebinds to the existing entity.
	var reconnectCellID MeshCellID
	g.coord.mu.RLock()
	if loc := g.coord.activeUserLocked(sess.userID); loc != nil && !loc.Active {
		reconnectCellID = loc.CellID
	}
	g.coord.mu.RUnlock()

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
					ConnID:           sess.connID,
					UserID:           sess.userID,
					Username:         sess.username,
					SessionToken:     sess.token,
					IsReconnect:      true,
					StreamGeneration: uint32(sess.epoch),
					SpawnLocation:    sess.spawnLoc,
				},
			}
			g.log.Log(CatNetConn, "gateway: reconnect conn=%d user=%s -> %s", sess.connID, sess.username, reconnectCellID)
			return nil
		}
		// Cell gone (e.g., merged) — fall through to fresh login.
	}

	targetNodeID := sess.cellID

	// Remote host path: the target cell lives on a remote `--mode=host`
	// process, not in this one. Happens in coord+gateway-without-host mode.
	// Delegate to the MeshData dispatch path — identical wire format to
	// standalone gateway mode, just with coord != nil.
	if !g.isLocalShortcut(sess.hostID) {
		return g.dispatchPlayerAssignmentRemote(sess)
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
			ConnID:           sess.connID,
			UserID:           sess.userID,
			Username:         sess.username,
			SessionToken:     sess.token,
			StreamGeneration: uint32(sess.epoch),
			SpawnLocation:    sess.spawnLoc,
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

// lookupSession returns an immutable-by-convention snapshot of the localSession
// for connID, or nil if absent. Never expose the map-owned pointer after
// releasing g.mu: OnUpstreamSwitch mutates route fields concurrently with the
// session pump, and a torn hostID/epoch pair sends an otherwise valid frame to
// the wrong routing generation.
func (g *Gateway) lookupSession(connID uint32) *localSession {
	g.mu.RLock()
	defer g.mu.RUnlock()
	sess := g.sessions[connID]
	if sess == nil {
		return nil
	}
	snapshot := *sess
	return &snapshot
}

// snapshotSessions returns a slice of copies of every live local session,
// taken under g.mu with the same discipline as lookupSession: the map-owned
// pointers are never exposed, because OnUpstreamSwitch mutates route fields
// concurrently with the session pump. Used by the control client to replay
// SessionAnnounce after a coordinator reconnect.
func (g *Gateway) snapshotSessions() []*localSession {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]*localSession, 0, len(g.sessions))
	for _, sess := range g.sessions {
		if sess == nil {
			continue
		}
		snapshot := *sess
		out = append(out, &snapshot)
	}
	return out
}

// replicationReceiptHost snapshots the current upstream route for a tracked
// replication frame. Receipt traffic is stricter than ordinary frame routing:
// both the gateway identity and epoch must match exactly so an enqueue from a
// superseded authority cannot acknowledge a new cell's pending transaction.
func (g *Gateway) replicationReceiptHost(connID uint32, gatewayID string, epoch uint64, sourceHostID string) (string, bool) {
	if g == nil || epoch == 0 || gatewayID == "" || gatewayID != g.id || sourceHostID == "" {
		return "", false
	}
	g.mu.RLock()
	sess, ok := g.sessions[connID]
	if !ok || sess.epoch != epoch || sess.hostID == "" || sess.hostID != sourceHostID {
		g.mu.RUnlock()
		return "", false
	}
	hostID := sess.hostID
	g.mu.RUnlock()
	return hostID, true
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
func (g *Gateway) OnUpstreamSwitch(connID uint32, newHost string, newCell MeshCellID, newEpoch uint64) {
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
	cells map[MeshCellID]string // cellID -> hostID (standalone mode only)
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
func (t *cachedTopology) HostForCell(cellID MeshCellID) string {
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
		cell, err := ParseCellID(string(cellID))
		if err != nil {
			continue
		}
		minX, minY, maxX, maxY := cell.WorldBounds(coords.CellSize)
		if worldX >= minX && worldX < maxX && worldY >= minY && worldY < maxY {
			return string(cellID)
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
	newMap := make(map[MeshCellID]string, len(cells))
	for _, co := range cells {
		newMap[MeshCellID(co.CellId)] = co.HostId
	}
	t.mu.Lock()
	t.cells = newMap
	t.mu.Unlock()
}

// dispatchPlayerAssignmentRemote forwards a PlayerAssignment to the target node
// via MeshData. Used in standalone gateway mode where there is no direct cell Inbox.
func (g *Gateway) dispatchPlayerAssignmentRemote(sess *localSession) error {
	if g.hostNetwork == nil {
		return fmt.Errorf("gateway: standalone dispatchPlayerAssignment — no hostNetwork")
	}

	frame := &meshpb.MeshFrame{
		DestCellId: string(sess.cellID),
		Msg: &meshpb.MeshFrame_PlayerAssignment{
			PlayerAssignment: &meshpb.PlayerAssignment{
				ConnId:           sess.connID,
				GatewayId:        g.id,
				UserId:           sess.userID.String(),
				Username:         sess.username,
				SessionToken:     sess.token,
				ToCellId:         string(sess.cellID),
				Epoch:            sess.epoch,
				StreamGeneration: uint32(sess.epoch),
				SpawnLocation:    locationToProto(sess.spawnLoc),
				IsReconnect:      sess.isReconnect,
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

	// The per-conn pump was already started in handleEvent at connect time —
	// it will pick up the freshly recorded session on its next tick and start
	// forwarding RoutePlayerCell ops + event frames to the assigned host.
	return nil
}

// runSessionPump is the per-conn goroutine that owns the inbound drain on
// gateways that route via MeshData (standalone gateway, coord+gateway-without-
// host, embedded always-proxy). Started at connect time from handleEvent so
// it covers BOTH pre-auth and post-auth traffic — the pre-auth window only
// sees RouteGatewayLocal typed-ops (auth.* by construction), which the pump
// dispatches inline; post-auth, RoutePlayerCell ops are forwarded to the
// player's authoritative host via MeshData.
//
// Forking on RouteKind inside the pump is what closes the race that
// previously caused chat (RouteGatewayLocal) ops to be silently forwarded
// to a host that doesn't run the chat service. Before the fork, the OpRouter
// (5ms) and the pump (1ms) drained the same queue concurrently; the faster
// pump wired everything to the host, so the local dispatch never ran. The
// OpRouter is now skipped entirely on processes that own this pump (see
// the gate at coordinator.go ~2417).
//
// The pump exits when the underlying connection is gone (Get(connID)==nil),
// regardless of session state — disconnect tears the conn down before the
// session is reaped, so this is a strict superset of "session removed".
func (g *Gateway) runSessionPump(connID uint32) {
	ticker := time.NewTicker(1 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		if g.hostNetwork == nil {
			return
		}
		if g.connMgr.Get(connID) == nil {
			return // connection closed
		}
		sess := g.lookupSession(connID) // may be nil pre-auth

		// Plan 1 Phase 5 unified the typed client-input channel (0x02)
		// with the event channel (0x00); Phase 7 then deleted the 0x02
		// constant entirely. The gateway drains two queues: events
		// (carries framework-level ServerEvent envelopes and typed-input
		// frames) and ops.
		//
		// Events have no gateway-local route kind — every event frame is
		// forwarded to the player's host. Skip when there's no session
		// (pre-auth window): event frames before authentication have
		// nowhere to go.
		if sess != nil {
			g.forwardChannel(sess, connID, g.connMgr.DrainInput(connID), net.ChannelEvent, "event")
		} else {
			// Drain anyway so frames don't accumulate; pre-auth event
			// frames are not expected, but we don't want them to wedge
			// the queue.
			g.connMgr.DrainInput(connID)
		}

		// Op frames are forked on RouteKind — see processOpFrame.
		for _, raw := range g.connMgr.DrainOpInput(connID) {
			g.processOpFrame(connID, sess, raw)
		}
	}
}

// processOpFrame forks per-frame on the typed-op RouteKind. RouteGatewayLocal
// ops are dispatched inline via DispatchTypedOpInbound and the response is
// shipped back to the client over the WS reliable channel. Everything else
// (RoutePlayerCell, unknown typeIDs) is forwarded to the player's host via
// the existing MeshData ClientInput path so the cell can route it.
//
// Forwarding pre-auth requires a session — without one, RoutePlayerCell ops
// have no destination. The ops registry rejects RouteGatewayLocal handlers
// from being registered with stale typeIDs, so an unknown typeID pre-auth is
// either a malformed frame or a misconfigured client; we drop with an
// OperationError back to the client (best-effort) rather than silently
// swallowing it.
// The recover is PER FRAME, not per goroutine: runSessionPump owns the
// connection's only drain, so unwinding it would leave the queues to fill until
// the connection is torn down. Recovering here drops exactly the offending
// frame and the pump keeps draining.
//
// The barrier is deliberately here and NOT inside DispatchTypedOpInbound. That
// function must stay panic-transparent so the codec fuzz targets report
// crashers instead of silently passing.
//
// This frame arrives PRE-AUTHENTICATION — sess is nil during the whole auth
// exchange. The reflection decoder reached from here no longer trusts
// wire-supplied lengths: DispatchTypedOpInbound decodes the body through
// ReflectUnmarshalStrict under this process's client ingress profile, which
// bounds every read and charges every allocation before making it. The recover
// stays because it also covers the registered handler, which is game code.
func (g *Gateway) processOpFrame(connID uint32, sess *localSession, raw []byte) {
	defer func() {
		if r := recover(); r != nil {
			g.log.Log(CatNetConn, "gateway: op frame panic conn=%d bytes=%d panic=%v",
				connID, len(raw), r)
		}
	}()

	typeID, _, _, decErr := DecodeTypedOpFrame(raw)
	if decErr != nil {
		// Structurally invalid frame: nothing we can do. The dispatcher
		// would also drop it (no request_id to correlate an error).
		g.log.Log(CatNetConn, "gateway: op decode conn=%d: %v", connID, decErr)
		return
	}

	var kind uint8
	var routeKnown bool
	if TypedOpHooks.LookupTypedOp != nil {
		k, _, _, _, _, ok := TypedOpHooks.LookupTypedOp(typeID)
		if ok {
			kind = k
			routeKnown = true
		}
	}

	if routeKnown && kind == TypedOpHooks.RouteGatewayLocal {
		// Build OpContext with ConnID + Username + ClientIP, matching
		// pkg/ops/router.go's poll() construction so handlers see the
		// same context shape regardless of which drain path delivered
		// the frame.
		username := ""
		if sess != nil {
			username = sess.username
		}
		opCtx := &ops.OpContext{
			ConnID:   connID,
			Username: username,
			ClientIP: ops.ParseClientIP(g.connMgr.RemoteAddrString(connID)),
		}
		// nil router: RouteGatewayLocal handlers don't use cell routing.
		// DispatchTypedOpInbound returns the response frame with the
		// channel byte (0x01) already prepended, suitable for direct
		// SendReliable.
		// g.process is set in BOTH embedded and standalone build paths, unlike
		// g.coord which is nil for a standalone gateway. clientWireLimits
		// tolerates a nil receiver for the fixtures that build a bare Gateway.
		if respFrame := DispatchTypedOpInbound(raw, opCtx, nil, g.process.clientWireLimits()); respFrame != nil {
			g.connMgr.SendReliable(connID, respFrame)
		}
		return
	}

	// RoutePlayerCell or unknown typeID → forward to the host. Without
	// a session, there's nowhere to forward; drop with a best-effort
	// OperationError so the client sees a failure instead of hanging.
	if sess == nil {
		if respFrame := encodeTypedOpUnroutable(raw); respFrame != nil {
			g.connMgr.SendReliable(connID, respFrame)
		}
		return
	}
	g.forwardOpFrame(sess, connID, raw)
}

// encodeTypedOpUnroutable builds an OperationError response correlated to
// the request_id in raw. Used for op frames that arrive before authentication
// completes (no session → no host to forward to). Returns nil when the
// typed-op hooks aren't wired or the frame is structurally bad.
func encodeTypedOpUnroutable(raw []byte) []byte {
	_, requestID, _, err := DecodeTypedOpFrame(raw)
	if err != nil {
		return nil
	}
	return encodeOpErrorViaHooks(requestID, opErrorHandlerFailed,
		"op requires an authenticated session")
}

// forwardOpFrame wraps a single op-channel payload in a MeshFrame.ClientInput
// and ships it to the player's authoritative host. Extracted from
// forwardChannel so the per-frame fork in processOpFrame can call it directly.
func (g *Gateway) forwardOpFrame(sess *localSession, connID uint32, raw []byte) {
	frame := &meshpb.MeshFrame{
		Msg: &meshpb.MeshFrame_ClientInput{
			ClientInput: &meshpb.ClientInput{
				GatewayId: g.id,
				ConnId:    connID,
				Epoch:     sess.epoch,
				Data:      raw,
				Channel:   uint32(net.ChannelOperation),
			},
		},
	}
	if err := g.hostNetwork.SendOrdered(sess.hostID, frame); err != nil {
		g.log.Log(CatNetConn, "gateway: ClientInput (op) forward conn=%d host=%s: %v", connID, sess.hostID, err)
	}
}

// forwardChannel wraps each drained payload in a MeshFrame.ClientInput tagged
// with its source wire channel and forwards to the authoritative host. The
// channel tag lets the host dispatch into the matching per-session queue
// without sniffing payload bytes. Op-channel forwarding now goes through
// processOpFrame → forwardOpFrame; this function remains for the event
// channel.
func (g *Gateway) forwardChannel(sess *localSession, connID uint32, msgs [][]byte, channel byte, kind string) {
	for _, raw := range msgs {
		frame := &meshpb.MeshFrame{
			Msg: &meshpb.MeshFrame_ClientInput{
				ClientInput: &meshpb.ClientInput{
					GatewayId: g.id,
					ConnId:    connID,
					Epoch:     sess.epoch,
					Data:      raw,
					Channel:   uint32(channel),
				},
			},
		}
		if err := g.hostNetwork.SendOrdered(sess.hostID, frame); err != nil {
			g.log.Log(CatNetConn, "gateway: ClientInput (%s) forward conn=%d host=%s: %v", kind, connID, sess.hostID, err)
		}
	}
}
