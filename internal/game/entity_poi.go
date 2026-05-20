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

// SpawnPOI is the world-manifest entry point. Task 7 replaces this stub
// with the real implementation that translates def.Type / def.Roster /
// def.Tier into a SpawnPOIWithRoster call. The stub panics so the
// per-cell bootstrap loop fails loudly when world/pois.json is non-empty
// before the implementation lands.
func (gw *GameWorld) SpawnPOI(localX, localY float32, def mmokit.WorldPOI) {
	_ = localX
	_ = localY
	_ = def
	panic("Task 7: implement SpawnPOI(localX, localY, def mmokit.WorldPOI)")
}

// SpawnPOIWithRoster creates a POI entity at the given local position
// and spawns its roster of NPCs anchored to it. Returns the POI's
// network ID. Used by admin spawn commands and unit tests; Task 7 will
// route the manifest-driven SpawnPOI through this helper.
func (gw *GameWorld) SpawnPOIWithRoster(x, y float32, poiType uint8, rosterIdx uint16, tier uint8) uint32 {
	e := gw.stage.Spawn(
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindPOI},
		gamecomp.POI{
			Type:         poiType,
			Status:       gamecomp.POIStatusActive,
			AnchorRadius: gw.Config.POIAnchorRadius,
			LeashRadius:  gw.Config.POILeashRadius,
			RosterDefIdx: rosterIdx,
			Tier:         tier,
		},
	)

	poiNetID := e.NetID()
	gw.spawnPOIRoster(x, y, poiNetID, rosterIdx, tier)
	gw.poiRosters[poiNetID] = gw.collectRosterNetIDs(poiNetID)

	gw.eng.Log.Log(CatPOI, "poi: spawned netID=%d tier=%d type=%d pos=(%.0f,%.0f) roster=%s",
		poiNetID, tier, poiType, x, y, rosterForIdx(rosterIdx).Name)
	return poiNetID
}

// spawnPOIRoster spawns all NPC members of the POI's roster around the
// POI center. Tier scales HP/Dmg/Shield per the TierDef table.
func (gw *GameWorld) spawnPOIRoster(cx, cy float32, poiNetID uint32, rosterIdx uint16, tier uint8) {
	rng := rand.New(rand.NewPCG(uint64(poiNetID), uint64(rosterIdx)))
	roster := rosterForIdx(rosterIdx)
	mul := tierDef(tier).StatMultiplier
	for _, m := range roster.Members {
		for i := 0; i < m.Count; i++ {
			angle := rng.Float64() * (2 * math.Pi)
			r := rng.Float32() * m.SpreadRadius
			ox := r * float32(math.Cos(angle))
			oy := r * float32(math.Sin(angle))
			gw.SpawnNPC(cx+ox, cy+oy, m.Archetype, poiNetID, NPCSpawnModifiers{
				HPMul:     mul,
				DmgMul:    mul,
				ShieldMul: mul,
			})
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
