package universe

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// ═══════════════════════════════════════════════════════════════════════════
// TestPerfDistributedFanOut verifies that `perf` at the coordinator returns
// snapshots from every registered remote host when dispatched over MeshControl.
//
// Setup mirrors the S7 harness pattern: 2-host in-process Coordinator with a
// 2x2 cell grid (each host owns 2 cells via round-robin assignment). We let
// cells run long enough for TickProfile to accumulate a few samples, then
// invoke `perf` via the real Dispatcher to exercise the full
// RouteLocal → InvokeInternal(RouteAllHosts) → MeshControl fan-out path.
//
// Assertions:
//  1. Default fan-out includes rows from all hosts.
//  2. --host drilldown includes rows only from the target host and excludes
//     the other host's ID.
// ═══════════════════════════════════════════════════════════════════════════

func TestPerfDistributedFanOut(t *testing.T) {
	coords.SetCellSize(1024)

	cfg := Config{
		CellsX:              2,
		CellsY:              2,
		CellSize:            1024,
		TestHosts:           []string{"host-a", "host-b"},
		Headless:            true,
		ConnManager:         net.NewConnManager(),
		Logger:              logger.New(),
		DynamicPartitioning: DisabledPartitionConfig(),
		LoginHandler:        func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
	}
	coord := NewCoordinator(cfg)
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()

	// In production, registerPerfBuiltins is called by startConsole. With
	// Headless=true the console doesn't start, so wire the verbs manually —
	// the same pattern used by wireInspectionBuiltins in builtins_inspection_test.go.
	if err := registerPerfBuiltins(coord.registry, coord.dispatcher, coord); err != nil {
		t.Fatalf("registerPerfBuiltins: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		coord.Shutdown()
	})
	for _, cell := range coord.Cells {
		go cell.Run(ctx)
	}

	// Let cells accumulate some TickProfile samples (20Hz × 250ms ≈ 5 ticks).
	time.Sleep(250 * time.Millisecond)

	cmdCtxPerf, cancelPerf := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelPerf()

	caller := cmdsys.NewOperatorIdentity("test-perf")
	disp := coord.CmdDispatcher()

	// ── Assertion 1: default fan-out returns rows from all hosts. ────────────
	res, err := disp.Invoke(cmdCtxPerf, caller, "perf", perfArgs{})
	if err != nil {
		t.Fatalf("perf: %v", err)
	}
	// perf is RouteLocal — always exactly one PerTarget entry.
	if len(res.PerTarget) != 1 {
		t.Fatalf("expected 1 PerTarget, got %d: %+v", len(res.PerTarget), res.PerTarget)
	}
	if !res.PerTarget[0].OK {
		t.Fatalf("perf frontend failed: %+v", res.PerTarget[0])
	}
	out, ok := res.PerTarget[0].Result.(perfResult)
	if !ok {
		t.Fatalf("result type = %T, want perfResult", res.PerTarget[0].Result)
	}

	for _, hostID := range []string{"host-a", "host-b"} {
		if !strings.Contains(out.Output, hostID) {
			t.Errorf("default fan-out output missing host %q\n--- output ---\n%s", hostID, out.Output)
		}
	}

	// ── Assertion 2: --host drilldown scopes to target host only. ───────────
	res2, err := disp.Invoke(cmdCtxPerf, caller, "perf", perfArgs{HostID: "host-a"})
	if err != nil {
		t.Fatalf("perf --host host-a: %v", err)
	}
	if len(res2.PerTarget) != 1 || !res2.PerTarget[0].OK {
		t.Fatalf("perf --host host-a frontend failed: %+v", res2.PerTarget)
	}
	out2, ok := res2.PerTarget[0].Result.(perfResult)
	if !ok {
		t.Fatalf("result type = %T, want perfResult", res2.PerTarget[0].Result)
	}

	if strings.Contains(out2.Output, "host-b") {
		t.Errorf("--host host-a output included host-b:\n%s", out2.Output)
	}
	// The drilldown must still contain host-a's data (or at least not be empty
	// or an error). "no cells reporting" would indicate the filter is broken.
	if strings.Contains(out2.Output, "no cells reporting") {
		t.Errorf("--host host-a: no cells reporting — HostID filter dropped all rows:\n%s", out2.Output)
	}
}
