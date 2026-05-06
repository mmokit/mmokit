package universe

import "reflect"

// ServerEventHooks is the import-cycle indirection that lets
// SendEventTyped consult the mmokit-owned server-event registry without
// importing pkg/mmokit (which would be a circular dependency — pkg/mmokit
// already depends on pkg/universe).
//
// pkg/mmokit populates these callbacks in its init(). When the hooks are
// nil (e.g. tests that build a Stage standalone without importing mmokit)
// SendEventTyped panics with a message pointing at the missing
// registration — same failure mode as forgetting to call
// mmokit.RegisterEvent[T] for the type.
//
// Same pattern as BroadcastHooks / ClientInputHooks.
var ServerEventHooks struct {
	// IsRegistered reports whether t was registered via mmokit.RegisterEvent[T].
	IsRegistered func(t reflect.Type) bool

	// TypeIDOf returns the wire-stable uint32 identifier for type t.
	// Computed on the mmokit side as fnv32(reflect.Type.String()).
	TypeIDOf func(t reflect.Type) uint32
}
