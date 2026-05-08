package service

import (
	"fmt"
	"reflect"
	"sync"
)

// EventTypeName returns the canonical wire identifier for event type T:
// the package-qualified Go type name (e.g.
// "github.com/zenion/mmoserver/pkg/service.SessionEnterEvent").
//
// Renames break the wire — same convention as the typed-event channel in
// mmokit. Phase 3 carries this string in MeshFrame.ServiceEvent.type_name;
// Phase 1 uses it only as the registry key so the API is stable.
func EventTypeName[T any]() string {
	var zero T
	t := reflect.TypeOf(zero)
	if t == nil {
		panic("service.EventTypeName: T must be a concrete struct type")
	}
	return typeName(t)
}

func typeName(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.PkgPath() == "" {
		// Builtin / unnamed — guard so we never silently collide.
		return t.String()
	}
	// PkgPath()/Name() avoids the "main." prefix Go uses in t.String() and
	// disambiguates two packages that share a Name.
	return t.PkgPath() + "." + t.Name()
}

var (
	eventTypeMu sync.RWMutex
	eventTypes  = map[string]reflect.Type{}
)

// RegisterEventType registers T's type-name → reflect.Type mapping in the
// process-global registry. Idempotent for the same T; panics on a name
// collision between two distinct Go types (almost impossible — Go's
// PkgPath qualification handles namespacing).
//
// Typically called from package init() of every package that declares
// event types so all processes that link the package have the registry
// pre-populated. Phase 3 receivers consult this registry to decode wire
// payloads back into Go values.
func RegisterEventType[T any]() {
	var zero T
	t := reflect.TypeOf(zero)
	if t == nil {
		// T is an interface or untyped nil — RegisterEventType[interface{}]
		// is disallowed. The registry is type-keyed; an interface key would
		// alias every concrete value.
		panic("service.RegisterEventType: T must be a concrete type, not an interface")
	}
	name := typeName(t)
	eventTypeMu.Lock()
	defer eventTypeMu.Unlock()
	if existing, ok := eventTypes[name]; ok {
		if existing == t {
			return // idempotent
		}
		panic(fmt.Sprintf("service.RegisterEventType: name %q already registered for %v (new=%v)", name, existing, t))
	}
	eventTypes[name] = t
}

// LookupEventType returns the reflect.Type registered under name, if any.
func LookupEventType(name string) (reflect.Type, bool) {
	eventTypeMu.RLock()
	defer eventTypeMu.RUnlock()
	t, ok := eventTypes[name]
	return t, ok
}
