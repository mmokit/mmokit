package game

import (
	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// StationBundle is the entity-kind component bundle for trade stations.
// The Station component is local-only — replication needs only the
// position + EntityKind so the client can render the station marker.
type StationBundle struct {
	Station *gamecomp.Station `mmokit:"local"`
}

// StationLocalX, StationLocalY are the station's position in local coords
// inside its StationCell. Tied to the cross-boundary mesh-test belt around
// the world (CellSize, CellSize) corner: close enough that the belt is the
// obvious first mining target on undock, far enough that StationRadius
// doesn't overlap any asteroids in the 0_0 belt chunk (chunk centered at
// (CellSize-15, CellSize-15) with radius 20). Exported so main.go can
// derive Config.DefaultSpawn from StationCell + this offset.
const (
	StationLocalX float32 = 8100
	StationLocalY float32 = 8100
)

// SpawnStation creates the trade station entity in the station cell.
func (gw *GameWorld) SpawnStation() {
	entity := gw.SpawnEntity(
		mmokit.Position{X: StationLocalX, Y: StationLocalY},
		mmokit.WithEntityKind(gamecomp.TypeStation),
		mmokit.WithCollider(gw.Config.StationRadius),
		mmokit.WithComponents(),
	)
	netID := gw.C.NetworkID.Get(entity).ID
	gw.eng.Log.Log(CatPlayerSpawn, "station spawned: netID=%d pos=(%.1f,%.1f)", netID, StationLocalX, StationLocalY)
}

// CollectStationMapData returns map marker data for all stations in the world.
func (gw *GameWorld) CollectStationMapData() []*gamepb.MapStationInfo {
	filter := ecs.NewFilter3[gamecomp.Station, mmokit.Position, mmokit.CellCoord](gw.eng.ECS)
	query := filter.Query()
	defer query.Close() // ark holds a world write-lock for the duration of an
	// open query; a panic in the loop body would otherwise leak the lock and
	// trip the next write-side op with "cannot modify a locked world".
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
