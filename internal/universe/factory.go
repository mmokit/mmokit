package universe

import (
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/system"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// GameNodeFactory creates a NodeFactory that builds game worlds with all systems.
func GameNodeFactory(
	gameCfg game.GameConfig,
	playerDB *game.PlayerRepo,
	playerSessions *mmokit.PlayerSessions,
) mmokit.NodeFactory {
	return func(base *mmokit.WorldBase) (mmokit.GameWorld, []mmokit.System) {
		eng := base.Engine()
		sector := base.Sector()
		id := base.NodeID()

		grid := mmokit.NewGrid(gameCfg.GridCellSize)
		gw := game.NewGameWorld(eng, gameCfg, playerDB, grid, mmokit.SectorCoord{
			SX: sector.SX,
			SY: sector.SY,
		})
		gw.NodeID = id
		gw.PlayerSessions = playerSessions

		systems := []mmokit.System{
			system.NewInputSystem(gw),
			system.NewDockingSystem(gw),
			system.NewTargetLockSystem(gw),
			system.NewShipControlSystem(gw),
			system.NewMiningSystem(gw),
			system.NewEconomySystem(gw),
			system.NewEquipmentSystem(gw),
			system.NewAbilitySystem(gw),
			system.NewStatusEffectSystem(gw),
			system.NewPhysicsSystem(gw),
			system.NewSectorBoundarySystem(gw),
			system.NewLifetimeSystem(gw),
			system.NewSpatialSystem(gw),
			system.NewCollisionSystem(gw),
			system.NewShieldRegenSystem(gw),
			system.NewNetworkSystem(gw),
		}

		replRegistry := buildReplicationRegistry(gw)
		base.SetReplicationRegistry(replRegistry)
		seRegistry := buildSideEffectRegistry(gw)
		adapter := newGameWorldAdapter(base, gw, seRegistry)
		return adapter, systems
	}
}
