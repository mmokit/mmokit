package mmokit

import (
	"reflect"

	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// HandleClient registers fn as the handler for client-originated typed
// messages of type M. The framework's gateway-side dispatch path
// dispatches when a connection sends a message of type M on the typed
// client-input wire channel — channel 0x00 (shared with server-emitted
// typed events; the dispatch direction is what differs).
//
// Routing: framework looks up the player Entity owned by the sending
// connection, decodes the message body via the reflection codec, and
// invokes fn with (player Entity, *msg).
//
// Trust contract:
//   - Framework guarantees: connection owns the target Entity; Entity
//     is alive on this stage; message body decoded successfully via the
//     reflection codec.
//   - Framework does NOT validate field values, player state, or rate.
//     The handler validates as appropriate for the input.
//
// Compare:
//   - HandleAll[M]            — entity → entity, broadcast post-handler.
//   - HandleAllInternal[M]    — entity → entity, no broadcast.
//   - HandleClient[M] (this)  — client → owned-player Entity.
//
// All three share the same handler signature (target Entity, msg *M);
// only the entry-point trust contract differs.
func HandleClient[M any](world *pkguniverse.Process, fn func(player Entity, msg *M)) {
	var zero M
	world.Wire().RegisterClientInput(reflect.TypeOf(zero))

	world.OnStageInit(func(stage *pkguniverse.Stage) {
		Handle(stage, fn)
	})
}

// ClientInputTypeOf returns a serializable schema describing a registered
// client-input type t. Reuses BroadcastTypeOf's field-walking logic since
// the wire layout is identical (reflection codec). Sdkgen consumes this
// schema to emit TS classes whose `encode()` method produces bytes the
// server-side ReflectUnmarshalOnStage call site decodes.
func ClientInputTypeOf(t reflect.Type) ClientInputTypeSchema {
	return BroadcastTypeOf(t)
}

// ClientInputTypes returns the registered client-input types in
// deterministic order (sorted by reflect.Type.String()). Used by sdkgen
// to emit TS class declarations for client-bound message types.
func ClientInputTypes() []reflect.Type {
	return pkguniverse.GlobalWire().ClientInputTypes()
}
