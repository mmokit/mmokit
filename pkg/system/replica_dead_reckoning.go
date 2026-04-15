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
	// Freeze replicas whose source cell has gone silent this tick. A
	// border frame from the source resets UpdatedThisTick to true via
	// upsertBorderReplica; PreTick clears it back to false. Integrating
	// velocity when no frame arrived this tick would extrapolate stale
	// data and produce the "ghost drift" trails seen when a source cell
	// stops pushing frames between ticks.
	for _, b := range s.replicas.All() {
		if !b.Rep.UpdatedThisTick {
			continue
		}
		b.Pos.X += b.Vel.X * dt
		b.Pos.Y += b.Vel.Y * dt
	}

	for _, b := range s.ghosts.All() {
		b.Pos.X += b.Vel.X * dt
		b.Pos.Y += b.Vel.Y * dt
	}
}
