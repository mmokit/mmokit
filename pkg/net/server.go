package net

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

// ConnStatsProvider is an optional interface a Transport may implement
// to expose write-path timing diagnostics. Used by HandleConnStats to
// dump per-connection counters as JSON.
type ConnStatsProvider interface {
	Stats() ConnStats
}

// PlayerEvent represents a player connecting or disconnecting.
type PlayerEvent struct {
	ConnID     uint32
	Connected  bool
	Disconnect bool
}

// UpgradeContext is passed to OnUpgrade after a successful WS upgrade.
type UpgradeContext struct {
	ConnID  uint32
	Request *http.Request // the original HTTP request — exposes cookies
}

// ConnManager manages all active connections (any transport).
type ConnManager struct {
	mu          sync.RWMutex
	conns       map[uint32]Transport
	remoteAddrs map[uint32]string // connID → remote address string (e.g. "1.2.3.4:5678")
	nextID      atomic.Uint32
	events      chan PlayerEvent
	onNewConn   func(connID uint32) // called when a new connection is established
	extraRoutes []route             // additional HTTP routes registered before ListenAndServe

	// OnUpgrade fires synchronously during HandleWebSocket, after the
	// WS upgrade succeeds and the connection is recorded but before
	// the connection's read loop starts. Used by the gateway to read
	// cookies set by /auth/* HTTPS endpoints.
	OnUpgrade func(UpgradeContext)

	// AllowedOrigins is the WebSocket Origin allowlist passed to
	// websocket.Accept's OriginPatterns. Empty = same-origin only. Requests
	// with no Origin header (native/non-browser clients) are always allowed —
	// this is deliberate (browsers always send Origin on WS upgrades, so this
	// is not a CSWSH gap; non-browser request integrity is the auth layer's
	// job). Entries are matched via path.Match against the Origin's host
	// (scheme-agnostic unless written "scheme://host"). The host includes a
	// port when the Origin carries one, so "*.example.com" matches
	// "app.example.com" but not "app.example.com:8443" — browsers omit default
	// ports, so this only matters for explicit non-standard ports.
	AllowedOrigins []string

	// limits bounds inbound frame size and per-connection queue depth for
	// every WebSocket connection this manager accepts. Guarded by mu
	// because SetWireLimits may run after the listener is up.
	limits WireLimits
}

type route struct {
	pattern string
	handler http.Handler
}

// NewConnManager creates a new connection manager.
func NewConnManager() *ConnManager {
	return &ConnManager{
		conns:       make(map[uint32]Transport),
		remoteAddrs: make(map[uint32]string),
		events:      make(chan PlayerEvent, 64),
		limits:      DefaultWireLimits(),
	}
}

// SetWireLimits installs the ingress limits applied to connections accepted
// from this point on. Non-positive fields fall back to their defaults.
// Connections already accepted keep the limits they were built with.
func (cm *ConnManager) SetWireLimits(l WireLimits) {
	cm.mu.Lock()
	cm.limits = l.Normalized()
	cm.mu.Unlock()
}

// WireLimits returns the ingress limits currently applied to new connections.
func (cm *ConnManager) WireLimits() WireLimits {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.limits.Normalized()
}

// Events returns the channel of player connect/disconnect events.
func (cm *ConnManager) Events() <-chan PlayerEvent {
	return cm.events
}

// Get returns a transport by ID (nil if not found).
func (cm *ConnManager) Get(id uint32) Transport {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.conns[id]
}

// Send sends a message unreliably (world updates, ephemeral state).
// For TCP transports this is identical to SendReliable.
func (cm *ConnManager) Send(connID uint32, data []byte) SendResult {
	cm.mu.RLock()
	t := cm.conns[connID]
	cm.mu.RUnlock()
	if t != nil {
		return t.SendUnreliable(data)
	}
	return SendResult{Disposition: SendNoRoute}
}

// DeliveryClassFor reports the delivery guarantee of connID's transport.
//
// Unknown connections and transports that do not implement
// DeliveryClassProvider report DeliveryReliableOrdered — the conservative
// answer, because it is what preserves the pre-existing AckReliable
// behaviour for every stub transport in the test suite.
func (cm *ConnManager) DeliveryClassFor(connID uint32) DeliveryClass {
	cm.mu.RLock()
	t := cm.conns[connID]
	cm.mu.RUnlock()
	if p, ok := t.(DeliveryClassProvider); ok {
		return p.DeliveryClass()
	}
	return DeliveryReliableOrdered
}

