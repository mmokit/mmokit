package main

import (
	"context"
	"embed"
	"log"

	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/universe"
)

// webDist is the built Vite output (web/dist) embedded into the binary
// at compile time. Run `bun run build` in the web/ directory before
// `go build` so this directory exists — the justfile's `build` recipe
// handles that automatically.
//
//go:embed all:web/dist
var webDist embed.FS

func main() {
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
		LoginHandler: mmokit.HandleLogin(
			uint32(basicpb.ClientEventCode_BCE_LOGIN),
			func(m *basicpb.LoginMsg) (string, any, error) {
				name, err := mmokit.ValidateUsername(m.Name, 20)
				return name, nil, err
			},
		),
		World: NewWorld,
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

	mmo := mmokit.New(cfg)

	mmo.AddSystem("Input", mmokit.NewInputSystem(func(router *mmokit.InputRouter, gw *World) {
		mmokit.Handle(router, basicpb.ClientEventCode_BCE_MOVE_TARGET,
			mmokit.States(mmokit.StateActive),
			func(ctx *mmokit.InputContext, msg *basicpb.MoveTargetMsg) {
				if !gw.MoveTargetMap.HasAll(ctx.Entity) {
					return
				}
				mmokit.SetMoveTarget(gw.MoveTargetMap.Get(ctx.Entity), msg.TargetX, msg.TargetY)
			})
	}))
	mmo.AddSystem("ClickToMove", mmokit.NewClickToMoveSystem())
	mmo.AddSystem("Physics", mmokit.NewPhysicsSystem())
	mmo.AddSystem("Spatial", mmokit.NewSpatialSystem())
	mmo.AddSystem("DebugInfo", func() mmokit.System { return &DebugInfoSystem{} })
	mmo.AddSystem("Bots", func() mmokit.System { return &BotSystem{} })
	mmo.AddSystem("Network", mmokit.NewNetworkSystem())

	log.Printf("4node-basic: grid %dx%d cells, cell size %.0f, AoI %.0f", CellsX, CellsY, CellSize, AoIRadius)
	mmo.Start(context.Background())
}
