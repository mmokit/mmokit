package main

import (
	"context"
	"embed"
	"flag"
	"log"

	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/mmokit"
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
		CellsX:         CellsX,
		CellsY:         CellsY,
		CellSize:       CellSize,
		TickRate:       TickRate,
		AoIRadius:      AoIRadius,
		StaticFS:       webDist,
		StaticFSPrefix: "web/dist",
		LoginHandler: mmokit.HandleLogin(
			uint32(basicpb.ClientEventCode_BCE_LOGIN),
			func(m *basicpb.LoginMsg) (string, any, error) {
				name, err := mmokit.ValidateUsername(m.Name, 20)
				return name, nil, err
			},
		),
	}
	cfg.BindFlags()
	dumpSchema := flag.Bool("dump-schema", false, "Dump protocol schema JSON to stdout and exit")
	flag.Parse()

	if *dumpSchema {
		dumpProtocolSchema()
		return
	}

	coord := mmokit.NewCoordinator(cfg)
	coord.SetWorld(NewWorld)
	coord.SetPlayerRouter(mmokit.DefaultPlayerRouter(coord, 0, 0))

	coord.AddSystem("Input", mmokit.NewInputSystem(func(router *mmokit.InputRouter, gw *World) {
		mmokit.Handle(router, basicpb.ClientEventCode_BCE_MOVE_TARGET,
			mmokit.States(mmokit.StateActive),
			func(ctx *mmokit.InputContext, msg *basicpb.MoveTargetMsg) {
				if !gw.MoveTargetMap.HasAll(ctx.Entity) {
					return
				}
				mmokit.SetMoveTarget(gw.MoveTargetMap.Get(ctx.Entity), msg.TargetX, msg.TargetY)
			})
	}))
	coord.AddSystem("ClickToMove", mmokit.NewClickToMoveSystem())
	coord.AddSystem("Physics", mmokit.NewPhysicsSystem())
	coord.AddSystem("DeadReckoning", mmokit.NewDeadReckoningSystem())
	coord.AddSystem("Spatial", mmokit.NewSpatialSystem())
	coord.AddSystem("DebugInfo", func() mmokit.System { return &DebugInfoSystem{} })
	coord.AddSystem("Network", mmokit.NewNetworkSystem())

	log.Printf("4node-basic: grid %dx%d cells, cell size %.0f, AoI %.0f", CellsX, CellsY, CellSize, AoIRadius)
	coord.Start(context.Background())
}
