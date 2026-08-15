package universe

import "github.com/mmokit/mmokit/pkg/engine"

// SystemBase is the canonical base for all game systems. Embed it to get:
//   - engine.SystemBase methods (ECSWorld, Engine, Init, default Update,
//     BindQueries, BuildQueries, SetDeps)
//   - Stage() — direct access to the per-cell *Stage
//
// Replaces the previous generic mmokit.SystemBase[W] alias. Game systems no
// longer parameterize on a typed game world; typed per-cell state is fetched
// explicitly via mmokit.State[T](s.Stage()) and cached in each system's Init().
type SystemBase struct {
	engine.SystemBase
	stage *Stage
}

// Stage returns the per-cell stage this system is wired to. Available
// after the framework has called InitStage (i.e. inside Init() and Update()
// — never in struct construction).
func (b *SystemBase) Stage() *Stage { return b.stage }

// Commands returns the per-stage deferred-mutation buffer. Shortcut
// for s.Stage().Commands(). Use inside Update to queue structural
// ECS changes — Add/RemoveComponent, Despawn, Defer — that would
// otherwise panic under ark's locked-world rule. Ops queued by
// System N are visible to System N+1 in the same tick (the engine
// game loop flushes via the AfterSystem hook between systems).
func (b *SystemBase) Commands() *Commands { return b.stage.Commands() }

// InitStage is called by the universe framework after SetDeps. Game code
// must not call this directly — the framework owns stage lifecycle.
func (b *SystemBase) InitStage(s *Stage) { b.stage = s }
