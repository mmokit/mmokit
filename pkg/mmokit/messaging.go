package mmokit

import (
	"reflect"

	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// Handle registers fn as the global handler for messages of type M on the
// given stage. fn is called whenever an Entity on this stage receives a Send
// of an M, regardless of whether the Send originated locally or cross-cell.
// One handler per message type per stage; calling Handle twice for the same
// M panics.
func Handle[M any](stage *pkguniverse.Stage, fn func(target Entity, msg *M)) {
	d := stage.Dispatcher()
	d.SetEntityCtor(entityCtorAdapter)
	var zero M
	msgType := reflect.TypeOf(zero)
	d.Register(msgType.Name(), msgType, reflect.ValueOf(fn))
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
func (e Entity) Send(msg any) {
	if e.stage == nil || e.netID == 0 {
		return
	}
	// Box into a pointer so handlers can mutate result fields.
	var msgPtr any
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Pointer {
		msgPtr = msg
	} else {
		ptr := reflect.New(reflect.TypeOf(msg))
		ptr.Elem().Set(reflect.ValueOf(msg))
		msgPtr = ptr.Interface()
	}
	e.stage.RouteTypedMessage(e.netID, msgPtr)
}
