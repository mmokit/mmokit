package game

import (
	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/coords"
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

// SpawnStation creates the trade station entity at the center of sector (0,0).
func (gw *GameWorld) SpawnStation() {
	m := gw.stationMappers
	netID := gw.NextNetID()
	cx := coords.SectorSize / 2
	cy := coords.SectorSize / 2
	entity := m.base.NewEntity(
		&component.Position{X: cx, Y: cy},
		&component.Velocity{},
		&component.Rotation{},
		&component.Collider{Radius: gw.Config.StationRadius, Layer: 0},
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: component.TypeStation},
	)
	gw.SectorCoordMap.Add(entity, &component.SectorCoord{SX: 0, SY: 0})
	m.marker.Add(entity, &component.Station{})
	gw.Log.Log(CatSpawn, "station spawned: netID=%d pos=(%.1f,%.1f)", netID, cx, cy)
}

// CollectStationMapData returns map marker data for all stations in the world.
func (gw *GameWorld) CollectStationMapData() []*gamepb.MapStationInfo {
	filter := ecs.NewFilter3[component.Station, component.Position, component.SectorCoord](gw.ECS)
	query := filter.Query()
	var stations []*gamepb.MapStationInfo
	for query.Next() {
		_, pos, sec := query.Get()
		stations = append(stations, &gamepb.MapStationInfo{
			SectorX: sec.SX,
			SectorY: sec.SY,
			LocalX:  pos.X,
			LocalY:  pos.Y,
			Name:    "TRADE STATION",
		})
	}
	return stations
}
