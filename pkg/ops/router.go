package ops

import (
	"context"
	"log"
	"net/netip"
	"sync"
	"time"

	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

// EventCode is any integer type usable as an op code (proto enums are int32).
type EventCode interface{ ~int32 | ~uint32 }

// OpContext provides identity and connection info to operation handlers.
type OpContext struct {
	ConnID   uint32
	Username string
	ClientIP netip.Addr // populated by gateway from WS RemoteAddr (or X-Forwarded-For when trusted)

	// SystemTrusted indicates the op was issued by a trusted in-process
	// caller (e.g. a cmdsys SourceConsole console wrapper) whose
	// authorization was already enforced upstream. Identity-based handler
	// auth (e.g. chat's connID → user_id → hasGlobalAdmin chain) should
	// short-circuit when set. Never set on frames arriving from clients.
	SystemTrusted bool

	bag sync.Map
}

// Bag returns a per-context key/value store used to thread connection-
// bound state from the gateway into handlers (e.g. session token).
func (c *OpContext) Bag() *sync.Map { return &c.bag }

// DrainSource is the abstraction the Router needs to poll and respond on
// behalf of a process's typed-op surface. The WebSocket-side
// *net.ConnManager and the host-side *universe.VirtualConnManager both
// satisfy it: the former drains direct WS clients, the latter drains
// gateway-forwarded ClientInput frames queued per VCM-local connID.
//
// Decoupling Router from a concrete ConnManager lets the same poll loop
// run on any process that needs to dispatch typed-ops — gateway, host,
// or future service processes — without duplicating drain machinery.
type DrainSource interface {
	ActiveConnIDs() []uint32
	DrainOpInput(connID uint32) [][]byte
	SendReliable(connID uint32, data []byte) pkgnet.SendResult
	RemoteAddrString(connID uint32) string
}

// Router polls all connections for channel-0x01 messages and dispatches
// them to the typed-op handler. Plan 2 Phase 5 retired the legacy
// code-keyed proto-op router — every 0x01 frame now rides through the
// typed-op dispatcher; the Router struct exists primarily as the host
// for the poll goroutine and the connection-manager handle.
type Router struct {
	mu       sync.RWMutex
	src      DrainSource
	sessions *PlayerSessions

	// typedOpHandler is the typed-op dispatcher. Wired by pkg/universe in
	// the gateway role; nil leaves all 0x01 frames silently dropped.
	typedOpHandler TypedOpHandler

	// framesPerPoll caps how many op frames one poll pass dispatches across
	// ALL connections. Zero means defaultFramesPerPoll.
	framesPerPoll int
}

// defaultFramesPerPoll bounds the work one poll pass may do. The loop ticks at
// 200 Hz and, before this cap, drained every connection's op queue to empty on
// every pass — one process-wide goroutine whose cost was set by how fast the
// clients chose to send. The per-connection share is bounded separately, by
// each drain source's MaxFramesPerDrain; whatever this cap leaves behind stays
// queued (behind MaxOpQueueDepth) for the next pass 5 ms later.
//
// 1024 frames per pass is ~200k op frames/second sustained, far above any
// legitimate op load, while still being a bound.
const defaultFramesPerPoll = 1024

// pollBudget returns the per-pass frame cap.
func (r *Router) pollBudget() int {
	if r.framesPerPoll <= 0 {
		return defaultFramesPerPoll
	}
	return r.framesPerPoll
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

// NewRouter creates a new operation router. The drain source is the
// connection surface to poll for typed-op frames; pkg/universe replaces
// it via SetDrainSource on remote-host processes (where the host's VCM
// owns the queues, not the unused-on-host WS ConnManager).
func NewRouter(src DrainSource, sessions *PlayerSessions) *Router {
	return &Router{
		src:      src,
		sessions: sessions,
	}
}

// SetDrainSource replaces the drain source the poll loop reads from.
// Used by pkg/universe to swap a host's drain target from the WS
// ConnManager (created upfront in cmd/server/main.go before the Process
// knows its mode) to the VirtualConnManager (created later inside the
// remote-host setup path). Safe to call before Run starts.
func (r *Router) SetDrainSource(src DrainSource) {
	r.mu.Lock()
	r.src = src
	r.mu.Unlock()
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
	r.mu.RLock()
	src := r.src
	r.mu.RUnlock()
	if src == nil {
		return
	}
	if r.typedOpHandler == nil {
		// Drain frames anyway so they don't accumulate in the per-conn
		// queue, but drop them — no dispatcher means no possible handler.
		for _, connID := range src.ActiveConnIDs() {
			src.DrainOpInput(connID)
		}
		return
	}
	budget := r.pollBudget()
	dispatched := 0
	for _, connID := range src.ActiveConnIDs() {
		if dispatched >= budget {
			// Remaining connections keep their queued frames; the next
			// tick 5 ms from now picks them up. ActiveConnIDs order is
			// unspecified, so this is not a stable per-connection
			// starvation: the set is re-walked from scratch each pass.
			log.Printf("ops: poll budget %d frames exhausted, deferring remaining connections", budget)
			return
		}
		msgs := src.DrainOpInput(connID)
		for _, raw := range msgs {
			username := r.sessions.Get(connID)
			ctx := &OpContext{
				ConnID:   connID,
				Username: username,
				ClientIP: ParseClientIP(src.RemoteAddrString(connID)),
			}
			r.dispatchOne(src, connID, raw, ctx)
			dispatched++
		}
	}
}

// dispatchOne runs one op frame through the typed-op handler behind a per-frame
// recover barrier.
//
// PER FRAME, not per goroutine: poll is the 0x01 drain for every connection on
// the single-process `all` preset (runSessionPump is launched only when
// g.hostNetwork != nil) and for host processes via SetDrainSource. Letting a
// panic unwind the poll loop would take down op handling process-wide, and
// letting it escape Run's goroutine would take down the process — the frame
// that triggers it is attacker-supplied and, on the auth path, pre-
// authentication.
//
// The barrier is here rather than inside the handler so the codec fuzz targets
// still observe raw panics.
func (r *Router) dispatchOne(src DrainSource, connID uint32, raw []byte, ctx *OpContext) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("ops: op frame panic conn=%d bytes=%d panic=%v", connID, len(raw), rec)
		}
	}()
	if respFrame := r.typedOpHandler(raw, ctx); respFrame != nil {
		src.SendReliable(connID, respFrame)
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