// SendReliable sends a message reliably (login, spawn, state changes).
func (cm *ConnManager) SendReliable(connID uint32, data []byte) SendResult {
	cm.mu.RLock()
	t := cm.conns[connID]
	cm.mu.RUnlock()
	if t != nil {
		return t.SendReliable(data)
	}
	return SendResult{Disposition: SendNoRoute}
}

// InjectInput appends data to the channel 0x00 input queue for a connection,
// as if the bytes had arrived from the client's transport. Used by the
// inter-cell input-forwarding path after a handoff commit.
// No-op if the connection does not exist.
func (cm *ConnManager) InjectInput(connID uint32, data []byte) {
	cm.mu.RLock()
	t := cm.conns[connID]
	cm.mu.RUnlock()
	if t != nil {
		t.InjectInput(data)
	}
}

// DrainInput drains all queued event messages (channel 0x00) for a connection.
func (cm *ConnManager) DrainInput(connID uint32) [][]byte {
	cm.mu.RLock()
	t := cm.conns[connID]
	cm.mu.RUnlock()
	if t == nil {
		return nil
	}
	return t.DrainInput()
}

// DrainOpInput drains all queued operation messages (channel 0x01) for a connection.
func (cm *ConnManager) DrainOpInput(connID uint32) [][]byte {
	cm.mu.RLock()
	t := cm.conns[connID]
	cm.mu.RUnlock()
	if t == nil {
		return nil
	}
	return t.DrainOpInput()
}

// ActiveConnIDs returns a snapshot of all active connection IDs.
func (cm *ConnManager) ActiveConnIDs() []uint32 {
	cm.mu.RLock()
	ids := make([]uint32, 0, len(cm.conns))
	for id := range cm.conns {
		ids = append(ids, id)
	}
	cm.mu.RUnlock()
	return ids
}

// TotalBytesSent returns the aggregate bytes sent across all active connections.
func (cm *ConnManager) TotalBytesSent() uint64 {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	var total uint64
	for _, t := range cm.conns {
		if bc, ok := t.(ByteCounter); ok {
			total += bc.BytesSent()
		}
	}
	return total
}

// TotalBytesRecv returns the aggregate bytes received across all active connections.
func (cm *ConnManager) TotalBytesRecv() uint64 {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	var total uint64
	for _, t := range cm.conns {
		if bc, ok := t.(ByteCounter); ok {
			total += bc.BytesRecv()
		}
	}
	return total
}

// ConnectionCount returns the number of active connections.
func (cm *ConnManager) ConnectionCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.conns)
}

// RemoteAddrString returns the remote address string (e.g. "1.2.3.4:5678") for
// a connection. Returns "" only when the registering caller had no peer address
// to record, or for unknown connIDs. Populated by HandleWebSocket from
// r.RemoteAddr and by AddTransport's remoteAddr argument.
func (cm *ConnManager) RemoteAddrString(connID uint32) string {
	cm.mu.RLock()
	addr := cm.remoteAddrs[connID]
	cm.mu.RUnlock()
	return addr
}

// Remove removes and closes a connection.
func (cm *ConnManager) Remove(id uint32) {
	cm.mu.Lock()
	t := cm.conns[id]
	delete(cm.conns, id)
	delete(cm.remoteAddrs, id)
	cm.mu.Unlock()
	if t != nil {
		t.Close()
	}
}

// Unregister removes a connection from tracking and fires a disconnect event.
// Unlike Remove, it does not close the transport (caller handles that).
func (cm *ConnManager) Unregister(id uint32) {
	cm.mu.Lock()
	delete(cm.conns, id)
	delete(cm.remoteAddrs, id)
	cm.mu.Unlock()
	cm.events <- PlayerEvent{ConnID: id, Disconnect: true}
}

func (cm *ConnManager) registerTransport(connID uint32, t Transport, remoteAddr string) {
	cm.mu.Lock()
	cm.conns[connID] = t
	if remoteAddr != "" {
		cm.remoteAddrs[connID] = remoteAddr
	}
	cm.mu.Unlock()
	cm.events <- PlayerEvent{ConnID: connID, Connected: true}
}

