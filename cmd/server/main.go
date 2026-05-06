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
	"github.com/zenion/mmoserver/pkg/auth"
	gamecommands "github.com/zenion/mmoserver/internal/game/commands"
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
	coordCfg.Protocol = mmokit.NewProtocol("space").
		ClientEvents(func(e *mmokit.ClientEvents) {
			// CE_PING is auto-registered by NewProtocol.
			// CE_LOGIN bypasses the input dispatcher (handled by
			// LoginHandler on the gateway). Typed client-input messages
			// (RESPAWN, BANK, DOCK, UNDOCK, etc.) are registered via
			// mmokit.HandleClient[T] and exposed through the
			// ClientInputTypes schema, not the ClientEvents registry.
			mmokit.RegisterClientEvent[enginepb.LoginMsg](e, enginepb.ClientEventCode_CE_LOGIN)
		}).
		ServerEvents(func(e *mmokit.ServerEvents) {
			// SE_PLAYER_SPAWNED: override engine default (enginepb.SpawnedMsg) with
			// game-specific payload that includes inventory + equipment.
			mmokit.RegisterServerEvent[gamepb.PlayerSpawnedMsg](e,
				enginepb.ServerEventCode_SE_PLAYER_SPAWNED, mmokit.WithEventName("playerSpawned"))
			mmokit.RegisterServerEvent[gamepb.WorldUpdateMsg](e,
				enginepb.ServerEventCode_SE_WORLD_UPDATE, mmokit.WithEventName("worldUpdate"))
			mmokit.RegisterServerEvent[gamepb.PlayerDiedMsg](e,
				gamepb.GameServerEventCode_GSE_PLAYER_DIED)
			mmokit.RegisterEvent[game.PlayerDied]()
			// SE_PLAYER_OWN_STATE: engine code, game-specific payload (no engine default).
			mmokit.RegisterServerEvent[gamepb.PlayerOwnStateMsg](e,
				enginepb.ServerEventCode_SE_PLAYER_OWN_STATE)
			// SE_PONG, SE_LOGIN_REJECTED, SE_CELL_CHANGE, SE_DEBUG_INFO
			// are auto-registered by NewProtocol.

			// Game-only events (no engine counterpart).
			mmokit.RegisterServerEvent[gamepb.BankContentsMsg](e,
				gamepb.GameServerEventCode_GSE_BANK_CONTENTS)
			mmokit.RegisterServerEvent[gamepb.TransferResultMsg](e,
				gamepb.GameServerEventCode_GSE_TRANSFER_RESULT)
			mmokit.RegisterServerEvent[gamepb.EquipResultMsg](e,
				gamepb.GameServerEventCode_GSE_EQUIP_RESULT)
			mmokit.RegisterServerEvent[gamepb.DockingStateMsg](e,
				gamepb.GameServerEventCode_GSE_DOCKING_STATE)
			mmokit.RegisterEvent[game.DockingState]()
			mmokit.RegisterServerEvent[gamepb.DockedMsg](e,
				gamepb.GameServerEventCode_GSE_DOCKED)
			mmokit.RegisterEvent[game.Docked]()
			mmokit.RegisterServerEvent[gamepb.MapDataMsg](e,
				gamepb.GameServerEventCode_GSE_MAP_DATA)
			mmokit.RegisterServerEvent[gamepb.CurrencyUpdateMsg](e,
				gamepb.GameServerEventCode_GSE_CURRENCY_UPDATE)
			mmokit.RegisterEvent[game.CurrencyUpdate]()
		})
	// Capture the registry for closures (LoginRejected, op-router pushes) that
	// emit server events without access to *GameWorld.
	events := coordCfg.Protocol.(*mmokit.Protocol).ServerEventsRegistry()
	coordCfg.BindFlags()
	flag.Parse()

	// Parse roles upfront so init decisions (Postgres, marketplace) can branch
	// on them before Process.Build runs.
	roles, err := mmokit.ParseRoles(coordCfg.Mode)
	if err != nil {
		log.Fatalf("invalid --mode: %v", err)
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
	//   cell grid via AssignmentEngine; hosts need it for systems.
	//
	//   needsGameState  — load playerDB + wire opRouter + marketplace +
	//   register the world factory and systems via game.GameSetup. Runs
	//   for processes that actually host cells (RoleHost — in-process or
	//   remote).
	//
	// Pure standalone gateway skips both — it terminates WebSockets, routes
	// via cached topology, and never touches Postgres. Note: RoleService
	// also disqualifies as "pure gateway" because services like auth need
	// DB access — a `--mode=gateway,service` process MUST open Postgres
	// or the auth service kind fails to start.
	isPureGateway := roles.Has(mmokit.RoleGateway) &&
		!roles.Has(mmokit.RoleCoordinator) &&
		!roles.Has(mmokit.RoleHost) &&
		!roles.Has(mmokit.RoleService)
	needsGameConfig := !isPureGateway
	needsGameState := roles.Has(mmokit.RoleHost)

	coordCfg.Logger = gameLog

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
		store, err = mmokit.OpenPostgres(context.Background(), postgresURL,
			mmokit.WithExtraMigrations(auth.MigrationsFS(), ".", "auth"))
		if err != nil {
			log.Fatalf("failed to open postgres (%s): %v", postgresURL, err)
		}
		defer store.Close()
		log.Printf("postgres connected at %s", postgresURL)

		// Hand the pre-opened store to the engine so services-framework kinds
		// (auth, future) can access it via service.Context.DB without
		// re-opening Postgres.
		coordCfg.DBStore = store

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

		// Default spawn is 30 units east of the trade station — outside
		// DockRange (13.3) so the player sees the station and decides to
		// dock instead of being auto-pulled. SpawnResolver overrides this
		// for players with a saved location.
		coordCfg.DefaultSpawn = coords.Location{
			X: float32(gameCfg.StationCell.CellX)*coords.CellSize + game.StationLocalX + 30,
			Y: float32(gameCfg.StationCell.CellY)*coords.CellSize + game.StationLocalY,
		}
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
					events.Send(connMgr, connID, uint32(gamepb.GameServerEventCode_GSE_BANK_CONTENTS), &gamepb.BankContentsMsg{
						Items:        items,
						TotalMass:    pdata.BankTotalMass(),
						MaxMass:      gameCfg.BankMaxMass,
						CargoItems:   cargoItems,
						CargoMass:    pdata.CargoTotalMass(),
						MaxCargoMass: gameCfg.MaxCargo,
						Currencies:   currencies,
					})
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

	// Wire dynamic-cells topology-change broadcast before constructing the
	// coordinator so the callback pointer is set before splits/merges can
	// fire. The callback captures `coordinator` by closure and nil-checks
	// in case something triggers it before New returns.
	var coordinator *mmokit.Process
	if coordCfg.DynamicPartitioning == nil {
		coordCfg.DynamicPartitioning = mmokit.DefaultPartitionConfig()
	}
	// Topology refresh after split/merge is handled reactively by the
	// mmokit.NewDebugBroadcaster system (added in GameSetup) — it
	// recomputes per-player debug payload hashes each tick and re-sends
	// when the topology view changes. No explicit OnTopologyChanged
	// callback needed.
	coordCfg.OpRouter = opRouter

	// Game admin commands register on every process that has a console
	// (coordinator, host, node) so operators can dispatch from any pane.
	// RoutePlayerOwner handlers (tp, damage, etc.) resolve to the owning
	// host at dispatch time; RouteLocal handlers that need a local world
	// guard with resolver.AnyLocalWorld(). Handlers that dereference
	// playerDB (player.list, player.info) check for nil — pure-coordinator
	// panes have no playerDB so those return an unavailable error instead
	// of panicking. World-bound builtins (config, entity) still need a
	// live cell and are skipped on pure-coordinator.
	if needsGameConfig {
		coordCfg.OnConsoleReady = func(p *mmokit.Process, console *mmokit.Console) {
			var anyWorld *game.GameWorld
			for _, node := range p.Cells {
				gw := game.UnwrapGameWorld(node.World)
				if anyWorld == nil {
					anyWorld = gw
				}
			}

			if anyWorld != nil {
				console.RegisterBuiltins(mmokit.BuiltinOpts{
					Engine:      anyWorld.Engine(),
					Config:      anyWorld.Config,
					ConfigSave:  func() error { return game.SaveConfig(context.Background(), configRepo, anyWorld.Config) },
					ConfigReset: func() { *anyWorld.Config = game.DefaultGameConfig() },
					// ConfigOnChanged: re-apply equipment stats on all active players via
					// the config.apply_stats command dispatched to all hosts.
					ConfigOnChanged: func(_ string) {
						for _, node := range p.Cells {
							gw := game.UnwrapGameWorld(node.World)
							if gw == nil {
								continue
							}
							eng := gw.Engine()
							stage := node.Stage
							eng.SubmitLoopJob(func() error {
								gw.Players.ForEach(mmokit.StateActive, func(s *mmokit.PlayerSession) {
									entity := mmokit.EntityFromECS(stage, s.Entity)
									if entity.Alive() {
										gw.ApplyEquipmentStats(entity)
									}
								})
								return nil
							})
						}
					},
				})
			} else {
				log.Printf("console: no local cells — world-bound builtins unavailable (roles=%s)", p.Roles())
			}

			if err := gamecommands.RegisterAll(console.Registry(), p, playerDB, &gameCfg); err != nil {
				log.Printf("console: failed to register game commands: %v", err)
			}
		}
	}

	if needsGameState {
		coordCfg.World = game.WorldFactory(&gameCfg, playerDB, playerSessions)
	}

	coordinator = mmokit.New(coordCfg)

	if playerDB != nil {
		coordinator.SetHasPlayerDB(true)
		coordinator.SetPlayerDataLocator(playerDB.Locator())
	}

	if needsGameState {
		game.GameSetup(coordinator)
		game.InitDropTables()

		coordinator.SetSpawnResolver(func(username string) (coords.Location, bool) {
			pdata := playerDB.Get(username)
			if pdata == nil || !pdata.HasSave {
				return coords.Location{}, false
			}
			return coords.Location{
				X: float32(pdata.CellX)*coords.CellSize + pdata.X,
				Y: float32(pdata.CellY)*coords.CellSize + pdata.Y,
				// Facing + Tag not yet persisted; leave zero. Follow-up work.
			}, true
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

	if err := mmokit.RegisterAuthService(coordinator, mmokit.DefaultAuthOpts()); err != nil {
		log.Fatalf("RegisterAuthService: %v", err)
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
