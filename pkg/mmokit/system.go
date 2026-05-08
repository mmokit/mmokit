package mmokit

import (
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/universe"
)

// SystemBase is the canonical base for all game systems. Embed it to get:
//   - engine.SystemBase methods (ECSWorld, Engine, Init, default Update,
//     BindQueries, BuildQueries, SetDeps)
//   - Stage() — direct access to the per-cell *universe.Stage
//
// Replaces the previous generic mmokit.SystemBase[W] alias. Game systems no
// longer parameterize on a typed game world; typed per-cell state is fetched
// explicitly via mmokit.State[T](s.Stage()) and cached in each system's Init().
type SystemBase struct {
	engine.SystemBase
	stage *universe.Stage
}

// Stage returns the per-cell stage this system is wired to. Available
// after the framework has called InitStage (i.e. inside Init() and Update()
// — never in struct construction).
func (b *SystemBase) Stage() *universe.Stage { return b.stage }

// InitStage is called by the universe framework after SetDeps. Game code
// must not call this directly — the framework owns stage lifecycle.
func (b *SystemBase) InitStage(s *universe.Stage) { b.stage = s }
