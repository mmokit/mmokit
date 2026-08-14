// Package query provides [Query], a bundle-based ECS rangefunc that wraps
// Ark's [ecs.UnsafeFilter] behind a single generic type parameterized on a
// component struct. It eliminates the arity-specific Filter1/Filter2/…
// boilerplate; range over it via the [Query.Iter] method.
//
// Most game code imports this indirectly through the mmokit facade
// (mmokit.Query, mmokit.Without, mmokit.IncludeAll). Only pkg/system and
// pkg/universe import pkg/query directly to avoid an import cycle with mmokit.
//
// # Bundle structs
//
// A bundle is a plain struct whose exported fields are pointers to ECS
// component types. Required fields form the filter — entities must have all
// of them. Fields tagged `ecs:"optional"` are looked up per-entity and set
// to nil when absent.
//
//	type MovementBundle struct {
//	    Pos    *component.Position
//	    Vel    *component.Velocity
//	    Params *component.MoveParams `ecs:"optional"`
//	}
//
// # Default exclusions
//
// By default, Ghost and Replica entities are excluded (covers 90%+ of game
// systems). Use [IncludeAll] to clear defaults, or [Without] to add extras.
// Options are applied in order: IncludeAll clears, Without accumulates.
package query

import (
	"reflect"
	"unsafe"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
)

// fieldMeta stores the precomputed info needed to populate one bundle field
// during iteration. Built once at Init time via reflection.
type fieldMeta struct {
	compID   ecs.ID  // Ark component ID, used with UnsafeQuery.Get()
	offset   uintptr // byte offset of the pointer field within the bundle struct
	optional bool    // true if tagged `ecs:"optional"`
}

// Query is a bundle-typed ECS query over entities matching the component
// bundle struct T. Configure with [Query.With] inside the system's Init();
// the framework discovers query fields via reflection, calls BuildFromECS
// after Init returns, and the query is ready by the first Update. Range
// using the [Query.Iter] method:
//
//	type PhysicsSystem struct {
//	    mmokit.SystemBase[*MyWorld]
//	    entities mmokit.Query[struct {
//	        Pos *component.Position
//	        Vel *component.Velocity
//	    }]
//	}
//
//	func (s *PhysicsSystem) Init() { s.entities.With() } // or .With(IncludeAll(), …)
//	func (s *PhysicsSystem) Update(dt float32) {
//	    for _, b := range s.entities.Iter {
//	        b.Pos.X += b.Vel.X * dt
//	    }
//	}
//
// Under the hood, reflection runs once at build to extract field offsets and
// component IDs. Per-tick iteration uses unsafe.Pointer arithmetic to populate
// a reusable bundle — zero allocations per entity. The *T pointer is reused
// across iterations; do not store it beyond the loop body.
type Query[T any] struct {
	iter  func(yield func(ecs.Entity, *T) bool)
	opts  []QueryOption
	built bool
}

// QueryOption configures how a [Query] filter is built.
// Create options with [Without] and [IncludeAll].
type QueryOption struct {
	tp         reflect.Type // non-nil for Without — the component type to exclude
	includeAll bool         // true for IncludeAll — clears default Ghost/Replica exclusion
}

// Without returns a [QueryOption] that excludes entities having component T.
// Multiple Without calls accumulate.
//
//	q.With(query.Without[component.Dormant]()) // default exclusions + Dormant
func Without[T any]() QueryOption {
	return QueryOption{tp: reflect.TypeFor[T]()}
}

// IncludeAll returns a [QueryOption] that clears the default Ghost/Replica
// exclusions. Combine with [Without] for fully custom exclusion sets:
//
//	q.With(query.IncludeAll(), query.Without[component.Ghost]()) // only Ghost
func IncludeAll() QueryOption {
	return QueryOption{includeAll: true}
}

// Iter is the rangefunc form of the query. Range over it from a system's
// Update():
//
//	for e, b := range s.entities.Iter { ... }
//
// Panics if the query has not been built yet (no With+build performed).
func (q *Query[T]) Iter(yield func(ecs.Entity, *T) bool) {
	if q.iter == nil {
		panic("query.Query: ranged before build (call With during system Init, or use NewQuery for ad-hoc)")
	}
	q.iter(yield)
}

// With configures the query with the given options. Options accumulate across
// calls. Must be called during the system's Init() — calling With after the
// framework's build phase panics.
func (q *Query[T]) With(opts ...QueryOption) *Query[T] {
	if q.built {
		panic("query.Query: With called after build (only call inside system Init)")
	}
	q.opts = append(q.opts, opts...)
	return q
}

