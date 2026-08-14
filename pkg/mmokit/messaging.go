package mmokit

import (
	"reflect"

	pkguniverse "github.com/zenion/mmokit/pkg/universe"
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
//
// Handle does NOT touch the broadcast registry. Broadcast eligibility is
// owned by the process-scoped registration verbs (HandleAll for broadcast,
// HandleAllInternal for server-internal no-broadcast). Same-stage tests
// that want to verify the broadcast queue path should call
// RegisterBroadcastType(reflect.TypeOf(zero)) directly.
func Handle[M any](stage *pkguniverse.Stage, fn func(target Entity, msg *M)) {
	d := stage.Dispatcher()
	d.SetEntityCtor(entityCtorAdapter)
	var zero M
	msgType := reflect.TypeOf(zero)
	d.Register(typeKeyOf(msgType), msgType, reflect.ValueOf(fn))
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

// SendEvent writes a single typed server→client event to one connection.
// T must be registered via RegisterEvent[T]() at startup, otherwise this
// panics. The frame is delivered reliably over the event channel.
//
// Usage:
//
//	mmokit.SendEvent(gw.Stage, connID, &MyEvent{Field: 42})
//
// Thin pass-through to pkguniverse.SendEventTyped so game code stays on
// the mmokit-facade-only convention.
func SendEvent[T any](stage *Stage, connID uint32, msg *T) {
	pkguniverse.SendEventTyped(stage, connID, msg)
}

// BuildTypedEventFrame returns a fully-framed channel-0x00 typed-event
// payload for msg, suitable for direct Conn.Send() — bypasses the engine
// connection manager entirely. T must be registered via RegisterEvent[T]
// at startup, otherwise this panics.
//
// Game-loop code should prefer SendEvent. Direct-framing is rare; the only
// remaining caller is the marketplace/bank update path that needs to fire
// frames from off-loop goroutines without a *Stage in scope.
func BuildTypedEventFrame[T any](msg *T) []byte {
	return pkguniverse.BuildTypedEventFrameRaw(msg)
}

// SendEventToAll builds the typed-event frame for msg once and writes it
// to every PlayerSession on eng that is in StateActive. Use for per-tick
// world snapshots and other broadcasts where every active player sees the
// same payload — calling SendEvent in a loop would re-marshal once per
// recipient. T must be registered via RegisterEvent[T].
//
// Usage from a system:
//
//	mmokit.SendEventToAll(s.Engine(), &WorldSnapshot{...})
func SendEventToAll[T any](eng *Engine, msg *T) {
	frame := BuildTypedEventFrame(msg)
	eng.Players.ForEach(StateActive, func(sess *PlayerSession) {
		eng.ConnMgr.SendReliable(sess.ConnID, frame)
	})
}
