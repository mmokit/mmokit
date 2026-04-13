package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"google.golang.org/protobuf/proto"

	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func main() {
	port := flag.Int("port", 8080, "HTTP server port")
	dumpSchema := flag.Bool("dump-schema", false, "Dump protocol schema JSON to stdout and exit")
	logFlag := flag.String("log", "", "comma-separated log categories/groups to enable (e.g. mesh,net:conn)")
	dynamicCells := flag.Bool("dynamic-cells", false, "enable dynamic cell partitioning (split/merge)")
	twoHosts := flag.Bool("two-hosts", false, "distribute cells across two in-process Host instances via gRPC loopback (dev/testing)")
	gatewayMode := flag.String("gateway-mode", "local-shortcut", "bridge mode when in multi-host: local-shortcut (default) or always-proxy")
	mode := flag.String("mode", "all-in-one", "operating mode: all-in-one | coordinator | node")
	controlListen := flag.String("control-listen", ":9100", "MeshControl listen addr (coordinator mode)")
	coordinatorAddr := flag.String("coordinator-addr", "", "MeshControl dial addr (node mode)")
	hostID := flag.String("host-id", "", "stable host identifier for node mode (empty = auto)")
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
		LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) {
			for _, data := range msgs {
				var evt enginepb.ClientEvent
				if err := proto.Unmarshal(data, &evt); err != nil {
					continue
				}
				if evt.Code == uint32(basicpb.ClientEventCode_BCE_LOGIN) {
					var login basicpb.LoginMsg
					if err := proto.Unmarshal(evt.Data, &login); err != nil {
						continue
					}
					name := strings.ToLower(strings.TrimSpace(login.Name))
					if name == "" || len(name) > 20 {
						continue
					}
					return name, nil, nil
				}
			}
			return "", nil, mmokit.ErrLoginPending
		},
		GatewayMode:     *gatewayMode,
		Mode:            *mode,
		ControlListen:   *controlListen,
		CoordinatorAddr: *coordinatorAddr,
		HostID:          *hostID,
	}
	if *dynamicCells {
		// OnTopologyChanged defaults to BroadcastCellTopology when nil.
		cfg.DynamicPartitioning = mmokit.DefaultPartitionConfig()
		log.Println("dynamic cell partitioning enabled")
	}
	if *twoHosts {
		cfg.TestHosts = []string{"host-a", "host-b"}
		log.Println("two-host mode enabled: cells distributed across host-a + host-b via gRPC loopback")
		if *dynamicCells {
			log.Println("WARNING: --two-hosts + --dynamic-cells is not fully supported in S3 (cellToHostMap is not updated on split/merge — see TODO(S4) in partition.go)")
		}
	}
	coord := mmokit.NewCoordinator(cfg)
	coord.SetWorld(NewWorld)
	coord.SetPlayerRouter(func(username string) string {
		return coord.NodeAtPosition(0, 0)
	})

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

	// Node mode doesn't accept client connections (per the S4 scope
	// decision — multi-process playable gameplay is deferred to S6).
	// Skip the HTTP listener entirely so multiple nodes can run on the
	// same host alongside a coordinator without port conflicts.
	if *mode != "node" {
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
	} else {
		log.Printf("4node-basic node starting (host-id=%s, coordinator=%s) — no HTTP listener", *hostID, *coordinatorAddr)
		log.Printf("grid: %dx%d nodes, cell size: %.0f, AoI: %.0f", CellsX, CellsY, CellSize, AoIRadius)
	}

	coord.Start(context.Background())
}
