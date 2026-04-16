package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// BuildEntityKindDefs constructs the space game's EntityKindDefs from a
// Components struct (which wraps an ecs.World). This is pure data construction
// with no dependency on a running GameWorld — used by both runtime entity kind
// registration (via initEntityKinds) and schema export (cmd/server --dump-schema).
func BuildEntityKindDefs(c *Components) []mmokit.EntityKindDef {
	return []mmokit.EntityKindDef{
		buildShipDef(c),
		buildAsteroidDef(c),
		buildStationDef(c),
		buildNpcDef(c),
		buildLootCrateDef(c),
	}
}

// RegisterGlobalTransferComponents registers the core components (Velocity,
// Rotation) present on every entity. These are registered once rather than
// per-kind to avoid duplicate entries in the ReplicationRegistry.
func RegisterGlobalTransferComponents(c *Components, reg *mmokit.ReplicationRegistry) {
	mmokit.RegisterComponent(reg, c.Velocity)
	mmokit.RegisterComponent(reg, c.Rotation)
}

func buildShipDef(c *Components) mmokit.EntityKindDef {
	def := mmokit.EntityKindDef{
		Kind:           gamecomp.TypeShip,
		Name:           "Ship",
		EngineBindings: &mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500, IncludeMeshState: true},
	}
	// Replicate Rotation to clients so the sprite faces the server-authoritative
	// heading. Without this, the client has to derive rotation from velocity
	// direction — which makes turning in place invisible, causing the sluggish
	// "ship doesn't turn until it slows down" feel on direction changes.
	// Rotation is already registered in the global transfer registry
	// (RegisterGlobalTransferComponents), so we only attach the network binding
	// here — no second transfer registration.
	def.NetworkBindings = append(def.NetworkBindings, mmokit.QAngle(c.Rotation))
	// Replicated components (transferred cross-cell + sent to clients)
	mmokit.KindComponent(&def, c.PilotName)
	mmokit.KindComponent(&def, c.Health)
	mmokit.KindComponent(&def, c.Shield)
	mmokit.KindComponent(&def, c.ShipControl)
	mmokit.KindComponent(&def, c.Equipment)
	mmokit.KindComponent(&def, c.Inventory,
		mmokit.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
	)
	mmokit.KindComponent(&def, c.TargetLock)
	mmokit.KindComponent(&def, c.AbilitySet)
	mmokit.KindComponentWithBinding(&def, c.StatusEffects, NewStatusEffectsBinding(c.StatusEffects),
		mmokit.WithPreMarshal(func(se *gamecomp.StatusEffects) {
			for i := uint8(0); i < se.Count; i++ {
				se.Effects[i].Source = ecs.Entity{}
			}
		}),
	)
	mmokit.KindComponent(&def, c.MoveTarget)
	mmokit.KindComponent(&def, c.LockedBy)
	mmokit.KindComponent(&def, c.ActiveMining)
	// Local-only components (added after transfer, not serialized)
	mmokit.KindComponentLocalOnly(&def, c.PlayerInput)
	mmokit.KindComponentLocalOnly(&def, c.MiningLaser)
	return def
}

func buildAsteroidDef(c *Components) mmokit.EntityKindDef {
	def := mmokit.EntityKindDef{
		Kind:           gamecomp.TypeAsteroid,
		Name:           "Asteroid",
		EngineBindings: &mmokit.EngineBindingsConfig{SizeQuantScale: 500, IncludeMeshState: true},
	}
	mmokit.KindComponent(&def, c.Minable)
	// No LockedBy — asteroids can't receive combat warnings. The "being
	// locked" ring is a private alarm for ships only, and replicating
	// LockedBy on asteroids leaked that information to every viewer.
	return def
}

func buildStationDef(c *Components) mmokit.EntityKindDef {
	def := mmokit.EntityKindDef{
		Kind:           gamecomp.TypeStation,
		Name:           "Station",
		EngineBindings: &mmokit.EngineBindingsConfig{SizeQuantScale: 500, IncludeMeshState: true},
	}
	mmokit.KindComponentLocalOnly(&def, c.Station)
	return def
}

func buildNpcDef(c *Components) mmokit.EntityKindDef {
	def := mmokit.EntityKindDef{
		Kind:           gamecomp.TypeNPC,
		Name:           "NPC",
		EngineBindings: &mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500, IncludeMeshState: true},
	}
	mmokit.KindComponent(&def, c.Health)
	mmokit.KindComponent(&def, c.Shield)
	// NPCs can be hit with status effects (ion burn, etc.) whose Source is a
	// player entity handle. The pre-marshal hook clears Source before cross-cell
	// transfer so the ecs.Entity reference doesn't leak into the wire payload.
	mmokit.KindComponentWithBinding(&def, c.StatusEffects, NewStatusEffectsBinding(c.StatusEffects),
		mmokit.WithPreMarshal(func(se *gamecomp.StatusEffects) {
			for i := uint8(0); i < se.Count; i++ {
				se.Effects[i].Source = ecs.Entity{}
			}
		}),
	)
	// NPCs don't replicate LockedBy — the combat-warning ring is a
	// private alarm that only belongs on the local player's own ship.
	return def
}

func buildLootCrateDef(c *Components) mmokit.EntityKindDef {
	def := mmokit.EntityKindDef{
		Kind:           gamecomp.TypeLootCrate,
		Name:           "LootCrate",
		EngineBindings: &mmokit.EngineBindingsConfig{IncludeMeshState: true},
	}
	mmokit.KindComponentWithBinding(&def, c.Inventory, NewInventoryBinding(c.Inventory),
		mmokit.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
	)
	mmokit.KindComponent(&def, c.Lifetime)
	mmokit.KindComponentLocalOnly(&def, c.LootCrate)
	return def
}

// initEntityKinds registers all entity types as EntityKindDefs and populates
// the EntityRegistry for admin commands. Replaces the old per-entity initXxxEntity
// functions and buildReplicationRegistry.
func (gw *GameWorld) initEntityKinds() {
	RegisterGlobalTransferComponents(gw.C, gw.ReplicationRegistry())

	for _, def := range BuildEntityKindDefs(gw.C) {
		gw.RegisterEntityKind(def)
	}

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
