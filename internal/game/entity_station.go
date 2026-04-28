package game

import (
	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// StationBundle is the entity-kind component bundle for trade stations.
// The Station component is local-only — replication needs only the
// position + EntityKind so the client can render the station marker.
type StationBundle struct {
	Station *gamecomp.Station `mmokit:"local"`
}

// SpawnStation creates the trade station entity at the center of the current cell.
func (gw *GameWorld) SpawnStation() {
	cx := coords.CellSize / 2
	cy := coords.CellSize / 2
	entity := gw.SpawnEntity(
		mmokit.Position{X: cx, Y: cy},
		mmokit.WithEntityKind(gamecomp.TypeStation),
		mmokit.WithCollider(gw.Config.StationRadius),
		mmokit.WithComponents(),
	)
	netID := gw.C.NetworkID.Get(entity).ID
	gw.eng.Log.Log(CatPlayerSpawn, "station spawned: netID=%d pos=(%.1f,%.1f)", netID, cx, cy)
}

// CollectStationMapData returns map marker data for all stations in the world.
func (gw *GameWorld) CollectStationMapData() []*gamepb.MapStationInfo {
	filter := ecs.NewFilter3[gamecomp.Station, mmokit.Position, mmokit.CellCoord](gw.eng.ECS)
	query := filter.Query()
	var stations []*gamepb.MapStationInfo
	for query.Next() {
		_, pos, sec := query.Get()
		stations = append(stations, &gamepb.MapStationInfo{
			CellX:  sec.CellX,
			CellY:  sec.CellY,
			LocalX: pos.X,
			LocalY: pos.Y,
			Name:   "TRADE STATION",
		})
	}
	return stations
}
