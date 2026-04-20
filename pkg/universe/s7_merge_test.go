package universe

import (
	"context"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
)

// ═══════════════════════════════════════════════════════════════════════════
// S7 T10 — distributed merge integration test
//
// TestS7MergeAcrossHosts stands up a 2-host cluster via the distributed
// fixture, splits cell_0_0 into its 4 depth-1 children (distributed across
// host-a and host-b by rendezvous+locality), then force-merges the 4 siblings
// back into the parent via Process.MergeCell(..., bypassCooldown=true).
// Validates topology + full donor-entity preservation post-commit.
//
// This test asserts:
//
//  1. All 3 donor cells are torn down from coord CellOwner / cellToHostMap.
//  2. The survivor cell is now keyed on the parent CellID in ownership.
//  3. cellToHostMap has the parent key and none of the 4 sibling keys.
//  4. ALL 4 planted netIDs (one per original sibling, including the 3
//     donors and the survivor) are present on the post-merge cell. This
//     is the entity-preservation guarantee — the merge executor must
//     populate donor entities into the surviving sibling rather than
//     dropping them via the Receive idempotency short-circuit.
//  5. No cell is left in a cellToHostMap / Host.Cells inconsistency — the
//     host that owns the parent key has a Cell pinned to the parent CellID.
// ═══════════════════════════════════════════════════════════════════════════

func TestS7MergeAcrossHosts(t *testing.T) {
	coords.SetCellSize(1024)

	fx := newDistributedFixture(t, FixtureConfig{
		CellsX:   2,
		CellsY:   2,
		CellSize: 1024,
		HostIDs:  []string{"host-a", "host-b"},
	})
	coord := fx.Coord()

	parentCellID := CellID{X: 0, Y: 0}
	parentKey := MeshCellID(parentCellID)

	// Step 1: split cell_0_0 so we have 4 siblings distributed across the
	// two hosts.
	if err := coord.SplitCell(parentCellID, true); err != nil {
		t.Fatalf("SplitCell: %v", err)
	}
	children := parentCellID.Children()
	childKeys := make([]string, 4)
	distribution := make(map[string]int)
	for i, ch := range children {
		childKeys[i] = MeshCellID(ch)
	}
	for _, k := range childKeys {
		owner := fx.CellOwner(k)
		if owner == "" {
			t.Fatalf("pre-merge: child %s missing from cellToHostMap", k)
		}
		distribution[owner]++
	}
	t.Logf("pre-merge: children distributed %v", distribution)

	// Step 2: plant a known entity in each child, positioned within that
	// child's own subcell bounds (in base-cell-local coords, per the
	// "entities always use base-cell coords" invariant). Without this,
	// BoundarySystem on donors transfers the entity away before the merge
	// executor can serialize it, and when strict netIDIndex enforcement is
	// on the merge then fails with "duplicate live netID" because the
	// entity already landed on the survivor via the boundary path.
	plantedOnChild := make(map[string]uint32) // childKey -> netID
	nextNet := uint32(12000)
	subSize := coords.CellSize / 2
	for i, ch := range children {
		childKey := MeshCellID(ch)
		childCell := fx.AnyCell(childKey)
		if childCell == nil {
			t.Fatalf("pre-merge: no cell found for %s", childKey)
		}
		nid := nextNet + uint32(i)
		plantedOnChild[childKey] = nid
		// Center of this subcell in base-cell-local coords.
		cx := float32(ch.X)*subSize + subSize*0.5
		cy := float32(ch.Y)*subSize + subSize*0.5
		execOnLoop(t, childCell, func() {
			spawnTestEntity(childCell, nid, cx, cy)
		})
	}

	// Step 3: fire the merge. MergeCell on any depth-1 sibling merges the
	// full 4-sibling group.
	if err := coord.MergeCell(children[0], true); err != nil {
		t.Fatalf("MergeCell: %v", err)
	}

	// ── Invariant 1: all 4 sibling keys gone from ownership. ────────────
	for _, k := range childKeys {
		if owner := fx.CellOwner(k); owner != "" {
			t.Errorf("post-merge: sibling %s still owned by %q", k, owner)
		}
		if cell := fx.AnyCell(k); cell != nil {
			t.Errorf("post-merge: sibling %s still has a live cell on some host", k)
		}
	}

	// ── Invariant 2 + 3: parent back in ownership. ────────────────────────
	parentHost := fx.CellOwner(parentKey)
	if parentHost == "" {
		t.Errorf("post-merge: parent %s missing from ownership", parentKey)
	}
	if parentHost != "host-a" && parentHost != "host-b" {
		t.Errorf("post-merge: parent host=%q not in {host-a, host-b}", parentHost)
	}

	parentCell := fx.AnyCell(parentKey)
	if parentCell == nil {
		t.Errorf("post-merge: no live cell found for parent %s", parentKey)
	} else {
		if parentCell.Cell != parentCellID {
			t.Errorf("post-merge: survivor cell has CellID %v, want %v", parentCell.Cell, parentCellID)
		}
		if parentCell.ID != parentKey {
			t.Errorf("post-merge: survivor cell has string ID %q, want %q", parentCell.ID, parentKey)
		}
	}

	// ── Invariant 5: the host that owns parentKey actually has a live
	// Cell at the parent CellID. Catches cellToHostMap / Host.Cells drift.
	if parentHost != "" && !fx.HostOwnsCell(parentHost, parentKey) {
		t.Errorf("post-merge: host %s does not own cell %s", parentHost, parentKey)
	}

	// ── Invariant 4: EVERY planted netID (one per original sibling,
	// across 3 donors + 1 survivor) is present on the post-merge cell.
	// The merge executor's populate-into-existing-cell path is
	// responsible for migrating all donor entities into the survivor.
	if parentCell != nil {
		// Wait a moment for the merge executor to fully settle entity
		// state on the winning host before reading ECS.
		mergeCtx, mergeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer mergeCancel()

		var seen map[uint32]bool
		for {
			seen = map[uint32]bool{}
			execOnLoop(t, parentCell, func() {
				netMap := ecs.NewMap1[component.NetworkID](parentCell.Engine.ECS)
				filter := ecs.NewFilter1[component.NetworkID](parentCell.Engine.ECS).
					Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
				q := filter.Query()
				for q.Next() {
					e := q.Entity()
					if !netMap.HasAll(e) {
						continue
					}
					seen[netMap.Get(e).ID] = true
				}
			})
			missing := []uint32{}
			for _, nid := range plantedOnChild {
				if !seen[nid] {
					missing = append(missing, nid)
				}
			}
			if len(missing) == 0 {
				break
			}
			select {
			case <-mergeCtx.Done():
				t.Errorf("post-merge: donor/survivor netIDs missing from merged cell: %v (planted %v, seen %d total)",
					missing, plantedOnChild, len(seen))
				goto doneEntityCheck
			case <-time.After(25 * time.Millisecond):
			}
		}
		t.Logf("post-merge: survivor cell contains %d netIDs (all %d planted present)",
			len(seen), len(plantedOnChild))
	}
doneEntityCheck:
}

