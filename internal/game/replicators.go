package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// buildReplicationRegistry creates a ReplicationRegistry with all game-specific
// component replicators using RegisterComponent for automatic reflection-based
// marshaling.
func buildReplicationRegistry(gw *GameWorld) *pkguniverse.ReplicationRegistry {
	reg := pkguniverse.NewReplicationRegistry()

	// Auto-marshaled via reflection (simple structs with numeric/bool fields)
	pkguniverse.RegisterComponent(reg, gw.C.Velocity)
	pkguniverse.RegisterComponent(reg, gw.C.Rotation)
	pkguniverse.RegisterComponent(reg, gw.C.Health)
	pkguniverse.RegisterComponent(reg, gw.C.Shield)
	pkguniverse.RegisterComponent(reg, gw.C.ShipControl)
	pkguniverse.RegisterComponent(reg, gw.C.Equipment)
	pkguniverse.RegisterComponent(reg, gw.C.AbilitySet)
	pkguniverse.RegisterComponent(reg, gw.C.StatusEffects,
		pkguniverse.WithPreMarshal(func(se *gamecomp.StatusEffects) {
			for i := uint8(0); i < se.Count; i++ {
				se.Effects[i].Source = ecs.Entity{}
			}
		}),
	)
	pkguniverse.RegisterComponent(reg, gw.C.MoveTarget)
	pkguniverse.RegisterComponent(reg, gw.C.Minable)
	pkguniverse.RegisterComponent(reg, gw.C.Lifetime)
	pkguniverse.RegisterComponent(reg, gw.C.Inventory,
		pkguniverse.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
	)
	pkguniverse.RegisterComponent(reg, gw.C.TargetLock)

	return reg
}
