package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/netutil"
	"github.com/zenion/mmoserver/internal/system"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/marketplace"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/persist"
	"github.com/zenion/mmoserver/pkg/spatial"
)

func main() {
	platformCfg := engine.DefaultConfig()
	connMgr := net.NewConnManager()

	// Handle pings immediately on the read goroutine (bypasses game loop
	// tick delay) so the client sees true network RTT, not RTT + up-to-50ms.
	connMgr.EventInterceptor = func(conn *net.Conn, payload []byte) bool {
		var evt gamepb.ClientEvent
		if err := proto.Unmarshal(payload, &evt); err != nil {
			return false
		}
		if gamepb.ClientEventCode(evt.Code) != gamepb.ClientEventCode_CE_PING {
			return false
		}
		var ping gamepb.PingMsg
		if err := proto.Unmarshal(evt.Data, &ping); err != nil {
			return false
		}
		pong := &gamepb.PongMsg{
			ClientTime: ping.ClientTime,
			ServerTime: time.Now().UnixMilli(),
		}
		pongData, err := proto.Marshal(pong)
		if err != nil {
			return true
		}
		srvEvt := &gamepb.ServerEvent{
			Code: uint32(gamepb.ServerEventCode_SE_PONG),
			Data: pongData,
		}
		srvEvtData, err := proto.Marshal(srvEvt)
		if err != nil {
			return true
		}
		frame := make([]byte, 1+len(srvEvtData))
		frame[0] = net.ChannelEvent
		copy(frame[1:], srvEvtData)
		conn.Send(frame)
		return true
	}

	// Create logger with desired categories enabled by default.
	// Toggle interactively at runtime via the server console.
	gameLog := logger.New(
		game.CatConnect,
		game.CatSpawn,
		game.CatCombat,
		game.CatKill,
		game.CatMining,
		game.CatEconomy,
		game.CatDock,
		game.CatLoot,
		game.CatMarket,
	)
	gameLog.RegisterCategories(game.GameCategories...)

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
	eng := engine.New(platformCfg, connMgr, gameLog)
	gw := game.NewGameWorld(eng, gameCfg, playerDB, grid, component.SectorCoord{SX: 0, SY: 0})
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
	systemNames := []string{
		"Input", "Docking", "TargetLock", "ShipControl", "Mining",
		"Economy", "Equipment", "Ability", "StatusEffect", "Physics",
		"SectorBoundary", "Lifetime", "Spatial", "Collision", "ShieldRegen", "Network",
	}

	gameLoop := engine.NewGameLoop(eng, systems, systemNames, gw.Hooks())

	// Operation router (marketplace + future services)
	playerSessions := ops.NewPlayerSessions()
	gw.PlayerSessions = playerSessions
	opRouter := ops.NewRouter(connMgr, playerSessions, 2,
		func(raw []byte) (ops.ParsedRequest, error) {
			var req gamepb.OperationRequest
			if err := proto.Unmarshal(raw, &req); err != nil {
				return ops.ParsedRequest{}, err
			}
			return ops.ParsedRequest{Code: req.Code, RequestID: req.RequestId, Data: req.Data}, nil
		},
		netutil.MakeOpResponse,
	)

	// Marketplace service
	marketStore, err := persist.OpenBolt("data/marketplace.db")
	if err != nil {
		log.Fatalf("failed to open marketplace database: %v", err)
	}
	marketWriter := persist.NewAsyncWriter(marketStore, 4096)
	marketWriter.Start()

	marketCfg := marketplace.Config{
		TaxPct:      gameCfg.MarketTaxPct,
		OrderExpiry: int64(gameCfg.MarketOrderExpiry * 3600), // hours -> seconds
		MinPrice:    gameCfg.MarketMinPrice,
		MaxOrders:   gameCfg.MarketMaxOrders,
	}
	marketSvc := marketplace.NewService(
		marketplace.BankOps{
			GetBankBalance: playerDB.GetBankBalance,
			ModifyBank:     playerDB.ModifyBank,
			GetFlux:        playerDB.GetFlux,
			ModifyFlux:     playerDB.ModifyFlux,
			MarkDirty:      playerDB.MarkDirty,
			SendBankUpdate: func(username string) {
				connID := opRouter.ConnIDForUsername(username)
				if connID == 0 {
					return
				}
				pdata := playerDB.Get(username)
				if pdata == nil {
					return
				}
				var items []*gamepb.InventoryItem
				for id, qty := range pdata.Bank {
					if qty > 0 {
						items = append(items, &gamepb.InventoryItem{ItemId: id, Quantity: qty})
					}
				}
				var cargoItems []*gamepb.InventoryItem
				for id, qty := range pdata.Cargo {
					if qty > 0 {
						cargoItems = append(cargoItems, &gamepb.InventoryItem{ItemId: id, Quantity: qty})
					}
				}
				frame := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_BANK_CONTENTS), &gamepb.BankContentsMsg{
					Items:        items,
					TotalMass:    pdata.BankTotalMass(),
					MaxMass:      gameCfg.BankMaxMass,
					CargoItems:   cargoItems,
					CargoMass:    pdata.CargoTotalMass(),
					MaxCargoMass: gameCfg.MaxCargo,
					FluxBalance:  pdata.Flux,
				})
				if frame != nil {
					connMgr.SendReliable(connID, frame)
				}
			},
		},
		marketCfg,
		gameLog,
		marketWriter,
		func(username string, code uint32, payload proto.Message) {
			connID := opRouter.ConnIDForUsername(username)
			if connID != 0 {
				opRouter.SendPush(connID, code, payload)
			}
		},
	)
	if err := marketSvc.LoadAll(marketStore); err != nil {
		log.Fatalf("failed to load marketplace data: %v", err)
	}
	marketplace.RegisterHandlers(opRouter, marketSvc, 1) // stationID=1 (single station)

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

	// Start operation router
	go opRouter.Run(ctx)

	// Periodic marketplace order expiry (every 60s)
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				marketSvc.ExpireOrders()
			}
		}
	}()

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
	marketWriter.Flush()
	store.Close()
	marketStore.Close()
	log.Println("shutdown complete")
}
