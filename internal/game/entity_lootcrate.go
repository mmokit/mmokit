package game

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	comp "github.com/zenion/mmoserver/pkg/component"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/engine"
)

type lootCrateMappers struct {
	base   *ecs.Map6[comp.Position, comp.Velocity, comp.Rotation, comp.Collider, comp.NetworkID, comp.EntityKind]
	extras *ecs.Map3[gamecomp.Inventory, comp.Lifetime, gamecomp.LootCrate]
}

func initLootCrateEntity(gw *GameWorld) {
	m := &lootCrateMappers{
		base:   ecs.NewMap6[comp.Position, comp.Velocity, comp.Rotation, comp.Collider, comp.NetworkID, comp.EntityKind](gw.ECS),
		extras: ecs.NewMap3[gamecomp.Inventory, comp.Lifetime, gamecomp.LootCrate](gw.ECS),
	}

	gw.Registry.Register(engine.EntityDef{
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
		&comp.Position{X: x, Y: y},
		&comp.Velocity{},
		&comp.Rotation{},
		&comp.Collider{Radius: gw.Config.LootCrateRadius, Layer: 0},
		&comp.NetworkID{ID: netID},
		&comp.EntityKind{Type: gamecomp.TypeLootCrate},
	)
	gw.C.SectorCoord.Add(entity, &comp.SectorCoord{SX: gw.Sector.SX, SY: gw.Sector.SY})
	m.extras.Add(entity,
		&gamecomp.Inventory{Items: items, MaxMass: math.MaxFloat32},
		&comp.Lifetime{Remaining: gw.Config.LootCrateLifetime},
		&gamecomp.LootCrate{},
	)
	gw.Log.Log(CatSpawn, "loot crate spawned: netID=%d pos=(%.0f,%.0f)", netID, x, y)
}
