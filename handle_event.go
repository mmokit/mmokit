package mmokit

import (
	"reflect"

	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// RegisterEvent registers T as a server→client typed event. Subsequent calls
// to SendEvent[T] use the FNV-1a typeID derived from T to identify the frame
// on the wire; the SDK codegen iterates this registry to emit per-event TS
// decoder classes and per-event onXxx handlers.
//
// Idempotent for the same Go type — safe to call from per-cell System.Init()
// where it would otherwise fire N times, which is also what makes it safe on
// the cell-split path that re-runs initSystems. Two distinct types hashing to
// the same typeID is a panic — collision should never happen at codebase
// scale; if it does, rename one type.
func RegisterEvent[T any]() {
	pkguniverse.GlobalWire().RegisterServerEvent(reflect.TypeFor[T]())
}

// LookupServerEventType returns the Go type registered for the given typeID,
// or (nil, false) if none.
func LookupServerEventType(id uint32) (reflect.Type, bool) {
	return pkguniverse.GlobalWire().ServerEventType(id)
}

// RegisteredServerEventTypes returns the registered types in deterministic
// (alphabetical) order. Used by sdkgen and protocol-schema export.
func RegisteredServerEventTypes() []reflect.Type {
	return pkguniverse.GlobalWire().ServerEventTypes()
}

// ResetServerEventRegistryForTest is exported for tests only.
func ResetServerEventRegistryForTest() {
	pkguniverse.GlobalWire().ResetServerEventsForTest()
}
