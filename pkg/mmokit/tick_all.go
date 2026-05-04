package mmokit

import (
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// OnWorldTickAll registers fn to fire once per simulation tick on every
// Stage owned by world — both initial cells and stages created later by
// dynamic partitioning. fn receives the stage so cross-stage state can be
// keyed by CellID.
func OnWorldTickAll(world *pkguniverse.Process, fn func(stage *pkguniverse.Stage, dt float32)) {
	world.OnStageInit(func(stage *pkguniverse.Stage) {
		OnWorldTick(stage, func(dt float32) { fn(stage, dt) })
	})
}

// OnTickAll registers fn to fire once per tick for every entity that has
// component T on every Stage owned by world. Auto-replays onto stages
// created later by dynamic partitioning.
func OnTickAll[T any](world *pkguniverse.Process, fn func(e Entity, dt float32)) {
	world.OnStageInit(func(stage *pkguniverse.Stage) {
		OnTick[T](stage, fn)
	})
}

// OnTickEachAll registers fn to fire once per tick for every entity matching
// bundle B on every Stage owned by world. Auto-replays onto stages created
// later by dynamic partitioning.
func OnTickEachAll[B any](world *pkguniverse.Process, fn func(e Entity, b *B, dt float32)) {
	world.OnStageInit(func(stage *pkguniverse.Stage) {
		OnTickEach(stage, fn)
	})
}
