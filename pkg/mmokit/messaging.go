package mmokit

import (
	"reflect"

	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// Handle registers fn as the handler for messages of type M on the given
// stage. fn runs whenever an Entity on this stage receives a Send of an M,
// regardless of whether the Send originated locally or cross-cell.
//
// Lifecycle: Stages are per-cell and dynamic partitioning may create new
// stages at runtime (cell splits, host migrations). Handlers registered
// here live on the stage they're registered against — they are NOT
// auto-replayed onto stages created later. Register Handle calls from your
// per-stage init hook (Process.OnPlayerJoin or the equivalent setup
// callback) so every cell — initial and split-created — has the handler.
//
// One handler per message type per stage; calling Handle twice for the
// same M on the same stage panics.
func Handle[M any](stage *pkguniverse.Stage, fn func(target Entity, msg *M)) {
	d := stage.Dispatcher()
	d.SetEntityCtor(entityCtorAdapter)
	var zero M
	msgType := reflect.TypeOf(zero)
	d.Register(typeKeyOf(msgType), msgType, reflect.ValueOf(fn))
	// Auto-register T for AoI broadcast unless it opts out via the
	// ServerOnly marker. Idempotent across multiple stages — the registry
	// is global, keyed by reflect.Type.
	if !IsServerOnly(msgType) {
		RegisterBroadcastType(msgType)
	}
}

// typeKeyOf returns the wire / dispatch key for a message type. Uses
// reflect.Type.String() (package-qualified, e.g. "combat.Damage") rather
// than Type.Name() ("Damage") so two types with the same Go name in
// different packages don't collide on the wire or in the dispatcher.
func typeKeyOf(t reflect.Type) string {
	return t.String()
}

// entityCtorAdapter is what the universe-layer dispatcher calls to construct
// the typed Entity argument for the handler. Lives here to avoid import
// cycles.
func entityCtorAdapter(stage *pkguniverse.Stage, netID uint32) any {
	return EntityByNetID(stage, netID)
}

// Send delivers msg to the entity. If the entity is local on its stage, the
// registered handler runs synchronously before Send returns. If the entity
// is a replica (lives elsewhere), Send is fire-and-forget — the handler runs
// on the authoritative stage when the wire message arrives.
//
// Send is a no-op on a zero-value Entity, an Entity with no stage, or a nil
// or typed-nil-pointer msg.
func (e Entity) Send(msg any) {
	if e.stage == nil || e.netID == 0 || msg == nil {
		return
	}
	v := reflect.ValueOf(msg)
	if !v.IsValid() {
		return
	}
	// Box into a pointer so handlers can mutate result fields.
	var msgPtr any
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		msgPtr = msg
	} else {
		ptr := reflect.New(v.Type())
		ptr.Elem().Set(v)
		msgPtr = ptr.Interface()
	}
	e.stage.RouteTypedMessage(e.netID, msgPtr)
}
