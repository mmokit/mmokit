package universe

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// buildReplicationRegistry creates a ReplicationRegistry with all game-specific
// component replicators using RegisterComponent for automatic reflection-based
// marshaling.
func buildReplicationRegistry(gw *game.GameWorld) *pkguniverse.ReplicationRegistry {
	reg := pkguniverse.NewReplicationRegistry()

	// Auto-marshaled via reflection (simple structs with numeric/bool fields)
	pkguniverse.RegisterComponent(reg, 1, gw.C.Velocity)
	pkguniverse.RegisterComponent(reg, 2, gw.C.Rotation)
	pkguniverse.RegisterComponent(reg, 3, gw.C.Health)
	pkguniverse.RegisterComponent(reg, 4, gw.C.Shield)
	pkguniverse.RegisterComponent(reg, 5, gw.C.ShipControl)
	pkguniverse.RegisterComponent(reg, 6, gw.C.Equipment)
	pkguniverse.RegisterComponent(reg, 7, gw.C.AbilitySet)
	pkguniverse.RegisterComponent(reg, 9, gw.C.MoveTarget)
	pkguniverse.RegisterComponent(reg, 10, gw.C.Minable)
	pkguniverse.RegisterComponent(reg, 11, gw.C.Lifetime)
	pkguniverse.RegisterComponent(reg, 13, gw.C.TargetLock)

	// StatusEffects: needs pre-marshal to clear entity references
	pkguniverse.RegisterComponent(reg, 8, gw.C.StatusEffects,
		pkguniverse.WithPreMarshal(func(se *gamecomp.StatusEffects) {
			for i := uint8(0); i < se.Count; i++ {
				se.Effects[i].Source = ecs.Entity{}
			}
		}),
	)

	// Inventory: has a map field, needs custom marshal
	pkguniverse.RegisterComponent(reg, 12, gw.C.Inventory,
		pkguniverse.WithMarshal(game.MarshalInventory, game.UnmarshalInventoryInto),
	)

	return reg
}
