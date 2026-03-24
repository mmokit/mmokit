package system

import (
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// PhysicsSystem wraps the generic engine physics system.
type PhysicsSystem struct {
	inner *mmokit.PhysicsSystem
}

func NewPhysicsSystem(gw *game.GameWorld) *PhysicsSystem {
	return &PhysicsSystem{
		inner: mmokit.NewPhysicsSystem(gw.ECS),
	}
}

func (s *PhysicsSystem) Name() string { return "Physics" }

func (s *PhysicsSystem) Update(dt float32) {
	s.inner.Update(dt)
}