// TestS7MergeWiresParentNeighbors is the regression guard for the bug
// where the merged parent cell's runtime Neighbors map stayed empty and
// its top-level neighbors (cell_1_0, cell_0_1, cell_1_1) never learned
// about the new parent cell. Symptom: post-merge AoI replication stops
// at the parent boundary — the client only sees entities inside the
// merged cell and loses sight of everything in the three adjacent cells.
// Root cause: computeRewireDirectivesLocked ran under the merge commit's
// lock, but the survivor's coord-level rekey (c.Cells[parentKey] = local,
// c.CellOwner[parent] = parentKey) happened AFTER the rewire computation.
// Looking up parent in c.CellOwner returned "" inside the rewire loop, so
// the parent's own directive and every external neighbor's reference to
// the parent silently dropped.
func TestS7MergeWiresParentNeighbors(t *testing.T) {
	coords.SetCellSize(1024)

	fx := newColocatedFixture(t, FixtureConfig{
		CellsX:   2,
		CellsY:   2,
		CellSize: 1024,
	})
	coord := fx.Coord()

	parentCellID := CellID{X: 0, Y: 0}
	parentKey := MeshCellID(parentCellID)

	if err := coord.SplitCell(parentCellID, true); err != nil {
		t.Fatalf("SplitCell: %v", err)
	}

	children := parentCellID.Children()
	if err := coord.MergeCell(children[0], true); err != nil {
		t.Fatalf("MergeCell: %v", err)
	}

	parentCell := fx.AnyCell(parentKey)
	if parentCell == nil {
		t.Fatalf("post-merge: parent cell missing")
	}

	// Parent must see its three top-level neighbors in the runtime map
	// used by ReplicationSystem / BoundarySystem.
	wantNeighbors := []string{"cell_1_0", "cell_0_1", "cell_1_1"}
	execOnLoop(t, parentCell, func() {
		for _, want := range wantNeighbors {
			if _, ok := parentCell.Neighbors[want]; !ok {
				t.Errorf("post-merge: parent.Neighbors missing %q (have %v)",
					want, mapKeys(parentCell.Neighbors))
			}
		}
	})

	// Each top-level neighbor must see the parent in its runtime
	// Neighbors map — otherwise ReplicationSystem on those cells won't
	// push border frames back to the parent.
	for _, nkey := range wantNeighbors {
		n := fx.AnyCell(nkey)
		if n == nil {
			t.Errorf("post-merge: neighbor cell %s missing", nkey)
			continue
		}
		execOnLoop(t, n, func() {
			if _, ok := n.Neighbors[parentKey]; !ok {
				t.Errorf("post-merge: %s.Neighbors missing %q (have %v)",
					nkey, parentKey, mapKeys(n.Neighbors))
			}
		})
	}
}

