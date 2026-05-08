package service

import (
	"reflect"
	"sync"
)

// Bus is the typed publish/subscribe primitive owned by Process. Phase 1
// is process-local only — Publish[T] dispatches synchronously to every
// local subscriber registered for T. Phase 3 will add cross-process
// peer-mesh fan-out behind the same API.
type Bus struct {
	processID string

	mu       sync.RWMutex
	handlers map[reflect.Type][]*handlerSlot
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
// detection; in Phase 1 it's stored only for diagnostic logging.
func NewBus(processID string) *Bus {
	return &Bus{
		processID: processID,
		handlers:  map[reflect.Type][]*handlerSlot{},
	}
}

// Subscribe registers a handler for events of type T. Multiple handlers
// per type are allowed; they fire in registration order. Handlers run on
// the caller's goroutine inside Publish — keep them fast.
//
// The returned Unsubscribe is idempotent.
func Subscribe[T any](b *Bus, handler func(T)) Unsubscribe {
	var zero T
	typ := reflect.TypeOf(zero)
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
	return func() {
		b.mu.Lock()
		slot.dead = true
		b.mu.Unlock()
	}
}

// Publish broadcasts ev to every local subscriber for type T.
// Synchronous. Handler panics are recovered and discarded so one bad
// subscriber cannot poison the publisher; callers that need to debug
// panicking handlers should set GODEBUG=panicnil=1 or wrap their
// handler explicitly.
func Publish[T any](b *Bus, ev T) {
	publishAny(b, reflect.TypeOf(ev), ev)
}

// PublishLocal is identical to Publish in Phase 1. Phase 3 will diverge:
// Publish fans out to remote subscribers, PublishLocal does not.
func PublishLocal[T any](b *Bus, ev T) {
	publishAny(b, reflect.TypeOf(ev), ev)
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
		invokeHandler(s.fn, ev)
	}
}

func invokeHandler(fn func(any), ev any) {
	defer func() {
		_ = recover() // swallow handler panics; callers verify behavior via side-effects in tests
	}()
	fn(ev)
}
