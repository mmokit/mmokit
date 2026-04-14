package universe

import (
	"context"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
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

// newMigrateTestCoord builds a 2-host Coordinator with a 2x2 cell grid and
// starts every cell's game loop on a test-scoped context so executor
// PendingAdminCmds closures actually drain. Uses real WorldBase as the
// GameWorld so SerializeEntity / SpawnFromTransfer produce and consume real
// bytes. Returns the coord plus a cancel func for test cleanup.
func newMigrateTestCoord(t *testing.T) (*Coordinator, context.CancelFunc) {
	t.Helper()
	coords.SetCellSize(1024)

	cfg := Config{
		CellsX:       2,
		CellsY:       2,
		CellSize:     1024,
		TestHosts:    []string{"host-a", "host-b"},
		Headless:     true,
		ConnManager:  net.NewConnManager(),
		Logger:       logger.New(),
		LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
	}
	coord := NewCoordinator(cfg)
	// Use the real WorldBase as the world so entity serialize/deserialize
	// round-trips through the reflect marshaller instead of a mock.
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()

	ctx, cancel := context.WithCancel(context.Background())
	for _, cell := range coord.Cells {
		go cell.Run(ctx)
	}
	// Let every cell drain its first admin-cmd pass before anything
	// else runs.
	time.Sleep(20 * time.Millisecond)

	return coord, cancel
}

// TestS7MigrateAcrossHosts drives a live cell migration between two
// in-process hosts via the orchestrator and asserts the essential post-
// commit invariants: ownership flipped in cellToHostMap, the destination
// host owns a cell with the correct ID, and entities that lived on the
// source cell round-tripped into the destination cell preserving NetID and
// position. Source-host teardown (removing the cell from the old Host.Cells
// map and shutting down its game loop) is still deferred work — see
// applyCellTransferCommit's migrate comment — so the test does not assert
// on the source host's Host.Cells after commit.
func TestS7MigrateAcrossHosts(t *testing.T) {
	coord, cancel := newMigrateTestCoord(t)
	t.Cleanup(func() {
		cancel()
		coord.Shutdown()
	})

	// Pick a cell that landed on host-a. The fixture assigns
	// round-robin: cell_0_0 -> host-a, cell_1_0 -> host-b,
	// cell_0_1 -> host-a, cell_1_1 -> host-b. We use cell_0_0.
	srcCellID := CellID{X: 0, Y: 0}
	srcKey := MeshCellID(srcCellID)
	if coord.cellToHostMap[srcKey] != "host-a" {
		t.Fatalf("pre-migrate: expected cell %s on host-a, got %q",
			srcKey, coord.cellToHostMap[srcKey])
	}
	srcCell := coord.Cells[srcKey]
	if srcCell == nil {
		t.Fatalf("pre-migrate: cell %s missing from coord.Cells", srcKey)
	}

	// Spawn two real entities on the source cell's game loop.
	execOnLoop(t, srcCell, func() {
		spawnTestEntity(srcCell, 4242, 10, 20)
		spawnTestEntity(srcCell, 4343, 30, 40)
	})

	// Fire the migrate via the orchestrator (same call path as the
	// `cell migrate` console command).
	req, err := coord.orchestrator.BeginMigrate(srcCellID, "host-b")
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

	// Invariant 1: ownership flipped in cellToHostMap.
	coord.mu.RLock()
	owner := coord.cellToHostMap[srcKey]
	coord.mu.RUnlock()
	if owner != "host-b" {
		t.Errorf("post-commit cellToHostMap[%s] = %q, want host-b", srcKey, owner)
	}

	// Invariant 2: host-b now has a cell under the shared ID, and it
	// is the same *Cell coord.Cells resolves to.
	destHost := coord.Hosts["host-b"]
	if destHost == nil {
		t.Fatal("host-b missing from coord.Hosts")
	}
	destCell := destHost.CellByID(srcKey)
	if destCell == nil {
		t.Fatalf("post-commit: host-b has no cell %s", srcKey)
	}
	if destCell.ID != srcKey {
		t.Errorf("destCell.ID = %q, want %q", destCell.ID, srcKey)
	}
	coord.mu.RLock()
	coordCell := coord.Cells[srcKey]
	coord.mu.RUnlock()
	if coordCell != destCell {
		t.Errorf("coord.Cells[%s] = %p, want dest cell %p", srcKey, coordCell, destCell)
	}
	// The source cell must not be the same object as the dest cell —
	// createNode on the dest host should have minted a fresh *Cell.
	if destCell == srcCell {
		t.Error("post-commit dest cell is the same object as src cell — expected a fresh Cell on host-b")
	}

	// Invariant 3: entities round-tripped. Walk the dest cell's ECS on
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
}
