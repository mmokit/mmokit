package game

import (
	"math/rand/v2"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
	"github.com/zenion/mmokit/pkg/spatial"
)

// NPCBundle is the entity-kind component bundle for NPC enemy ships.
// NPCs do not carry LockedBy — the combat-warning ring belongs only on
// the local player's own ship. NPCs track their engage target via
// NPCAI.TargetNetID, not a lock indirection. Leashing is
// intentionally NOT in the bundle: it's a transient state marker added/
// removed dynamically by the leash subsystem. Adding it as a bundle
// field would attach it at spawn time (via EnsureEntityKindComponents),
// which would tag every fresh NPC as currently leashing.
type NPCBundle struct {
	Health        *gamecomp.Health
	Shield        *gamecomp.Shield
	StatusEffects *gamecomp.StatusEffects
	NPCAI         *gamecomp.NPCAI
	DungeonAnchor *gamecomp.DungeonAnchor `mmokit:"local"`
	Pathing       *gamecomp.Pathing       `mmokit:"local"`
	// PlacedID is attached only to NPCs spawned as part of a world-manifest
	// POI / dungeon (so DespawnPlacedByID cascades to the roster). NPCs
	// spawned via admin verbs / chamber respawn leave the field absent;
	// the `mmokit:"optional"` tag tells the framework to skip the
	// replication-binding presence check while still transferring the
	// component across cell boundaries when set.
	PlacedID *gamecomp.PlacedID `mmokit:"optional"`
}

// NPCSpawnModifiers customizes a spawn-time NPC: elite roster variants
// (multiplied HP/damage/speed for ChamberSideBoss "champion" entries),
// the BossGuardian "main boss" flag for ChamberTerminal rosters, and
// explicit HP/Damage/Shield multipliers (used by tier-based difficulty
// scaling — they stack on top of the Elite scaling). Pass the zero
// value for vanilla spawns. Modifier flags/values are spawn-time only —
// they're applied to the captured NPCAI/Health/Shield stats during
// SpawnNPC and not stored on the entity afterwards.
//
// A zero multiplier is treated as 1.0 (no-op) so callers can fill in
// only the fields they care about.
type NPCSpawnModifiers struct {
	Elite     bool    // multiplies HP/Damage/Speed via Config.BossSolo* multipliers
	Main      bool    // reserved for BossGuardian-only hooks (no per-stat multiplier here — the archetype itself already encodes the Main-boss base stats)
	HPMul     float32 // additional HP multiplier; zero treated as 1.0
	DmgMul    float32 // additional damage multiplier; zero treated as 1.0
	ShieldMul float32 // additional shield multiplier; zero treated as 1.0
}