// TestS7MergeNoDuplicateNetIDs is the regression guard for the bug where
// drainDonorResidualsToSurvivor re-serialized EVERY donor entity (not just
// ones that arrived after the initial snapshot) and re-spawned them on the
// survivor via SpawnFromTransferCore. That unconditional re-spawn ignored
// netID collisions, so every player/asteroid on a donor ended up present
// twice in the merged cell — once from the executor's initial populate and
// once from the drain. Symptom: client loses control of its ship after
// merge because replication sends frames for two entities sharing the same
// netID and the stationary duplicate wins on most ticks.
func TestS7MergeNoDuplicateNetIDs(t *testing.T) {
	coords.SetCellSize(1024)

	fx := newColocatedFixture(t, FixtureConfig{
		CellsX:   2,
		CellsY:   2,
		CellSize: 1024,
	})
	coord := fx.Coord()

	parentCellID := CellID{X: 0, Y: 0}
	parentKey := MeshCellID(parentCellID)

	if err := coord.SplitCell(parentCellID, true); err != nil {
		t.Fatalf("SplitCell: %v", err)
	}

	// Plant one known entity in each child, positioned within that child's
	// subcell bounds (in base-cell-local coords, so BoundarySystem on the
	// donor doesn't transfer the entity out before the merge Execute can
	// serialize it). Without this, newly-spawned entities at a generic
	// "center of a depth-1 cell" position land in a subcell bounds check
	// that routes them somewhere else, and the race between BoundarySystem
	// transfers and the merge executor produces duplicate/missing entities
	// that muddle the drain-duplicate check.
	children := parentCellID.Children()
	subSize := coords.CellSize / 2
	for i, ch := range children {
		childKey := MeshCellID(ch)
		childCell := fx.AnyCell(childKey)
		if childCell == nil {
			t.Fatalf("pre-merge: no cell for %s", childKey)
		}
		netID := uint32(22000 + i)
		// Center of this subcell in base-cell-local coords.
		cx := float32(ch.X)*subSize + subSize*0.5
		cy := float32(ch.Y)*subSize + subSize*0.5
		execOnLoop(t, childCell, func() {
			spawnTestEntity(childCell, netID, cx, cy)
		})
	}

	if err := coord.MergeCell(children[0], true); err != nil {
		t.Fatalf("MergeCell: %v", err)
	}

	parentCell := fx.AnyCell(parentKey)
	if parentCell == nil {
		t.Fatalf("post-merge: parent cell missing")
	}

	execOnLoop(t, parentCell, func() {
		netMap := ecs.NewMap1[component.NetworkID](parentCell.Engine.ECS)
		filter := ecs.NewFilter1[component.NetworkID](parentCell.Engine.ECS).
			Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
		q := filter.Query()
		counts := map[uint32]int{}
		for q.Next() {
			e := q.Entity()
			if !netMap.HasAll(e) {
				continue
			}
			counts[netMap.Get(e).ID]++
		}
		for nid, n := range counts {
			if n > 1 {
				t.Errorf("post-merge: netID %d appears %d times on merged cell (want 1)", nid, n)
			}
		}
	})
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestS7MergeShutsDownDonorCells is the regression guard for the merge
// zombie-cell bug: after a merge, the three donor cells must have their
// 20Hz game loops stopped. Before the fix, applyMergeCommit pre-removed
// donors from host.Cells inside the lock, which caused the subsequent
// localHostOps.ReleaseCell lookup to return "unknown cell", skipping
// cell.Shutdown() and leaking zombie loops that kept replicating stale
// state to clients.
//
// Colocated-only: the distributed path goes through MeshControl's
// CellRelease, which looks up the cell in the REMOTE host's own Host.Cells
// (never mutated by the coord's pre-remove loop), so the remote path
// never exhibited the bug.
func TestS7MergeShutsDownDonorCells(t *testing.T) {
	coords.SetCellSize(1024)

	fx := newColocatedFixture(t, FixtureConfig{
		CellsX:   2,
		CellsY:   2,
		CellSize: 1024,
	})
	coord := fx.Coord()

	parentCellID := CellID{X: 0, Y: 0}
	if err := coord.SplitCell(parentCellID, true); err != nil {
		t.Fatalf("SplitCell: %v", err)
	}

	// Capture every child cell BEFORE the merge so we can observe which
	// game loops exit.
	children := parentCellID.Children()
	childCells := make([]*Cell, 0, 4)
	for _, ch := range children {
		key := MeshCellID(ch)
		cell := fx.AnyCell(key)
		if cell == nil {
			t.Fatalf("post-split: child %s missing", key)
		}
		childCells = append(childCells, cell)
	}

	if err := coord.MergeCell(children[0], true); err != nil {
		t.Fatalf("MergeCell: %v", err)
	}

	// Exactly one child survives (renamed to the parent); the other three
	// must have their game loops stopped.
	parentKey := MeshCellID(parentCellID)
	parentCell := fx.AnyCell(parentKey)
	if parentCell == nil {
		t.Fatalf("post-merge: parent cell missing")
	}
	survivorCount := 0
	for _, c := range childCells {
		if c == parentCell {
			survivorCount++
			continue
		}
		assertCellGoroutineExited(t, c, 2*time.Second)
	}
	if survivorCount != 1 {
		t.Fatalf("post-merge: expected exactly 1 surviving cell among children, got %d", survivorCount)
	}
}
