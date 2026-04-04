package main

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func setupReplication(w *ecs.World, cellSizeFn func() float32) *mmokit.ReplicatorRegistry {
	replicators := mmokit.NewReplicatorRegistry()
	replicators.Register(mmokit.AutoReplicator(KindPlayer,
		mmokit.ViewerRelativePosWithCellSize(ecs.NewMap1[mmokit.Position](w), ecs.NewMap1[mmokit.CellCoord](w), cellSizeFn),
		mmokit.QVelocity(ecs.NewMap1[mmokit.Velocity](w), 2000),
		mmokit.QSize(ecs.NewMap1[mmokit.Collider](w), 500),
		mmokit.Component(ecs.NewMap1[DebugInfo](w)),
		mmokit.Component(ecs.NewMap1[PlayerName](w)),
	))
	return replicators
}
