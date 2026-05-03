package mmokit

import (
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// OnWorldTick registers fn to fire once per simulation tick on the stage.
// Fires after systems run, before FlushRemovals — the same window where
// game systems' Update observed the world. Use for stage-wide bookkeeping
// that doesn't iterate entities.
func OnWorldTick(stage *pkguniverse.Stage, fn func(dt float32)) {
	stage.RegisterTickCallback(fn)
}
