package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// SpatialSystem rebuilds the spatial hash grid each tick.
type SpatialSystem struct {
	gw     *game.GameWorld
	filter *ecs.Filter4[component.Position, component.Rotation, component.Collider, component.NetworkID]
}

func NewSpatialSystem(gw *game.GameWorld) *SpatialSystem {
	return &SpatialSystem{gw: gw}
}

func (s *SpatialSystem) Update(dt float32) {
	gw := s.gw
	if s.filter == nil {
		s.filter = ecs.NewFilter4[component.Position, component.Rotation, component.Collider, component.NetworkID](gw.ECS)
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

		gw.Grid.Insert(spatial.Entry{
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
