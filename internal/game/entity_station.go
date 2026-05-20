package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// StationBundle is the entity-kind component bundle for trade stations.
// The Station component is local-only — replication needs only the
// position + EntityKind so the client can render the station marker.
// PlacedID carries the world-manifest id so world.* verbs can despawn
// the station cleanly; it's local-only and survives cell transfer.
type StationBundle struct {
	Station  *gamecomp.Station  `mmokit:"local"`
	PlacedID *gamecomp.PlacedID `mmokit:"local"`
}

// SpawnStation creates a trade station entity at (localX, localY) inside
// the current cell using the provided world-manifest definition. When
// def.Radius is zero, falls back to gw.Config.StationRadius so dev
// manifests can omit the field and still get the sane default. Returns
// the station entity's NetID so admin verbs that spawn-then-track (e.g.
// world.place) can locate it post-spawn.
func (gw *GameWorld) SpawnStation(localX, localY float32, def mmokit.WorldStation) uint32 {
	radius := def.Radius
	if radius == 0 {
		radius = gw.Config.StationRadius
	}
	e := gw.stage.Spawn(
		mmokit.Position{X: localX, Y: localY},
		mmokit.EntityKind{Type: gamecomp.KindStation},
		mmokit.Collider{
			Radius: radius,
			Shape:  spatial.ShapeCircle,
			Layer:  spatial.LayerStatic,
		},
		gamecomp.Station{Name: def.Name},
		gamecomp.PlacedID{ID: def.ID},
	)
	gw.eng.Log.Log(CatPlayerSpawn, "station spawned: id=%s netID=%d pos=(%.1f,%.1f) name=%q",
		def.ID, e.NetID(), localX, localY, def.Name)
	return e.NetID()
}

// CollectStationMapData returns map marker data for all stations on this stage.
func (gw *GameWorld) CollectStationMapData() []MapStationInfo {
	var stations []MapStationInfo
	mmokit.ForEach3(gw.stage, func(_ mmokit.Entity, st *gamecomp.Station, pos *mmokit.Position, sec *mmokit.CellCoord) {
		name := st.Name
		if name == "" {
			name = "TRADE STATION"
		}
		stations = append(stations, MapStationInfo{
			CellX:  sec.CellX,
			CellY:  sec.CellY,
			LocalX: pos.X,
			LocalY: pos.Y,
			Name:   name,
		})
	})
	return stations
}
