package game

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// WorldFactory returns a world constructor function for use as Config.World.
// The gameCfg pointer is shared across every GameWorld the coordinator creates
// so that runtime config changes made through the console apply to every node.
func WorldFactory(
	gameCfg *GameConfig,
	playerDB *PlayerRepo,
	playerSessions *mmokit.PlayerSessions,
) func(base *mmokit.Stage) mmokit.GameWorld {
	return func(base *mmokit.Stage) mmokit.GameWorld {
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
	}
}

// GameSetup registers game-specific entity kinds and systems on the coordinator.
func GameSetup(coord *mmokit.Process) {
	RegisterEntityKinds(coord)
	coord.AddSystem(mmokit.NewInputSystem(func(router *mmokit.InputRouter, gw *GameWorld) {
		SetupInputHandlers(router, gw)
	}))
	coord.AddSystem(mmokit.NewSystem(&DockingSystem{}))
	coord.AddSystem(mmokit.NewSystem(&TargetLockSystem{}))
	coord.AddSystem(mmokit.NewSystem(&ShipDynamicsSystem{}))
	coord.AddSystem(mmokit.NewSystem(&MiningSystem{}))
	coord.AddSystem(mmokit.NewSystem(&EconomySystem{}))
	coord.AddSystem(mmokit.NewSystem(&EquipmentSystem{}))
	coord.AddSystem(mmokit.NewSystem(&AbilitySystem{}))
	coord.AddSystem(mmokit.NewSystem(&StatusEffectSystem{}))
	coord.AddSystem(mmokit.NewSystem(&WanderSystem{}))
	coord.AddSystem(mmokit.NewPhysicsSystem())
	coord.AddSystem(mmokit.NewLifetimeSystem())
	coord.AddSystem(mmokit.NewSpatialSystemWith(func(gw *GameWorld) mmokit.SpatialHooks {
		return mmokit.SpatialHooks{
			PreTick: func() { clear(gw.NetIDToEntity) },
			OnEntity: func(entity mmokit.Entity, _ mmokit.SpatialEntry) {
				gw.NetIDToEntity[gw.C.NetworkID.Get(entity).ID] = entity
			},
		}
	}))
	coord.AddSystem(mmokit.NewSystem(&CollisionSystem{}))
	coord.AddSystem(mmokit.NewSystem(&ShieldRegenSystem{}))
	coord.AddSystem(mmokit.NewSystem(&NetworkSystem{}))
}
