package game

import (
	ecs "github.com/mlange-42/ark/ecs"
	comp "github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/persist/persisttest"
	"github.com/zenion/mmoserver/pkg/spatial"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// newTestCell creates a Node for the given cell suitable for unit tests.
// It does NOT start the game loop or any goroutines.
func newTestCell(cell pkguniverse.CellID) *pkguniverse.Cell {
	log := logger.New()
	connMgr := net.NewConnManager()
	playerDB := NewPlayerRepo(persisttest.NewPlayerRepoMock(), nil)
	playerSessions := ops.NewPlayerSessions()
	cfg := DefaultGameConfig()
	platformCfg := engine.Config{TickRate: 20}

	eng := engine.New(platformCfg, connMgr, log)
	events := make(chan net.PlayerEvent, 64)

	base := pkguniverse.NewWorldBase(eng, cell, cfg.AoIRadius, nil)
	base.SetSpatialGrid(spatial.NewHashGrid(coords.CellSize / 10))

	// Build the game world directly (same logic as the world factory in GameSetup)
	gw := NewGameWorld(base, &cfg, playerDB, comp.CellCoord{
		CellX: cell.X, CellY: cell.Y,
	}, false)
	gw.PlayerSessions = playerSessions
	gw.sideEffectRegistry = buildSideEffectRegistry(gw)

	base.SetOnTransferReceived(func(entity ecs.Entity, frame *pkguniverse.TransferFrame) {
		gw.FinishTransferSpawn(entity, frame)
	})

	// Collect system defs via a throwaway coordinator
	tmpCoord := pkguniverse.New(pkguniverse.Config{CellsX: 1, CellsY: 1, TickRate: platformCfg.TickRate})
	GameSetup(tmpCoord, &cfg, playerDB, playerSessions)

	defs := tmpCoord.SystemDefs()
	gameSystems := make([]engine.System, len(defs))
	systemNames := make([]string, len(defs))
	for i, def := range defs {
		sys := def.Factory()

		type depsInjectable interface {
			SetDeps(w *ecs.World, eng *engine.Engine, gw any)
		}
		type initializable interface {
			Init()
		}
		if di, ok := sys.(depsInjectable); ok {
			di.SetDeps(eng.ECS, eng, gw)
		}
		if init, ok := sys.(initializable); ok {
			init.Init()
		}

		gameSystems[i] = sys
		systemNames[i] = def.Name
	}

	// Wire spatial grid deregistration
	eng.OnEntityRemoved = func(e ecs.Entity) {
		base.SpatialGrid().Deregister(e)
	}

	gameHooks := gw.Hooks()
	gameLoop := engine.NewGameLoop(eng, gameSystems, systemNames, gameHooks)
	gameLoop.SetEventsCh(events)

	node := &pkguniverse.Cell{
		ID:        pkguniverse.MeshCellID(cell),
		Cell:      cell,
		Engine:    eng,
		World:     gw,
		Base:      base,
		Loop:      gameLoop,
		Bridge:    pkguniverse.NoopBridge{},
		Inbox:     make(chan pkguniverse.CellMessage, 256),
		Events:    events,
		Neighbors: make(map[string]*pkguniverse.Cell),
		Log:       log,
	}

	gw.SetBridge(pkguniverse.NoopBridge{})
	return node
}

// testGW extracts the underlying *GameWorld from a test node.
func testGW(node *pkguniverse.Cell) *GameWorld {
	return UnwrapGameWorld(node.World)
}