// BuildFromECS materializes the query's ECS filter from the accumulated
// options. Called by SystemBase[W].BuildQueries() after the system's Init()
// returns. Game code should not invoke this directly; use With(opts...)
// inside Init() and let the framework call BuildFromECS for you.
func (q *Query[T]) BuildFromECS(w *ecs.World) {
	if q.built {
		panic("query.Query: BuildFromECS called twice")
	}
	q.iter = build[T](q.opts, w)
	q.built = true
}

// NewQuery creates and initializes a [Query] in one step. Useful when using
// named bundle types rather than anonymous structs on the system field:
//
//	s.entities = query.NewQuery[MyBundle](s)
func NewQuery[T any](sys interface{ ECSWorld() *ecs.World }, opts ...QueryOption) Query[T] {
	q := Query[T]{}
	q.opts = opts
	q.iter = build[T](q.opts, sys.ECSWorld())
	q.built = true
	return q
}

// build is the shared constructor used by NewQuery and BuildFromECS.
func build[T any](opts []QueryOption, w *ecs.World) func(yield func(ecs.Entity, *T) bool) {
	fields := buildFields[T](w)
	filter := buildFilter(w, fields, opts)
	var bundle T
	base := unsafe.Pointer(&bundle)
	return func(yield func(ecs.Entity, *T) bool) {
		uq := filter.Query()
		// defer Close() so the world unlocks even when the loop body panics.
		// Without this, a panic in yield (any system's Update body) propagates
		// up through the rangefunc without re-entering Next() — uq.Close()
		// never runs and the world's lock counter stays > 0 forever. The
		// recovered-panic path in processAdminCmds + the normal panic
		// propagation through gl.tick both bypass the inline Close call,
		// so defer is the only reliable cleanup. Close is idempotent in
		// ark v0.7.1, so calling it again from the early-break branch (or
		// after Next() returns false naturally) is a no-op.
		defer uq.Close()
		for uq.Next() {
			for i := range fields {
				fm := &fields[i]
				fieldPtr := (*unsafe.Pointer)(unsafe.Add(base, fm.offset))
				if fm.optional && !uq.Has(fm.compID) {
					*fieldPtr = nil
				} else {
					*fieldPtr = uq.Get(fm.compID)
				}
			}
			if !yield(uq.Entity(), &bundle) {
				return
			}
		}
	}
}

// buildFields uses reflection to scan T's exported pointer-to-struct fields,
// register each as an Ark component, and record its offset for fast population.
func buildFields[T any](w *ecs.World) []fieldMeta {
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Struct {
		panic("query.Query: T must be a struct")
	}
	var fields []fieldMeta
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Type.Kind() != reflect.Pointer || f.Type.Elem().Kind() != reflect.Struct {
			panic("query.Query: field " + f.Name + " must be a pointer to a struct")
		}
		fields = append(fields, fieldMeta{
			compID:   ecs.TypeID(w, f.Type.Elem()),
			offset:   f.Offset,
			optional: f.Tag.Get("ecs") == "optional",
		})
	}
	if len(fields) == 0 {
		panic("query.Query: bundle struct has no exported pointer fields")
	}
	return fields
}

// buildFilter builds the UnsafeFilter from required component IDs and applies
// exclusion options. Default: exclude Ghost + Replica. IncludeAll clears
// defaults; Without adds to the exclusion set.
func buildFilter(w *ecs.World, fields []fieldMeta, opts []QueryOption) ecs.UnsafeFilter {
	var required []ecs.ID
	for i := range fields {
		if !fields[i].optional {
			required = append(required, fields[i].compID)
		}
	}
	filter := ecs.NewUnsafeFilter(w, required...)

	includeAll := false
	var extraWithout []ecs.ID
	for _, opt := range opts {
		if opt.includeAll {
			includeAll = true
		}
		if opt.tp != nil {
			extraWithout = append(extraWithout, ecs.TypeID(w, opt.tp))
		}
	}

	var withoutIDs []ecs.ID
	if !includeAll {
		withoutIDs = append(withoutIDs,
			ecs.ComponentID[component.Ghost](w),
			ecs.ComponentID[component.Replica](w),
		)
	}
	withoutIDs = append(withoutIDs, extraWithout...)

	if len(withoutIDs) > 0 {
		filter = filter.Without(withoutIDs...)
	}
	return filter
}
