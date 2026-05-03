package universe

import (
	"reflect"
	"sync"
)

// EntityCtor builds the typed mmokit.Entity passed to handlers. Provided by
// the mmokit layer at handler-registration time so this package avoids an
// import cycle. Returns `any` here; the receiving handler reflects it as
// the concrete mmokit.Entity type.
type EntityCtor func(stage *Stage, netID uint32) any

// MessageDispatcher routes typed messages to registered handlers. One
// dispatcher per Stage. Handlers are keyed by message type name (Go
// reflect.Type.Name()) for cross-cell wire compatibility.
type MessageDispatcher struct {
	stage    *Stage
	mu       sync.RWMutex
	handlers map[string]reflect.Value // type name → handler reflect.Value
	types    map[string]reflect.Type  // type name → message type for decode
	ctor     EntityCtor               // set once by SetEntityCtor
}

func newMessageDispatcher(s *Stage) *MessageDispatcher {
	return &MessageDispatcher{
		stage:    s,
		handlers: make(map[string]reflect.Value),
		types:    make(map[string]reflect.Type),
	}
}

// SetEntityCtor installs the callback the dispatcher uses to build typed
// Entity values for handler invocations. Idempotent — second and later
// calls with the same ctor are no-ops; mismatched ctors panic.
func (d *MessageDispatcher) SetEntityCtor(c EntityCtor) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctor != nil {
		if reflect.ValueOf(d.ctor).Pointer() != reflect.ValueOf(c).Pointer() {
			panic("mmokit: conflicting EntityCtor on dispatcher")
		}
		return
	}
	d.ctor = c
}

// Register installs fn as the handler for messages of the named type. fn
// must have signature func(target Entity, msg *M). Panics if a handler is
// already registered for that type name (one handler per type).
func (d *MessageDispatcher) Register(typeName string, msgType reflect.Type, fn reflect.Value) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.handlers[typeName]; exists {
		panic("mmokit: handler already registered for " + typeName)
	}
	d.handlers[typeName] = fn
	d.types[typeName] = msgType
}

// Invoke synchronously calls the handler for msg, with target bound to the
// given netID on this dispatcher's stage. No-op if no handler is registered
// or no entity ctor is set. msgPtr must be a pointer to a value of the
// registered type.
func (d *MessageDispatcher) Invoke(targetNetID uint32, msgPtr any) {
	typeName := reflect.TypeOf(msgPtr).Elem().Name()
	d.mu.RLock()
	fn, ok := d.handlers[typeName]
	ctor := d.ctor
	d.mu.RUnlock()
	if !ok || ctor == nil {
		return
	}
	target := ctor(d.stage, targetNetID)
	fn.Call([]reflect.Value{
		reflect.ValueOf(target),
		reflect.ValueOf(msgPtr),
	})
}

// MessageType returns the registered Go type for a type name, or nil if
// unregistered. Used by the cross-cell decoder (Phase 6) to allocate a
// fresh value of the right type.
func (d *MessageDispatcher) MessageType(typeName string) reflect.Type {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.types[typeName]
}
