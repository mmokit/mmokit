package system

import (
	"github.com/zenion/mmoserver/internal/game"
	pkgsys "github.com/zenion/mmoserver/pkg/system"
)

// PhysicsSystem wraps the generic engine physics system.
type PhysicsSystem struct {
	inner *pkgsys.PhysicsSystem
}

func NewPhysicsSystem(gw *game.GameWorld) *PhysicsSystem {
	return &PhysicsSystem{
		inner: pkgsys.NewPhysicsSystem(gw.ECS),
	}
}

func (s *PhysicsSystem) Update(dt float32) {
	s.inner.Update(dt)
}
