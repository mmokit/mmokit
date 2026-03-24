package game

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type lootCrateMappers struct {
	base   *ecs.Map6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind]
	extras *ecs.Map3[gamecomp.Inventory, mmokit.Lifetime, gamecomp.LootCrate]
}

func initLootCrateEntity(gw *GameWorld) {
	m := &lootCrateMappers{
		base:   ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](gw.ECS),
		extras: ecs.NewMap3[gamecomp.Inventory, mmokit.Lifetime, gamecomp.LootCrate](gw.ECS),
	}

	gw.Registry.Register(mmokit.EntityDef{
		Mappers: m,
		Name:        "loot",
		Description: "loot crate with cargo",
		EntityType:  gamecomp.TypeLootCrate,
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
	m := gw.Registry.ByType(gamecomp.TypeLootCrate).Mappers.(*lootCrateMappers)
	netID := gw.NextNetID()
	entity := m.base.NewEntity(
		&mmokit.Position{X: x, Y: y},
		&mmokit.Velocity{},
		&mmokit.Rotation{},
		&mmokit.Collider{Radius: gw.Config.LootCrateRadius, Layer: 0},
		&mmokit.NetworkID{ID: netID},
		&mmokit.EntityKind{Type: gamecomp.TypeLootCrate},
	)
	gw.C.SectorCoord.Add(entity, &mmokit.SectorCoord{SX: gw.Sector.SX, SY: gw.Sector.SY})
	m.extras.Add(entity,
		&gamecomp.Inventory{Items: items, MaxMass: math.MaxFloat32},
		&mmokit.Lifetime{Remaining: gw.Config.LootCrateLifetime},
		&gamecomp.LootCrate{},
	)
	gw.Log.Log(CatSpawn, "loot crate spawned: netID=%d pos=(%.0f,%.0f)", netID, x, y)
}
