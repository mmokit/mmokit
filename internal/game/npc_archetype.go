package game

// NPCArchetype enumerates the v1 enemy archetypes. Values are wire-stable
// (replicated via NPCAI.Archetype) — append new variants at the end.
const (
	ArchetypeBrawler uint8 = 0
	ArchetypeSniper  uint8 = 1
	ArchetypeSwarmer uint8 = 2
)

// NPCAIState — current state-machine slot for an NPC.
const (
	AIStateIdle    uint8 = 0
	AIStateAcquire uint8 = 1
	AIStateEngage  uint8 = 2
	AIStateLeash   uint8 = 3
)

// MotionPolicy — how an NPC moves while in Engage. Applied per-archetype.
const (
	MotionCharge    uint8 = 0
	MotionHoldRange uint8 = 1
	MotionEncircle  uint8 = 2
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
	case ArchetypeSniper:
		return ArchetypeDefaults{
			HP:             cfg.SniperHP,
			Shield:         cfg.SniperShield,
			MaxSpeed:       cfg.SniperMaxSpeed,
			TurnRate:       cfg.SniperTurnRate,
			PreferredRange: cfg.SniperPreferredRange,
			WeaponRange:    cfg.SniperWeaponRange,
			AggroRadius:    cfg.SniperAggroRadius,
			MotionPolicy:   MotionHoldRange,
			DamagePerShot:  cfg.SniperDamagePerShot,
			FireRate:       cfg.SniperFireRate,
		}
	case ArchetypeSwarmer:
		return ArchetypeDefaults{
			HP:             cfg.SwarmerHP,
			Shield:         cfg.SwarmerShield,
			MaxSpeed:       cfg.SwarmerMaxSpeed,
			TurnRate:       cfg.SwarmerTurnRate,
			PreferredRange: cfg.SwarmerPreferredRange,
			WeaponRange:    cfg.SwarmerWeaponRange,
			AggroRadius:    cfg.SwarmerAggroRadius,
			MotionPolicy:   MotionEncircle,
			DamagePerShot:  cfg.SwarmerDamagePerShot,
			FireRate:       cfg.SwarmerFireRate,
		}
	}
	panic("unknown archetype")
}
