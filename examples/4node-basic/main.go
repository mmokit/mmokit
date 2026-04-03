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
	flag.Parse()

	if *dumpSchema {
		dumpProtocolSchema()
		return
	}

	coord := mmokit.NewCoordinator(mmokit.Config{
		CellsX:       MeshCellsX,
		CellsY:       MeshCellsY,
		CellSize:     CellSize,
		TickRate:     TickRate,
		AoIRadius:    AoIRadius,
		LogCategories: *logFlag,
		WorldFactory: func(base *mmokit.WorldBase) mmokit.GameWorld {
			return NewBasicWorld(base)
		},
	})

	// Register systems in order of execution.
	coord.AddSystem("Input", mmokit.NewInputSystem(func(router *mmokit.InputRouter, gw *BasicWorld) {
		mmokit.Handle(router, basicpb.BasicClientEventCode_BCE_MOVE_TARGET,
			mmokit.States(mmokit.StateActive),
			func(ctx *mmokit.InputContext, msg *basicpb.BasicMoveTargetMsg) {
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

	coord.AddSystem("Network", mmokit.NewNetworkSystem(func(cfg *mmokit.ReplicationConfig, gw *BasicWorld) {
		cfg.Replicators = setupReplication(gw.ECSWorld())
		cfg.AoIRadius = AoIRadius
	}))

	cm := coord.ConnManager()

	// Build coordinator first so /metrics route is registered on the ConnManager.
	coord.Build()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", cm.HandleWebSocket)
	mux.Handle("/metrics", coord.MetricsHandler())
	mux.Handle("/", http.FileServer(http.Dir("web")))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("4node-basic starting on http://localhost%s", addr)
	log.Printf("grid: %dx%d nodes, cell size: %.0f, AoI: %.0f", MeshCellsX, MeshCellsY, CellSize, AoIRadius)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("FATAL: http server: %v", err)
			os.Exit(1)
		}
	}()

	coord.Start(context.Background())
}
