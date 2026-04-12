package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	slitherpb "github.com/zenion/mmoserver/gen/go/slitherpb"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func main() {
	port := flag.Int("port", 8080, "HTTP server port")
	gridSize := flag.Int("grid", 2, "grid size (NxN cells)")
	headless := flag.Bool("headless", false, "disable interactive console")
	logFlag := flag.String("log", "", "comma-separated log categories/groups to enable (e.g. snake,mesh)")
	flag.Parse()

	cfg := DefaultConfig()

	cm := mmokit.NewConnManager()
	logger := mmokit.NewLogger()

	// Register log categories
	logger.RegisterCategories(SlitherCategories...)

	coord := mmokit.NewCoordinator(mmokit.Config{
		CellsX:        uint32(*gridSize),
		CellsY:        uint32(*gridSize),
		TickRate:      20,
		AoIRadius:     cfg.AoIRadius,
		Headless:      *headless,
		ConnManager:   cm,
		Logger:        logger,
		LogCategories: *logFlag,
		LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) {
			for _, data := range msgs {
				var evt enginepb.ClientEvent
				if err := proto.Unmarshal(data, &evt); err != nil {
					continue
				}
				if evt.Code == uint32(slitherpb.SlitherClientEventCode_SCE_SKIN_SELECT) {
					var m slitherpb.SkinSelectMsg
					if err := proto.Unmarshal(evt.Data, &m); err != nil {
						continue
					}
					name := strings.ToLower(strings.TrimSpace(m.Name))
					if name == "" || len(name) > 20 {
						continue
					}
					return name, &SlitherSessionData{SkinID: uint8(m.SkinId)}, nil
				}
			}
			return "", nil, mmokit.ErrLoginPending
		},
	})
	coord.SetWorld(func(base *mmokit.WorldBase) mmokit.GameWorld {
		return NewSlitherWorld(base, cfg)
	})
	coord.SetPlayerRouter(func(username string) string {
		return coord.NodeAtPosition(0, 0)
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
			Registry: registry,
			Entities: buildEntityOpts(gw, registry),
		})
	})

	coord.AddSystem("Input", mmokit.NewInputSystem(setupInputHandlers))
	coord.AddSystem("Bot", func() mmokit.System { return &BotSystem{} })
	coord.AddSystem("Boost", func() mmokit.System { return &BoostSystem{} })
	coord.AddSystem("Movement", func() mmokit.System { return &MovementSystem{} })
	coord.AddSystem("Physics", mmokit.NewPhysicsSystem())
	coord.AddSystem("ReplicaDeadReckoning", mmokit.NewDeadReckoningSystem())
	coord.AddSystem("Spatial", mmokit.NewSpatialSystemWith(setupSpatialHooks))
	coord.AddSystem("Eating", func() mmokit.System { return &EatingSystem{} })
	coord.AddSystem("Collision", func() mmokit.System { return &CollisionSystem{} })
	coord.AddSystem("Decay", func() mmokit.System { return &DecaySystem{} })
	coord.AddSystem("Death", func() mmokit.System { return &DeathSystem{} })
	coord.AddSystem("FoodSpawn", func() mmokit.System { return &FoodSpawnSystem{} })
	coord.AddSystem("Leaderboard", func() mmokit.System { return &LeaderboardSystem{} })
	coord.AddSystem("Network", func() mmokit.System { return &NetworkSystem{} })

	ctx := context.Background()

	// HTTP server: WebSocket + static files + metrics
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", cm.HandleWebSocket)
	mux.Handle("/metrics", coord.MetricsHandler())

	webDir := "web/dist"
	if _, err := os.Stat(webDir); os.IsNotExist(err) {
		webDir = "web"
	}
	mux.Handle("/", http.FileServer(http.Dir(webDir)))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("slither server starting on http://localhost%s", addr)
	log.Printf("grid: %dx%d nodes, %d bots per node, %d food per node",
		*gridSize, *gridSize, cfg.BotsPerNode, cfg.FoodPerNode)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("FATAL: http server: %v (is port %d already in use?)", err, *port)
			os.Exit(1)
		}
	}()

	// Blocks: runs console + handles signals + shuts down nodes
	coord.Start(ctx)
}
