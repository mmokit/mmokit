package ops

import (
	"context"
	"log"
	"net/netip"
	"sync"
	"time"

	"github.com/zenion/mmoserver/pkg/net"
)

// EventCode is any integer type usable as an op code (proto enums are int32).
type EventCode interface{ ~int32 | ~uint32 }

// OpContext provides identity and connection info to operation handlers.
type OpContext struct {
	ConnID   uint32
	Username string
	ClientIP netip.Addr // populated by gateway from WS RemoteAddr (or X-Forwarded-For when trusted)

	bag sync.Map
}

// Bag returns a per-context key/value store used to thread connection-
// bound state from the gateway into handlers (e.g. session token).
func (c *OpContext) Bag() *sync.Map { return &c.bag }

// Router polls all connections for channel-0x01 messages and dispatches
// them to the typed-op handler. Plan 2 Phase 5 retired the legacy
// code-keyed proto-op router — every 0x01 frame now rides through the
// typed-op dispatcher; the Router struct exists primarily as the host
// for the poll goroutine and the connection-manager handle.
type Router struct {
	connMgr  *net.ConnManager // concrete type intentional: uses gateway-only ActiveConnIDs()
	sessions *PlayerSessions

	// typedOpHandler is the typed-op dispatcher. Wired by pkg/universe in
	// the gateway role; nil leaves all 0x01 frames silently dropped.
	typedOpHandler TypedOpHandler
}

// TypedOpHandler is the inbound typed-op dispatcher. It consumes a 0x01
// payload (channel byte stripped), allocates the registered request
// type, decodes the body, calls the handler, and returns the encoded
// response frame (channel byte included). Returns nil for structurally
// invalid frames or successful async dispatches whose responses ship
// later via the cell engine's connection manager.
type TypedOpHandler func(payload []byte, ctx *OpContext) []byte

// SetTypedOpHandler installs the typed-op dispatcher. Called once during
// gateway wiring.
func (r *Router) SetTypedOpHandler(h TypedOpHandler) {
	r.typedOpHandler = h
}

// NewRouter creates a new operation router.
func NewRouter(connMgr *net.ConnManager, sessions *PlayerSessions) *Router {
	return &Router{
		connMgr:  connMgr,
		sessions: sessions,
	}
}

// Run starts the poll loop. Blocks until ctx is done.
func (r *Router) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.poll()
		}
	}
}

func (r *Router) poll() {
	if r.typedOpHandler == nil {
		// Drain frames anyway so they don't accumulate in the per-conn
		// queue, but drop them — no dispatcher means no possible handler.
		for _, connID := range r.connMgr.ActiveConnIDs() {
			r.connMgr.DrainOpInput(connID)
		}
		return
	}
	for _, connID := range r.connMgr.ActiveConnIDs() {
		msgs := r.connMgr.DrainOpInput(connID)
		for _, raw := range msgs {
			username := r.sessions.Get(connID)
			ctx := &OpContext{
				ConnID:   connID,
				Username: username,
				ClientIP: ParseClientIP(r.connMgr.RemoteAddrString(connID)),
			}
			if respFrame := r.typedOpHandler(raw, ctx); respFrame != nil {
				r.connMgr.SendReliable(connID, respFrame)
			}
		}
	}
}

// ConnIDForUsername returns the connID for a given username, or 0 if not found.
func (r *Router) ConnIDForUsername(username string) uint32 {
	r.sessions.mu.RLock()
	defer r.sessions.mu.RUnlock()
	for connID, name := range r.sessions.sessions {
		if name == username {
			return connID
		}
	}
	return 0
}

// ParseClientIP extracts a netip.Addr from a "host:port" or bare "host" address
// string (as returned by net.ConnManager.RemoteAddrString). Returns the zero
// value on any parse failure — callers must tolerate an invalid addr.
func ParseClientIP(addrStr string) netip.Addr {
	if addrStr == "" {
		return netip.Addr{}
	}
	// Try host:port first (the common case from HTTP r.RemoteAddr).
	if ap, err := netip.ParseAddrPort(addrStr); err == nil {
		return ap.Addr().Unmap()
	}
	// Fall back to bare address (no port).
	if a, err := netip.ParseAddr(addrStr); err == nil {
		return a.Unmap()
	}
	return netip.Addr{}
}

func init() {
	_ = log.Println
}
