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
		CellsX:              CellsX,
		CellsY:              CellsY,
		CellSize:            CellSize,
		TickRate:            TickRate,
		AoIRadius:           AoIRadius,
		StaticFS:            webDist,
		StaticFSPrefix:      "web/dist",
		DefaultSpawn:        mmokit.WorldCenterOfCell(0, 0),
		DynamicPartitioning: mmokit.DisabledPartitionConfig(),
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
	partitionDemo := flag.Bool("partition-demo", false,
		"enable demo-tuned auto-split config (5s sustain, 1s eval, entity-weighted metric). Default: manual split/merge only via console commands.")
	flag.Parse()

	if *dumpSchema {
		dumpProtocolSchema()
		return
	}

	// Partition policy: the Config literal above defaults to manual-only so
	// operators drive splits/merges via `cell split` / `cell merge` console
	// commands. --partition-demo overrides with a demo-tuned auto-split
	// config that fires quickly enough for a live browser demo.
	if *partitionDemo {
		pc := mmokit.DefaultPartitionConfig()
		pc.EvalInterval = 1 * time.Second
		pc.SplitSustain = 5 * time.Second
		pc.MetricFunc = func(snap metrics.LoadSnapshot) float64 {
			// Entity-heavy metric — each bot contributes ~1.5% so the
			// 0.75 threshold lands around ~50 entities.
			return float64(snap.Entities.Real) / 67.0
		}
		cfg.DynamicPartitioning = pc
		log.Print("4node-basic: --partition-demo enabled — auto-split fires at ~50 entities after 5s sustain")
	}

	mmo := mmokit.New(cfg)
	mmo.SetWorld(NewWorld)
	mmo.OnConsoleReady(func(console *engine.Console) {
		if err := registerBotCommands(mmo, console.Registry()); err != nil {
			log.Printf("4node-basic: failed to register bot commands: %v", err)
		}
	})

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
	mmo.AddSystem("DeadReckoning", mmokit.NewDeadReckoningSystem())
	mmo.AddSystem("Spatial", mmokit.NewSpatialSystem())
	mmo.AddSystem("DebugInfo", func() mmokit.System { return &DebugInfoSystem{} })
	mmo.AddSystem("Bots", func() mmokit.System { return &BotSystem{} })
	mmo.AddSystem("Network", mmokit.NewNetworkSystem())

	log.Printf("4node-basic: grid %dx%d cells, cell size %.0f, AoI %.0f", CellsX, CellsY, CellSize, AoIRadius)
	mmo.Start(context.Background())
}
