package game

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/logger"
)

type lootCrateMappers struct {
	base   *ecs.Map6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind]
	extras *ecs.Map3[component.Inventory, component.Lifetime, component.LootCrate]
}

func initLootCrateEntity(gw *GameWorld) {
	gw.lootCrateMappers = &lootCrateMappers{
		base:   ecs.NewMap6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind](gw.ECS),
		extras: ecs.NewMap3[component.Inventory, component.Lifetime, component.LootCrate](gw.ECS),
	}

	gw.Registry.Register(EntityDef{
		Name:        "loot",
		Description: "loot crate with cargo",
		EntityType:  component.TypeLootCrate,
		Spawnable:   true,
		Spawn: func(x, y float32) {
			gw.SpawnLootCrate(x, y, [4]float32{10, 10, 10, 10})
		},
	})
}

// SpawnLootCrate creates a loot crate entity with the given cargo.
func (gw *GameWorld) SpawnLootCrate(x, y float32, resources [4]float32) {
	m := gw.lootCrateMappers
	netID := gw.NextNetID()
	entity := m.base.NewEntity(
		&component.Position{X: x, Y: y},
		&component.Velocity{},
		&component.Rotation{},
		&component.Collider{Radius: gw.Config.LootCrateRadius, Layer: 0},
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: component.TypeLootCrate},
	)
	m.extras.Add(entity,
		&component.Inventory{Resources: resources},
		&component.Lifetime{Remaining: gw.Config.LootCrateLifetime},
		&component.LootCrate{},
	)
	gw.Log.Log(logger.CatSpawn, "loot crate spawned: netID=%d pos=(%.0f,%.0f)", netID, x, y)
}
