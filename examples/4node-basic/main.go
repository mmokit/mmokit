package main

import (
	"context"
	"embed"
	"flag"
	"log"

	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/universe"

	"github.com/zenion/mmoserver/examples/4node-basic/migrations"
	"github.com/zenion/mmoserver/examples/4node-basic/services/echo"
)

//go:embed all:web/dist
var webDist embed.FS

func main() {
	postgresURL := flag.String("postgres-url", "",
		"Postgres connection URL (empty = skip DB, only works without service kinds requiring it)")

	cfg := mmokit.Config{
		InvariantMode:    universe.InvariantPanic,
		StrictNetIDIndex: true,
		CellsX:           CellsX,
		CellsY:           CellsY,
		CellSize:         CellSize,
		TickRate:         TickRate,
		AoIRadius:        AoIRadius,
		StaticFS:         webDist,
		StaticFSPrefix:   "web/dist",
		DefaultSpawn:     mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85},
		World:            NewWorld,
		LoginHandler: mmokit.HandleLogin(
			uint32(basicpb.ClientEventCode_BCE_LOGIN),
			func(m *basicpb.LoginMsg) (string, any, error) {
				name, err := mmokit.ValidateUsername(m.Name, 20)
				return name, nil, err
			},
		),
		OnConsoleReady: func(p *mmokit.Process, console *engine.Console) {
			if err := registerBotCommands(p, console.Registry()); err != nil {
				log.Printf("4node-basic: failed to register bot commands: %v", err)
			}
		},
		Protocol: mmokit.NewProtocol("basic").
			ClientEvents(func(e *mmokit.ClientEvents) {
				// BCE_LOGIN is handled by LoginHandler (bypasses InputRouter).
				mmokit.RegisterClientEvent[basicpb.LoginMsg](e, basicpb.ClientEventCode_BCE_LOGIN)
			}),
	}

	// Open Postgres up front when a URL is supplied so the echo service
	// (RequiresDB=true) finds DB ready at Build time. The 4node example
	// runs services optionally; without --postgres-url the example still
	// boots, just without echo.
	if *postgresURL != "" {
		store, err := mmokit.OpenPostgres(context.Background(), *postgresURL,
			mmokit.WithExtraMigrations(migrations.FS, "."))
		if err != nil {
			log.Fatalf("4node-basic: open postgres: %v", err)
		}
		cfg.PostgresURL = *postgresURL
		cfg.DBStore = store
	}

	mmo := mmokit.New(cfg)

	// Register the echo demo service. Engine instantiates it only when
	// the role set includes "service" AND --services= names "echo"; the
	// registration alone is harmless on processes that don't host it.
	if err := mmo.RegisterService(echo.Kind); err != nil {
		log.Fatalf("4node-basic: register echo service: %v", err)
	}

	mmo.AddSystem(mmokit.NewInputSystem(func(router *mmokit.InputRouter, gw *World) {
		mmokit.Handle(router, basicpb.ClientEventCode_BCE_MOVE_TARGET,
			mmokit.States(mmokit.StateActive),
			func(ctx *mmokit.InputContext, msg *basicpb.MoveTargetMsg) {
				if !gw.MoveTargetMap.HasAll(ctx.Entity) {
					return
				}
				mmokit.SetMoveTarget(gw.MoveTargetMap.Get(ctx.Entity), msg.TargetX, msg.TargetY)
			})
	}))
	mmo.AddSystem(mmokit.NewClickToMoveSystem())
	mmo.AddSystem(mmokit.NewPhysicsSystem())
	mmo.AddSystem(mmokit.NewSpatialSystem())
	mmo.AddSystem(mmokit.NewSystem(&DebugInfoSystem{}))
	mmo.AddSystem(mmokit.NewTopologyBroadcaster())
	mmo.AddSystem(mmokit.NewSystem(&BotSystem{}))
	mmo.AddSystem(mmokit.NewNetworkSystem())

	log.Printf("4node-basic: grid %dx%d cells, cell size %.0f, AoI %.0f", CellsX, CellsY, CellSize, AoIRadius)
	mmo.Start()
}
