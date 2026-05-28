package game

import (
	"hash/fnv"
	"math"
	"math/rand/v2"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// DungeonBundle is the entity-kind bundle for a dungeon POI.
//
// Only the world-level Dungeon component travels with the entity — chamber
// state (clear/cooldown, mob roster, terminal-boss progress) is kept
// server-side in GameWorld.dungeonChambers keyed by the dungeon's netID.
// PlacedID tags dungeons spawned from world manifests so world.* verbs can
// despawn the dungeon + its walls + its chamber roster cleanly; it's
// optional so procgen/admin dungeons can omit it.
type DungeonBundle struct {
	Dungeon  *gamecomp.Dungeon
	PlacedID *gamecomp.PlacedID `mmokit:"optional"`
}

// SpawnDungeonAt is the world-manifest entry point. Delegates to
// SpawnDungeonFromGraph using def.Seed (or fnv64(def.ID) when zero) so
// the chamber layout is deterministic per-id — renaming a dungeon
// re-rolls its interior, moving it does not. Tags the dungeon marker
// + every wall + every chamber roster NPC with PlacedID{def.ID} so
// DespawnPlacedByID sweeps the whole subtree. Returns the dungeon
// entity's NetID.
func (gw *GameWorld) SpawnDungeonAt(localX, localY float32, def mmokit.WorldDungeon) uint32 {
	seed := uint64(def.Seed)
	if seed == 0 {
		seed = fnvDungeon(def.ID)
	}
	netID := gw.SpawnDungeonFromGraph(localX, localY, seed)

	// Tag dungeon marker.
	if e := mmokit.EntityByNetID(gw.stage, netID); e.Alive() {
		mmokit.Set(e, gamecomp.PlacedID{ID: def.ID})
	}
	// Tag every wall the dungeon procgen spawned.
	for _, wallNetID := range gw.dungeonWalls[netID] {
		if we := mmokit.EntityByNetID(gw.stage, wallNetID); we.Alive() {
			mmokit.Set(we, gamecomp.PlacedID{ID: def.ID})
		}
	}
	// Tag every chamber roster NPC.
	for _, chamber := range gw.dungeonChambers[netID] {
		for _, npcNetID := range chamber.AliveNetIDs {
			if ne := mmokit.EntityByNetID(gw.stage, npcNetID); ne.Alive() {
				mmokit.Set(ne, gamecomp.PlacedID{ID: def.ID})
			}
		}
	}

	gw.eng.Log.Log(CatPlayerSpawn, "dungeon spawned: id=%s name=%q netID=%d pos=(%.1f,%.1f) seed=%d",
		def.ID, def.Name, netID, localX, localY, seed)
	return netID
}

func fnvDungeon(id string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(id))
	return h.Sum64()
}

// SpawnDungeon creates the dungeon marker entity at the given local
// position with the given dungeon-state values. Returns the entity's
// NetID.
//
// The caller is responsible for spawning the walls + chambers + NPCs —
// SpawnDungeon only creates the world-level marker. Procgen
// (Tasks 19-22) wraps this with the geometry + roster setup.
func (gw *GameWorld) SpawnDungeon(x, y float32, d gamecomp.Dungeon) uint32 {
	// AoI visibility relies on SpatialSystem registering an entity in
	// the spatial grid — entities without a Collider are never indexed
	// and never replicated to clients. Give the dungeon a Collider with
	// Radius = OuterRadius so AoI queries within (viewerAoI + asteroid
	// radius) of the dungeon center pick it up; Layer=LayerEntity keeps
	// it transparent to LOS / shot / ship-vs-terrain collision (those
	// gate on LayerStatic | LayerProp).
	e := gw.stage.Spawn(
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindDungeon},
		mmokit.Collider{
			Radius: d.OuterRadius,
			Shape:  spatial.ShapeCircle,
			Layer:  spatial.LayerEntity,
		},
		d,
	)
	gw.eng.Log.Log(CatDungeon, "dungeon: spawned netID=%d pos=(%.0f,%.0f) name=%q entrances=%d",
		e.NetID(), x, y, d.Name, d.EntranceCount)
	return e.NetID()
}

