package mmokit

import (
	"github.com/mlange-42/ark/ecs"
)

// Get returns a pointer to the entity's component of type T, or nil if the
// entity is dead or does not have the component.
func Get[T any](e Entity) *T {
	h := e.resolveHandle()
	if h == (ecs.Entity{}) || e.stage == nil {
		return nil
	}
	m := ecs.NewMap1[T](e.stage.ECSWorld())
	if !m.HasAll(h) {
		return nil
	}
	return m.Get(h)
}

// Has reports whether the entity has a component of type T.
func Has[T any](e Entity) bool {
	h := e.resolveHandle()
	if h == (ecs.Entity{}) || e.stage == nil {
		return false
	}
	m := ecs.NewMap1[T](e.stage.ECSWorld())
	return m.HasAll(h)
}

// Set installs the component on the entity, adding it if absent or
// overwriting if present. No-op on dead entities.
func Set[T any](e Entity, v T) {
	h := e.resolveHandle()
	if h == (ecs.Entity{}) || e.stage == nil {
		return
	}
	m := ecs.NewMap1[T](e.stage.ECSWorld())
	if m.HasAll(h) {
		*m.Get(h) = v
		return
	}
	m.Add(h, &v)
}
