package mmokit

import (
	"github.com/mlange-42/ark/ecs"

	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// Commands is the per-stage deferred-mutation buffer. Aliased from
// pkg/universe so game code can refer to it as `mmokit.Commands`
// without importing universe directly.
type Commands = pkguniverse.Commands

// AddComponent queues a component add/overwrite for entity e. T is
// inferred from val. If the component is already present on e, it's
// overwritten (same semantics as mmokit.Set). Applied at next Flush
// (end of current system Update). No-op if e is dead at flush time.
//
// Safe to call from inside a system's Update query iteration — the
// actual ECS mutation runs after the system completes and the world
// lock is released.
func AddComponent[T any](c *Commands, e Entity, val T) {
	stage := e.Stage()
	if stage == nil {
		return // not on a stage; nothing to do
	}
	h := e.Handle()
	c.AddOp(func() {
		w := stage.ECSWorld()
		if !w.Alive(h) {
			return
		}
		m := ecs.NewMap1[T](w)
		if m.HasAll(h) {
			*m.Get(h) = val
			return
		}
		m.Add(h, &val)
	})
}

// RemoveComponent queues removal of component T from entity e at
// next Flush. T must be specified explicitly (no value to infer
// from). Silent no-op if e doesn't have the component or is dead.
func RemoveComponent[T any](c *Commands, e Entity) {
	stage := e.Stage()
	if stage == nil {
		return
	}
	h := e.Handle()
	c.AddOp(func() {
		w := stage.ECSWorld()
		if !w.Alive(h) {
			return
		}
		m := ecs.NewMap1[T](w)
		if m.HasAll(h) {
			m.Remove(h)
		}
	})
}
