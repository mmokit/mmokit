package game

import (
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
	e := gw.stage.Spawn(
		mmokit.Position{X: StationLocalX, Y: StationLocalY},
		mmokit.EntityKind{Type: gamecomp.KindStation},
		mmokit.Collider{Radius: gw.Config.StationRadius},
		gamecomp.Station{},
	)
	gw.eng.Log.Log(CatPlayerSpawn, "station spawned: netID=%d pos=(%.1f,%.1f)", e.NetID(), StationLocalX, StationLocalY)
}

// CollectStationMapData returns map marker data for all stations in the world.
func (gw *GameWorld) CollectStationMapData() []MapStationInfo {
	var stations []MapStationInfo
	mmokit.ForEach3(gw.stage, func(_ mmokit.Entity, _ *gamecomp.Station, pos *mmokit.Position, sec *mmokit.CellCoord) {
		stations = append(stations, MapStationInfo{
			CellX:  sec.CellX,
			CellY:  sec.CellY,
			LocalX: pos.X,
			LocalY: pos.Y,
			Name:   "TRADE STATION",
		})
	})
	return stations
}
