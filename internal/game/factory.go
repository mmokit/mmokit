package game

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// GameSetup configures the coordinator with game-specific world factory and systems.
func GameSetup(
	coord *mmokit.Coordinator,
	gameCfg GameConfig,
	playerDB *PlayerRepo,
	playerSessions *mmokit.PlayerSessions,
) {
	coord.SetWorld(func(base *mmokit.WorldBase) mmokit.GameWorld {
		eng := base.Engine()
		cell := base.Cell()
		id := base.NodeID()

		gw := NewGameWorld(eng, gameCfg, playerDB, base.SpatialGrid(), mmokit.CellCoord{
			CellX: cell.X,
			CellY: cell.Y,
		}, base.FromSplit())
		gw.NodeID = id
		gw.PlayerSessions = playerSessions
		// Inherit debug state from coordinator so split-created nodes match
		if coord := base.Coordinator(); coord != nil {
			gw.DebugShowCellGrid = coord.DebugTopology()
		}

		seRegistry := buildSideEffectRegistry(gw)
		return newGameWorldAdapter(base, gw, seRegistry)
	})

	// Register systems in the same order as before
	coord.AddSystem("Input", mmokit.NewInputSystem(func(router *mmokit.InputRouter, a *gameWorldAdapter) {
		SetupInputHandlers(router, a.GW())
	}))
	coord.AddSystem("Docking", func() mmokit.System { return &DockingSystem{} })
	coord.AddSystem("TargetLock", func() mmokit.System { return &TargetLockSystem{} })
	coord.AddSystem("ShipControl", func() mmokit.System { return &ShipControlSystem{} })
	coord.AddSystem("Mining", func() mmokit.System { return &MiningSystem{} })
	coord.AddSystem("Economy", func() mmokit.System { return &EconomySystem{} })
	coord.AddSystem("Equipment", func() mmokit.System { return &EquipmentSystem{} })
	coord.AddSystem("Ability", func() mmokit.System { return &AbilitySystem{} })
	coord.AddSystem("StatusEffect", func() mmokit.System { return &StatusEffectSystem{} })
	coord.AddSystem("Wander", func() mmokit.System { return &WanderSystem{} })
	coord.AddSystem("Physics", mmokit.NewPhysicsSystem())
	coord.AddSystem("DeadReckoning", mmokit.NewDeadReckoningSystem())
	coord.AddSystem("Lifetime", mmokit.NewLifetimeSystem())
	coord.AddSystem("Spatial", mmokit.NewSpatialSystemWith(func(adapter *gameWorldAdapter) mmokit.SpatialHooks {
		gw := adapter.GW()
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
