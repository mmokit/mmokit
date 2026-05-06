package universe

import "reflect"

// BroadcastHooks is the import-cycle indirection that lets Stage.maybeBroadcast
// consult mmokit-side data (eligibility, typeID, anchor extraction) without
// importing pkg/mmokit (which would be a circular dependency — pkg/mmokit
// already depends on pkg/universe).
//
// pkg/mmokit populates these callbacks in its init(). When the hooks are nil
// (e.g. tests that build a Stage standalone without importing mmokit) the
// auto-broadcast path is a silent no-op.
//
// Same pattern as the existing entityCtorAdapter wired through
// MessageDispatcher.SetEntityCtor.
var BroadcastHooks struct {
	// Eligible reports whether typed messages of type t should auto-broadcast.
	// Returns true iff t was registered via mmokit.HandleAll (or directly via
	// mmokit.RegisterBroadcastType); types registered via HandleAllInternal
	// are absent from the registry and return false.
	Eligible func(t reflect.Type) bool

	// TypeIDOf returns the wire-stable uint32 identifier for type t.
	// Computed on the mmokit side as fnv32(reflect.Type.String()).
	TypeIDOf func(t reflect.Type) uint32

	// ExtractAnchors returns deduped NetIDs of Entity-typed fields in msgPtr
	// plus the receiver (target). Anchors drive the AoI filter at drain time.
	ExtractAnchors func(msgPtr any, targetNetID uint32, stage *Stage) []uint32
}
