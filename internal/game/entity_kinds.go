package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// initEntityKinds registers all entity types as EntityKindDefs and populates
// the EntityRegistry for admin commands. Replaces the old per-entity initXxxEntity
// functions and buildReplicationRegistry.
func (gw *GameWorld) initEntityKinds() {
	// Register engine components for cross-node transfer/replica replication.
	// These are core components present on all entities; registering them once
	// (rather than per-kind) avoids duplicate entries in the ReplicationRegistry.
	reg := gw.ReplicationRegistry()
	mmokit.RegisterComponent(reg, gw.C.Velocity)
	mmokit.RegisterComponent(reg, gw.C.Rotation)

	// Ship
	shipDef := mmokit.EntityKindDef{
		Kind:           gamecomp.TypeShip,
		Name:           "Ship",
		EngineBindings: &mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500},
	}
	// Replicated components (transferred cross-node + sent to clients)
	mmokit.KindComponent(&shipDef, gw.C.PilotName)
	mmokit.KindComponent(&shipDef, gw.C.Health)
	mmokit.KindComponent(&shipDef, gw.C.Shield)
	mmokit.KindComponent(&shipDef, gw.C.ShipControl)
	mmokit.KindComponent(&shipDef, gw.C.Equipment)
	mmokit.KindComponent(&shipDef, gw.C.Inventory,
		mmokit.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
	)
	mmokit.KindComponent(&shipDef, gw.C.TargetLock)
	mmokit.KindComponent(&shipDef, gw.C.AbilitySet)
	mmokit.KindComponent(&shipDef, gw.C.StatusEffects,
		mmokit.WithPreMarshal(func(se *gamecomp.StatusEffects) {
			for i := uint8(0); i < se.Count; i++ {
				se.Effects[i].Source = ecs.Entity{}
			}
		}),
	)
	mmokit.KindComponent(&shipDef, gw.C.MoveTarget)
	// Local-only components (added after transfer, not serialized)
	mmokit.KindComponentLocalOnly(&shipDef, gw.C.PlayerInput)
	mmokit.KindComponentLocalOnly(&shipDef, gw.C.MiningLaser)
	gw.RegisterEntityKind(shipDef)

	// Asteroid
	asteroidDef := mmokit.EntityKindDef{
		Kind:           gamecomp.TypeAsteroid,
		Name:           "Asteroid",
		EngineBindings: &mmokit.EngineBindingsConfig{SizeQuantScale: 500},
	}
	mmokit.KindComponent(&asteroidDef, gw.C.Minable)
	gw.RegisterEntityKind(asteroidDef)

	// Station
	stationDef := mmokit.EntityKindDef{
		Kind:           gamecomp.TypeStation,
		Name:           "Station",
		EngineBindings: &mmokit.EngineBindingsConfig{SizeQuantScale: 500},
	}
	mmokit.KindComponentLocalOnly(&stationDef, gw.C.Station)
	gw.RegisterEntityKind(stationDef)

	// NPC
	npcDef := mmokit.EntityKindDef{
		Kind:           gamecomp.TypeNPC,
		Name:           "NPC",
		EngineBindings: &mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500},
	}
	mmokit.KindComponent(&npcDef, gw.C.Health)
	mmokit.KindComponent(&npcDef, gw.C.Shield)
	mmokit.KindComponent(&npcDef, gw.C.StatusEffects)
	gw.RegisterEntityKind(npcDef)

	// LootCrate
	lootDef := mmokit.EntityKindDef{
		Kind:           gamecomp.TypeLootCrate,
		Name:           "LootCrate",
		EngineBindings: &mmokit.EngineBindingsConfig{},
	}
	mmokit.KindComponent(&lootDef, gw.C.Inventory,
		mmokit.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
	)
	mmokit.KindComponent(&lootDef, gw.C.Lifetime)
	mmokit.KindComponentLocalOnly(&lootDef, gw.C.LootCrate)
	gw.RegisterEntityKind(lootDef)

	// Register with EntityRegistry for admin commands
	gw.Registry.Register(mmokit.EntityDef{
		Name: "ship", Description: "player ship",
		EntityType: gamecomp.TypeShip, Spawnable: false,
	})
	gw.Registry.Register(mmokit.EntityDef{
		Name: "asteroid", Description: "mineable asteroid",
		EntityType: gamecomp.TypeAsteroid, Spawnable: true,
		Spawn: func(x, y float32) { gw.spawnAsteroid(x, y) },
	})
	gw.Registry.Register(mmokit.EntityDef{
		Name: "station", Description: "trade station",
		EntityType: gamecomp.TypeStation, Spawnable: false,
	})
	gw.Registry.Register(mmokit.EntityDef{
		Name: "npc", Description: "NPC enemy ship (target dummy)",
		EntityType: gamecomp.TypeNPC, Spawnable: true,
		Spawn: func(x, y float32) { gw.SpawnNPC(x, y) },
	})
	gw.Registry.Register(mmokit.EntityDef{
		Name: "loot", Description: "loot crate with cargo",
		EntityType: gamecomp.TypeLootCrate, Spawnable: true,
		Spawn: func(x, y float32) {
			contents := make(map[uint32]int32)
			for _, id := range item.ResourceIDs() {
				contents[id] = 10
			}
			gw.SpawnLootCrate(x, y, contents)
		},
	})
}
