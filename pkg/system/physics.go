package system

import (
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/query"
)

// PhysicsSystem integrates velocity into position each tick.
// Skips Ghost and Replica entities.
type PhysicsSystem struct {
	engine.SystemBase
	entities query.Query[struct {
		Pos *component.Position
		Vel *component.Velocity
	}]
}

func (s *PhysicsSystem) Init() {
	s.entities.Init(s)
}

func (s *PhysicsSystem) Update(dt float32) {
	for _, b := range s.entities.All() {
		b.Pos.X += b.Vel.X * dt
		b.Pos.Y += b.Vel.Y * dt
	}
}
