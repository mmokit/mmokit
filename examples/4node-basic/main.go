package main

import (
	"embed"
	"log"

	"github.com/mlange-42/ark/ecs"
	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/mmokit"

	"github.com/zenion/mmoserver/examples/4node-basic/migrations"
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
		ExtraMigrations: []mmokit.ExtraMigrationSource{
			{FS: migrations.FS},
		},
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
		},
		Protocol: mmokit.NewProtocol("basic").
			ClientEvents(func(e *mmokit.ClientEvents) {
				// BCE_LOGIN is handled by LoginHandler (bypasses InputRouter).
				mmokit.RegisterClientEvent[basicpb.LoginMsg](e, basicpb.ClientEventCode_BCE_LOGIN)
			}),
	})

	playerBindings := mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500, IncludeMeshState: true}
	mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player", playerBindings,
		mmokit.Field[PlayerName](),
		mmokit.Field[DebugInfo](),
		mmokit.Field[mmokit.MoveTarget](),
	)
	mmokit.RegisterKind[BotComponents](mmo, KindBot, "Bot", playerBindings,
		mmokit.Field[PlayerName](),
		mmokit.Field[DebugInfo](),
		mmokit.Field[mmokit.MoveTarget](),
		mmokit.Field[mmokit.Position](),
		mmokit.Field[BotBehavior](),
	)

	mmo.OnPlayerJoin(func(s *mmokit.PlayerSession, stage *mmokit.Stage) {
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
	mmo.AddSystem(mmokit.NewSystem(&DebugInfoSystem{}))
	mmo.AddSystem(mmokit.NewTopologyBroadcaster())
	mmo.AddSystem(mmokit.NewSystem(&BotSystem{}))
	mmo.AddSystem(mmokit.NewNetworkSystem())

	log.Printf("4node-basic: grid %dx%d cells, cell size %.0f, AoI %.0f", CellsX, CellsY, CellSize, AoIRadius)
	mmo.Start()
}
