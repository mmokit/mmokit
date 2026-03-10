package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/system"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/persist"
	"github.com/zenion/mmoserver/pkg/spatial"
)

func main() {
	platformCfg := engine.DefaultConfig()
	connMgr := net.NewConnManager()

	// Create logger with desired categories enabled by default.
	// Toggle interactively at runtime via the server console.
	gameLog := logger.New(
		logger.CatConnect,
		logger.CatSpawn,
		logger.CatCombat,
		logger.CatKill,
		logger.CatMining,
		logger.CatEconomy,
	)

	// Open persistence store
	if err := os.MkdirAll("data", 0755); err != nil {
		log.Fatalf("failed to create data dir: %v", err)
	}
	store, err := persist.OpenBolt("data/gameserver.db")
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	writer := persist.NewAsyncWriter(store, 4096)
	writer.Start()

	// Load game config from DB (uses defaults if not found)
	gameCfg, err := game.LoadConfig(store)
	if err != nil {
		log.Fatalf("failed to load game config: %v", err)
	}
	log.Println("game config loaded")

	// Load player data from disk
	playerDB := game.NewPlayerRepo(writer)
	if err := playerDB.LoadAll(store); err != nil {
		log.Fatalf("failed to load player data: %v", err)
	}

	grid := spatial.NewGrid(gameCfg.GridCellSize)
	eng := engine.New(platformCfg, connMgr, grid, gameLog)
	gw := game.NewGameWorld(eng, gameCfg, playerDB)

	systems := []engine.System{
		system.NewInputSystem(gw),
		system.NewTargetLockSystem(gw),
		system.NewShipControlSystem(gw),
		system.NewMiningSystem(gw),
		system.NewEconomySystem(gw),
		system.NewAbilitySystem(gw),
		system.NewStatusEffectSystem(gw),
		system.NewPhysicsSystem(gw),
		system.NewLifetimeSystem(gw),
		system.NewSpatialSystem(gw),
		system.NewDamageSystem(gw),
		system.NewNetworkSystem(gw),
	}

	gameLoop := engine.NewGameLoop(eng, systems, gw.Hooks())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("shutting down...")
		cancel()
	}()

	// Start game loop
	go gameLoop.Run(ctx)

	// Start WebSocket server
	go func() {
		if err := connMgr.ListenAndServe(ctx, platformCfg.ListenAddr); err != nil {
			log.Printf("websocket server stopped: %v", err)
		}
	}()

	// Start UDP server
	udpServer, err := net.NewUDPServer(platformCfg.UDPAddr, connMgr)
	if err != nil {
		log.Fatalf("failed to start UDP server: %v", err)
	}
	log.Printf("udp server listening on %s", platformCfg.UDPAddr)
	go udpServer.Run(ctx)

	// Set up and run interactive console on main goroutine
	console := engine.NewConsole(eng, gameLog)
	game.RegisterCommands(console, gw, store)
	console.Run(ctx)

	// Shutdown sequence
	gw.Shutdown()
	writer.Flush()
	store.Close()
	log.Println("shutdown complete")
}
