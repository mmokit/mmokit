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

	handle := gw.stage.SpawnEntity(
		mmokit.Position{X: x, Y: y},
		mmokit.WithEntityKind(gamecomp.KindNPC),
		mmokit.WithCollider(br),
		mmokit.WithRotation(0),
		mmokit.WithComponents(),
	)
	entity := mmokit.EntityFromECS(gw.stage, handle)

	// Set collider shape details
	if col := mmokit.Get[mmokit.Collider](entity); col != nil {
		col.Width = gw.Config.NpcWidth
		col.Height = gw.Config.NpcHeight
		col.Layer = gamecomp.LayerPlayer
		col.Shape = mmokit.ShapeRect
	}

	// Set non-zero field values on auto-added components
	mmokit.Set(entity, gamecomp.Health{Current: gw.Config.NpcHealth, Max: gw.Config.NpcHealth})
	mmokit.Set(entity, gamecomp.Shield{
		Current:    gw.Config.NpcShield,
		Max:        gw.Config.NpcShield,
		RegenRate:  gw.Config.NpcShieldRegenRate,
		RegenDelay: gw.Config.NpcShieldRegenDelay,
	})

	gw.eng.Log.Log(CatPlayerSpawn, "npc spawned: netID=%d pos=(%.0f,%.0f)", entity.NetID(), x, y)
	return handle
}
