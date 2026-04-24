package main

import (
	"context"
	"flag"
	"log"
	"os"

	slitherpb "github.com/zenion/mmoserver/gen/go/slitherpb"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func main() {
	slitherCfg := DefaultConfig()

	logger := mmokit.NewLogger()
	logger.RegisterCategories(SlitherCategories...)

	// Slither serves static assets from a filesystem path (not an embed).
	// Prefer web/dist (Vite build output); fall back to web/ for dev.
	webDir := "web/dist"
	if _, err := os.Stat(webDir); os.IsNotExist(err) {
		webDir = "web"
	}

	cfg := mmokit.Config{
		TickRate:    20,
		AoIRadius:   slitherCfg.AoIRadius,
		ConnManager: mmokit.NewConnManager(),
		Logger:      logger,
		WebDir:      webDir,
		LoginHandler: mmokit.HandleLogin(
			uint32(slitherpb.SlitherClientEventCode_SCE_SKIN_SELECT),
			func(m *slitherpb.SkinSelectMsg) (string, any, error) {
				name, err := mmokit.ValidateUsername(m.Name, 20)
				if err != nil {
					return "", nil, err
				}
				return name, &SlitherSessionData{SkinID: uint8(m.SkinId)}, nil
			},
		),
	}
	cfg.Protocol = mmokit.NewProtocol("slither").
		ServerEvents(func(e *mmokit.ServerEvents) {
			mmokit.RegisterServerEvent[slitherpb.SlitherLeaderboardMsg](e,
				slitherpb.SlitherServerEventCode_SSE_LEADERBOARD)
			mmokit.RegisterServerEvent[slitherpb.SlitherKillFeedMsg](e,
				slitherpb.SlitherServerEventCode_SSE_KILL_FEED)
		})

	cfg.BindFlags()
	gridSize := flag.Int("grid", 2, "grid size (NxN cells)")
	flag.Parse()
	cfg.CellsX = uint32(*gridSize)
	cfg.CellsY = uint32(*gridSize)

	// Slither plays on the default 8192 cell size. Center of cell (0,0) is
	// (4096, 4096) — within cell 0_0's world bounds [0,8192)².
	cfg.DefaultSpawn = mmokit.Location{X: 4096, Y: 4096}
	coord := mmokit.New(cfg)
	coord.SetWorld(func(base *mmokit.WorldBase) mmokit.GameWorld {
		return NewSlitherWorld(base, slitherCfg)
	})
	coord.OnConsoleReady(func(console *mmokit.Console) {
		var gw *SlitherWorld
		for _, node := range coord.Cells {
			gw = node.World.(*SlitherWorld)
			break
		}
		if gw == nil {
			return
		}
		registry := buildEntityRegistry(gw)
		console.RegisterBuiltins(mmokit.BuiltinOpts{
			Engine:   gw.Engine(),
			Registry: registry,
			Entities: buildEntityOpts(gw, registry),
		})
	})

	coord.AddSystem("Input", mmokit.NewInputSystem(setupInputHandlers))
	coord.AddSystem("Bot", func() mmokit.System { return &BotSystem{} })
	coord.AddSystem("Boost", func() mmokit.System { return &BoostSystem{} })
	coord.AddSystem("Movement", func() mmokit.System { return &MovementSystem{} })
	coord.AddSystem("Physics", mmokit.NewPhysicsSystem())
	coord.AddSystem("Spatial", mmokit.NewSpatialSystemWith(setupSpatialHooks))
	coord.AddSystem("Eating", func() mmokit.System { return &EatingSystem{} })
	coord.AddSystem("Collision", func() mmokit.System { return &CollisionSystem{} })
	coord.AddSystem("Decay", func() mmokit.System { return &DecaySystem{} })
	coord.AddSystem("Death", func() mmokit.System { return &DeathSystem{} })
	coord.AddSystem("FoodSpawn", func() mmokit.System { return &FoodSpawnSystem{} })
	coord.AddSystem("Leaderboard", func() mmokit.System { return &LeaderboardSystem{} })
	coord.AddSystem("Network", func() mmokit.System { return &NetworkSystem{} })

	log.Printf("slither: grid %dx%d, %d bots per node, %d food per node",
		*gridSize, *gridSize, slitherCfg.BotsPerNode, slitherCfg.FoodPerNode)
	coord.Start(context.Background())
}

