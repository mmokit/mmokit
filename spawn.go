package mmokit

import (
	"github.com/mlange-42/ark/ecs"
)

// Despawn marks the entity for removal. The actual ECS removal happens at
// the next FlushRemovals in the simulation tick. Safe on dead/zero entities
// (no-op).
func Despawn(e Entity) {
	stage := e.Stage()
	if stage == nil {
		return
	}
	h := e.Handle()
	if h == (ecs.Entity{}) {
		return
	}
	stage.MarkForRemoval(h)
}
