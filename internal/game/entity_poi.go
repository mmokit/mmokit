package game

import (
	"math"
	"math/rand/v2"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// POIBundle is the component bundle for a Point-of-Interest entity.
type POIBundle struct {
	POI *gamecomp.POI
}

// spawnPOIs runs the per-cell POI procgen and spawns the resulting
// entities. Called from cell bootstrap after asteroids/belts have been
// materialized so the clearance check has real data.
func (gw *GameWorld) spawnPOIs() {
	belts := GenerateBelts(gw.RootCell, gw.Config.StationCell)
	defs := GeneratePOIs(gw.RootCell, gw.Config.StationCell, gw.Config, belts)
	for _, d := range defs {
		gw.SpawnPOI(d.X, d.Y, d.Type, d.RosterIdx)
	}
}

// SpawnPOI creates a POI entity at the given local position and spawns
// its roster of NPCs anchored to it. Returns the POI's network ID.
func (gw *GameWorld) SpawnPOI(x, y float32, poiType uint8, rosterIdx uint16) uint32 {
	e := gw.stage.Spawn(
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindPOI},
		gamecomp.POI{
			Type:         poiType,
			Status:       gamecomp.POIStatusActive,
			AnchorRadius: gw.Config.POIAnchorRadius,
			LeashRadius:  gw.Config.POILeashRadius,
			RosterDefIdx: rosterIdx,
		},
	)

	poiNetID := e.NetID()
	gw.spawnPOIRoster(x, y, poiNetID, rosterIdx)
	gw.poiRosters[poiNetID] = gw.collectRosterNetIDs(poiNetID)

	gw.eng.Log.Log(CatPOI, "poi: spawned netID=%d type=%d pos=(%.0f,%.0f) roster=%s",
		poiNetID, poiType, x, y, rosterForIdx(rosterIdx).Name)
	return poiNetID
}

// spawnPOIRoster spawns all NPC members of the POI's roster around the
// POI center.
func (gw *GameWorld) spawnPOIRoster(cx, cy float32, poiNetID uint32, rosterIdx uint16) {
	rng := rand.New(rand.NewPCG(uint64(poiNetID), uint64(rosterIdx)))
	roster := rosterForIdx(rosterIdx)
	for _, m := range roster.Members {
		for i := 0; i < m.Count; i++ {
			angle := rng.Float64() * (2 * math.Pi)
			r := rng.Float32() * m.SpreadRadius
			ox := r * float32(math.Cos(angle))
			oy := r * float32(math.Sin(angle))
			gw.SpawnNPC(cx+ox, cy+oy, m.Archetype, poiNetID)
		}
	}
}

// collectRosterNetIDs scans all NPC entities and returns the netIDs of
// those anchored to poiNetID. Used to rebuild the roster after respawn.
func (gw *GameWorld) collectRosterNetIDs(poiNetID uint32) []uint32 {
	var ids []uint32
	mmokit.ForEach1(gw.stage, func(e mmokit.Entity, anchor *gamecomp.DungeonAnchor) {
		if anchor.DungeonNetID != poiNetID {
			return
		}
		ids = append(ids, e.NetID())
	})
	return ids
}
