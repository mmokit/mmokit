package game

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// GameSetup configures the coordinator with game-specific world factory and systems.
// The gameCfg pointer is shared across every GameWorld the coordinator creates
// so that runtime config changes made through the console apply to every node.
func GameSetup(
	coord *mmokit.Process,
	gameCfg *GameConfig,
	playerDB *PlayerRepo,
	playerSessions *mmokit.PlayerSessions,
) {
	coord.SetWorld(func(base *mmokit.WorldBase) mmokit.GameWorld {
		cell := base.Cell()

		// Use root cell (depth 0) for CellCoord — entities always keep base-cell coordinates
		rootCell := cell
		for rootCell.Depth > 0 {
			rootCell = rootCell.Parent()
		}
		gw := NewGameWorld(base, gameCfg, playerDB, mmokit.CellCoord{
			CellX: rootCell.X,
			CellY: rootCell.Y,
		}, base.FromSplit())
		gw.PlayerSessions = playerSessions
		gw.sideEffectRegistry = buildSideEffectRegistry(gw)
		return gw
	})

	// Register systems in the same order as before
	coord.AddSystem("Input", mmokit.NewInputSystem(func(router *mmokit.InputRouter, gw *GameWorld) {
		SetupInputHandlers(router, gw)
	}))
	coord.AddSystem("Docking", func() mmokit.System { return &DockingSystem{} })
	coord.AddSystem("TargetLock", func() mmokit.System { return &TargetLockSystem{} })
	coord.AddSystem("ShipDynamics", func() mmokit.System { return &ShipDynamicsSystem{} })
	coord.AddSystem("Mining", func() mmokit.System { return &MiningSystem{} })
	coord.AddSystem("Economy", func() mmokit.System { return &EconomySystem{} })
	coord.AddSystem("Equipment", func() mmokit.System { return &EquipmentSystem{} })
	coord.AddSystem("Ability", func() mmokit.System { return &AbilitySystem{} })
	coord.AddSystem("StatusEffect", func() mmokit.System { return &StatusEffectSystem{} })
	coord.AddSystem("Wander", func() mmokit.System { return &WanderSystem{} })
	coord.AddSystem("Physics", mmokit.NewPhysicsSystem())
	coord.AddSystem("Lifetime", mmokit.NewLifetimeSystem())
	coord.AddSystem("Spatial", mmokit.NewSpatialSystemWith(func(gw *GameWorld) mmokit.SpatialHooks {
		return mmokit.SpatialHooks{
			PreTick:  func() { clear(gw.NetIDToEntity) },
			OnEntity: func(entity mmokit.Entity, _ mmokit.SpatialEntry) {
				gw.NetIDToEntity[gw.C.NetworkID.Get(entity).ID] = entity
			},
		}
	}))
	coord.AddSystem("Collision", func() mmokit.System { return &CollisionSystem{} })
	coord.AddSystem("ShieldRegen", func() mmokit.System { return &ShieldRegenSystem{} })
	coord.AddSystem("Network", func() mmokit.System { return &NetworkSystem{} })
}
