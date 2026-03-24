package game

import (
	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type stationMappers struct {
	base   *ecs.Map6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind]
	marker *ecs.Map1[gamecomp.Station]
}

func initStationEntity(gw *GameWorld) {
	m := &stationMappers{
		base:   ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.ECS),
		marker: ecs.NewMap1[gamecomp.Station](gw.ECS),
	}

	gw.Registry.Register(mmokit.EntityDef{
		Name:        "station",
		Description: "trade station",
		EntityType:  gamecomp.TypeStation,
		Spawnable:   false,
		Mappers:     m,
	})
}

// SpawnStation creates the trade station entity at the center of sector (0,0).
func (gw *GameWorld) SpawnStation() {
	m := gw.Registry.ByType(gamecomp.TypeStation).Mappers.(*stationMappers)
	netID := gw.NextNetID()
	cx := coords.SectorSize / 2
	cy := coords.SectorSize / 2
	entity := m.base.NewEntity(
		&mmokit.Position{X: cx, Y: cy},
		&mmokit.Velocity{},
		&mmokit.Rotation{},
		&mmokit.Collider{Radius: gw.Config.StationRadius, Layer: 0},
		&mmokit.NetworkID{ID: netID},
		&mmokit.EntityKind{Type: gamecomp.TypeStation},
	)
	gw.C.SectorCoord.Add(entity, &mmokit.SectorCoord{SX: 0, SY: 0})
	m.marker.Add(entity, &gamecomp.Station{})
	gw.Log.Log(CatSpawn, "station spawned: netID=%d pos=(%.1f,%.1f)", netID, cx, cy)
}

// CollectStationMapData returns map marker data for all stations in the world.
func (gw *GameWorld) CollectStationMapData() []*gamepb.MapStationInfo {
	filter := ecs.NewFilter3[gamecomp.Station, mmokit.Position, mmokit.SectorCoord](gw.ECS)
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
