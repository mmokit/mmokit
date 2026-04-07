package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type npcMappers struct {
	base   *ecs.Map6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind]
	combat *ecs.Map3[gamecomp.Health, gamecomp.Shield, gamecomp.StatusEffects]
}

func initNpcEntity(gw *GameWorld) {
	m := &npcMappers{
		base:   ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.ECS),
		combat: ecs.NewMap3[gamecomp.Health, gamecomp.Shield, gamecomp.StatusEffects](gw.ECS),
	}

	gw.Registry.Register(mmokit.EntityDef{
		Mappers:     m,
		Name:        "npc",
		Description: "NPC enemy ship (target dummy)",
		EntityType:  gamecomp.TypeNPC,
		Spawnable:   true,
		Spawn: func(x, y float32) {
			gw.SpawnNPC(x, y)
		},
	})
}

// SpawnNPC creates a stationary NPC ship entity at the given position.
func (gw *GameWorld) SpawnNPC(x, y float32) ecs.Entity {
	m := gw.Registry.ByType(gamecomp.TypeNPC).Mappers.(*npcMappers)
	netID := gw.NextNetID()

	boundingRadius := boundingRadius(gw.Config.NpcWidth, gw.Config.NpcHeight)

	entity := m.base.NewEntity(
		&mmokit.Position{X: x, Y: y},
		&mmokit.Velocity{},
		&mmokit.Rotation{},
		&mmokit.Collider{
			Radius: boundingRadius,
			Width:  gw.Config.NpcWidth,
			Height: gw.Config.NpcHeight,
			Layer:  gamecomp.LayerPlayer,
			Shape:  mmokit.ShapeRect,
		},
		&mmokit.NetworkID{ID: netID},
		&mmokit.EntityKind{Type: gamecomp.TypeNPC},
	)

	gw.C.CellCoord.Add(entity, &mmokit.CellCoord{CellX: gw.Cell.CellX, CellY: gw.Cell.CellY})
	m.combat.Add(entity,
		&gamecomp.Health{Current: gw.Config.NpcHealth, Max: gw.Config.NpcHealth},
		&gamecomp.Shield{
			Current:    gw.Config.NpcShield,
			Max:        gw.Config.NpcShield,
			RegenRate:  gw.Config.NpcShieldRegenRate,
			RegenDelay: gw.Config.NpcShieldRegenDelay,
		},
		&gamecomp.StatusEffects{},
	)

	gw.Log.Log(CatPlayerSpawn, "npc spawned: netID=%d pos=(%.0f,%.0f)", netID, x, y)
	return entity
}
