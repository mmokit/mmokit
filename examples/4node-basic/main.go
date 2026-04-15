package main

import (
	"context"
	"embed"
	"flag"
	"log"
	"time"

	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/metrics"
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
	splitSustain := flag.Duration("split-sustain", 5*time.Second,
		"PartitionConfig.SplitSustain — how long a cell must stay overloaded before auto-splitting (demo default 5s)")
	flag.Parse()

	if *dumpSchema {
		dumpProtocolSchema()
		return
	}

	// Install a demo-tuned PartitionConfig. This binary is the S7
	// visualization harness so the defaults lean toward "split fires
	// quickly enough for a live demo":
	//   * MetricFunc is entity-heavy — each bot contributes ~1.5% so
	//     the 0.75 threshold lands around ~50 entities.
	//   * EvalInterval is 1s instead of 5s so the monitor reacts
	//     quickly.
	//   * SplitSustain drops from 30s to 5s (override via flag).
	// Bots are spawned on demand via the interactive `bot spawn`
	// console command — see bot_console.go.
	pc := mmokit.DefaultPartitionConfig()
	pc.EvalInterval = 1 * time.Second
	pc.SplitSustain = *splitSustain
	pc.MetricFunc = func(snap metrics.LoadSnapshot) float64 {
		return float64(snap.Entities.Real) / 67.0
	}
	cfg.DynamicPartitioning = pc
	log.Printf("4node-basic: S7 demo mode — split_sustain=%s; type `bot spawn 60` in the console to start the demo",
		pc.SplitSustain)

	coord := mmokit.NewCoordinator(cfg)
	coord.SetWorld(NewWorld)
	coord.SetPlayerRouter(mmokit.DefaultPlayerRouter(coord, 0, 0))
	coord.OnConsoleReady(func(console *engine.Console) {
		registerBotCommands(coord, console)
	})

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
	coord.AddSystem("Bots", func() mmokit.System { return &BotSystem{} })
	coord.AddSystem("Network", mmokit.NewNetworkSystem())

	log.Printf("4node-basic: grid %dx%d cells, cell size %.0f, AoI %.0f", CellsX, CellsY, CellSize, AoIRadius)
	coord.Start(context.Background())
}
