package main

import (
	"embed"
	"log"

	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/universe"
)

//go:embed all:web/dist
var webDist embed.FS

func main() {
	mmo := mmokit.New(mmokit.Config{
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
	})

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
