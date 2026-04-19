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
// back into the parent via Coordinator.MergeCell(..., bypassCooldown=true).
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

	// Step 2: plant a known entity in each child so that after the merge
	// we can prove the SURVIVOR'S own entities are intact. (Donor entities
	// will be lost — see the caveat at the top of this file.)
	plantedOnChild := make(map[string]uint32) // childKey -> netID
	nextNet := uint32(12000)
	for i, ch := range children {
		childKey := MeshCellID(ch)
		childCell := fx.AnyCell(childKey)
		if childCell == nil {
			t.Fatalf("pre-merge: no cell found for %s", childKey)
		}
		cellSize := ch.Size(coords.CellSize)
		nid := nextNet + uint32(i)
		plantedOnChild[childKey] = nid
		execOnLoop(t, childCell, func() {
			// Plant near the cell center (local coords).
			spawnTestEntity(childCell, nid, cellSize*0.5, cellSize*0.5)
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
