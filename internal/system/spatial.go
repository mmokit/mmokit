package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// SpatialSystem rebuilds the spatial hash grid each tick.
type SpatialSystem struct {
	gw     *game.GameWorld
	filter *ecs.Filter4[mmokit.Position, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID]
}

func NewSpatialSystem(gw *game.GameWorld) *SpatialSystem {
	return &SpatialSystem{gw: gw}
}

func (s *SpatialSystem) Name() string { return "Spatial" }

func (s *SpatialSystem) Update(dt float32) {
	gw := s.gw
	if s.filter == nil {
		s.filter = ecs.NewFilter4[mmokit.Position, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID](gw.ECS)
	}

	gw.Grid.Clear()
	// Clear and rebuild NetID lookup
	for k := range gw.NetIDToEntity {
		delete(gw.NetIDToEntity, k)
	}

	query := s.filter.Query()
	for query.Next() {
		pos, rot, col, netID := query.Get()
		entity := query.Entity()

		gw.NetIDToEntity[netID.ID] = entity

		gw.Grid.Insert(mmokit.SpatialEntry{
			Entity:   entity,
			X:        pos.X,
			Y:        pos.Y,
			Radius:   col.Radius,
			Width:    col.Width,
			Height:   col.Height,
			Rotation: rot.Angle,
			Layer:    col.Layer,
			Shape:    col.Shape,
		})
	}
}
