package mmokit

import (
	"reflect"

	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// init wires:
//
//  1. The universe-side BroadcastHooks callbacks that let Stage.maybeBroadcast
//     consult the mmokit-owned broadcast registry (eligibility, typeID,
//     anchor extraction) without an import cycle.
//  2. The universe-side ClientInputHooks callbacks that let the gateway-
//     side dispatchClientInput phase consult the mmokit-owned
//     HandleClient registry (eligibility, typeID, typeID→Type lookup)
//     without an import cycle.
//  3. The universe-side ServerEventHooks callbacks that let
//     universe.SendEventTyped consult the mmokit-owned server-event
//     registry (RegisterEvent[T]) without an import cycle.
//
// All universe.Process instances built after package init see all three
// wirings. Tests that build engines/stages outside the mmokit stack and
// never import mmokit get nil hooks: BroadcastHooks and ClientInputHooks
// silent no-op, but ServerEventHooks panics in SendEventTyped — silently
// dropping a typed event is worse than failing loudly, so unregistered
// (or unwired) types must surface as a panic.
func init() {
	pkguniverse.BroadcastHooks.Eligible = brIsRegistered
	pkguniverse.BroadcastHooks.TypeIDOf = TypeIDOf
	pkguniverse.BroadcastHooks.ExtractAnchors = func(msgPtr any, targetNetID uint32, stage *pkguniverse.Stage) []uint32 {
		return ExtractAnchors(msgPtr, EntityByNetID(stage, targetNetID))
	}

	pkguniverse.ClientInputHooks.IsRegistered = ciIsRegistered
	pkguniverse.ClientInputHooks.TypeIDOf = TypeIDOf
	pkguniverse.ClientInputHooks.TypeOfTypeID = ciTypeOfTypeID

	pkguniverse.ServerEventHooks.IsRegistered = func(t reflect.Type) bool {
		_, ok := LookupServerEventType(TypeIDOf(t))
		return ok
	}
	pkguniverse.ServerEventHooks.TypeIDOf = TypeIDOf
}
