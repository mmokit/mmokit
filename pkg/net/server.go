package net

import (
	"context"
	"log"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

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
	mu               sync.RWMutex
	conns            map[uint32]Transport
	remoteAddrs      map[uint32]string // connID → remote address string (e.g. "1.2.3.4:5678")
	nextID           atomic.Uint32
	events           chan PlayerEvent
	onNewConn        func(connID uint32) // called when a new connection is established
	EventInterceptor EventInterceptor    // if set, called for each event frame before queuing
	extraRoutes      []route             // additional HTTP routes registered before ListenAndServe

	// OnUpgrade fires synchronously during HandleWebSocket, after the
	// WS upgrade succeeds and the connection is recorded but before
	// the connection's read loop starts. Used by the gateway to read
	// cookies set by /auth/* HTTPS endpoints.
	OnUpgrade func(UpgradeContext)
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
	}
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
func (cm *ConnManager) Send(connID uint32, data []byte) {
	cm.mu.RLock()
	t := cm.conns[connID]
	cm.mu.RUnlock()
	if t != nil {
		t.SendUnreliable(data)
	}
}

// SendReliable sends a message reliably (login, spawn, state changes).
func (cm *ConnManager) SendReliable(connID uint32, data []byte) {
	cm.mu.RLock()
	t := cm.conns[connID]
	cm.mu.RUnlock()
	if t != nil {
		t.SendReliable(data)
	}
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
// a connection. Returns "" for connections registered via AddTransport (non-WS),
// or for unknown connIDs. Populated by HandleWebSocket from r.RemoteAddr.
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

// AddTransport registers any Transport and returns its assigned connection ID.
func (cm *ConnManager) AddTransport(t Transport) uint32 {
	connID := cm.nextID.Add(1)
	cm.mu.Lock()
	cm.conns[connID] = t
	cm.mu.Unlock()
	cm.events <- PlayerEvent{ConnID: connID, Connected: true}
	return connID
}

// HandleWebSocket is the HTTP handler for WebSocket upgrades.
func (cm *ConnManager) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Allow any origin for development
	})
	if err != nil {
		log.Printf("websocket accept error: %v", err)
		return
	}

	conn := newConn(0, ws) // ID assigned by AddTransport
	conn.eventInterceptor = cm.EventInterceptor
	t := NewWSTransport(conn)
	connID := cm.AddTransport(t)
	conn.id = connID

	// Record the remote address for auth handlers (rate limiting, audit logs).
	// r.RemoteAddr is set by the HTTP server from the TCP connection before any
	// proxy header processing — it is the direct peer address (e.g. "1.2.3.4:5678").
	if r.RemoteAddr != "" {
		cm.mu.Lock()
		cm.remoteAddrs[connID] = r.RemoteAddr
		cm.mu.Unlock()
	}

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

// ListenAndServe starts the WebSocket server.
func (cm *ConnManager) ListenAndServe(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", cm.HandleWebSocket)
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
