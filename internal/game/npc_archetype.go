package game

// NPCArchetype enumerates the v2 enemy archetypes. Values are wire-stable
// (replicated via NPCAI.Archetype) — append new variants at the end.
const (
	ArchetypeBrawler   uint8 = 0
	ArchetypeArtillery uint8 = 1
	ArchetypeKamikaze  uint8 = 2
)

// NPCAIState — current state-machine slot for an NPC.
const (
	AIStateIdle    uint8 = 0
	AIStateAcquire uint8 = 1
	AIStateEngage  uint8 = 2
	AIStateLeash   uint8 = 3
	AIStateCast    uint8 = 4 // PVE v2: Artillery casting an AoE
	AIStateBeep    uint8 = 5 // PVE v2: Kamikaze charging detonation
)

// MotionPolicy — how an NPC moves while in Engage. Applied per-archetype.
const (
	MotionCharge     uint8 = 0
	MotionStationary uint8 = 1 // Artillery between casts; no retreat
)

// ArchetypeDefaults holds the spawn-time defaults for an archetype.
// Lifted from GameConfig so a single source-of-truth populates NPCAI on
// SpawnNPC; config changes apply to newly spawned NPCs only (existing
// NPCs keep their captured copy).
type ArchetypeDefaults struct {
	HP             float32
	Shield         float32
	MaxSpeed       float32
	TurnRate       float32
	PreferredRange float32
	WeaponRange    float32
	AggroRadius    float32
	MotionPolicy   uint8
	DamagePerShot  float32
	FireRate       float32
}

// archetypeDefaults returns the captured defaults for an archetype from
// the active GameConfig. Panics on unknown archetype — caller error.
func archetypeDefaults(cfg *GameConfig, kind uint8) ArchetypeDefaults {
	switch kind {
	case ArchetypeBrawler:
		return ArchetypeDefaults{
			HP:             cfg.BrawlerHP,
			Shield:         cfg.BrawlerShield,
			MaxSpeed:       cfg.BrawlerMaxSpeed,
			TurnRate:       cfg.BrawlerTurnRate,
			PreferredRange: cfg.BrawlerPreferredRange,
			WeaponRange:    cfg.BrawlerWeaponRange,
			AggroRadius:    cfg.BrawlerAggroRadius,
			MotionPolicy:   MotionCharge,
			DamagePerShot:  cfg.BrawlerDamagePerShot,
			FireRate:       cfg.BrawlerFireRate,
		}
	case ArchetypeArtillery:
		return ArchetypeDefaults{
			HP:             cfg.ArtilleryHP,
			Shield:         cfg.ArtilleryShield,
			MaxSpeed:       cfg.ArtilleryMaxSpeed,
			TurnRate:       cfg.ArtilleryTurnRate,
			PreferredRange: cfg.ArtilleryWeaponRange,
			WeaponRange:    cfg.ArtilleryWeaponRange,
			AggroRadius:    cfg.ArtilleryAggroRadius,
			MotionPolicy:   MotionStationary,
			DamagePerShot:  0,
			FireRate:       0,
		}
	case ArchetypeKamikaze:
		return ArchetypeDefaults{
			HP:             cfg.KamikazeHP,
			Shield:         cfg.KamikazeShield,
			MaxSpeed:       cfg.KamikazeMaxSpeed,
			TurnRate:       cfg.KamikazeTurnRate,
			PreferredRange: cfg.KamikazeDetonateRange,
			WeaponRange:    cfg.KamikazeDetonateRange,
			AggroRadius:    cfg.KamikazeAggroRadius,
			MotionPolicy:   MotionCharge,
			DamagePerShot:  0, // damage via AoE on detonation
			FireRate:       0,
		}
	}
	panic("archetypeDefaults: unknown archetype")
}
