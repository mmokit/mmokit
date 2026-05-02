package universe

import (
	"context"
	"testing"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════
// S7 T7 — graceful shutdown integration test
//
// TestS7GracefulShutdown stands up a 3-host cluster with a 2x2 grid (4 cells
// — round-robin lands 2 on host-a, 1 on host-b, 1 on host-c) and drives the
// graceful-leave path via fx.StopHost. In colocated mode this calls
// coord.drainHost directly (same entry point the coord-side GracefulLeave
// handler uses); in distributed mode it drives the host-role Process's
// Shutdown (which sends GracefulLeave over MeshControl and waits for
// CellsDrained) — the full wire path a live cluster uses.
//
// Assertions after drain:
//   - every cell that was on host-a now maps to a DIFFERENT host
//   - no cell remains without an owner
//   - no cell maps back to host-a
//
// Per the T7 task notes, source-host teardown is deferred — the leaving
// host's local Cells map may still have stale entries after migrate commit.
// The test deliberately does NOT assert on host-a's Host.Cells post-drain;
// the orphaned goroutines are cleaned up by the node's local shutdown loop
// in real usage.
// ═══════════════════════════════════════════════════════════════════════════

func TestS7GracefulShutdown(t *testing.T) {
	forEachTopology(t, FixtureConfig{
		CellsX: 2, CellsY: 2, CellSize: 1024,
		HostIDs: []string{"host-a", "host-b", "host-c"},
	}, func(t *testing.T, fx clusterFixture) {
		// Snapshot pre-drain ownership so we can diff after StopHost runs.
		// Round-robin assignment across [host-a, host-b, host-c] for the 2x2
		// grid in CellsY-outer, CellsX-inner iteration order gives:
		//   cell_0_0 -> host-a
		//   cell_1_0 -> host-b
		//   cell_0_1 -> host-c
		//   cell_1_1 -> host-a
		// so host-a owns 2 cells and is the most interesting drain source.
		cellKeys := []string{
			CellID{X: 0, Y: 0}.MeshID(),
			CellID{X: 1, Y: 0}.MeshID(),
			CellID{X: 0, Y: 1}.MeshID(),
			CellID{X: 1, Y: 1}.MeshID(),
		}
		preOwnership := make(map[string]string, len(cellKeys))
		for _, k := range cellKeys {
			preOwnership[k] = fx.CellOwner(k)
		}

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
			cell := fx.CellOn("host-a", cellKey)
			if cell == nil {
				t.Fatalf("pre-drain: host-a is missing cell %s", cellKey)
			}
			execOnLoop(t, cell, func() {
				spawnTestEntity(cell, 7001, 10, 20)
				spawnTestEntity(cell, 7002, 30, 40)
			})
		}

		// Drive the graceful shutdown. In colocated mode this calls
		// coord.drainHost directly; in distributed mode it drives host-a's
		// Process.Shutdown, which sends GracefulLeave over the control
		// stream and waits for CellsDrained.
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer drainCancel()
		if err := fx.StopHost(drainCtx, "host-a"); err != nil {
			t.Fatalf("StopHost(host-a): %v", err)
		}

		// Invariant 1: every cell that lived on host-a is now owned by a
		// different host, and the new owner is one of the survivors.
		postOwnership := make(map[string]string, len(cellKeys))
		for _, k := range cellKeys {
			postOwnership[k] = fx.CellOwner(k)
		}

		for _, cellKey := range hostACells {
			newOwner := postOwnership[cellKey]
			if newOwner == "" {
				t.Errorf("post-drain: cell %s has no owner", cellKey)
				continue
			}
			if newOwner == "host-a" {
				t.Errorf("post-drain: cell %s still owned by host-a", cellKey)
			}
			if newOwner != "host-b" && newOwner != "host-c" {
				t.Errorf("post-drain: cell %s new owner %q not in surviving set", cellKey, newOwner)
			}
		}

		// Invariant 2: no cell maps back to host-a.
		for k, v := range postOwnership {
			if v == "host-a" {
				t.Errorf("post-drain: cell %s still points at host-a", k)
			}
		}

		// Invariant 3: no cell is orphaned (empty owner).
		for k, v := range postOwnership {
			if v == "" {
				t.Errorf("post-drain: cell %s has empty owner", k)
			}
		}

		// Invariant 4: fx.HostIDs() no longer includes host-a.
		for _, h := range fx.HostIDs() {
			if h == "host-a" {
				t.Errorf("post-drain: fx.HostIDs() still lists host-a")
			}
		}

		// Invariant 5: the surviving hosts collectively own every cell.
		// Sum HostOwnsCell over the remaining hosts for each cell key —
		// exactly one of them should answer true per cell.
		for _, cellKey := range cellKeys {
			owners := 0
			for _, h := range fx.HostIDs() {
				if fx.HostOwnsCell(h, cellKey) {
					owners++
				}
			}
			if owners != 1 {
				t.Errorf("post-drain: cell %s has %d surviving-host owners, want 1", cellKey, owners)
			}
		}

		t.Logf("post-drain: host-a fully drained; post-ownership=%v", postOwnership)
	})
}
