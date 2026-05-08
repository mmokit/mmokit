package engine

import (
	"reflect"
	"unsafe"

	"github.com/mlange-42/ark/ecs"
)

// queryBuildable is implemented by *query.Query[T] (defined in pkg/query).
// SystemBase only sees this minimal contract — it doesn't import pkg/query
// (would be a cycle); discovery records pointers via reflection at SetDeps.
type queryBuildable interface {
	BuildFromECS(w *ecs.World)
}

// System is the interface all game systems implement.
// Embed SystemBase for automatic dependency injection via SetDeps/Init.
type System interface {
	Update(dt float32)
}

// SystemBase provides dependency injection for systems. Embed it to get
// ECSWorld(), Engine(), default no-op Init() and Update(), and automatic
// query-field discovery via BindQueries.
//
// Game-side systems should embed mmokit.SystemBase, which wraps this base
// and additionally exposes Stage() (per-cell *universe.Stage accessor).
type SystemBase struct {
	ecsWorld *ecs.World
	eng      *Engine
	queries  []queryBuildable // populated in BindQueries
}

// ECSWorld returns the ECS world for this cell.
func (b *SystemBase) ECSWorld() *ecs.World { return b.ecsWorld }

// Engine returns the engine for this cell.
func (b *SystemBase) Engine() *Engine { return b.eng }

// Init is called once after SetDeps. Override to create filters, configure
// queries via Query.With(opts...), etc. Default is a no-op.
func (b *SystemBase) Init() {}

// Update is called every tick by the engine. Default is a no-op so systems
// that only need Init() may omit Update entirely.
func (b *SystemBase) Update(dt float32) {}

// BindQueries discovers query.Query[T] fields on the outer system struct
// via reflection and records them for the build phase.
func (b *SystemBase) BindQueries(outer any) {
	v := reflect.ValueOf(outer)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		panic("engine.SystemBase: BindQueries requires *Struct")
	}
	v = v.Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		ft := t.Field(i)
		fp := unsafe.Pointer(v.Field(i).UnsafeAddr())
		field := reflect.NewAt(ft.Type, fp).Interface()
		if qb, ok := field.(queryBuildable); ok {
			b.queries = append(b.queries, qb)
		}
	}
}

// BuildQueries materializes each discovered query's ECS filter using the
// options the user accumulated during Init(). Called by the framework after
// Init() returns.
func (b *SystemBase) BuildQueries() {
	for _, q := range b.queries {
		q.BuildFromECS(b.ecsWorld)
	}
}

// SetDeps is called by the framework to inject dependencies.
func (b *SystemBase) SetDeps(w *ecs.World, eng *Engine) {
	b.ecsWorld = w
	b.eng = eng
}

// SystemDef pairs a name with a factory that creates a fresh system instance.
type SystemDef struct {
	Name    string
	Factory func() System
}

// Named overrides the auto-derived system name.
//
//	mmo.AddSystem(mmokit.NewSystem(&BotSystem{}).Named("AILogic"))
func (d SystemDef) Named(name string) SystemDef {
	d.Name = name
	return d
}
