package system

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
)

// PhysicsSystem integrates velocity into position each tick.
// Skips Ghost and Replica entities.
type PhysicsSystem struct {
	world  *ecs.World
	filter *ecs.Filter2[component.Position, component.Velocity]
}

// NewPhysicsSystem creates a physics system operating on the given ECS world.
func NewPhysicsSystem(world *ecs.World) *PhysicsSystem {
	return &PhysicsSystem{world: world}
}

func (s *PhysicsSystem) Update(dt float32) {
	if s.filter == nil {
		s.filter = ecs.NewFilter2[component.Position, component.Velocity](s.world).
			Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
	}
	query := s.filter.Query()
	for query.Next() {
		pos, vel := query.Get()
		pos.X += vel.X * dt
		pos.Y += vel.Y * dt
	}
}
