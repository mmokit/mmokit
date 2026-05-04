package game

import (
	ecs "github.com/mlange-42/ark/ecs"
	comp "github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/mmokit"
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

	base := pkguniverse.NewStage(eng, cell, cfg.AoIRadius, nil)
	base.SetSpatialGrid(spatial.NewHashGrid(coords.CellSize / 10))

	// Collect system defs and realize entity kinds via a throwaway coordinator.
	// Kind specs must run against the stage before NewGameWorld spawns any
	// initial entities (asteroids, station) — those calls rely on
	// EntityKindDefs being populated.
	tmpCoord := pkguniverse.New(pkguniverse.Config{CellsX: 1, CellsY: 1, TickRate: platformCfg.TickRate})
	GameSetup(tmpCoord)
	tmpCoord.RealizeKindSpecs(base)

	// Build the game world directly (same logic as the world factory in GameSetup)
	gw := NewGameWorld(base, &cfg, playerDB, comp.CellCoord{
		CellX: cell.X, CellY: cell.Y,
	}, false)
	gw.PlayerSessions = playerSessions

	base.SetOnTransferReceived(func(entity ecs.Entity, frame *pkguniverse.TransferFrame) {
		gw.FinishTransferSpawn(entity, frame)
	})

	defs := tmpCoord.SystemDefs()
	gameSystems := make([]engine.System, len(defs))
	systemNames := make([]string, len(defs))
	for i, def := range defs {
		sys := def.Factory()
		mmokit.WireSystem(sys, eng.ECS, eng, gw)
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
		MeshID:    cell.MeshID(),
		Cell:      cell,
		Engine:    eng,
		World:     gw,
		Stage:     base,
		Loop:      gameLoop,
		Bridge:    pkguniverse.NoopBridge{},
		Inbox:     make(chan pkguniverse.CellMessage, 256),
		Events:    events,
		Neighbors: make(map[pkguniverse.MeshCellID]*pkguniverse.Cell),
		Log:       log,
	}

	gw.SetBridge(pkguniverse.NoopBridge{})
	return node
}

// testGW extracts the underlying *GameWorld from a test node.
func testGW(node *pkguniverse.Cell) *GameWorld {
	return UnwrapGameWorld(node.World)
}
