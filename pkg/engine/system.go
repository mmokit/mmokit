package engine

import "github.com/mlange-42/ark/ecs"

// System is the interface all game systems implement.
// Embed SystemBase for automatic dependency injection via SetDeps/Init.
type System interface {
	Update(dt float32)
}

// SystemBase provides dependency injection for systems.
// Embed it in your system struct to get ECSWorld(), Engine(), and GameWorld().
// The framework calls SetDeps() then Init() before the first Update().
type SystemBase struct {
	ecsWorld  *ecs.World
	eng       *Engine
	gameWorld any
}

// ECSWorld returns the ECS world for this node.
func (b *SystemBase) ECSWorld() *ecs.World { return b.ecsWorld }

// Engine returns the engine for this node.
func (b *SystemBase) Engine() *Engine { return b.eng }

// GameWorld returns the game world for this node.
// Type-assert to your concrete world type in Init().
func (b *SystemBase) GameWorld() any { return b.gameWorld }

// Init is called once after SetDeps. Override to create filters, etc.
func (b *SystemBase) Init() {}

// SetDeps is called by the framework to inject dependencies.
func (b *SystemBase) SetDeps(w *ecs.World, eng *Engine, gw any) {
	b.ecsWorld = w
	b.eng = eng
	b.gameWorld = gw
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
