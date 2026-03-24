package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/marketplace"
	"github.com/zenion/mmoserver/internal/netutil"
	internaluniverse "github.com/zenion/mmoserver/internal/universe"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/orderbook"
	"github.com/zenion/mmoserver/pkg/persist"
	"github.com/zenion/mmoserver/pkg/universe"
)

func main() {
	platformCfg := engine.DefaultConfig()
	connMgr := net.NewConnManager()

	// Handle pings immediately on the read goroutine (bypasses game loop
	// tick delay) so the client sees true network RTT, not RTT + up-to-50ms.
	connMgr.EventInterceptor = func(conn *net.Conn, payload []byte) bool {
		var evt enginepb.ClientEvent
		if err := proto.Unmarshal(payload, &evt); err != nil {
			return false
		}
		if enginepb.ClientEventCode(evt.Code) != enginepb.ClientEventCode_CE_PING {
			return false
		}
		var ping enginepb.PingMsg
		if err := proto.Unmarshal(evt.Data, &ping); err != nil {
			return false
		}
		pong := &enginepb.PongMsg{
			ClientTime: ping.ClientTime,
			ServerTime: time.Now().UnixMilli(),
		}
		pongData, err := proto.Marshal(pong)
		if err != nil {
			return true
		}
		srvEvt := &enginepb.ServerEvent{
			Code: uint32(enginepb.ServerEventCode_SE_PONG),
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
		game.CatTransfer,
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

	// Operation router session tracker (wired into factory so each node's world gets it)
	playerSessions := ops.NewPlayerSessions()

	// Create coordinator with 9 nodes (3x3 sector grid)
	grid := universe.GridConfig{MinSX: -1, MaxSX: 1, MinSY: -1, MaxSY: 1}
	factory := internaluniverse.GameNodeFactory(gameCfg, connMgr, playerDB, playerSessions, gameLog)
	coordinator := universe.NewCoordinator(grid, platformCfg, connMgr, gameLog, factory)
	game.InitDropTables()

	opRouter := ops.NewRouter(connMgr, playerSessions, 2,
		func(raw []byte) (ops.ParsedRequest, error) {
			var req enginepb.OperationRequest
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

	marketCfg := orderbook.Config{
		TaxPct:      gameCfg.MarketTaxPct,
		OrderExpiry: int64(gameCfg.MarketOrderExpiry * 3600), // hours -> seconds
		MinPrice:    gameCfg.MarketMinPrice,
		MaxOrders:   gameCfg.MarketMaxOrders,
	}
	obSvc := orderbook.NewService(marketCfg)
	marketSvc := marketplace.NewSettlement(
		obSvc,
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
				frame := netutil.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_BANK_CONTENTS), &gamepb.BankContentsMsg{
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
		func(username string, code uint32, payload []byte) {
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

	// Start all node game loops
	coordinator.Start(ctx)

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

	// Set up and run interactive console on main goroutine (uses default node)
	defaultNode := coordinator.DefaultNode()
	console := engine.NewConsole(defaultNode.Engine, gameLog)

	// Build node info list for cross-node admin commands
	var allNodes []game.NodeInfo
	for _, node := range coordinator.Nodes {
		allNodes = append(allNodes, game.NodeInfo{
			ID:     node.ID,
			Sector: node.Sector,
			World:  internaluniverse.UnwrapGameWorld(node.World),
		})
	}
	game.RegisterCommands(console, internaluniverse.UnwrapGameWorld(defaultNode.World), store, allNodes)
	console.Run(ctx)

	// Shutdown sequence
	coordinator.Shutdown()
	writer.Flush()
	marketWriter.Flush()
	store.Close()
	marketStore.Close()
	log.Println("shutdown complete")
}
