package game

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/logger"
)

type stationMappers struct {
	base   *ecs.Map6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind]
	marker *ecs.Map1[component.Station]
}

func initStationEntity(gw *GameWorld) {
	gw.stationMappers = &stationMappers{
		base:   ecs.NewMap6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind](gw.ECS),
		marker: ecs.NewMap1[component.Station](gw.ECS),
	}

	gw.Registry.Register(EntityDef{
		Name:        "station",
		Description: "trade station",
		EntityType:  component.TypeStation,
		Spawnable:   false,
	})
}

// SpawnStation creates the trade station entity at the origin.
func (gw *GameWorld) SpawnStation() {
	m := gw.stationMappers
	netID := gw.NextNetID()
	entity := m.base.NewEntity(
		&component.Position{X: 0, Y: 0},
		&component.Velocity{},
		&component.Rotation{},
		&component.Collider{Radius: gw.Config.StationRadius, Layer: 0},
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: component.TypeStation},
	)
	m.marker.Add(entity, &component.Station{})
	gw.Log.Log(logger.CatSpawn, "station spawned: netID=%d pos=(0,0)", netID)
}
