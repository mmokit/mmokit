package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
)

// PhysicsSystem integrates velocity into position.
type PhysicsSystem struct {
	gw     *game.GameWorld
	filter *ecs.Filter2[component.Position, component.Velocity]
}

func NewPhysicsSystem(gw *game.GameWorld) *PhysicsSystem {
	return &PhysicsSystem{gw: gw}
}

func (s *PhysicsSystem) Update(dt float32) {
	if s.filter == nil {
		s.filter = ecs.NewFilter2[component.Position, component.Velocity](s.gw.ECS)
	}

	query := s.filter.Query()
	for query.Next() {
		pos, vel := query.Get()
		pos.X += vel.X * dt
		pos.Y += vel.Y * dt
	}
}
