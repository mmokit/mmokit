package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// NPCBundle is the entity-kind component bundle for NPC enemy ships.
// NPCs do not carry LockedBy — the combat-warning ring belongs only on
// the local player's own ship.
type NPCBundle struct {
	Health        *gamecomp.Health
	Shield        *gamecomp.Shield
	StatusEffects *gamecomp.StatusEffects
}

// SpawnNPC creates a stationary NPC ship entity at the given position.
func (gw *GameWorld) SpawnNPC(x, y float32) ecs.Entity {
	br := boundingRadius(gw.Config.NpcWidth, gw.Config.NpcHeight)

	entity := gw.SpawnEntity(
		mmokit.Position{X: x, Y: y},
		mmokit.WithEntityKind(gamecomp.TypeNPC),
		mmokit.WithCollider(br),
		mmokit.WithRotation(0),
		mmokit.WithComponents(),
	)

	// Set collider shape details
	col := gw.C.Collider.Get(entity)
	col.Width = gw.Config.NpcWidth
	col.Height = gw.Config.NpcHeight
	col.Layer = gamecomp.LayerPlayer
	col.Shape = mmokit.ShapeRect

	// Set non-zero field values on auto-added components
	*gw.C.Health.Get(entity) = gamecomp.Health{Current: gw.Config.NpcHealth, Max: gw.Config.NpcHealth}
	*gw.C.Shield.Get(entity) = gamecomp.Shield{
		Current:    gw.Config.NpcShield,
		Max:        gw.Config.NpcShield,
		RegenRate:  gw.Config.NpcShieldRegenRate,
		RegenDelay: gw.Config.NpcShieldRegenDelay,
	}

	netID := gw.C.NetworkID.Get(entity).ID
	gw.eng.Log.Log(CatPlayerSpawn, "npc spawned: netID=%d pos=(%.0f,%.0f)", netID, x, y)
	return entity
}
