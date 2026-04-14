package universe

import (
	"context"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// ═══════════════════════════════════════════════════════════════════════════
// S7 T7 — graceful shutdown integration test
//
// TestS7GracefulShutdown stands up a 3-host in-process Coordinator with a
// 2x2 grid (4 cells — round-robin lands 2 on host-a, 1 on host-b, 1 on
// host-c) and drives the coordinator-side drain path directly via
// Coordinator.drainHost. This exercises the same logic that runs under
// meshControlServer.handleGracefulLeave in a live cluster, without needing
// to spin up a real gRPC control plane (the all-preset TestHosts fixture
// has controlClient==nil by design, so the node-side Shutdown path is not
// reachable here — drainHost is the shared entry point).
//
// Assertions after drain:
//   - every cell that was on host-a now maps to a DIFFERENT host in
//     cellToHostMap
//   - no cell remains without an owner in cellToHostMap
//   - cellToHostMap never points at host-a for any cell
//
// Per the T7 task notes, source-host teardown is deferred to T9 — the
// leaving host's Host.Cells map still has stale entries after migrate
// commit. The test deliberately does NOT assert on host-a's Host.Cells
// post-drain; the orphaned goroutines are cleaned up by the node's local
// `for _, node := range c.Cells { node.Shutdown() }` loop in real usage.
// ═══════════════════════════════════════════════════════════════════════════

func TestS7GracefulShutdown(t *testing.T) {
	coords.SetCellSize(1024)

	cfg := Config{
		CellsX:       2,
		CellsY:       2,
		CellSize:     1024,
		TestHosts:    []string{"host-a", "host-b", "host-c"},
		Headless:     true,
		ConnManager:  net.NewConnManager(),
		Logger:       logger.New(),
		LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
	}
	coord := NewCoordinator(cfg)
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		coord.Shutdown()
	})
	for _, cell := range coord.Cells {
		go cell.Run(ctx)
	}
	time.Sleep(20 * time.Millisecond)

	// Snapshot pre-drain ownership so we can diff after drainHost runs.
	// Round-robin assignment across [host-a, host-b, host-c] for the 2x2
	// grid in CellsY-outer, CellsX-inner iteration order gives:
	//   cell_0_0 -> host-a
	//   cell_1_0 -> host-b
	//   cell_0_1 -> host-c
	//   cell_1_1 -> host-a
	// so host-a owns 2 cells and is the most interesting drain source.
	preOwnership := make(map[string]string)
	coord.mu.RLock()
	for k, v := range coord.cellToHostMap {
		preOwnership[k] = v
	}
	coord.mu.RUnlock()

	var hostACells []string
	for cellKey, hostID := range preOwnership {
		if hostID == "host-a" {
			hostACells = append(hostACells, cellKey)
		}
	}
	if len(hostACells) == 0 {
		t.Fatalf("pre-drain: host-a owns no cells; preOwnership=%v", preOwnership)
	}
	t.Logf("pre-drain: host-a owns %d cells %v", len(hostACells), hostACells)

	// Spawn a few entities into the cells on host-a so the migrate codec
	// has real state to round-trip. Position values are irrelevant — we
	// just want the executor path to actually move something.
	for _, cellKey := range hostACells {
		coord.mu.RLock()
		cell := coord.Cells[cellKey]
		coord.mu.RUnlock()
		if cell == nil {
			t.Fatalf("pre-drain: coord.Cells[%s] is nil", cellKey)
		}
		execOnLoop(t, cell, func() {
			spawnTestEntity(cell, 7001, 10, 20)
			spawnTestEntity(cell, 7002, 30, 40)
		})
	}

	// Drive the drain. This is the exact entry point the coord-side
	// GracefulLeave handler invokes.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer drainCancel()
	if err := coord.drainHost(drainCtx, "host-a"); err != nil {
		t.Fatalf("drainHost(host-a): %v", err)
	}

	// Invariant 1: every cell that lived on host-a is now owned by a
	// different host, and the new owner is one of the survivors.
	coord.mu.RLock()
	postOwnership := make(map[string]string, len(coord.cellToHostMap))
	for k, v := range coord.cellToHostMap {
		postOwnership[k] = v
	}
	coord.mu.RUnlock()

	for _, cellKey := range hostACells {
		newOwner, ok := postOwnership[cellKey]
		if !ok {
			t.Errorf("post-drain: cell %s missing from cellToHostMap", cellKey)
			continue
		}
		if newOwner == "host-a" {
			t.Errorf("post-drain: cell %s still owned by host-a", cellKey)
		}
		if newOwner != "host-b" && newOwner != "host-c" {
			t.Errorf("post-drain: cell %s new owner %q not in surviving set", cellKey, newOwner)
		}
	}

	// Invariant 2: cellToHostMap has no host-a entries at all.
	for k, v := range postOwnership {
		if v == "host-a" {
			t.Errorf("post-drain: cellToHostMap[%s] still points at host-a", k)
		}
	}

	// Invariant 3: no cell is orphaned (empty owner).
	for k, v := range postOwnership {
		if v == "" {
			t.Errorf("post-drain: cell %s has empty owner", k)
		}
	}

	// Invariant 4: the drain should have covered every cell previously on
	// host-a — no pre-drain host-a cell is missing from post-drain.
	if len(postOwnership) < len(preOwnership) {
		t.Errorf("post-drain: cellToHostMap shrank from %d to %d entries",
			len(preOwnership), len(postOwnership))
	}

	t.Logf("post-drain: host-a fully drained; post-ownership=%v", postOwnership)
}
