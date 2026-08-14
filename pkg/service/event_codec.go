package service

import (
	"fmt"
	"reflect"
	"sync"
)

// EventTypeName returns the canonical wire identifier for event type T:
// the package-qualified Go type name (e.g.
// "github.com/zenion/mmokit/pkg/service.SessionEnterEvent").
//
// Renames break the wire — same convention as the typed-event channel in
// mmokit. Phase 3 carries this string in MeshFrame.ServiceEvent.type_name;
// Phase 1 uses it only as the registry key so the API is stable.
func EventTypeName[T any]() string {
	t := reflect.TypeFor[T]()
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
	t := reflect.TypeFor[T]()
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

var (
	wireMu          sync.RWMutex
	wireEligibleSet = map[string]struct{}{}
)

// RegisterWireEvent declares that event type T is safe to ship across
// processes via the service bus's MeshData fan-out. Events default to
// process-local-only — Bus.Publish[T] will fan out to remote subscribers
// only when T has been registered as wire-eligible here.
//
// **Use deliberately.** Wire-eligible events are visible to every
// service on every process that subscribes to them. Events carrying
// bearer credentials, PII, or any value that should not leave the
// authoring process must NOT be wire-eligible. (See spec §14.4 for the
// long-term plan: per-event capability gating; until that lands,
// "deliberate opt-in" is the gate.)
//
// Calling RegisterWireEvent[T]() also implies RegisterEventType[T]() —
// the type is registered in the codec registry first if it wasn't
// already, so cross-process receivers can decode it.
//
// Idempotent for the same T.
func RegisterWireEvent[T any]() {
	RegisterEventType[T]() // idempotent — guarantees codec entry
	name := EventTypeName[T]()
	wireMu.Lock()
	defer wireMu.Unlock()
	wireEligibleSet[name] = struct{}{}
}

// IsWireEligible reports whether the type with the given canonical name
// is registered as wire-eligible (i.e. safe for cross-process fan-out).
//
// Bus.peerIDsExceptSelf consults this before returning peer IDs so that
// a Publish for a non-wire-eligible type with remote subscribers in the
// routing cache silently drops the remote fan-out — same behavior as
// "no remote subscribers."
func IsWireEligible(name string) bool {
	wireMu.RLock()
	defer wireMu.RUnlock()
	_, ok := wireEligibleSet[name]
	return ok
}
