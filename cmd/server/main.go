package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
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
		ClientEvents(func(_ *mmokit.ClientEvents) {
			// CE_PING is auto-registered by NewProtocol. Login moved to
			// pkg/auth's typed op channel (AUTH_OPCODE_LOGIN) and no
			// longer rides this CE_* event registry. Typed client-input
			// messages (RESPAWN, BANK, DOCK, UNDOCK, etc.) are registered
			// via mmokit.HandleClient[T] and exposed through the
			// ClientInputTypes schema, not the ClientEvents registry.
		}).
		ServerEvents(func(e *mmokit.ServerEvents) {
			// All game-specific server events ride the typed
			// reflection-codec channel after Plan 1 Phase 7. SE_PLAYER_SPAWNED
			// keeps an engine-default proto registration (enginepb.SpawnedMsg)
			// for SDK schema visibility; the typed PlayerSpawned below is the
			// payload the space client actually consumes.
			_ = e
			mmokit.RegisterEvent[game.PlayerSpawned]()
			mmokit.RegisterEvent[game.PlayerDied]()
			mmokit.RegisterEvent[game.PlayerOwnState]()
			mmokit.RegisterEvent[game.BankContents]()
			mmokit.RegisterEvent[game.TransferResult]()
			mmokit.RegisterEvent[game.EquipResult]()
			mmokit.RegisterEvent[game.DockingState]()
			mmokit.RegisterEvent[game.Docked]()
			mmokit.RegisterEvent[game.MapData]()
			mmokit.RegisterEvent[game.CurrencyUpdate]()
			mmokit.RegisterEvent[marketplace.MarketTradeNotification]()
		})
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
	//
	// TODO(events-channel-redesign Phase 5): migrate the inbound CE_PING
	// envelope to a typed client-input frame (channel 0x02). The
	// EventInterceptor currently only sees channel 0x00; switching Ping
	// would require either a parallel ClientInputInterceptor or routing
	// channel 0x02 through this hook with a typeID dispatch. Outbound
	// Pong is already typed (mmokit.BuildTypedEventFrame[Pong]).
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
		// Typed Pong: bypasses the engine ConnMgr (no Stage exists yet for
		// pre-login conns) by framing inline and writing directly to conn.
		conn.Send(mmokit.BuildTypedEventFrame(&mmokit.Pong{
			ClientTime: ping.ClientTime,
			ServerTime: time.Now().UnixMilli(),
		}))
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
		opRouter = mmokit.NewOpRouter(connMgr, playerSessions)

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
					var items []game.InventoryItem
					for id, qty := range pdata.Bank {
						if qty > 0 {
							items = append(items, game.InventoryItem{ItemID: id, Quantity: qty})
						}
					}
					var cargoItems []game.InventoryItem
					for id, qty := range pdata.Cargo {
						if qty > 0 {
							cargoItems = append(cargoItems, game.InventoryItem{ItemID: id, Quantity: qty})
						}
					}
					var currencies []game.CurrencyBalance
					for curID, bal := range pdata.Currencies {
						if bal != 0 {
							currencies = append(currencies, game.CurrencyBalance{CurrencyID: curID, Balance: bal})
						}
					}
					connMgr.SendReliable(connID, mmokit.BuildTypedEventFrame(&game.BankContents{
						Items:        items,
						TotalMass:    pdata.BankTotalMass(),
						MaxMass:      gameCfg.BankMaxMass,
						CargoItems:   cargoItems,
						CargoMass:    pdata.CargoTotalMass(),
						MaxCargoMass: gameCfg.MaxCargo,
						Currencies:   currencies,
					}))
				},
			},
			marketCfg,
			gameCfg.SettlementCurrencyID,
			gameLog,
			marketRepo,
			func(username string, msg *marketplace.MarketTradeNotification) {
				connID := opRouter.ConnIDForUsername(username)
				if connID == 0 {
					return
				}
				connMgr.SendReliable(connID, mmokit.BuildTypedEventFrame(msg))
			},
		)
		if err := marketSvc.LoadAll(context.Background()); err != nil {
			log.Fatalf("failed to load marketplace data: %v", err)
		}
		marketplace.RegisterHandlers(marketSvc, 1)
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
