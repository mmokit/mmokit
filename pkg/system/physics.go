package system

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
)

// PhysicsSystem integrates velocity into position each tick.
// Skips Ghost and Replica entities.
type PhysicsSystem struct {
	engine.SystemBase
	filter *ecs.Filter2[component.Position, component.Velocity]
}

func (s *PhysicsSystem) Init() {
	s.filter = ecs.NewFilter2[component.Position, component.Velocity](s.ECSWorld()).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
}

func (s *PhysicsSystem) Update(dt float32) {
	query := s.filter.Query()
	for query.Next() {
		pos, vel := query.Get()
		pos.X += vel.X * dt
		pos.Y += vel.Y * dt
	}
}
