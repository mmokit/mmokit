package system

import (
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/query"
)

// ReplicaDeadReckoningSystem advances replica and ghost entity positions
// each tick using their last-known velocity.
type ReplicaDeadReckoningSystem struct {
	engine.SystemBase
	replicas query.Query[struct {
		Pos *component.Position
		Vel *component.Velocity
		Rep *component.Replica
	}]
	ghosts query.Query[struct {
		Pos   *component.Position
		Vel   *component.Velocity
		Ghost *component.Ghost
	}]
}

func (s *ReplicaDeadReckoningSystem) Init() {
	s.replicas.Init(s, query.IncludeAll())
	s.ghosts.Init(s, query.IncludeAll())
}

func (s *ReplicaDeadReckoningSystem) Update(dt float32) {
	for _, b := range s.replicas.All() {
		b.Pos.X += b.Vel.X * dt
		b.Pos.Y += b.Vel.Y * dt
	}

	for _, b := range s.ghosts.All() {
		b.Pos.X += b.Vel.X * dt
		b.Pos.Y += b.Vel.Y * dt
	}
}
