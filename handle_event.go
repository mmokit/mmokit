package mmokit

import (
	"fmt"
	"reflect"
	"sort"
	"sync"

	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

var (
	seMu     sync.RWMutex
	seByType = map[uint32]reflect.Type{}
	seSet    = map[reflect.Type]struct{}{}
)

// RegisterEvent registers T as a server→client typed event. Subsequent calls
// to Stage.SendEvent[T] use the FNV-1a typeID derived from T to identify the
// frame on the wire; the SDK codegen iterates this registry to emit per-event
// TS decoder classes and per-event onXxx handlers.
//
// Idempotent for the same Go type — safe to call from per-cell System.Init()
// where it would otherwise fire N times. Two distinct types hashing to the
// same typeID is a panic — collision should never happen at codebase scale;
// if it does, rename one type.
func RegisterEvent[T any]() {
	t := reflect.TypeFor[T]()
	// Registration-time shape check. ValidateMessageType, never
	// ValidateComponentType: twelve production wire types carry slice fields
	// and the component validator rejects every one of them.
	pkguniverse.ValidateMessageType(t)
	id := TypeIDOf(t)

	seMu.Lock()
	defer seMu.Unlock()
	if _, ok := seSet[t]; ok {
		return
	}
	if existing, ok := seByType[id]; ok && existing != t {
		panic(fmt.Sprintf("RegisterEvent: typeID collision between %s and %s (id=%#x)",
			existing.String(), t.String(), id))
	}
	// Also claim the id across the shared downstream namespace, which catches
	// the collision this registry cannot see on its own: a broadcast type
	// hashing to the same id. Clients dispatch both through one switch.
	claimDownstreamTypeID("RegisterEvent", id, t)
	seByType[id] = t
	seSet[t] = struct{}{}
}

// LookupServerEventType returns the Go type registered for the given typeID,
// or (nil, false) if none.
func LookupServerEventType(id uint32) (reflect.Type, bool) {
	seMu.RLock()
	defer seMu.RUnlock()
	t, ok := seByType[id]
	return t, ok
}

// RegisteredServerEventTypes returns the registered types in deterministic
// (alphabetical) order. Used by sdkgen and protocol-schema export.
func RegisteredServerEventTypes() []reflect.Type {
	seMu.RLock()
	defer seMu.RUnlock()
	out := make([]reflect.Type, 0, len(seSet))
	for t := range seSet {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// ResetServerEventRegistryForTest is exported for tests only.
func ResetServerEventRegistryForTest() {
	seMu.Lock()
	defer seMu.Unlock()
	// Release this registry's claims on the shared downstream namespace too.
	// Without it, a test that resets and then registers a DIFFERENT type
	// hashing to a previously-claimed id would panic on a collision with a
	// registration that no longer exists.
	releaseDownstreamTypeIDs(seByType)
	seByType = map[uint32]reflect.Type{}
	seSet = map[reflect.Type]struct{}{}
}