// AddTransport registers any Transport and returns its assigned connection ID.
//
// remoteAddr is the peer's address in "host:port" form. It is required, not
// decorative: ops.ParseClientIP feeds it to the auth service's per-IP login
// rate limiter and to the audit log. Passing "" collapses every such connection
// into a single shared rate-limit bucket and records a null IP, so only pass ""
// for transports that genuinely have no peer address (in-process test stubs).
func (cm *ConnManager) AddTransport(t Transport, remoteAddr string) uint32 {
	connID := cm.nextID.Add(1)
	cm.registerTransport(connID, t, remoteAddr)
	return connID
}

// HandleWebSocket is the HTTP handler for WebSocket upgrades.
func (cm *ConnManager) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: cm.AllowedOrigins,
	})
	if err != nil {
		log.Printf("websocket accept error: %v", err)
		return
	}

	// Bound the largest message the WebSocket layer will read at all. Without
	// this the ceiling is coder/websocket's own defaultReadLimit — the right
	// number, but an accidental dependency default rather than a decision we
	// own. Exceeding it fails the read, which ends the pump and closes the
	// connection; that is the intended answer for a peer sending frames the
	// protocol has no shape for.
	limits := cm.WireLimits()
	ws.SetReadLimit(int64(limits.MaxFrameBytes))

	// Allocate the ID before the write pump starts. Conn.id is immutable after
	// construction, so writers, diagnostics, and event consumers can never race
	// with a post-publication ID assignment.
	connID := cm.nextID.Add(1)
	conn := newConn(connID, ws, limits)
	t := NewWSTransport(conn)
	// Record the remote address for auth handlers (rate limiting, audit logs).
	// r.RemoteAddr is set by the HTTP server from the TCP connection before any
	// proxy header processing — it is the direct peer address (e.g. "1.2.3.4:5678").
	cm.registerTransport(connID, t, r.RemoteAddr)

	// Fire the upgrade hook synchronously before the read loop starts.
	// The original *http.Request is still in scope here, so cookies are
	// accessible. After this point only WS frames flow.
	if cm.OnUpgrade != nil {
		cm.OnUpgrade(UpgradeContext{ConnID: connID, Request: r})
	}

	// Run read pump (blocks until disconnect)
	conn.readPump(r.Context())

	// Player disconnected — unregister from ConnManager
	cm.Unregister(connID)
}

// Handle registers an additional HTTP route on the server mux.
// Must be called before ListenAndServe.
func (cm *ConnManager) Handle(pattern string, handler http.Handler) {
	cm.extraRoutes = append(cm.extraRoutes, route{pattern, handler})
}

// HandleConnStats returns a JSON snapshot of every active connection's
// write-path counters. Mounted at /debug/conn-stats by the production
// HTTP listener in pkg/universe/bootstrap.go.
//
// Layout: { "connections": [ {ConnStats}, … ] }
//
// Used by the Bun probe — it polls this after a 10s session to assert
// "server-side mean write duration < 1ms", etc. — and by the in-browser
// diagnostic overlay.
func (cm *ConnManager) HandleConnStats(w http.ResponseWriter, _ *http.Request) {
	cm.mu.RLock()
	stats := make([]ConnStats, 0, len(cm.conns))
	for _, t := range cm.conns {
		if p, ok := t.(ConnStatsProvider); ok {
			stats = append(stats, p.Stats())
		}
	}
	cm.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"connections": stats,
	})
}

// ListenAndServe starts the WebSocket server.
func (cm *ConnManager) ListenAndServe(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", cm.HandleWebSocket)
	// Diagnostic heartbeat endpoint — emits a 32-byte frame every 50ms
	// directly via ws.Write (no game logic, no outbound channel). Used by
	// the Bun probe (just probe) and the in-browser diagnostic to
	// isolate "is the network path itself bursty?" from game-side issues.
	mux.HandleFunc("/probe-ws", HandleProbeWS)
	// Per-connection write-path stats (mean/max queue + write duration,
	// slow-event counts). Lets the probe / diagnostic UI assert that the
	// server itself emitted on time vs. that downstream layers buffered.
	mux.HandleFunc("/debug/conn-stats", cm.HandleConnStats)
	for _, r := range cm.extraRoutes {
		mux.Handle(r.pattern, r.handler)
	}

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		server.Close()
	}()

	log.Printf("game server listening on %s", addr)
	return server.ListenAndServe()
}
