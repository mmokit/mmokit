package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
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
	POIAnchor     *gamecomp.POIAnchor `mmokit:"local"`
}

// SpawnNPC creates an NPC ship of the given archetype, anchored at the
// given local position with anchor link to poiNetID. Pass poiNetID=0
// for console-spawned test NPCs (they leash to their spawn position
// when no POI exists).
func (gw *GameWorld) SpawnNPC(x, y float32, archetype uint8, poiNetID uint32) mmokit.Entity {
	d := archetypeDefaults(gw.Config, archetype)

	components := []any{
		mmokit.Position{X: x, Y: y},
		mmokit.EntityKind{Type: gamecomp.KindNPC},
		mmokit.Collider{
			Width:  gw.Config.NpcWidth,
			Height: gw.Config.NpcHeight,
			Layer:  gamecomp.LayerPlayer,
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
			Archetype:      archetype,
			State:          AIStateIdle,
			MaxSpeed:       d.MaxSpeed,
			TurnRate:       d.TurnRate,
			PreferredRange: d.PreferredRange,
			WeaponRange:    d.WeaponRange,
			AggroRadius:    d.AggroRadius,
			LockRange:      d.LockRange,
			MotionPolicy:   d.MotionPolicy,
			DamagePerShot:  d.DamagePerShot,
			FireRate:       d.FireRate,
		},
	}
	if poiNetID != 0 {
		components = append(components, gamecomp.POIAnchor{POINetID: poiNetID})
	}

	e := gw.stage.Spawn(components...)
	gw.eng.Log.Log(CatPlayerSpawn, "npc spawned: netID=%d archetype=%d pos=(%.0f,%.0f) anchor=%d",
		e.NetID(), archetype, x, y, poiNetID)
	return e
}
