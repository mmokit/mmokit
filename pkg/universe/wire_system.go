package universe

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/engine"
)

// WireSystem wires a system as the coordinator does — SetDeps, InitStage (if
// applicable), BindQueries, Init, BuildQueries — in one call. Use in tests
// where you want a fully-initialized system without spinning up a coordinator.
// Pass stage=nil for engine-only systems that don't embed mmokit.SystemBase.
func WireSystem(sys engine.System, ecsWorld *ecs.World, eng *engine.Engine, stage *Stage) {
	type depsInjectable interface {
		SetDeps(w *ecs.World, eng *engine.Engine)
	}
	type stageInjectable interface {
		InitStage(s *Stage)
	}
	type queryBinder interface{ BindQueries(outer any) }
	type initializable interface{ Init() }
	type queryBuilder interface{ BuildQueries() }

	if di, ok := sys.(depsInjectable); ok {
		di.SetDeps(ecsWorld, eng)
	}
	if si, ok := sys.(stageInjectable); ok && stage != nil {
		si.InitStage(stage)
	}
	if qb, ok := sys.(queryBinder); ok {
		qb.BindQueries(sys)
	}
	if i, ok := sys.(initializable); ok {
		i.Init()
	}
	if qb, ok := sys.(queryBuilder); ok {
		qb.BuildQueries()
	}
}
