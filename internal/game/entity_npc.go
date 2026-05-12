package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// NPCBundle is the entity-kind component bundle for NPC enemy ships.
// NPCs do not carry LockedBy — the combat-warning ring belongs only on
// the local player's own ship. Leashing is intentionally NOT in the
// bundle: it's a transient state marker added/removed dynamically by
// the leash subsystem. Adding it as a bundle field would attach it at
// spawn time (via EnsureEntityKindComponents), which would tag every
// fresh NPC as currently leashing.
type NPCBundle struct {
	Health        *gamecomp.Health
	Shield        *gamecomp.Shield
	StatusEffects *gamecomp.StatusEffects
	TargetLock    *gamecomp.TargetLock `mmokit:"local"` // multi-slot but always MaxSlots=1 for NPCs
	NPCAI         *gamecomp.NPCAI
	POIAnchor     *gamecomp.POIAnchor `mmokit:"local"`
}

// SpawnNPC creates an NPC ship of the given archetype, anchored at the
// given local position with anchor link to poiNetID. Pass poiNetID=0
// for console-spawned test NPCs (they leash to their spawn position
// when no POI exists).
func (gw *GameWorld) SpawnNPC(x, y float32, archetype uint8, poiNetID uint32) ecs.Entity {
	defaults := archetypeDefaults(gw.Config, archetype)
	br := boundingRadius(gw.Config.NpcWidth, gw.Config.NpcHeight)

	handle := gw.stage.SpawnEntity(
		mmokit.Position{X: x, Y: y},
		mmokit.WithEntityKind(gamecomp.KindNPC),
		mmokit.WithCollider(br),
		mmokit.WithRotation(0),
		mmokit.WithComponents(),
	)
	entity := mmokit.EntityFromECS(gw.stage, handle)

	if col := mmokit.Get[mmokit.Collider](entity); col != nil {
		col.Width = gw.Config.NpcWidth
		col.Height = gw.Config.NpcHeight
		col.Layer = gamecomp.LayerPlayer
		col.Shape = mmokit.ShapeRect
	}

	mmokit.Set(entity, gamecomp.Health{Current: defaults.HP, Max: defaults.HP})
	mmokit.Set(entity, gamecomp.Shield{
		Current:    defaults.Shield,
		Max:        defaults.Shield,
		RegenRate:  gw.Config.NpcShieldRegenRate,
		RegenDelay: gw.Config.NpcShieldRegenDelay,
	})
	mmokit.Set(entity, gamecomp.TargetLock{
		MaxSlots: gw.Config.LockMaxSlotsNPC,
		Range:    defaults.AggroRadius, // NPCs lock anything they can aggro
	})
	mmokit.Set(entity, gamecomp.NPCAI{
		Archetype:      archetype,
		State:          AIStateIdle,
		MaxSpeed:       defaults.MaxSpeed,
		TurnRate:       defaults.TurnRate,
		PreferredRange: defaults.PreferredRange,
		WeaponRange:    defaults.WeaponRange,
		AggroRadius:    defaults.AggroRadius,
		MotionPolicy:   defaults.MotionPolicy,
		DamagePerShot:  defaults.DamagePerShot,
		FireRate:       defaults.FireRate,
	})
	if poiNetID != 0 {
		mmokit.Set(entity, gamecomp.POIAnchor{POINetID: poiNetID})
	}

	gw.eng.Log.Log(CatPlayerSpawn, "npc spawned: netID=%d archetype=%d pos=(%.0f,%.0f) anchor=%d",
		entity.NetID(), archetype, x, y, poiNetID)
	return handle
}
