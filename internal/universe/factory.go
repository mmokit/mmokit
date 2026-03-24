package universe

import (
	comp "github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/system"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/spatial"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// GameNodeFactory creates a NodeFactory that builds game worlds with all systems.
func GameNodeFactory(
	gameCfg game.GameConfig,
	connMgr *net.ConnManager,
	playerDB *game.PlayerRepo,
	playerSessions *ops.PlayerSessions,
	gameLog *logger.Logger,
) pkguniverse.NodeFactory {
	return func(sector coords.SectorCoord, eng *engine.Engine, events chan net.PlayerEvent, log *logger.Logger) (pkguniverse.GameWorld, *engine.GameLoop) {
		id := pkguniverse.SectorID(sector)

		grid := spatial.NewGrid(gameCfg.GridCellSize)
		gw := game.NewGameWorld(eng, gameCfg, playerDB, grid, comp.SectorCoord{
			SX: sector.SX,
			SY: sector.SY,
		})
		gw.NodeID = id
		gw.PlayerSessions = playerSessions

		systems := []engine.System{
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
		sysNames := []string{
			"Input", "Docking", "TargetLock", "ShipControl", "Mining",
			"Economy", "Equipment", "Ability", "StatusEffect", "Physics",
			"SectorBoundary", "Lifetime", "Spatial", "Collision", "ShieldRegen", "Network",
		}

		gameLoop := engine.NewGameLoop(eng, systems, sysNames, gw.Hooks())

		adapter := newGameWorldAdapter(gw)
		return adapter, gameLoop
	}
}
