package universe

import "reflect"

// ClientInputHooks is the import-cycle indirection that lets the gateway-
// side per-tick dispatch consult the mmokit-owned client-input registry
// (mmokit.HandleClient) without importing pkg/mmokit (which would be a
// circular dependency — pkg/mmokit already depends on pkg/universe).
//
// pkg/mmokit populates these callbacks in its init(). When the hooks are
// nil (e.g. tests that build a Stage standalone without importing mmokit)
// the dispatchClientInput phase is a silent no-op.
//
// Same pattern as BroadcastHooks / entityCtorAdapter.
var ClientInputHooks struct {
	// IsRegistered reports whether t was registered via mmokit.HandleClient.
	// Used by sdkgen + diagnostics.
	IsRegistered func(t reflect.Type) bool

	// TypeIDOf returns the wire-stable uint32 identifier for type t.
	// Computed on the mmokit side as fnv32(reflect.Type.String()).
	TypeIDOf func(t reflect.Type) uint32

	// TypeOfTypeID resolves a wire typeID back to its Go reflect.Type.
	// Used by the gateway-side dispatch to allocate a fresh value of the
	// right type for ReflectUnmarshalOnStage. Returns nil for unknown
	// (un-trusted) typeIDs.
	TypeOfTypeID func(typeID uint32) reflect.Type
}
