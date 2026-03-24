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

	id := pkguniverse.SectorID(sector)
	eng := engine.New(platformCfg, connMgr, log)
	events := make(chan net.PlayerEvent, 64)

	factory := GameNodeFactory(cfg, connMgr, playerDB, playerSessions, log)
	world, gameLoop := factory(sector, eng, events, log)
	gameLoop.SetEventsCh(events)

	return &pkguniverse.Node{
		ID:        id,
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
}

// testGW extracts the underlying *game.GameWorld from a test node.
func testGW(node *pkguniverse.Node) *game.GameWorld {
	return UnwrapGameWorld(node.World)
}
