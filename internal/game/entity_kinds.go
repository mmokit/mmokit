package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/system"
)

// RegisterEntityKinds registers all space-game entity kinds on the
// process. Each per-cell realize closure runs against the cell's own
// ECS world, so the StatusEffects/Inventory var-tail bindings and the
// Rotation extra binding are constructed fresh per stage rather than
// captured at process scope.
func RegisterEntityKinds(p *mmokit.Process) {
	statusEffectsClearSource := mmokit.WithPreMarshal(func(se *gamecomp.StatusEffects) {
		for i := uint8(0); i < se.Count; i++ {
			se.Effects[i].Source = ecs.Entity{}
		}
	})

	statusEffectsBindingFn := mmokit.WithBindingFn(func(w *ecs.World) system.ComponentBinding {
		return NewStatusEffectsBinding(ecs.NewMap1[gamecomp.StatusEffects](w))
	})

	mmokit.RegisterKind[ShipBundle](p, gamecomp.TypeShip, "Ship",
		mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500, IncludeMeshState: true},
		// Replicate Rotation to clients so the sprite faces the
		// server-authoritative heading. Without this, the client has
		// to derive rotation from velocity direction — which makes
		// turning in place invisible. Rotation is registered for
		// transfer globally by RegisterGlobalTransferComponents, so
		// we attach only the per-kind network binding here.
		mmokit.WithExtraBindingFn(func(w *ecs.World) system.ComponentBinding {
			return mmokit.QAngle(ecs.NewMap1[mmokit.Rotation](w))
		}),
		mmokit.WithField[gamecomp.Inventory](
			mmokit.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
		),
		mmokit.WithField[gamecomp.StatusEffects](
			statusEffectsBindingFn,
			statusEffectsClearSource,
		),
	)

	mmokit.RegisterKind[AsteroidBundle](p, gamecomp.TypeAsteroid, "Asteroid",
		mmokit.EngineBindingsConfig{SizeQuantScale: 500, IncludeMeshState: true},
	)

	mmokit.RegisterKind[StationBundle](p, gamecomp.TypeStation, "Station",
		mmokit.EngineBindingsConfig{SizeQuantScale: 500, IncludeMeshState: true},
	)

	mmokit.RegisterKind[NPCBundle](p, gamecomp.TypeNPC, "NPC",
		mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500, IncludeMeshState: true},
		// NPCs can be hit with status effects (ion burn, etc.) whose
		// Source is a player entity handle. The pre-marshal hook
		// clears Source before cross-cell transfer so the ecs.Entity
		// reference doesn't leak into the wire payload.
		mmokit.WithField[gamecomp.StatusEffects](
			statusEffectsBindingFn,
			statusEffectsClearSource,
		),
	)

	mmokit.RegisterKind[LootCrateBundle](p, gamecomp.TypeLootCrate, "LootCrate",
		mmokit.EngineBindingsConfig{IncludeMeshState: true},
		mmokit.WithField[gamecomp.Inventory](
			mmokit.WithBindingFn(func(w *ecs.World) system.ComponentBinding {
				return NewInventoryBinding(ecs.NewMap1[gamecomp.Inventory](w))
			}),
			mmokit.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
		),
	)
}

// RegisterGlobalTransferComponents registers the core components (Velocity,
// Rotation) present on every entity. These are registered once per stage
// rather than per-kind to avoid duplicate entries in the ReplicationRegistry.
func RegisterGlobalTransferComponents(c *Components, reg *mmokit.ReplicationRegistry) {
	mmokit.RegisterComponent(reg, c.Velocity)
	mmokit.RegisterComponent(reg, c.Rotation)
}

// initEntityKinds populates per-stage state that depends on the running
// GameWorld: the global transfer registrations (Velocity/Rotation) and the
// EntityRegistry used by admin commands. Entity-kind specs themselves are
// registered process-wide via RegisterEntityKinds(coord) in GameSetup.
func (gw *GameWorld) initEntityKinds() {
	RegisterGlobalTransferComponents(gw.C, gw.ReplicationRegistry())

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
