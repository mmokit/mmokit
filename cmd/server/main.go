package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/marketplace"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func main() {
	dynamicCells := flag.Bool("dynamic-cells", false, "enable dynamic cell partitioning")
	flag.Parse()

	platformCfg := mmokit.DefaultEngineConfig()
	connMgr := mmokit.NewConnManager()

	// Handle pings immediately on the read goroutine (bypasses game loop
	// tick delay) so the client sees true network RTT, not RTT + up-to-50ms.
	connMgr.EventInterceptor = func(conn *mmokit.Conn, payload []byte) bool {
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
		frame[0] = mmokit.ChannelEvent
		copy(frame[1:], srvEvtData)
		conn.Send(frame)
		return true
	}

	// Create logger with desired categories enabled by default.
	// Toggle interactively at runtime via the server console.
	gameLog := mmokit.NewLogger(
		game.CatPlayerConnect,
		game.CatPlayerSpawn,
		game.CatCombatHit,
		game.CatCombatKill,
		game.CatEconomyMining,
		game.CatEconomyBank,
		game.CatPlayerDock,
		game.CatEconomyLoot,
		game.CatEconomyMarket,
		game.CatWorldTransfer,
	)
	gameLog.RegisterCategories(game.GameCategories...)

	// Open persistence store
	if err := os.MkdirAll("data", 0755); err != nil {
		log.Fatalf("failed to create data dir: %v", err)
	}
	store, err := mmokit.OpenBolt("data/gameserver.db")
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	writer := mmokit.NewAsyncWriter(store, 4096)
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
	playerSessions := mmokit.NewPlayerSessions()

	coordCfg := mmokit.Config{
		CellsX:      gameCfg.MeshCellsX,
		CellsY:      gameCfg.MeshCellsY,
		TickRate:    platformCfg.TickRate,
		ConnManager: connMgr,
		Logger:      gameLog,
		LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) {
			for _, data := range msgs {
				var evt enginepb.ClientEvent
				if err := proto.Unmarshal(data, &evt); err != nil {
					continue
				}
				if enginepb.ClientEventCode(evt.Code) == enginepb.ClientEventCode_CE_LOGIN {
					var login enginepb.LoginMsg
					if err := proto.Unmarshal(evt.Data, &login); err != nil {
						continue
					}
					username := strings.ToLower(login.Username)
					if username == "" {
						continue
					}
					return username, nil, nil
				}
			}
			return "", nil, mmokit.ErrLoginPending
		},
		LoginRejected: func(connID uint32, reason string) {
			gameLog.Log(game.CatPlayerConnect, "login rejected: conn=%d reason=%s", connID, reason)
			rejectData := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_LOGIN_REJECTED), &enginepb.LoginRejectedMsg{
				Reason: reason,
			})
			if rejectData != nil {
				connMgr.SendReliable(connID, rejectData)
			}
		},
	}
	if *dynamicCells {
		coordCfg.DynamicPartitioning = mmokit.DefaultPartitionConfig()
		log.Println("dynamic cell partitioning enabled")
	}
	var coordinator *mmokit.Coordinator
	coordinator = mmokit.NewCoordinator(coordCfg)
	coordinator.OnConsoleReady(func(console *mmokit.Console) {
		var allNodes []game.NodeInfo
		var anyWorld *game.GameWorld
		for _, node := range coordinator.Nodes {
			gw := game.UnwrapGameWorld(node.World)
			allNodes = append(allNodes, game.NodeInfo{
				ID:    node.ID,
				Cell:  node.Cell,
				World: gw,
			})
			if anyWorld == nil {
				anyWorld = gw
			}
		}

		console.RegisterBuiltins(mmokit.BuiltinOpts{
			Config:      &anyWorld.Config,
			ConfigSave:  func() error { return game.SaveConfig(store, &anyWorld.Config) },
			ConfigReset: func() { anyWorld.Config = game.DefaultGameConfig() },
			Registry:    anyWorld.Registry,
			Entities:    game.BuildEntityOpts(anyWorld),
		})
		game.RegisterCommands(console, coordinator, playerDB, store, allNodes)
	})
	game.GameSetup(coordinator, gameCfg, playerDB, playerSessions)
	game.InitDropTables()

	coordinator.SetPlayerRouter(func(username string) string {
		if pdata := playerDB.Get(username); pdata != nil {
			worldX := float32(pdata.CellX)*coords.CellSize + pdata.X
			worldY := float32(pdata.CellY)*coords.CellSize + pdata.Y
			nodeID := coordinator.NodeAtPosition(worldX, worldY)
			log.Printf("[router] %s: saved=(%d,%d):(%.0f,%.0f) world=(%.0f,%.0f) -> node=%q",
				username, pdata.CellX, pdata.CellY, pdata.X, pdata.Y, worldX, worldY, nodeID)
			if nodeID != "" {
				return nodeID
			}
		}
		// New player or invalid saved position — spawn at station
		stationWorldX := float32(gameCfg.StationCell.CellX)*coords.CellSize + coords.CellSize/2
		stationWorldY := float32(gameCfg.StationCell.CellY)*coords.CellSize + coords.CellSize/2
		nodeID := coordinator.NodeAtPosition(stationWorldX, stationWorldY)
		log.Printf("[router] %s: fallback station world=(%.0f,%.0f) -> node=%q", username, stationWorldX, stationWorldY, nodeID)
		return nodeID
	})

	opRouter := mmokit.NewOpRouter(connMgr, playerSessions, 2,
		func(raw []byte) (mmokit.ParsedRequest, error) {
			var req enginepb.OperationRequest
			if err := proto.Unmarshal(raw, &req); err != nil {
				return mmokit.ParsedRequest{}, err
			}
			return mmokit.ParsedRequest{Code: req.Code, RequestID: req.RequestId, Data: req.Data}, nil
		},
		mmokit.MakeOpResponse,
	)

	// Marketplace service
	marketStore, err := mmokit.OpenBolt("data/marketplace.db")
	if err != nil {
		log.Fatalf("failed to open marketplace database: %v", err)
	}
	marketWriter := mmokit.NewAsyncWriter(marketStore, 4096)
	marketWriter.Start()

	marketCfg := mmokit.OrderBookConfig{
		TaxPct:      gameCfg.MarketTaxPct,
		OrderExpiry: int64(gameCfg.MarketOrderExpiry * 3600),
		MinPrice:    gameCfg.MarketMinPrice,
		MaxOrders:   gameCfg.MarketMaxOrders,
	}
	obSvc := mmokit.NewOrderBookService(marketCfg)
	marketSvc := marketplace.NewSettlement(
		obSvc,
		marketplace.BankOps{
			GetBankBalance: playerDB.GetBankBalance,
			ModifyBank:     playerDB.ModifyBank,
			GetCurrency:    playerDB.GetCurrency,
			ModifyCurrency: playerDB.ModifyCurrency,
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
				var currencies []*gamepb.CurrencyBalance
				for curID, bal := range pdata.Currencies {
					if bal != 0 {
						currencies = append(currencies, &gamepb.CurrencyBalance{CurrencyId: curID, Balance: bal})
					}
				}
				frame := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_BANK_CONTENTS), &gamepb.BankContentsMsg{
					Items:        items,
					TotalMass:    pdata.BankTotalMass(),
					MaxMass:      gameCfg.BankMaxMass,
					CargoItems:   cargoItems,
					CargoMass:    pdata.CargoTotalMass(),
					MaxCargoMass: gameCfg.MaxCargo,
					Currencies:   currencies,
				})
				if frame != nil {
					connMgr.SendReliable(connID, frame)
				}
			},
		},
		marketCfg,
		gameCfg.SettlementCurrencyID,
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
	marketplace.RegisterHandlers(opRouter, marketSvc, 1)

	ctx := context.Background()

	// Start operation router
	go opRouter.Run(ctx)

	// Periodic marketplace order expiry
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

	// Build coordinator first so /metrics and other routes are registered
	// on the ConnManager before the HTTP server starts.
	coordinator.Build()

	// Start WebSocket server
	go func() {
		if err := connMgr.ListenAndServe(ctx, platformCfg.ListenAddr); err != nil {
			log.Printf("websocket server stopped: %v", err)
		}
	}()

	// Start UDP server
	udpServer, err := mmokit.NewUDPServer(platformCfg.UDPAddr, connMgr)
	if err != nil {
		log.Fatalf("failed to start UDP server: %v", err)
	}
	log.Printf("udp server listening on %s", platformCfg.UDPAddr)
	go udpServer.Run(ctx)

	// Blocks: runs console + handles signals + shuts down nodes
	coordinator.Start(ctx)

	// Post-shutdown cleanup
	writer.Flush()
	marketWriter.Flush()
	store.Close()
	marketStore.Close()
	log.Println("shutdown complete")
}
