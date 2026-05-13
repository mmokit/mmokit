package mmokit

import (
	"github.com/mlange-42/ark/ecs"
)

// Get returns a pointer to the entity's component of type T, or nil if the
// entity is dead or does not have the component.
func Get[T any](e Entity) *T {
	stage := e.Stage()
	if stage == nil {
		return nil
	}
	h := e.Handle()
	if h == (ecs.Entity{}) {
		return nil
	}
	m := ecs.NewMap1[T](stage.ECSWorld())
	if !m.HasAll(h) {
		return nil
	}
	return m.Get(h)
}

// Has reports whether the entity has a component of type T.
func Has[T any](e Entity) bool {
	stage := e.Stage()
	if stage == nil {
		return false
	}
	h := e.Handle()
	if h == (ecs.Entity{}) {
		return false
	}
	m := ecs.NewMap1[T](stage.ECSWorld())
	return m.HasAll(h)
}

// Set installs the component on the entity, adding it if absent or
// overwriting if present. No-op on dead entities — but logs a
// use-after-free diagnostic when called on a non-zero netID whose
// underlying ECS handle is no longer alive (foundation deferral c).
func Set[T any](e Entity, v T) {
	stage := e.Stage()
	if stage == nil {
		return
	}
	h := e.Handle()
	if h == (ecs.Entity{}) {
		return
	}
	if !stage.ECSWorld().Alive(h) {
		if e.NetID() != 0 && stage.Engine() != nil {
			stage.Engine().Log.Log("mmokit", "Set: entity netID=%d not alive (probable use-after-free)", e.NetID())
		}
		return
	}
	m := ecs.NewMap1[T](stage.ECSWorld())
	if m.HasAll(h) {
		*m.Get(h) = v
		return
	}
	m.Add(h, &v)
}