// SpawnNPC creates an NPC ship of the given archetype, anchored at the
// given local position with anchor link to poiNetID. Pass poiNetID=0
// for console-spawned test NPCs (they leash to their spawn position
// when no POI exists). Modifier flags scale the archetype's base stats
// at spawn time — see NPCSpawnModifiers.
func (gw *GameWorld) SpawnNPC(x, y float32, archetype uint8, poiNetID uint32, mods NPCSpawnModifiers) mmokit.Entity {
	d := archetypeDefaults(gw.Config, archetype)

	// Apply elite scaling to the archetype defaults BEFORE the component
	// values are captured. Elite is the "champion" flag on a
	// ChamberSideBoss roster — HP/damage/speed get multiplied so the boss
	// reads as visibly stronger than its escorts.
	if mods.Elite {
		d.HP *= gw.Config.BossSoloHPMultiplier
		d.MaxSpeed *= gw.Config.BossSoloSpeedMultiplier
		d.DamagePerShot *= gw.Config.BossSoloDmgMultiplier
	}

	// Tier / explicit multipliers stack on top of Elite scaling. Zero
	// is treated as 1.0 (no-op) so callers can fill in only the fields
	// they care about.
	if mods.HPMul > 0 {
		d.HP *= mods.HPMul
	}
	if mods.DmgMul > 0 {
		d.DamagePerShot *= mods.DmgMul
	}
	if mods.ShieldMul > 0 {
		d.Shield *= mods.ShieldMul
	}

	// Initial ability-cooldown offsets so a pack of NPCs that aggro on the
	// same tick don't fire their telegraphed attacks in lockstep. Each
	// archetype seeds the relevant clock; values are sampled uniformly in
	// [0, base × (1 + jitter)]. The whole-base ceiling guarantees the
	// first cast/charge isn't unfairly delayed past the steady-state
	// cadence — but two NPCs spawning together are statistically
	// guaranteed to drift apart.
	jitter := gw.Config.NPCAttackJitter
	var initCastCD, initSpecialCD, initRecover float32
	switch archetype {
	case ArchetypeArtillery, ArchetypeEliteArtillery:
		initCastCD = rand.Float32() * gw.Config.ArtilleryCastCooldown * (1 + jitter)
	case ArchetypeBrawler, ArchetypeEliteBrawler:
		initSpecialCD = rand.Float32() * gw.Config.BrawlerSpecialCooldown * (1 + jitter)
	case ArchetypeDisruptor:
		initSpecialCD = rand.Float32() * gw.Config.DisruptorDebuffCooldown * (1 + jitter)
	case ArchetypeLancer, ArchetypeEliteLancer:
		// RecoverRemaining is the gate for Lancer charges (see tickEngage:
		// the windup trigger requires RecoverRemaining<=0). Stamping a
		// small initial recover prevents instant-windup-on-aggro for
		// freshly spawned packs.
		initRecover = rand.Float32() * gw.Config.LancerRecoverTime * (1 + jitter)
	}

	// Dungeon-spawned NPCs override their archetype's default MotionPolicy
	// with MotionPathfind so they navigate around walls (the dungeon
	// NavGrid is built by SpawnDungeonFromGraph). Open-world NPCs keep
	// their archetype-default policy (Charge / Stationary) — no NavGrid
	// exists for the open world. Archetypes that are *already* Stationary
	// (Artillery, BossGuardian) keep MotionStationary even inside a
	// dungeon — they're designed to hold position and pathfind would
	// compute a route they can't follow at speed 0.
	motion := d.MotionPolicy
	if poiNetID != 0 && motion != MotionStationary {
		if _, ok := gw.dungeonNavGrids[poiNetID]; ok {
			motion = MotionPathfind
		}
	}

	components := []any{
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindNPC},
		mmokit.Collider{
			Width:  gw.Config.NpcWidth,
			Height: gw.Config.NpcHeight,
			Layer:  spatial.LayerEntity,
			Shape:  mmokit.ShapeRect,
			Radius: boundingRadius(gw.Config.NpcWidth, gw.Config.NpcHeight),
		},
		mmokit.Rotation{},
		gamecomp.Health{Current: d.HP, Max: d.HP},
		gamecomp.Shield{
			Current:    d.Shield,
			Max:        d.Shield,
			RegenRate:  gw.Config.NpcShieldRegenRate,
			RegenDelay: gw.Config.NpcShieldRegenDelay,
		},
		gamecomp.StatusEffects{},
		gamecomp.NPCAI{
			Archetype:        archetype,
			State:            AIStateIdle,
			MaxSpeed:         d.MaxSpeed,
			TurnRate:         d.TurnRate,
			PreferredRange:   d.PreferredRange,
			WeaponRange:      d.WeaponRange,
			AggroRadius:      d.AggroRadius,
			LockRange:        d.LockRange,
			MotionPolicy:     motion,
			DamagePerShot:    d.DamagePerShot,
			FireRate:         d.FireRate,
			CastCooldown:     initCastCD,
			SpecialCooldown:  initSpecialCD,
			RecoverRemaining: initRecover,
		},
	}
	if poiNetID != 0 {
		components = append(components, gamecomp.DungeonAnchor{DungeonNetID: poiNetID})
	}
	if motion == MotionPathfind {
		components = append(components, gamecomp.Pathing{DungeonNetID: poiNetID})
	}

	e := gw.stage.Spawn(components...)
	gw.eng.Log.Log(CatPlayerSpawn, "npc spawned: netID=%d archetype=%d pos=(%.0f,%.0f) anchor=%d elite=%v main=%v",
		e.NetID(), archetype, x, y, poiNetID, mods.Elite, mods.Main)
	return e
}
