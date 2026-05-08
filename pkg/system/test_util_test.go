package system

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/engine"
)

// wireSystem is a test-only helper that runs the framework's full system
// lifecycle (SetDeps → BindQueries → Init → BuildQueries) without spinning
// up a coordinator. Mirrors mmokit.WireSystem but lives in the system
// package's test scope to avoid a pkg/mmokit import cycle.
func wireSystem(sys engine.System, ecsWorld *ecs.World, eng *engine.Engine) {
	type depsInjectable interface {
		SetDeps(w *ecs.World, eng *engine.Engine)
	}
	type queryBinder interface{ BindQueries(outer any) }
	type initializable interface{ Init() }
	type queryBuilder interface{ BuildQueries() }

	if di, ok := sys.(depsInjectable); ok {
		di.SetDeps(ecsWorld, eng)
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
