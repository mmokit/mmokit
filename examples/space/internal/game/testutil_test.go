package game

import (
	ecs "github.com/mlange-42/ark/ecs"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit"
	gamepersisttest "github.com/mmokit/mmokit/examples/space/internal/persist/persisttest"
	comp "github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/ops"
	"github.com/mmokit/mmokit/pkg/persist/persisttest"
	"github.com/mmokit/mmokit/pkg/spatial"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// testUserIDNamespace is a fixed UUID namespace used to derive a
// deterministic UserID from a test username via uuid.NewSHA1. Tests
// that pass "alice" always get the same UserID, so test sessions and
// PlayerDB lookups stay in agreement across helper boundaries.
var testUserIDNamespace = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// testUserID returns a deterministic UserID for a test username.
// Mirrors what the production gateway populates from auth.users at
// login time — see hooks.go's OnEnter contract.
func testUserID(username string) uuid.UUID {
	return uuid.NewSHA1(testUserIDNamespace, []byte(username))
}

// newTestCell creates a Node for the given cell suitable for unit tests.
// It does NOT start the game loop or any goroutines.
func newTestCell(cell pkguniverse.CellID) *pkguniverse.Cell {
	log := logger.New()
	connMgr := net.NewConnManager()
	playerDB := NewPlayerRepo(nil, persisttest.NewPlayerRepoMock(), gamepersisttest.NewPlayerStateRepoMock(), nil)
	playerSessions := ops.NewPlayerSessions()
	cfg := DefaultGameConfig()
	platformCfg := engine.Config{TickRate: 20}

	eng := engine.New(platformCfg, connMgr, log)
	events := make(chan net.PlayerEvent, 64)

	base := pkguniverse.NewStage(eng, cell, cfg.AoIRadius, nil, pkguniverse.GlobalWire())
	base.SetSpatialGrid(spatial.NewHashGrid(coords.DefaultCellSize / 10))

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
	}, false, nil, nil)
	gw.PlayerSessions = playerSessions

	base.SetOnTransferReceived(func(entity ecs.Entity, frame *pkguniverse.TransferFrame) {
		gw.FinishTransferSpawn(entity, frame)
	})

	// Register GameWorld as cell-local state so mmokit.State[GameWorld](stage)
	// resolves to gw inside production code paths exercised by the test.
	base.SetStateByName("game.GameWorld", gw)

	defs := tmpCoord.SystemDefs()
	gameSystems := make([]engine.System, len(defs))
	systemNames := make([]string, len(defs))
	for i, def := range defs {
		sys := def.Factory()
		mmokit.WireSystem(sys, eng.ECS, eng, base)
		gameSystems[i] = sys
		systemNames[i] = def.Name
	}

	gameLoop := engine.NewGameLoop(eng, gameSystems, systemNames, base.Hooks())
	gameLoop.SetEventsCh(events)

	node := pkguniverse.NewCell(cell.MeshID(), cell)
	node.Engine = eng
	node.Stage = base
	node.Loop = gameLoop
	node.Bridge = pkguniverse.NoopBridge{}
	node.Inbox = make(chan pkguniverse.CellMessage, 256)
	node.Events = events
	node.Neighbors = make(map[pkguniverse.MeshCellID]*pkguniverse.Cell)
	node.Log = log

	base.SetBridge(pkguniverse.NoopBridge{})
	return node
}

// testGW extracts the underlying *GameWorld from a test node.
func testGW(node *pkguniverse.Cell) *GameWorld {
	return mmokit.State[GameWorld](node.Stage)
}
