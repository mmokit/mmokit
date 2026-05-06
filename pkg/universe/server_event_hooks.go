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

// EngineDefaultFrameHooks lets pkg/universe and pkg/engine emit the
// engine-default typed events without importing pkg/mmokit. The mmokit
// init() populates each builder with a closure that constructs the typed
// Go struct, registers the type via RegisterEvent at package init, and
// returns the encoded channel-0x00 typed-event frame ready for ConnMgr.Send.
//
// When a hook is nil (e.g. tests that never import mmokit) the calling
// site short-circuits — silently dropping the framework event is safe
// for those test paths because no client is consuming it.
var EngineDefaultFrameHooks struct {
	// PlayerEntityAssigned builds the typed-event frame announcing the
	// player's authoritative entity NetID and world position. Used by
	// Stage.SpawnPlayer.
	PlayerEntityAssigned func(netID uint32, worldX, worldY float32) []byte
}
