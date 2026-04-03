package system

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
)

// ReplicaDeadReckoningSystem advances replica and ghost entity positions
// each tick using their last-known velocity. This keeps entities moving
// smoothly during inter-node transfers (ghosts) and between replication
// updates (replicas). Without this, ghosts freeze at the crossing position
// and replicas stall when inter-node updates are delayed.
type ReplicaDeadReckoningSystem struct {
	engine.SystemBase
	replicaFilt *ecs.Filter3[component.Position, component.Velocity, component.Replica]
	ghostFilt   *ecs.Filter3[component.Position, component.Velocity, component.Ghost]
}

func (s *ReplicaDeadReckoningSystem) Init() {
	s.replicaFilt = ecs.NewFilter3[component.Position, component.Velocity, component.Replica](s.ECSWorld())
	s.ghostFilt = ecs.NewFilter3[component.Position, component.Velocity, component.Ghost](s.ECSWorld())
}

func (s *ReplicaDeadReckoningSystem) Update(dt float32) {
	// Advance replicas (inter-node entities from neighboring nodes)
	rq := s.replicaFilt.Query()
	for rq.Next() {
		pos, vel, _ := rq.Get()
		pos.X += vel.X * dt
		pos.Y += vel.Y * dt
	}

	// Advance ghosts (entities transferring away, kept as visual placeholders)
	gq := s.ghostFilt.Query()
	for gq.Next() {
		pos, vel, _ := gq.Get()
		pos.X += vel.X * dt
		pos.Y += vel.Y * dt
	}
}
