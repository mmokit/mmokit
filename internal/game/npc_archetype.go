package game

// NPCArchetype enumerates the v2 enemy archetypes. Values are wire-stable
// (replicated via NPCAI.Archetype) — append new variants at the end.
// Slot 2 originally held Kamikaze ("charge + beep + detonate"); replaced
// with Lancer (telegraphed line-charge) at the same wire value.
const (
	ArchetypeBrawler   uint8 = 0
	ArchetypeArtillery uint8 = 1
	ArchetypeLancer    uint8 = 2
)

// NPCAIState — current state-machine slot for an NPC.
//
// AIStateBeep (5) is no longer used after Kamikaze was replaced by Lancer
// but the constant is kept at its wire value to avoid renumbering. The
// Lancer states (Windup / Charge / Recover) appended at 7/8/9.
const (
	AIStateIdle         uint8 = 0
	AIStateAcquire      uint8 = 1
	AIStateEngage       uint8 = 2
	AIStateLeash        uint8 = 3
	AIStateCast         uint8 = 4 // PVE v2: Artillery casting an AoE
	AIStateBeep         uint8 = 5 // unused (was Kamikaze detonation telegraph)
	AIStateApproach     uint8 = 6 // PVE v2: alerted, moving toward target before lock
	AIStateLanceWindup  uint8 = 7 // PVE v2: Lancer holding telegraph
	AIStateLanceCharge  uint8 = 8 // PVE v2: Lancer dashing through committed line
	AIStateLanceRecover uint8 = 9 // PVE v2: Lancer cooldown after charge
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
	LockRange      float32 // PVE v2: must be within this to start locking. 0 = lock at AggroRadius (legacy behavior).
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
			LockRange:      cfg.BrawlerAggroRadius, // same as aggro → no approach state (legacy)
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
			LockRange:      50, // ~half of aggro — approach before locking
			MotionPolicy:   MotionStationary,
			DamagePerShot:  0,
			FireRate:       0,
		}
	case ArchetypeLancer:
		return ArchetypeDefaults{
			HP:             cfg.LancerHP,
			Shield:         cfg.LancerShield,
			MaxSpeed:       cfg.LancerMaxSpeed,
			TurnRate:       cfg.LancerTurnRate,
			PreferredRange: cfg.LancerLanceRange, // close-in stalker
			WeaponRange:    cfg.LancerLanceRange,
			AggroRadius:    cfg.LancerAggroRadius,
			LockRange:      cfg.LancerLockRange,
			MotionPolicy:   MotionCharge,
			DamagePerShot:  0, // damage applied during charge tick
			FireRate:       0,
		}
	}
	panic("archetypeDefaults: unknown archetype")
}

// FairnessFactor describes how dodgeable a telegraphed AoE is:
//
//	FairnessStrict — base-speed player at marker center clears the radius
//	FairnessEffort — base-speed gets you partway, Afterburner clears it
//	FairnessFocusFire — not spatially dodgeable; counter-play is to
//	                    kill or interrupt before resolution
type FairnessFactor float32

const (
	FairnessStrict    FairnessFactor = 1.0
	FairnessEffort    FairnessFactor = 0.4
	FairnessFocusFire FairnessFactor = 0.0
)

// archetypeAoEParams returns (radius, castTime, fairness) for archetypes
// that emit telegraphed AoEs. Returns zero values for archetypes that
// don't (Brawler — pure hitscan).
func archetypeAoEParams(cfg *GameConfig, arch uint8) (radius, castTime float32, fairness FairnessFactor) {
	switch arch {
	case ArchetypeArtillery:
		return cfg.ArtilleryAoERadius, cfg.ArtilleryCastTime, FairnessEffort
	case ArchetypeLancer:
		// Lancer is a line-charge, not a circular AoE. Counter-play is
		// to sidestep the telegraphed line or punish the recovery, not
		// "stand outside this circle." FocusFire instructs the fairness
		// test to skip the spatial-dodge check for this archetype.
		return 0, 0, FairnessFocusFire
	}
	return 0, 0, FairnessStrict
}

// PlayerBaseSpeedForFairness is the reference speed used in the
// fair-by-design invariant. Matches the in-game ship base MaxSpeed.
const PlayerBaseSpeedForFairness float32 = 6
