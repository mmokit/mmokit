package universe

import (
	"context"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
)

// ═══════════════════════════════════════════════════════════════════════════
// S7 T6 — cell migrate admin integration tests
//
// TestS7MigrateAcrossHosts stands up a 2-host in-process coordinator with a
// 2x2 grid (4 cells, 2 per host via round-robin assignment), spawns real
// entities into a source cell, drives the orchestrator's BeginMigrate path
// end-to-end, and verifies post-commit topology + entity preservation.
//
// The test exercises the same path the `cell migrate` console command uses:
// orchestrator.BeginMigrate → real dispatcher → source host executor.Execute
// (serialize on src game loop) → shipToDestination → dest host
// executor.Receive (createNode + populate on dest game loop) → reportReady
// → orchestrator.OnReady → commit → applyMutationOnly.
// ═══════════════════════════════════════════════════════════════════════════

// TestS7MigrateAcrossHosts drives a live cell migration between two
// in-process hosts via the orchestrator and asserts the essential post-
// commit invariants: ownership flipped, the destination host owns a cell
// with the correct ID, entities round-tripped preserving NetID and
// position, and the source host's Host.Cells map no longer contains the
// migrated cell (S7-T9 source teardown invariant).
func TestS7MigrateAcrossHosts(t *testing.T) {
	forEachTopology(t, FixtureConfig{
		CellsX: 2, CellsY: 2, CellSize: 1024,
		HostIDs: []string{"host-a", "host-b"},
	}, func(t *testing.T, fx clusterFixture) {
		srcCellID := CellID{X: 0, Y: 0}
		srcKey := MeshCellID(srcCellID)

		if owner := fx.CellOwner(srcKey); owner != "host-a" {
			t.Fatalf("pre-migrate: expected cell %s on host-a, got %q", srcKey, owner)
		}
		srcCell := fx.CellOn("host-a", srcKey)
		if srcCell == nil {
			t.Fatalf("pre-migrate: cell %s missing from host-a", srcKey)
		}

		// Spawn two real entities on the source cell's game loop.
		execOnLoop(t, srcCell, func() {
			spawnTestEntity(srcCell, 4242, 10, 20)
			spawnTestEntity(srcCell, 4343, 30, 40)
		})

		// Fire the migrate via the orchestrator (same call path as the
		// `cell migrate` console command).
		req, err := fx.Coord().orchestrator.BeginMigrate(srcCellID, "host-b")
		if err != nil {
			t.Fatalf("BeginMigrate: %v", err)
		}
		select {
		case <-req.Done:
		case <-time.After(5 * time.Second):
			t.Fatalf("BeginMigrate req=%d did not complete in 5s", req.ID)
		}
		if req.Result != nil {
			t.Fatalf("BeginMigrate req=%d failed: %v", req.ID, req.Result)
		}

		// Invariant 1: ownership flipped.
		if owner := fx.CellOwner(srcKey); owner != "host-b" {
			t.Errorf("post-commit: CellOwner(%s) = %q, want host-b", srcKey, owner)
		}

		// Invariant 2: host-b has the cell locally.
		destCell := fx.CellOn("host-b", srcKey)
		if destCell == nil {
			t.Fatalf("post-commit: host-b has no cell %s", srcKey)
		}
		if destCell.ID != srcKey {
			t.Errorf("destCell.ID = %q, want %q", destCell.ID, srcKey)
		}
		// The source cell must not be the same object as the dest cell —
		// createNode on the dest host should have minted a fresh *Cell.
		if destCell == srcCell {
			t.Error("post-commit dest cell is the same object as src cell — expected a fresh Cell on host-b")
		}

		// Invariant 3 (S7-T9 source teardown): host-a has released the cell.
		// In distributed mode applyMigrateCommit fires CellRelease async over
		// MeshControl, so source teardown lands after req.Done — we poll
		// until it arrives. Colocated is synchronous and returns on the
		// first check. STAGE-2 TODO: make BeginMigrate wait for CellStopped
		// so this poll collapses to a single read.
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		if err := fx.WaitForCellReleased(releaseCtx, srcKey, "host-a"); err != nil {
			t.Errorf("post-commit: %v", err)
		}

		// Invariant 4: entities round-tripped. Walk the dest cell's ECS on
		// its game loop and confirm netIDs 4242 and 4343 show up with the
		// original positions.
		netFound := map[uint32]component.Position{}
		execOnLoop(t, destCell, func() {
			posMap := ecs.NewMap1[component.Position](destCell.Engine.ECS)
			netMap := ecs.NewMap1[component.NetworkID](destCell.Engine.ECS)
			filter := ecs.NewFilter2[component.Position, component.NetworkID](destCell.Engine.ECS).
				Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
			q := filter.Query()
			for q.Next() {
				e := q.Entity()
				if !posMap.HasAll(e) || !netMap.HasAll(e) {
					continue
				}
				netFound[netMap.Get(e).ID] = *posMap.Get(e)
			}
		})
		if len(netFound) != 2 {
			t.Fatalf("dest cell has %d entities, want 2", len(netFound))
		}
		if got, ok := netFound[4242]; !ok || got.X != 10 || got.Y != 20 {
			t.Errorf("netID 4242: got (%.0f,%.0f) ok=%v want (10,20)", got.X, got.Y, ok)
		}
		if got, ok := netFound[4343]; !ok || got.X != 30 || got.Y != 40 {
			t.Errorf("netID 4343: got (%.0f,%.0f) ok=%v want (30,40)", got.X, got.Y, ok)
		}
	})
}
