package mmokit

import (
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// HandleAll registers fn as the handler for messages of type M on every
// Stage owned by world — both stages that exist now and stages created
// later by dynamic partitioning (cell splits, host migrations).
//
// This is the common case for game-defined message handlers; prefer it
// over the per-stage Handle unless you have a specific reason to register
// only on one stage. Internally, HandleAll is a thin wrapper over
// Process.OnStageInit that calls Handle on each Stage.
func HandleAll[M any](world *pkguniverse.Process, fn func(target Entity, msg *M)) {
	world.OnStageInit(func(stage *pkguniverse.Stage) {
		Handle(stage, fn)
	})
}
