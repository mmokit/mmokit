package mmokit

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/query"
)

// Query is a bundle-based ECS query. T must be a struct whose exported fields
// are pointers to component types (e.g. *component.Position). Fields tagged
// `ecs:"optional"` are populated when present and set to nil otherwise.
//
// By default, entities with Ghost or Replica components are excluded. Use
// IncludeAll() to include them, or Without[T]() to add custom exclusions.
type Query[T any] = query.Query[T]

// QueryOption configures a Query's filter behavior.
type QueryOption = query.QueryOption

// Without excludes entities that have component T.
func Without[T any]() QueryOption {
	return query.Without[T]()
}

// IncludeAll disables the default Ghost/Replica exclusion.
func IncludeAll() QueryOption {
	return query.IncludeAll()
}

// NewQuery creates and initializes a Query in one step.
func NewQuery[T any](sys interface{ ECSWorld() *ecs.World }, opts ...QueryOption) Query[T] {
	return query.NewQuery[T](sys, opts...)
}
