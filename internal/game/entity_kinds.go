package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
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

	mmokit.RegisterKind[ShipBundle](p, gamecomp.KindShip, "Ship",
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

	mmokit.RegisterKind[AsteroidBundle](p, gamecomp.KindAsteroid, "Asteroid")

	mmokit.RegisterKind[StationBundle](p, gamecomp.KindStation, "Station")

	mmokit.RegisterKind[NPCBundle](p, gamecomp.KindNPC, "NPC",
		// Replicate Rotation so client-side ability VFX (beams,
		// muzzle flashes) originate from the NPC's facing barrel
		// instead of its 0-radian "front." Same pattern as the
		// Ship binding above; Rotation is registered for transfer
		// globally by initEntityKinds.
		mmokit.WithExtraBindingFn(func(w *ecs.World) system.ComponentBinding {
			return mmokit.QAngle(ecs.NewMap1[mmokit.Rotation](w))
		}),
		// NPCs can be hit with status effects (ion burn, etc.) whose
		// Source is a player entity handle. The pre-marshal hook
		// clears Source before cross-cell transfer so the ecs.Entity
		// reference doesn't leak into the wire payload.
		mmokit.WithField[gamecomp.StatusEffects](
			statusEffectsBindingFn,
			statusEffectsClearSource,
		),
	)

	mmokit.RegisterKind[LootCrateBundle](p, gamecomp.KindLootCrate, "LootCrate",
		mmokit.WithField[gamecomp.Inventory](
			mmokit.WithBindingFn(func(w *ecs.World) system.ComponentBinding {
				return NewInventoryBinding(ecs.NewMap1[gamecomp.Inventory](w))
			}),
			mmokit.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
		),
	)

	mmokit.RegisterKind[POIBundle](p, gamecomp.KindPOI, "POI")

	mmokit.RegisterKind[AoEMarkerBundle](p, gamecomp.KindAoEMarker, "AoEMarker")
}

// initEntityKinds populates per-stage state that depends on the running
// GameWorld: the global transfer registrations (Velocity/Rotation) and the
// process-wide via RegisterEntityKinds(coord) in GameSetup. Game-side
// spawning of asteroids/loot/NPCs goes through the dedicated SpawnX helpers
// (e.g. gw.spawnAsteroid, gw.SpawnNPC, gw.SpawnLootCrate); the
// EntityRegistry that previously powered the legacy entity.add console
// command was removed when the cluster-aware entity.spawn landed.
func (gw *GameWorld) initEntityKinds() {
	w := gw.stage.ECSWorld()
	reg := gw.stage.ReplicationRegistry()
	mmokit.RegisterComponent(reg, ecs.NewMap1[mmokit.Velocity](w))
	mmokit.RegisterComponent(reg, ecs.NewMap1[mmokit.Rotation](w))
}
