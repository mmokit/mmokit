package main

import (
	"embed"
	"log"

	"github.com/mlange-42/ark/ecs"
	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/mmokit"

	"github.com/zenion/mmoserver/examples/4node-basic/services/echo"
)

//go:embed all:web/dist
var webDist embed.FS

func main() {
	mmo := mmokit.New(mmokit.Config{
		InvariantMode:    mmokit.InvariantPanic,
		StrictNetIDIndex: true,
		CellsX:           CellsX,
		CellsY:           CellsY,
		CellSize:         CellSize,
		TickRate:         TickRate,
		AoIRadius:        AoIRadius,
		StaticFS:         webDist,
		StaticFSPrefix:   "web/dist",
		DefaultSpawn:     mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85},
		// PostgresURL empty → engine auto-opens the local docker-compose
		// default. Override via $POSTGRES_URL env var picked up by main
		// before this struct literal if the dev DB lives elsewhere.
		LoginHandler: mmokit.HandleLogin(
			uint32(basicpb.ClientEventCode_BCE_LOGIN),
			func(m *basicpb.LoginMsg) (string, any, error) {
				name, err := mmokit.ValidateUsername(m.Name, 20)
				return name, nil, err
			},
		),
		OnConsoleReady: func(p *mmokit.Process, console *mmokit.Console) {
			if err := registerBotCommands(p, console.Registry()); err != nil {
				log.Printf("4node-basic: failed to register bot commands: %v", err)
			}
			if err := mmokit.RegisterDebugCommands(p); err != nil {
				log.Printf("4node-basic: failed to register debug commands: %v", err)
			}
		},
		Protocol: mmokit.NewProtocol("basic").
			ClientEvents(func(e *mmokit.ClientEvents) {
				// BCE_LOGIN is handled by LoginHandler (bypasses InputRouter).
				mmokit.RegisterClientEvent[basicpb.LoginMsg](e, basicpb.ClientEventCode_BCE_LOGIN)
			}),
	})

	mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player")
	mmokit.RegisterKind[BotComponents](mmo, KindBot, "Bot")

	mmo.OnPlayerJoin(func(s *mmokit.PlayerSession, stage *mmokit.Stage) {
		// Demo auto-grant: every player gets the topology overlay by
		// default so the cell-boundary debug rendering Just Works in
		// the example. Production deployments should drop this line
		// and grant per-user via `debug grant <user> topology`.
		s.DebugFlags |= mmokit.DebugTopology
		stage.SpawnPlayer(s,
			mmokit.WithCollider(PlayerRadius),
			mmokit.WithEntityKind(KindPlayer),
			mmokit.Init(func(c *PlayerComponents) {
				c.Name.Name = s.Username
			}),
		)
	})

	// Register the echo demo service. Engine instantiates it only when
	// the role set includes "service" AND --services= names "echo"; the
	// registration alone is harmless on processes that don't host it.
	if err := mmo.RegisterService(echo.Kind); err != nil {
		log.Fatalf("4node-basic: register echo service: %v", err)
	}

	mmo.AddSystem(mmokit.NewInputSystem(func(router *mmokit.InputRouter, gw *mmokit.Stage) {
		moveTargetMap := ecs.NewMap1[mmokit.MoveTarget](gw.ECSWorld())
		mmokit.Handle(router, basicpb.ClientEventCode_BCE_MOVE_TARGET,
			mmokit.States(mmokit.StateActive),
			func(ctx *mmokit.InputContext, msg *basicpb.MoveTargetMsg) {
				if !moveTargetMap.HasAll(ctx.Entity) {
					return
				}
				mmokit.SetMoveTarget(moveTargetMap.Get(ctx.Entity), msg.TargetX, msg.TargetY)
			})
	}))
	mmo.AddSystem(mmokit.NewClickToMoveSystem())
	mmo.AddSystem(mmokit.NewPhysicsSystem())
	mmo.AddSystem(mmokit.NewSpatialSystem())
	mmo.AddSystem(mmokit.NewDebugBroadcaster())
	mmo.AddSystem(mmokit.NewSystem(&BotSystem{}))
	mmo.AddSystem(mmokit.NewNetworkSystem())

	log.Printf("4node-basic: grid %dx%d cells, cell size %.0f, AoI %.0f", CellsX, CellsY, CellSize, AoIRadius)
	mmo.Start()
}
