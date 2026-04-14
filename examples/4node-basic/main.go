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
	mode := flag.String("mode", "all-in-one", "role set: all-in-one | coordinator[,gateway][,host] | node | gateway")
	controlListen := flag.String("control-listen", ":9100", "MeshControl listen addr (coordinator role)")
	coordinatorAddr := flag.String("coordinator-addr", "", "MeshControl dial addr (node/standalone-gateway roles)")
	hostID := flag.String("host-id", "", "stable host identifier for node mode (empty = auto)")
	gatewayID := flag.String("gateway-id", "", "stable gateway identifier for gateway role (empty = auto)")
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
		GatewayID:       *gatewayID,
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
	// Routing needs local cells to exist. Standalone-gateway processes
	// (no RoleHost) defer to cached PeerList topology via empty string.
	roles, err := mmokit.ParseRoles(*mode)
	if err != nil {
		log.Fatalf("invalid --mode: %v", err)
	}
	if roles.Has(mmokit.RoleHost) {
		coord.SetPlayerRouter(func(username string) string {
			return coord.NodeAtPosition(0, 0)
		})
	} else {
		coord.SetPlayerRouter(func(username string) string {
			return ""
		})
	}

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

	// Only processes with the gateway role terminate client connections.
	// Pure coordinator / pure node processes skip the HTTP listener so they
	// can coexist with a standalone gateway on the same host without port
	// conflicts on :8080 / --port.
	if coord.ServesClients() {
		mux := http.NewServeMux()
		mux.HandleFunc("/ws", coord.ConnManager().HandleWebSocket)
		mux.Handle("/metrics", coord.MetricsHandler())
		mux.Handle("/", http.FileServer(http.Dir("web")))

		addr := fmt.Sprintf(":%d", *port)
		if roles.Has(mmokit.RoleCoordinator) {
			log.Printf("4node-basic starting on http://localhost%s (roles=%s)", addr, coord.Roles())
			log.Printf("grid: %dx%d cells, cell size: %.0f, AoI: %.0f", CellsX, CellsY, CellSize, AoIRadius)
		} else {
			log.Printf("4node-basic standalone gateway starting on http://localhost%s (gateway-id=%s, coordinator=%s)", addr, *gatewayID, *coordinatorAddr)
		}

		go func() {
			if err := http.ListenAndServe(addr, mux); err != nil {
				log.Printf("FATAL: http server: %v", err)
				os.Exit(1)
			}
		}()
	} else {
		log.Printf("4node-basic starting with roles=%s — no HTTP listener", coord.Roles())
		log.Printf("grid: %dx%d cells, cell size: %.0f, AoI: %.0f", CellsX, CellsY, CellSize, AoIRadius)
	}

	coord.Start(context.Background())
}
