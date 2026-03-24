package game

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/spatial"
)

type npcMappers struct {
	base   *ecs.Map6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind]
	combat *ecs.Map3[component.Health, component.Shield, component.StatusEffects]
}

func initNpcEntity(gw *GameWorld) {
	m := &npcMappers{
		base:   ecs.NewMap6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind](gw.ECS),
		combat: ecs.NewMap3[component.Health, component.Shield, component.StatusEffects](gw.ECS),
	}

	gw.Registry.Register(engine.EntityDef{
		Mappers: m,
		Name:        "npc",
		Description: "NPC enemy ship (target dummy)",
		EntityType:  component.TypeNPC,
		Spawnable:   true,
		Spawn: func(x, y float32) {
			gw.SpawnNPC(x, y)
		},
	})
}

// SpawnNPC creates a stationary NPC ship entity at the given position.
func (gw *GameWorld) SpawnNPC(x, y float32) {
	m := gw.Registry.ByType(component.TypeNPC).Mappers.(*npcMappers)
	netID := gw.NextNetID()

	boundingRadius := boundingRadius(gw.Config.NpcWidth, gw.Config.NpcHeight)

	entity := m.base.NewEntity(
		&component.Position{X: x, Y: y},
		&component.Velocity{},
		&component.Rotation{},
		&component.Collider{
			Radius: boundingRadius,
			Width:  gw.Config.NpcWidth,
			Height: gw.Config.NpcHeight,
			Layer:  component.LayerPlayer,
			Shape:  spatial.ShapeRect,
		},
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: component.TypeNPC},
	)

	gw.C.SectorCoord.Add(entity, &component.SectorCoord{SX: gw.Sector.SX, SY: gw.Sector.SY})
	m.combat.Add(entity,
		&component.Health{Current: gw.Config.NpcHealth, Max: gw.Config.NpcHealth},
		&component.Shield{
			Current:    gw.Config.NpcShield,
			Max:        gw.Config.NpcShield,
			RegenRate:  gw.Config.NpcShieldRegenRate,
			RegenDelay: gw.Config.NpcShieldRegenDelay,
		},
		&component.StatusEffects{},
	)

	gw.Log.Log(CatSpawn, "npc spawned: netID=%d pos=(%.0f,%.0f)", netID, x, y)
}
