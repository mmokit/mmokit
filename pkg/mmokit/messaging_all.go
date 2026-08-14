package mmokit

import (
	"reflect"

	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// HandleAll registers fn as the handler for messages of type M on every
// Stage owned by world — both stages that exist now and stages created
// later by dynamic partitioning (cell splits, host migrations).
//
// HandleAll marks M as broadcast-eligible: post-handler the framework
// pushes msg into each anchor stage's broadcast queue so AoI viewers see
// the result. Use HandleAllInternal for server-internal messages that
// should never reach clients.
//
// This is the common case for game-defined message handlers; prefer it
// over the per-stage Handle unless you have a specific reason to register
// only on one stage. Internally, HandleAll is a thin wrapper over
// Process.OnStageInit that calls Handle on each Stage.
func HandleAll[M any](world *pkguniverse.Process, fn func(target Entity, msg *M)) {
	var zero M
	RegisterBroadcastType(reflect.TypeOf(zero))
	world.OnStageInit(func(stage *pkguniverse.Stage) {
		Handle(stage, fn)
	})
}
