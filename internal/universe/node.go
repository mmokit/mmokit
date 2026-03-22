package universe

import (
	"context"
	"log"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/system"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// Node is a self-contained game simulation owning one sector.
type Node struct {
	ID        string
	Sector    coords.SectorCoord
	Engine    *engine.Engine
	World     *game.GameWorld
	Loop      *engine.GameLoop

	Inbox     chan NodeMessage
	Events    chan net.PlayerEvent
	Neighbors map[string]*Node
}

// NewNode creates a node for the given sector with its own ECS world and systems.
func NewNode(
	sector coords.SectorCoord,
	platformCfg engine.Config,
	gameCfg game.GameConfig,
	connMgr *net.ConnManager,
	playerDB *game.PlayerRepo,
	gameLog *logger.Logger,
) *Node {
	id := SectorID(sector)

	eng := engine.New(platformCfg, connMgr, gameLog)
	grid := spatial.NewGrid(gameCfg.GridCellSize)
	gw := game.NewGameWorld(eng, gameCfg, playerDB, grid, component.SectorCoord{
		SX: sector.SX,
		SY: sector.SY,
	})
	gw.NodeID = id
	game.InitDropTables()

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

	// Per-node events channel — Coordinator fans out ConnManager events here
	events := make(chan net.PlayerEvent, 64)
	gameLoop.SetEventsCh(events)

	return &Node{
		ID:        id,
		Sector:    sector,
		Engine:    eng,
		World:     gw,
		Loop:      gameLoop,
		Inbox:     make(chan NodeMessage, 256),
		Events:    events,
		Neighbors: make(map[string]*Node),
	}
}

// Run starts the node's game loop. Blocks until context is cancelled.
func (n *Node) Run(ctx context.Context) {
	log.Printf("[%s] node started for sector (%d,%d)", n.ID, n.Sector.SX, n.Sector.SY)
	n.Loop.Run(ctx)
}

// Shutdown saves all player state on this node.
func (n *Node) Shutdown() {
	n.World.Shutdown()
	log.Printf("[%s] node shutdown complete", n.ID)
}
