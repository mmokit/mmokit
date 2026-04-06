// Package query provides [Query], a bundle-based ECS query that wraps Ark's
// [ecs.UnsafeFilter] behind a single generic type parameterized on a component
// struct. It eliminates the arity-specific Filter1/Filter2/… boilerplate and
// provides Go 1.23+ range-over-function iteration.
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
	"iter"
	"reflect"
	"unsafe"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
)

// fieldMeta stores the precomputed info needed to populate one bundle field
// during iteration. Built once at Init time via reflection.
type fieldMeta struct {
	compID   ecs.ID  // Ark component ID, used with UnsafeQuery.Get()
	offset   uintptr // byte offset of the pointer field within the bundle struct
	optional bool    // true if tagged `ecs:"optional"`
}

// Query wraps an Ark [ecs.UnsafeFilter] and provides ergonomic, arity-independent
// iteration over entities matching a component bundle struct T.
//
// Declare as a struct field on your system, call [Query.Init] in Init(), then
// iterate with [Query.All] in Update():
//
//	type PhysicsSystem struct {
//	    mmokit.SystemBase
//	    entities mmokit.Query[struct {
//	        Pos *component.Position
//	        Vel *component.Velocity
//	    }]
//	}
//
//	func (s *PhysicsSystem) Init()            { s.entities.Init(s) }
//	func (s *PhysicsSystem) Update(dt float32) {
//	    for _, b := range s.entities.All() {
//	        b.Pos.X += b.Vel.X * dt
//	    }
//	}
//
// Under the hood, reflection runs once at Init to extract field offsets and
// component IDs. Per-tick iteration uses unsafe.Pointer arithmetic to populate
// a reusable bundle — zero allocations per entity.
type Query[T any] struct {
	filter ecs.UnsafeFilter
	fields []fieldMeta
	bundle T    // reusable; component pointers inside change each iteration
	inited bool // guards against double-init
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
//	q.Init(s, query.Without[component.Dormant]()) // default exclusions + Dormant
func Without[T any]() QueryOption {
	return QueryOption{tp: reflect.TypeFor[T]()}
}

// IncludeAll returns a [QueryOption] that clears the default Ghost/Replica
// exclusions. Combine with [Without] for fully custom exclusion sets:
//
//	q.Init(s, query.IncludeAll(), query.Without[component.Ghost]()) // only Ghost
func IncludeAll() QueryOption {
	return QueryOption{includeAll: true}
}

// Init initializes the query from a system's ECS world. The sys parameter
// must implement ECSWorld() *ecs.World (satisfied by engine.SystemBase and
// mmokit.SystemBase). T is inferred from the struct field — no type
// repetition needed.
//
// Panics if called twice, if T is not a struct, or if any exported field
// is not a pointer to a struct type.
func (q *Query[T]) Init(sys interface{ ECSWorld() *ecs.World }, opts ...QueryOption) {
	if q.inited {
		panic("query.Query: Init called twice")
	}
	w := sys.ECSWorld()
	q.initFields(w)
	q.initFilter(w, opts)
	q.inited = true
}

// NewQuery creates and initializes a [Query] in one step. Useful when using
// named bundle types rather than anonymous structs on the system field:
//
//	s.entities = query.NewQuery[MyBundle](s)
func NewQuery[T any](sys interface{ ECSWorld() *ecs.World }, opts ...QueryOption) Query[T] {
	var q Query[T]
	q.Init(sys, opts...)
	return q
}

// initFields uses reflection to scan T's exported pointer-to-struct fields,
// register each as an Ark component, and record its offset for fast population.
func (q *Query[T]) initFields(w *ecs.World) {
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Struct {
		panic("query.Query: T must be a struct")
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Type.Kind() != reflect.Ptr || f.Type.Elem().Kind() != reflect.Struct {
			panic("query.Query: field " + f.Name + " must be a pointer to a struct")
		}
		compID := ecs.TypeID(w, f.Type.Elem())
		optional := f.Tag.Get("ecs") == "optional"
		q.fields = append(q.fields, fieldMeta{
			compID:   compID,
			offset:   f.Offset,
			optional: optional,
		})
	}
	if len(q.fields) == 0 {
		panic("query.Query: bundle struct has no exported pointer fields")
	}
}

// initFilter builds the UnsafeFilter from required component IDs and applies
// exclusion options. Default: exclude Ghost + Replica. IncludeAll clears
// defaults; Without adds to the exclusion set.
func (q *Query[T]) initFilter(w *ecs.World, opts []QueryOption) {
	// Required fields form the filter — entities must have all of them.
	var required []ecs.ID
	for i := range q.fields {
		if !q.fields[i].optional {
			required = append(required, q.fields[i].compID)
		}
	}
	q.filter = ecs.NewUnsafeFilter(w, required...)

	// Parse options in order.
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

	// Build the final exclusion set.
	var withoutIDs []ecs.ID
	if !includeAll {
		withoutIDs = append(withoutIDs,
			ecs.ComponentID[component.Ghost](w),
			ecs.ComponentID[component.Replica](w),
		)
	}
	withoutIDs = append(withoutIDs, extraWithout...)

	if len(withoutIDs) > 0 {
		q.filter = q.filter.Without(withoutIDs...)
	}
}

// populateBundle writes component pointers from the current query row into the
// reusable bundle struct. Each field's pointer is set via unsafe offset math —
// no reflect calls in the hot path.
func (q *Query[T]) populateBundle(uq *ecs.UnsafeQuery) {
	base := unsafe.Pointer(&q.bundle)
	for i := range q.fields {
		fm := &q.fields[i]
		fieldPtr := (*unsafe.Pointer)(unsafe.Add(base, fm.offset))
		if fm.optional && !uq.Has(fm.compID) {
			*fieldPtr = nil
		} else {
			*fieldPtr = uq.Get(fm.compID)
		}
	}
}

// All returns a Go range iterator over all matching entities. Use with
// range-over-func syntax:
//
//	for entity, bundle := range q.All() { ... }
//
// Breaking early is safe — the underlying Ark query is properly closed.
//
// The *T bundle pointer is reused across iterations. Component pointers
// inside it point directly into Ark's column storage and are valid until
// the next iteration or until the world is modified. Do not store the
// bundle pointer beyond the loop body.
func (q *Query[T]) All() iter.Seq2[ecs.Entity, *T] {
	return func(yield func(ecs.Entity, *T) bool) {
		uq := q.filter.Query()
		for uq.Next() {
			q.populateBundle(&uq)
			if !yield(uq.Entity(), &q.bundle) {
				uq.Close()
				return
			}
		}
	}
}

// Each calls fn for every matching entity. Cannot break early — use [Query.All]
// with a range loop and break statement if needed.
func (q *Query[T]) Each(fn func(ecs.Entity, *T)) {
	uq := q.filter.Query()
	for uq.Next() {
		q.populateBundle(&uq)
		fn(uq.Entity(), &q.bundle)
	}
}

// Count returns the number of entities matching this query's filter.
// Iterates archetypes but not individual entities — O(archetypes).
func (q *Query[T]) Count() int {
	uq := q.filter.Query()
	defer uq.Close()
	return uq.Count()
}

// Any returns true if at least one entity matches this query's filter.
func (q *Query[T]) Any() bool {
	return q.Count() > 0
}
