package mmokit

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/query"
)

// Query is a bundle-based ECS query. T must be a struct whose exported fields
// are pointers to component types. Fields tagged `ecs:"optional"` are set to
// nil when the entity lacks that component.
//
// By default, Ghost and Replica entities are excluded. Use [IncludeAll] to
// clear defaults, or [Without] to add extra exclusions.
//
// This is a type alias for [query.Query] — see that package for full
// documentation. The alias exists so game code can use the mmokit import
// exclusively; pkg/system and pkg/universe import pkg/query directly to
// avoid an import cycle.
type Query[T any] = query.Query[T]

// QueryOption configures a [Query] filter. See [Without] and [IncludeAll].
type QueryOption = query.QueryOption

// Without returns a [QueryOption] that excludes entities having component T.
// Multiple calls accumulate.
func Without[T any]() QueryOption {
	return query.Without[T]()
}

// IncludeAll returns a [QueryOption] that clears the default Ghost/Replica
// exclusions. Combine with [Without] for fully custom exclusion sets.
func IncludeAll() QueryOption {
	return query.IncludeAll()
}

// NewQuery creates and initializes a [Query] in one step.
func NewQuery[T any](sys interface{ ECSWorld() *ecs.World }, opts ...QueryOption) Query[T] {
	return query.NewQuery[T](sys, opts...)
}
