package game

import (
	"github.com/mlange-42/ark/ecs"

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
	handle := gw.SpawnEntity(
		mmokit.Position{X: StationLocalX, Y: StationLocalY},
		mmokit.WithEntityKind(gamecomp.KindStation),
		mmokit.WithCollider(gw.Config.StationRadius),
		mmokit.WithComponents(),
	)
	entity := mmokit.EntityFromECS(gw.Stage, handle)
	gw.eng.Log.Log(CatPlayerSpawn, "station spawned: netID=%d pos=(%.1f,%.1f)", entity.NetID(), StationLocalX, StationLocalY)
}

// CollectStationMapData returns map marker data for all stations in the world.
func (gw *GameWorld) CollectStationMapData() []MapStationInfo {
	filter := ecs.NewFilter3[gamecomp.Station, mmokit.Position, mmokit.CellCoord](gw.Stage.ECSWorld())
	query := filter.Query()
	defer query.Close() // ark holds a world write-lock for the duration of an
	// open query; a panic in the loop body would otherwise leak the lock and
	// trip the next write-side op with "cannot modify a locked world".
	var stations []MapStationInfo
	for query.Next() {
		_, pos, sec := query.Get()
		stations = append(stations, MapStationInfo{
			CellX:  sec.CellX,
			CellY:  sec.CellY,
			LocalX: pos.X,
			LocalY: pos.Y,
			Name:   "TRADE STATION",
		})
	}
	return stations
}
