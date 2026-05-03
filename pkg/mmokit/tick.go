package mmokit

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/query"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// OnWorldTick registers fn to fire once per simulation tick on the stage.
// Fires after systems run, before FlushRemovals — the same window where
// game systems' Update observed the world. Use for stage-wide bookkeeping
// that doesn't iterate entities.
func OnWorldTick(stage *pkguniverse.Stage, fn func(dt float32)) {
	stage.RegisterTickCallback(fn)
}

// OnTick registers fn to fire once per tick for every entity that has
// component T on the stage. fn receives an Entity bound to the stage and
// the tick dt.
func OnTick[T any](stage *pkguniverse.Stage, fn func(e Entity, dt float32)) {
	type onTickBundle struct{ X *T }
	q := query.NewQuery[onTickBundle](stageQueryAdapter{world: stage.ECSWorld()})
	OnWorldTick(stage, func(dt float32) {
		// Range over the rangefunc iterator; bundle is unused since the
		// caller only sees a single component type T.
		q.Iter(func(h ecs.Entity, _ *onTickBundle) bool {
			fn(EntityFromECS(stage, h), dt)
			return true
		})
	})
}

// stageQueryAdapter satisfies the
// `interface{ ECSWorld() *ecs.World }` shape required by query.NewQuery.
type stageQueryAdapter struct{ world *ecs.World }

func (a stageQueryAdapter) ECSWorld() *ecs.World { return a.world }
