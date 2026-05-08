package service

import (
	"reflect"
	"runtime/debug"
	"sync"
)

// Bus is the typed publish/subscribe primitive owned by Process. Phase 1
// is process-local only — Publish[T] dispatches synchronously to every
// local subscriber registered for T. Phase 3 will add cross-process
// peer-mesh fan-out behind the same API.
//
// processID is the per-process identifier used by Phase 3 self-echo skip.
// It lives under remoteMu (in remoteState) — readers of processID
// (peerIDsExceptSelf) already hold remoteMu, so co-locating the field
// keeps every accessor on a single lock and gives universe a hook
// (SetProcessID) to align the at-construction-time identifier with the
// post-Build gateway-derived ID without racing the publish path.
type Bus struct {
	mu          sync.RWMutex
	handlers    map[reflect.Type][]*handlerSlot
	panicLogger PanicLogger

	// Phase 3 fields (see bus_remote.go).
	remoteState
}

// Unsubscribe removes a registered handler. Idempotent; safe to call from
// any goroutine, including from inside the handler being unsubscribed.
type Unsubscribe func()

// handlerSlot wraps a handler + a tombstone bit so Unsubscribe is O(1)
// and re-entrant from inside Publish without mutating the slice the
// dispatch loop is iterating.
type handlerSlot struct {
	fn   func(any)
	dead bool
}

// NewBus returns an empty Bus. processID is used by Phase 3 self-echo
// detection; in Phase 1 it's stored only for diagnostic logging. The
// processID can be updated post-construction via SetProcessID — universe
// uses this to align the bus identifier with the post-Build gateway-
// derived ID (auto-generated gateway IDs are populated during Build,
// after Bus construction).
func NewBus(processID string) *Bus {
	b := &Bus{
		handlers: map[reflect.Type][]*handlerSlot{},
	}
	b.processID = processID
	return b
}

// SetProcessID updates the bus's process identifier. Used by universe
// to align the bus's self-echo-skip identifier with the gateway-derived
// process ID resolved during Build (which may differ from the
// at-construction-time identifier when GatewayID is auto-generated).
func (b *Bus) SetProcessID(id string) {
	b.remoteMu.Lock()
	defer b.remoteMu.Unlock()
	b.processID = id
}

// Subscribe registers a handler for events of type T. Multiple handlers
// per type are allowed; they fire in registration order. Handlers run on
// the caller's goroutine inside Publish — keep them fast.
//
// The returned Unsubscribe is idempotent.
func Subscribe[T any](b *Bus, handler func(T)) Unsubscribe {
	typ := reflect.TypeFor[T]()
	if typ == nil {
		// T is an interface or untyped nil — Subscribe[interface{}] is
		// disallowed. The Bus is type-keyed; an interface key would
		// match every event.
		panic("service.Subscribe: T must be a concrete type, not an interface")
	}
	slot := &handlerSlot{
		fn: func(v any) { handler(v.(T)) },
	}
	b.mu.Lock()
	b.handlers[typ] = append(b.handlers[typ], slot)
	b.mu.Unlock()
	b.notifySubscriptionChanged()
	return func() {
		b.mu.Lock()
		slot.dead = true
		b.mu.Unlock()
		b.notifySubscriptionChanged()
	}
}

// Publish broadcasts ev to every local subscriber for type T.
// Synchronous. Handler panics are recovered and discarded so one bad
// subscriber cannot poison the publisher; callers that need to debug
// panicking handlers should set GODEBUG=panicnil=1 or wrap their
// handler explicitly.
func Publish[T any](b *Bus, ev T) {
	typ := reflect.TypeFor[T]()
	publishAny(b, typ, ev)
	name := typeName(typ)
	peers := b.peerIDsExceptSelf(name)
	if len(peers) > 0 {
		b.notifyRemote(name, peers, ev)
	}
}

// PublishLocal is identical to Publish in Phase 1. Phase 3 will diverge:
// Publish fans out to remote subscribers, PublishLocal does not.
func PublishLocal[T any](b *Bus, ev T) {
	publishAny(b, reflect.TypeFor[T](), ev)
}

// publishAny is the type-erased dispatch core, shared by Publish and
// PublishLocal (and Phase 3's cross-process receiver).
func publishAny(b *Bus, typ reflect.Type, ev any) {
	b.mu.RLock()
	slots := b.handlers[typ]
	// Snapshot so we can drop the lock before invoking handlers — handlers
	// may call Subscribe/Unsubscribe re-entrantly.
	live := make([]*handlerSlot, 0, len(slots))
	for _, s := range slots {
		if !s.dead {
			live = append(live, s)
		}
	}
	b.mu.RUnlock()
	for _, s := range live {
		invokeHandler(b, typ, s.fn, ev)
	}
}

// PanicLogger receives diagnostic info when a handler panics. The Bus
// recovers panics so one bad subscriber can't poison sibling handlers,
// but a vanished panic is undebuggable in production. Universe wires
// this to log under "services:bus" with a stack trace.
type PanicLogger func(typeName, processID string, panicValue any, stack []byte)

// SetPanicLogger installs a diagnostic callback for panics caught by the
// Bus's per-handler recover. nil disables logging (panics are still
// recovered; they just leave no trace).
func (b *Bus) SetPanicLogger(fn PanicLogger) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.panicLogger = fn
}

func invokeHandler(b *Bus, typ reflect.Type, fn func(any), ev any) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		b.mu.RLock()
		log := b.panicLogger
		b.mu.RUnlock()
		if log != nil {
			b.remoteMu.RLock()
			pid := b.processID
			b.remoteMu.RUnlock()
			log(typ.String(), pid, r, debug.Stack())
		}
	}()
	fn(ev)
}
