package engine

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"
)

// System is the interface all game systems implement.
// Embed SystemBase[W] for automatic dependency injection via SetDeps/Init.
type System interface {
	Update(dt float32)
}

// SystemBase provides dependency injection for systems, parameterized on the
// game world type. Embed it in your system struct to get ECSWorld(), Engine(),
// GameWorld(), and the typed World() accessor. The framework calls SetDeps()
// then Init() before the first Update().
//
// Game systems use SystemBase[*MyWorld] for typed access; engine-side systems
// that don't need world methods use SystemBase[any].
type SystemBase[W any] struct {
	ecsWorld *ecs.World
	eng      *Engine
	world    W
}

// ECSWorld returns the ECS world for this cell.
func (b *SystemBase[W]) ECSWorld() *ecs.World { return b.ecsWorld }

// Engine returns the engine for this cell.
func (b *SystemBase[W]) Engine() *Engine { return b.eng }

// GameWorld returns the game world as `any`. Prefer the typed World()
// accessor — GameWorld is kept for callers that need the untyped form
// (e.g. the network system's reflection-based world probing).
func (b *SystemBase[W]) GameWorld() any { return b.world }

// World returns the typed game world. The type parameter W is supplied at
// the embed site (e.g. `SystemBase[*MyWorld]`).
func (b *SystemBase[W]) World() W { return b.world }

// Init is called once after SetDeps. Override to create filters, etc.
// Auto-bind machinery for Query[T] fields is added in Task 3 — for now
// systems still call q.Init(s, ...) inside their own Init bodies.
func (b *SystemBase[W]) Init() {}

// SetDeps is called by the framework to inject dependencies. Panics if gw
// is not assignable to W — opensource callers hit this immediately instead
// of debugging a silent nil.
func (b *SystemBase[W]) SetDeps(w *ecs.World, eng *Engine, gw any) {
	b.ecsWorld = w
	b.eng = eng
	if gw == nil {
		var zero W
		b.world = zero
		return
	}
	typed, ok := gw.(W)
	if !ok {
		var zero W
		panic(fmt.Sprintf("engine.SystemBase[%T]: GameWorld is %T, not assignable", zero, gw))
	}
	b.world = typed
}

// SystemDef pairs a name with a factory that creates a fresh system instance.
// All mmokit factory helpers (NewPhysicsSystem, NewSpatialSystem, NewSystem,
// etc.) return a SystemDef. Pass it to Process.AddSystem.
type SystemDef struct {
	Name    string
	Factory func() System
}

// Named overrides the auto-derived system name. Use when a system label needs
// to differ from the type-derived default — e.g. two instances of the same
// type registered side-by-side, or when the type name is too long to read in
// perf output.
//
//	mmo.AddSystem(mmokit.NewSystem(&BotSystem{}).Named("AILogic"))
func (d SystemDef) Named(name string) SystemDef {
	d.Name = name
	return d
}
