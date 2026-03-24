package game

import (
	"github.com/mlange-42/ark/ecs"

	comp "github.com/zenion/mmoserver/pkg/component"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/spatial"
)

type npcMappers struct {
	base   *ecs.Map6[comp.Position, comp.Velocity, comp.Rotation, comp.Collider, comp.NetworkID, comp.EntityKind]
	combat *ecs.Map3[comp.Health, comp.Shield, gamecomp.StatusEffects]
}

func initNpcEntity(gw *GameWorld) {
	m := &npcMappers{
		base:   ecs.NewMap6[comp.Position, comp.Velocity, comp.Rotation, comp.Collider, comp.NetworkID, comp.EntityKind](gw.ECS),
		combat: ecs.NewMap3[comp.Health, comp.Shield, gamecomp.StatusEffects](gw.ECS),
	}

	gw.Registry.Register(engine.EntityDef{
		Mappers: m,
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
func (gw *GameWorld) SpawnNPC(x, y float32) {
	m := gw.Registry.ByType(gamecomp.TypeNPC).Mappers.(*npcMappers)
	netID := gw.NextNetID()

	boundingRadius := boundingRadius(gw.Config.NpcWidth, gw.Config.NpcHeight)

	entity := m.base.NewEntity(
		&comp.Position{X: x, Y: y},
		&comp.Velocity{},
		&comp.Rotation{},
		&comp.Collider{
			Radius: boundingRadius,
			Width:  gw.Config.NpcWidth,
			Height: gw.Config.NpcHeight,
			Layer:  gamecomp.LayerPlayer,
			Shape:  spatial.ShapeRect,
		},
		&comp.NetworkID{ID: netID},
		&comp.EntityKind{Type: gamecomp.TypeNPC},
	)

	gw.C.SectorCoord.Add(entity, &comp.SectorCoord{SX: gw.Sector.SX, SY: gw.Sector.SY})
	m.combat.Add(entity,
		&comp.Health{Current: gw.Config.NpcHealth, Max: gw.Config.NpcHealth},
		&comp.Shield{
			Current:    gw.Config.NpcShield,
			Max:        gw.Config.NpcShield,
			RegenRate:  gw.Config.NpcShieldRegenRate,
			RegenDelay: gw.Config.NpcShieldRegenDelay,
		},
		&gamecomp.StatusEffects{},
	)

	gw.Log.Log(CatSpawn, "npc spawned: netID=%d pos=(%.0f,%.0f)", netID, x, y)
}
