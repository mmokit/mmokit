// Package query provides a type-safe, struct-bundle based ECS query for the Ark ECS.
package query

import (
	"iter"
	"reflect"
	"unsafe"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
)

type fieldMeta struct {
	compID   ecs.ID
	offset   uintptr
	optional bool
}

// Query is a bundle-based ECS query. T must be a struct whose exported fields
// are pointers to component types (e.g. *component.Position). Fields tagged
// `ecs:"optional"` are populated when present and set to nil otherwise.
//
// By default, entities with Ghost or Replica components are excluded. Use
// IncludeAll() to include them, or Without[T]() to add custom exclusions.
type Query[T any] struct {
	filter ecs.UnsafeFilter
	fields []fieldMeta
	bundle T
	inited bool
}

// QueryOption configures a Query's filter behavior.
type QueryOption struct {
	tp         reflect.Type
	includeAll bool
}

// Without excludes entities that have component T.
func Without[T any]() QueryOption {
	return QueryOption{tp: reflect.TypeFor[T]()}
}

// IncludeAll disables the default Ghost/Replica exclusion.
func IncludeAll() QueryOption {
	return QueryOption{includeAll: true}
}

// Init initializes the query. sys must implement ECSWorld() *ecs.World.
// Panics if called twice or if T is not a valid bundle struct.
func (q *Query[T]) Init(sys interface{ ECSWorld() *ecs.World }, opts ...QueryOption) {
	if q.inited {
		panic("query.Query: Init called twice")
	}
	w := sys.ECSWorld()
	q.initFields(w)
	q.initFilter(w, opts)
	q.inited = true
}

// NewQuery creates and initializes a Query in one step.
func NewQuery[T any](sys interface{ ECSWorld() *ecs.World }, opts ...QueryOption) Query[T] {
	var q Query[T]
	q.Init(sys, opts...)
	return q
}

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

func (q *Query[T]) initFilter(w *ecs.World, opts []QueryOption) {
	var required []ecs.ID
	for i := range q.fields {
		if !q.fields[i].optional {
			required = append(required, q.fields[i].compID)
		}
	}
	q.filter = ecs.NewUnsafeFilter(w, required...)

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
		q.filter = q.filter.Without(withoutIDs...)
	}
}

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

// All returns a range iterator over all matching entities and their bundles.
// The bundle pointer is reused across iterations — copy fields if needed.
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

// Each calls fn for each matching entity.
func (q *Query[T]) Each(fn func(ecs.Entity, *T)) {
	uq := q.filter.Query()
	for uq.Next() {
		q.populateBundle(&uq)
		fn(uq.Entity(), &q.bundle)
	}
}

// Count returns the number of matching entities.
func (q *Query[T]) Count() int {
	uq := q.filter.Query()
	defer uq.Close()
	return uq.Count()
}

// Any returns true if at least one entity matches.
func (q *Query[T]) Any() bool {
	return q.Count() > 0
}
