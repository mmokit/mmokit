package mmokit

import (
	"reflect"
	"sort"
	"sync"

	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
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
	msgType := reflect.TypeOf(zero)
	registerClientInputType(msgType)

	world.OnStageInit(func(stage *pkguniverse.Stage) {
		Handle(stage, fn)
	})
}

// ─── client-input registry ────────────────────────────────────────────────────

var (
	ciMu     sync.RWMutex
	ciSet    = map[reflect.Type]struct{}{}    // T (NOT *T)
	ciByType = map[uint32]reflect.Type{}      // typeID → reflect.Type for dispatch
)

// registerClientInputType marks t as a HandleClient-eligible type and
// indexes it by typeID for inbound-frame lookup. Idempotent.
func registerClientInputType(t reflect.Type) {
	// Registration-time shape check. This is the one registry whose types are
	// decoded straight off a client socket, so an unsupported field here is a
	// wire bug reachable by an unauthenticated peer rather than a codegen one.
	pkguniverse.ValidateMessageType(t)
	id := TypeIDOf(t)
	ciMu.Lock()
	ciSet[t] = struct{}{}
	ciByType[id] = t
	ciMu.Unlock()
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
	ciMu.RLock()
	defer ciMu.RUnlock()
	out := make([]reflect.Type, 0, len(ciSet))
	for t := range ciSet {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// ciIsRegistered reports whether t is currently in the client-input
// registry. Internal helper for the universe-side ClientInputHooks
// indirection (see init.go).
func ciIsRegistered(t reflect.Type) bool {
	ciMu.RLock()
	_, ok := ciSet[t]
	ciMu.RUnlock()
	return ok
}

// ciTypeOfTypeID resolves a wire typeID back to its registered Go type.
// Returns nil for unknown typeIDs (which the dispatch path treats as
// untrusted and drops with a log).
func ciTypeOfTypeID(typeID uint32) reflect.Type {
	ciMu.RLock()
	t := ciByType[typeID]
	ciMu.RUnlock()
	return t
}
