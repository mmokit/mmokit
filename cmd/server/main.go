package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/marketplace"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
	webpixi "github.com/zenion/mmoserver/web-pixi"
)

func main() {
	platformCfg := mmokit.DefaultEngineConfig()
	connMgr := mmokit.NewConnManager()

	// The engine's startHTTPListener owns /ws, /metrics, and static asset
	// serving on any gateway-role process. The space game's web client lives
	// in web-pixi/dist and is embedded via the webpixi package. UDP is started
	// separately below — different protocol, same ConnManager.
	coordCfg := mmokit.Config{
		TickRate:       platformCfg.TickRate,
		ConnManager:    connMgr,
		StaticFS:       webpixi.FS,
		StaticFSPrefix: "dist",
	}
	coordCfg.BindFlags()
	dumpSchema := flag.Bool("dump-schema", false, "dump protocol schema JSON to stdout and exit")
	flag.Parse()

	// Parse roles upfront so init decisions (Postgres, marketplace) can branch
	// on them before Coordinator.Build runs.
	roles, err := mmokit.ParseRoles(coordCfg.Mode)
	if err != nil {
		log.Fatalf("invalid --mode: %v", err)
	}

	if *dumpSchema {
		dumpProtocolSchema()
		return
	}

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

	// Two gates split game-side init by what each role actually needs:
	//
	//   needsGameConfig — open Postgres and load gameCfg (for grid dims +
	//   system tuning constants). Runs for any process that isn't a pure
	//   standalone gateway. Pure coordinator needs gameCfg to enumerate the
	//   cell grid via AssignmentEngine; host/node need it for systems.
	//
	//   needsGameState  — load playerDB + wire opRouter + marketplace +
	//   register the world factory and systems via game.GameSetup. Runs
	//   for processes that actually host cells (host or node).
	//
	// Pure standalone gateway skips both — it terminates WebSockets, routes
	// via cached topology, and never touches Postgres.
	isPureGateway := roles.Has(mmokit.RoleGateway) &&
		!roles.Has(mmokit.RoleCoordinator) &&
		!roles.Has(mmokit.RoleHost) &&
		!roles.Has(mmokit.RoleNode)
	needsGameConfig := !isPureGateway
	needsGameState := roles.Has(mmokit.RoleHost) || roles.Has(mmokit.RoleNode)

	coordCfg.Logger = gameLog
	coordCfg.LoginHandler = mmokit.HandleLogin(
		uint32(enginepb.ClientEventCode_CE_LOGIN),
		func(m *enginepb.LoginMsg) (string, any, error) {
			name, err := mmokit.ValidateUsername(m.Username, 0)
			return name, nil, err
		},
	)
	coordCfg.LoginRejected = func(connID uint32, reason string) {
		gameLog.Log(game.CatPlayerConnect, "login rejected: conn=%d reason=%s", connID, reason)
		rejectData := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_LOGIN_REJECTED), &enginepb.LoginRejectedMsg{
			Reason: reason,
		})
		if rejectData != nil {
			connMgr.SendReliable(connID, rejectData)
		}
	}

	// State declared up front so closures below can capture them.
	// Nil/zero in pure-gateway mode — guarded by the needs* flags.
	var playerDB *game.PlayerRepo
	var opRouter *mmokit.OpRouter
	var playerSessions *mmokit.PlayerSessions
	var gameCfg game.GameConfig
	var configRepo mmokit.ConfigRepository
	var marketSvc *marketplace.Settlement
	var store *mmokit.PostgresStore

	if needsGameConfig {
		// Open the persistence store. Defaults to the local docker-compose
		// Postgres; override via POSTGRES_URL env var.
		postgresURL := os.Getenv("POSTGRES_URL")
		if postgresURL == "" {
			postgresURL = "postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable"
		}
		var err error
		store, err = mmokit.OpenPostgres(context.Background(), postgresURL)
		if err != nil {
			log.Fatalf("failed to open postgres (%s): %v", postgresURL, err)
		}
		defer store.Close()
		log.Printf("postgres connected at %s", postgresURL)

		configRepo = store.Config()

		// Load game config (uses defaults if not found). Every role that isn't
		// a pure standalone gateway needs it — pure coordinator for grid dims,
		// host/node for system tuning constants.
		gameCfg, err = game.LoadConfig(context.Background(), configRepo)
		if err != nil {
			log.Fatalf("failed to load game config: %v", err)
		}
		log.Println("game config loaded")

		coordCfg.CellsX = gameCfg.MeshCellsX
		coordCfg.CellsY = gameCfg.MeshCellsY
	}

	if needsGameState {
		playerRepo := store.Players()
		marketRepo := store.Market()

		playerDB = game.NewPlayerRepo(playerRepo, gameLog)
		if err := playerDB.LoadAll(context.Background()); err != nil {
			log.Fatalf("failed to load player data: %v", err)
		}

		playerSessions = mmokit.NewPlayerSessions()
		opRouter = mmokit.NewOpRouter(connMgr, playerSessions, 2,
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
		marketCfg := mmokit.OrderBookConfig{
			TaxPct:      gameCfg.MarketTaxPct,
			OrderExpiry: int64(gameCfg.MarketOrderExpiry * 3600),
			MinPrice:    gameCfg.MarketMinPrice,
			MaxOrders:   gameCfg.MarketMaxOrders,
		}
		obSvc := mmokit.NewOrderBookService(marketCfg)
		marketSvc = marketplace.NewSettlement(
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
			marketRepo,
			func(username string, code uint32, payload []byte) {
				connID := opRouter.ConnIDForUsername(username)
				if connID != 0 {
					opRouter.SendPush(connID, code, payload)
				}
			},
		)
		if err := marketSvc.LoadAll(context.Background()); err != nil {
			log.Fatalf("failed to load marketplace data: %v", err)
		}
		marketplace.RegisterHandlers(opRouter, marketSvc, 1)
	}

	coordinator := mmokit.NewCoordinator(coordCfg)

	if needsGameState {
		coordinator.OnConsoleReady(func(console *mmokit.Console) {
			var allNodes []game.NodeInfo
			var anyWorld *game.GameWorld
			for _, node := range coordinator.Cells {
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
				Config:      anyWorld.Config,
				ConfigSave:  func() error { return game.SaveConfig(context.Background(), configRepo, anyWorld.Config) },
				ConfigReset: func() { *anyWorld.Config = game.DefaultGameConfig() },
				// When any config field changes at runtime, re-apply equipment-derived
				// stats (Thrust, MaxSpeed, TurnRate, Shield caps) on every active
				// ship across every node so the change takes effect immediately
				// instead of only on next spawn/equip.
				ConfigOnChanged: func(_ string) {
					for _, ni := range allNodes {
						gw := ni.World
						eng := gw.Engine()
						eng.PendingAdminCmds <- func() {
							gw.Players.ForEach(mmokit.StateActive, func(s *mmokit.PlayerSession) {
								if eng.ECS.Alive(s.Entity) {
									gw.ApplyEquipmentStats(s.Entity)
								}
							})
						}
					}
				},
				Registry: anyWorld.Registry,
				Entities: game.BuildEntityOpts(anyWorld),
			})
			game.RegisterCommands(console, coordinator, playerDB, allNodes)
		})
		game.GameSetup(coordinator, &gameCfg, playerDB, playerSessions)
		game.InitDropTables()

		coordinator.SetPlayerRouter(func(username string) string {
			if pdata := playerDB.Get(username); pdata != nil {
				worldX := float32(pdata.CellX)*coords.CellSize + pdata.X
				worldY := float32(pdata.CellY)*coords.CellSize + pdata.Y
				nodeID := coordinator.NodeAtPosition(worldX, worldY)
				if nodeID != "" {
					return nodeID
				}
			}
			// New player or invalid saved position — spawn at station
			stationWorldX := float32(gameCfg.StationCell.CellX)*coords.CellSize + coords.CellSize/2
			stationWorldY := float32(gameCfg.StationCell.CellY)*coords.CellSize + coords.CellSize/2
			nodeID := coordinator.NodeAtPosition(stationWorldX, stationWorldY)
			return nodeID
		})
	}

	ctx := context.Background()

	if needsGameState {
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
	}

	// Build coordinator first so /metrics and other routes are registered
	// on the ConnManager before the HTTP server starts.
	coordinator.Build()

	// Only processes with the gateway role terminate client connections.
	// HTTP (/ws, /metrics, static web client) is owned by the engine's
	// startHTTPListener — started inside coordinator.Start below. UDP is a
	// separate protocol and has to be launched manually here.
	if coordinator.ServesClients() {
		udpServer, err := mmokit.NewUDPServer(platformCfg.UDPAddr, connMgr)
		if err != nil {
			log.Fatalf("failed to start UDP server: %v", err)
		}
		log.Printf("udp server listening on %s", platformCfg.UDPAddr)
		go udpServer.Run(ctx)
	} else {
		log.Printf("mmoserver starting with roles=%s — no client listeners", coordinator.Roles())
	}

	// Blocks: runs console + handles signals + shuts down nodes
	coordinator.Start(ctx)

	// Post-shutdown cleanup — flush the player dirty set synchronously
	// so the final tick's state lands in storage before we exit.
	if needsGameState && playerDB != nil {
		if n, err := playerDB.FlushDirty(context.Background()); err != nil {
			log.Printf("shutdown: flush error: %v", err)
		} else {
			log.Printf("shutdown: flushed %d players", n)
		}
	}
	log.Println("shutdown complete")
}
