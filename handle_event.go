package mmokit

import "reflect"

// RegisterEvent registers T as a server→client typed event on p's registry.
// Subsequent calls to SendEvent[T] use the FNV-1a typeID derived from T to
// identify the frame on the wire; the SDK codegen iterates the registry to
// emit per-event TS decoder classes and per-event onXxx handlers.
//
// Idempotent for the same Go type — safe to call from per-cell System.Init(),
// where it fires once per cell, and safe on the cell-split path that re-runs
// initSystems on a background goroutine. That idempotence is why the registry
// stays open for registration for the life of the process rather than sealing
// at Build().
//
// Two distinct types hashing to the same typeID is a panic — collision should
// never happen at codebase scale; if it does, rename one type.
//
// Registries are per-Process: registering on one Process does not register on
// another built by the same binary. Reach the registry directly with
// src.Wire() for lookups and enumeration.
//
// src is normally the *Process. It is a WireSource rather than a *Process so
// that a System registering from Init() — the documented pattern, and the one
// a split re-runs — can pass its own s.Stage() instead of reaching through
// s.Stage().Process(), which is nil on a stage built without one.
func RegisterEvent[T any](src WireSource) {
	src.Wire().RegisterServerEvent(reflect.TypeFor[T]())
}