// SpawnDungeonFromGraph runs the full procgen pipeline for one dungeon
// at world-space (centerX, centerY) with the given seed. Spawns:
//  1. the dungeon marker entity
//  2. all wall colliders
//  3. all chamber rosters (NPCs anchored to the dungeon + chamber)
//  4. records ChamberState in gw.dungeonChambers
//
// Returns the dungeon entity's NetID.
func (gw *GameWorld) SpawnDungeonFromGraph(centerX, centerY float32, seed uint64) uint32 {
	g := buildDungeonGraph(gw.Config, seed)

	// Entrance positions follow the same south-biased pattern as
	// pickEntranceAngles. Local coords relative to the dungeon center;
	// the dungeon entity itself sits at (centerX, centerY) so the
	// client can translate when it needs them.
	var ent [3]spatial.Vec2
	r := gw.Config.DungeonAsteroidRadius
	// Entrance 0: south
	ent[0] = spatial.Vec2{X: 0, Y: -r}
	if gw.Config.DungeonEntranceCount >= 2 {
		ent[1] = spatial.Vec2{
			X: r * float32(math.Cos(math.Pi/6)),
			Y: -r * float32(math.Sin(math.Pi/6)),
		}
	}
	if gw.Config.DungeonEntranceCount >= 3 {
		ent[2] = spatial.Vec2{
			X: -r * float32(math.Cos(math.Pi/6)),
			Y: -r * float32(math.Sin(math.Pi/6)),
		}
	}

	dungeonNetID := gw.SpawnDungeon(centerX, centerY, gamecomp.Dungeon{
		Name:          g.name,
		EntranceMask:  entranceMaskFor(g, gw.Config, 16),
		OuterRadius:   gw.Config.DungeonAsteroidRadius,
		EntranceCount: uint8(gw.Config.DungeonEntranceCount),
		EntranceX0:    ent[0].X, EntranceY0: ent[0].Y,
		EntranceX1:    ent[1].X, EntranceY1: ent[1].Y,
		EntranceX2:    ent[2].X, EntranceY2: ent[2].Y,
		Seed: seed,
	})

	// Generate walls once — reused for spawning the wall colliders
	// AND for rasterizing the NavGrid below.
	walls := generateWalls(g, gw.Config)
	// Spawn walls (world-space = local + center). Track the spawned wall
	// netIDs so dungeon.regenerate can despawn them cleanly without
	// orphaning colliders in the spatial grid.
	wallNetIDs := make([]uint32, 0, len(walls))
	for _, w := range walls {
		netID := gw.SpawnDungeonWall(centerX+w.Center.X, centerY+w.Center.Y, w.Width, w.Height, w.Rotation)
		wallNetIDs = append(wallNetIDs, netID)
	}
	gw.dungeonWalls[dungeonNetID] = wallNetIDs

	// Build the NavGrid for this dungeon and shift its origin so all
	// cells (and pathfinding queries) are in world-space — NPCs query
	// against pos.X/pos.Y directly.
	ng := buildDungeonNavGrid(g, walls, gw.Config)
	ng.Origin.X += centerX
	ng.Origin.Y += centerY
	gw.dungeonNavGrids[dungeonNetID] = ng

	// Spawn chambers + rosters. Chamber 0 is the entry — no roster.
	chambers := make(map[uint16]*ChamberState)
	for _, c := range g.chambers {
		if c.isEntry {
			continue
		}
		rngU64 := seed ^ uint64(c.id+1)*0xbf58476d1ce4e5b9
		rosterIdx := rosterIdxForRole(c.role, rngU64)
		state := &ChamberState{
			ID:           c.id,
			Center:       spatial.Vec2{X: centerX + c.center.X, Y: centerY + c.center.Y},
			Radius:       c.radius,
			Role:         c.role,
			Status:       ChamberActive,
			RosterDefIdx: rosterIdx,
		}
		chambers[c.id] = state
		gw.spawnChamberRoster(dungeonNetID, state)
	}
	gw.dungeonChambers[dungeonNetID] = chambers

	gw.eng.Log.Log(CatDungeonGen, "dungeon procgen: seed=%d chambers=%d entrances=%d",
		seed, len(chambers), gw.Config.DungeonEntranceCount)
	return dungeonNetID
}

// spawnChamberRoster spawns every roster member for the chamber and
// records their netIDs in c.AliveNetIDs. Each NPC's DungeonAnchor is
// retagged with the chamber ID (SpawnNPC attaches DungeonAnchor with
// ChamberID=0 — we override here so the chamber lifecycle can identify
// per-chamber kills). The roster's Elite/Main flags pass through to
// SpawnNPC so champion mobs scale up and BossGuardians are flagged as
// main bosses.
func (gw *GameWorld) spawnChamberRoster(dungeonNetID uint32, c *ChamberState) {
	roster := dungeonRosters[c.RosterDefIdx]
	rng := rand.New(rand.NewPCG(uint64(dungeonNetID), uint64(c.ID)+uint64(c.RespawnCount)))
	for _, m := range roster.Members {
		mods := NPCSpawnModifiers{Elite: m.Elite, Main: m.Main}
		for i := 0; i < m.Count; i++ {
			angle := rng.Float64() * 2 * math.Pi
			r := rng.Float32() * m.SpreadRadius
			px := c.Center.X + r*float32(math.Cos(angle))
			py := c.Center.Y + r*float32(math.Sin(angle))
			e := gw.SpawnNPC(px, py, m.Archetype, dungeonNetID, mods)
			if anchor := mmokit.Get[gamecomp.DungeonAnchor](e); anchor != nil {
				anchor.ChamberID = c.ID
			}
			c.AliveNetIDs = append(c.AliveNetIDs, e.NetID())
		}
	}
}
