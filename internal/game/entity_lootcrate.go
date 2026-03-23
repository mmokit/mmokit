package game

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
)

type lootCrateMappers struct {
	base   *ecs.Map6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind]
	extras *ecs.Map3[component.Inventory, component.Lifetime, component.LootCrate]
}

func initLootCrateEntity(gw *GameWorld) {
	m := &lootCrateMappers{
		base:   ecs.NewMap6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind](gw.ECS),
		extras: ecs.NewMap3[component.Inventory, component.Lifetime, component.LootCrate](gw.ECS),
	}

	gw.Registry.Register(EntityDef{
		Mappers: m,
		Name:        "loot",
		Description: "loot crate with cargo",
		EntityType:  component.TypeLootCrate,
		Spawnable:   true,
		Spawn: func(x, y float32) {
			gw.SpawnLootCrate(x, y, map[uint32]int32{
				item.ResourceItemID(0): 10,
				item.ResourceItemID(1): 10,
				item.ResourceItemID(2): 10,
				item.ResourceItemID(3): 10,
			})
		},
	})
}

// SpawnLootCrate creates a loot crate entity with the given cargo.
func (gw *GameWorld) SpawnLootCrate(x, y float32, items map[uint32]int32) {
	m := gw.Registry.ByType(component.TypeLootCrate).Mappers.(*lootCrateMappers)
	netID := gw.NextNetID()
	entity := m.base.NewEntity(
		&component.Position{X: x, Y: y},
		&component.Velocity{},
		&component.Rotation{},
		&component.Collider{Radius: gw.Config.LootCrateRadius, Layer: 0},
		&component.NetworkID{ID: netID},
		&component.EntityKind{Type: component.TypeLootCrate},
	)
	gw.C.SectorCoord.Add(entity, &component.SectorCoord{SX: gw.Sector.SX, SY: gw.Sector.SY})
	m.extras.Add(entity,
		&component.Inventory{Items: items, MaxMass: math.MaxFloat32},
		&component.Lifetime{Remaining: gw.Config.LootCrateLifetime},
		&component.LootCrate{},
	)
	gw.Log.Log(CatSpawn, "loot crate spawned: netID=%d pos=(%.0f,%.0f)", netID, x, y)
}
