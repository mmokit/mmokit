package game

import (
	"math"
	"math/rand/v2"

	gamecomp "github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// POIBundle is the component bundle for a Point-of-Interest entity.
// PlacedID tags POIs spawned from world manifests so world.* verbs can
// despawn the POI + its roster cleanly; it's optional so POIs spawned
// via poi.spawn (admin, no manifest id) can omit the field.
type POIBundle struct {
	POI      *gamecomp.POI
	PlacedID *gamecomp.PlacedID `mmokit:"optional"`
}

// SpawnPOI is the world-manifest entry point. Translates a world.POI def
// into a SpawnPOIWithRoster call, resolving the roster name to its table
// index. Validates tier (1..3); a bad tier logs a warning and returns
// silently so the per-cell bootstrap loop never crashes on a malformed
// manifest entry — validation at edit time is the editor's job. Tags the
// POI marker entity AND every roster NPC with PlacedID{def.ID} so
// world.* verbs can sweep the whole subtree with one DespawnPlacedByID
// call. Returns the POI marker's NetID (0 on validation failure).
func (gw *GameWorld) SpawnPOI(localX, localY float32, def mmokit.WorldPOI) uint32 {
	if def.Tier < 1 || def.Tier > 3 {
		gw.eng.Log.Log(CatPOI, "world: skip poi id=%s — invalid tier %d", def.ID, def.Tier)
		return 0
	}
	rosterIdx := RosterIdxForName(def.Roster)
	netID := gw.SpawnPOIWithRoster(localX, localY, gamecomp.POITypeCombat, rosterIdx, def.Tier)

	// Tag the POI marker entity.
	if e := mmokit.EntityByNetID(gw.stage, netID); e.Alive() {
		mmokit.Set(e, gamecomp.PlacedID{ID: def.ID})
	}
	// Tag every roster NPC the POI just spawned so DespawnPlacedByID
	// sweeps the whole subtree in one pass.
	for _, npcNetID := range gw.poiRosters[netID] {
		if ne := mmokit.EntityByNetID(gw.stage, npcNetID); ne.Alive() {
			mmokit.Set(ne, gamecomp.PlacedID{ID: def.ID})
		}
	}

	gw.eng.Log.Log(CatPOI, "poi spawned: id=%s netID=%d tier=%d roster=%s pos=(%.1f,%.1f)",
		def.ID, netID, def.Tier, def.Roster, localX, localY)
	return netID
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
