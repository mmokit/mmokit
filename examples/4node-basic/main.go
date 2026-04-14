package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"log"
	"time"

	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
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
	synthBots := flag.Int("synth-bots", 0,
		"S7 demo: spawn N AI bot entities into one cell to drive auto-split (0 = off)")
	synthBotsCell := flag.String("synth-bots-cell", "0,0",
		"S7 demo: target cell coordinate 'x,y' at depth 0 for --synth-bots")
	splitSustain := flag.Duration("split-sustain", 0,
		"S7 demo: override PartitionConfig.SplitSustain (0 = engine default 30s)")
	flag.Parse()

	if *dumpSchema {
		dumpProtocolSchema()
		return
	}

	// Configure synthetic load if --synth-bots > 0. Parsing the cell
	// coord happens up here so a bad value fails loud before Start.
	if *synthBots > 0 {
		var cx, cy int32
		if _, err := fmt.Sscanf(*synthBotsCell, "%d,%d", &cx, &cy); err != nil {
			log.Fatalf("invalid --synth-bots-cell %q: %v", *synthBotsCell, err)
		}
		botConfig.Count = *synthBots
		botConfig.Cell = mmokit.CellID{X: cx, Y: cy, Depth: 0}

		// Install a demo-tuned PartitionConfig. We want the auto-split
		// to fire reliably within ~30-60s of sustained bot traffic, so:
		//   * MetricFunc is entity-heavy — each bot contributes ~1.5%
		//     so the 0.75 threshold lands around ~50 entities.
		//   * EvalInterval is 1s instead of 5s so the monitor reacts
		//     quickly enough for a live demo.
		//   * SplitSustain drops from 30s to 5s (override via flag).
		pc := mmokit.DefaultPartitionConfig()
		pc.EvalInterval = 1 * time.Second
		pc.SplitSustain = 5 * time.Second
		if *splitSustain > 0 {
			pc.SplitSustain = *splitSustain
		}
		pc.MetricFunc = func(snap metrics.LoadSnapshot) float64 {
			// 50-bot target: 50/67 ≈ 0.75. Plays nice with the stock
			// 0.75 SplitThreshold and gives merge breathing room
			// (merge threshold 0.20 → ~13 entities).
			return float64(snap.Entities.Real) / 67.0
		}
		cfg.DynamicPartitioning = pc
		log.Printf("4node-basic: S7 demo mode — %d bots into cell (%d,%d), split_sustain=%s",
			*synthBots, cx, cy, pc.SplitSustain)
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
	coord.AddSystem("Bots", func() mmokit.System { return &BotSystem{} })
	coord.AddSystem("Network", mmokit.NewNetworkSystem())

	log.Printf("4node-basic: grid %dx%d cells, cell size %.0f, AoI %.0f", CellsX, CellsY, CellSize, AoIRadius)
	coord.Start(context.Background())
}
