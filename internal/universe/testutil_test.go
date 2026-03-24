package universe

import (
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/ops"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// newTestNode creates a Node for the given sector suitable for unit tests.
// It does NOT start the game loop or any goroutines.
func newTestNode(sector coords.SectorCoord) *pkguniverse.Node {
	log := logger.New()
	connMgr := net.NewConnManager()
	playerDB := game.NewPlayerRepo(nil)
	playerSessions := ops.NewPlayerSessions()
	cfg := game.DefaultGameConfig()
	platformCfg := engine.Config{TickRate: 20}

	eng := engine.New(platformCfg, connMgr, log)
	events := make(chan net.PlayerEvent, 64)

	base := pkguniverse.NewWorldBase(eng, sector, cfg.AoIRadius, nil)
	factory := GameNodeFactory(cfg, playerDB, playerSessions)
	world, systems := factory(&base)

	gameHooks := world.Hooks()
	gameLoop := engine.NewGameLoop(eng, systems, gameHooks)
	gameLoop.SetEventsCh(events)

	node := &pkguniverse.Node{
		ID:        pkguniverse.SectorID(sector),
		Sector:    sector,
		Engine:    eng,
		World:     world,
		Loop:      gameLoop,
		Bridge:    pkguniverse.NoopNodeBridge{},
		Inbox:     make(chan pkguniverse.NodeMessage, 256),
		Events:    events,
		Neighbors: make(map[string]*pkguniverse.Node),
		Log:       log,
	}

	world.SetBridge(pkguniverse.NoopNodeBridge{})
	return node
}

// testGW extracts the underlying *game.GameWorld from a test node.
func testGW(node *pkguniverse.Node) *game.GameWorld {
	return UnwrapGameWorld(node.World)
}
