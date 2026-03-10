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

// ConnManager manages all active connections (any transport).
type ConnManager struct {
	mu          sync.RWMutex
	conns       map[uint32]Transport
	nextID      atomic.Uint32
	events      chan PlayerEvent
	onNewConn   func(connID uint32) // called when a new connection is established
}

// NewConnManager creates a new connection manager.
func NewConnManager() *ConnManager {
	return &ConnManager{
		conns:  make(map[uint32]Transport),
		events: make(chan PlayerEvent, 64),
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

// SendReliable sends a message reliably (login, death, spawn, sell results).
func (cm *ConnManager) SendReliable(connID uint32, data []byte) {
	cm.mu.RLock()
	t := cm.conns[connID]
	cm.mu.RUnlock()
	if t != nil {
		t.SendReliable(data)
	}
}

// DrainInput drains all queued input messages for a connection.
func (cm *ConnManager) DrainInput(connID uint32) [][]byte {
	cm.mu.RLock()
	t := cm.conns[connID]
	cm.mu.RUnlock()
	if t == nil {
		return nil
	}
	return t.DrainInput()
}

// Remove removes and closes a connection.
func (cm *ConnManager) Remove(id uint32) {
	cm.mu.Lock()
	t := cm.conns[id]
	delete(cm.conns, id)
	cm.mu.Unlock()
	if t != nil {
		t.Close()
	}
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
	t := NewWSTransport(conn)
	connID := cm.AddTransport(t)
	conn.id = connID

	// Run read pump (blocks until disconnect)
	conn.readPump(r.Context())

	// Player disconnected
	cm.mu.Lock()
	delete(cm.conns, connID)
	cm.mu.Unlock()

	cm.events <- PlayerEvent{ConnID: connID, Disconnect: true}
}

// ListenAndServe starts the WebSocket server.
func (cm *ConnManager) ListenAndServe(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", cm.HandleWebSocket)

	// Serve static files for the web test client and generated code
	mux.Handle("/gen/", http.StripPrefix("/gen/", http.FileServer(http.Dir("gen"))))
	mux.Handle("/", http.FileServer(http.Dir("web")))

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
