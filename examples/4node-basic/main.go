package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func main() {
	port := flag.Int("port", 8080, "HTTP server port")
	dumpSchema := flag.Bool("dump-schema", false, "Dump protocol schema JSON to stdout and exit")
	logFlag := flag.String("log", "", "comma-separated log categories/groups to enable (e.g. mesh,net:conn)")
	dynamicCells := flag.Bool("dynamic-cells", false, "enable dynamic cell partitioning (split/merge)")
	flag.Parse()

	if *dumpSchema {
		dumpProtocolSchema()
		return
	}

	cfg := mmokit.Config{
		CellsX:        CellsX,
		CellsY:        CellsY,
		CellSize:      CellSize,
		TickRate:      TickRate,
		AoIRadius:     AoIRadius,
		DebugTopology: true,
		LogCategories: *logFlag,
	}
	if *dynamicCells {
		// OnTopologyChanged defaults to BroadcastCellTopology when nil.
		cfg.DynamicPartitioning = mmokit.DefaultPartitionConfig()
		log.Println("dynamic cell partitioning enabled")
	}
	coord := mmokit.NewCoordinator(cfg)
	coord.SetWorld(NewWorld)

	// Register systems in order of execution.
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

	// Network system auto-discovers replicators from registered EntityKindDefs.
	coord.AddSystem("Network", mmokit.NewNetworkSystem())

	// Build coordinator first so /metrics route is registered on the ConnManager.
	coord.Build()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", coord.ConnManager().HandleWebSocket)
	mux.Handle("/metrics", coord.MetricsHandler())
	mux.Handle("/", http.FileServer(http.Dir("web")))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("4node-basic starting on http://localhost%s", addr)
	log.Printf("grid: %dx%d nodes, cell size: %.0f, AoI: %.0f", CellsX, CellsY, CellSize, AoIRadius)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("FATAL: http server: %v", err)
			os.Exit(1)
		}
	}()

	coord.Start(context.Background())
}
