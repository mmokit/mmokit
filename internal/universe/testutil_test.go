package universe

import (
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// newTestNode creates a Node for the given sector suitable for unit tests.
// It does NOT start the game loop or any goroutines.
func newTestNode(sector coords.SectorCoord) *Node {
	log := logger.New()
	connMgr := net.NewConnManager()
	playerDB := game.NewPlayerRepo(nil)
	cfg := game.DefaultGameConfig()
	platformCfg := engine.Config{TickRate: 20}
	return NewNode(sector, platformCfg, cfg, connMgr, playerDB, log)
}
