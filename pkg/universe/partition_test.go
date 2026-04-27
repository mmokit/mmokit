package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// partitionFixtureConfig returns a FixtureConfig tuned for the partition
// tests: CellSize 8192 so MinCellSize defaults to 2048, allowing exactly
// two recursive splits before hitting the minimum (matches the original
// TestSplitCell_MinCellSize expectations). DynamicPartitioning is
// installed explicitly — it's off by default at the engine level, and
// the partition tests drive cooldowns + the auto-monitor directly.
func partitionFixtureConfig() FixtureConfig {
	return FixtureConfig{
		CellsX:              2,
		CellsY:              2,
		CellSize:            8192,
		HostIDs:             []string{"host-a", "host-b"},
		DynamicPartitioning: DefaultPartitionConfig(),
	}
}

func TestSplitCell_Basic(t *testing.T) {
	forEachTopology(t, partitionFixtureConfig(), func(t *testing.T, fx clusterFixture) {
		cell := CellID{X: 0, Y: 0}

		// Before split: 4 cells in the cluster. snapshotCellOwnership
		// merges hostRegistry (distributed) and cellToHostMap (in-process)
		// so the count is meaningful in both topologies.
		if got := len(fx.Coord().snapshotCellOwnership()); got != 4 {
			t.Fatalf("expected 4 cells before split, got %d", got)
		}

		if err := fx.Coord().SplitCell(cell, true); err != nil {
			t.Fatalf("SplitCell failed: %v", err)
		}

		// After split: 4 original - 1 split + 4 children = 7 cells.
		if got := len(fx.Coord().snapshotCellOwnership()); got != 7 {
			t.Fatalf("expected 7 cells after split, got %d", got)
		}

		// All 4 children should be owned by some host.
		for _, child := range cell.Children() {
			if fx.CellOwner(MeshCellID(child)) == "" {
				t.Errorf("child cell %s has no owner after split", child)
			}
		}

		// Original parent cell should have no owner.
		if owner := fx.CellOwner(MeshCellID(cell)); owner != "" {
			t.Errorf("original cell %s still owned by %q after split", cell, owner)
		}
	})
}

func TestSplitCell_MinCellSize(t *testing.T) {
	forEachTopology(t, partitionFixtureConfig(), func(t *testing.T, fx clusterFixture) {
		// Default MinCellSize = 8192/4 = 2048, max depth = 2.
		cell := CellID{X: 0, Y: 0}

		// First split: 8192 -> 4096 (ok).
		if err := fx.Coord().SplitCell(cell, true); err != nil {
			t.Fatalf("first split failed: %v", err)
		}

		// Second split: 4096 -> 2048 (ok, at the limit).
		child := cell.Children()[0]
		if err := fx.Coord().SplitCell(child, true); err != nil {
			t.Fatalf("second split failed: %v", err)
		}

		// Third split: 2048 -> 1024 (should fail — below min).
		grandchild := child.Children()[0]
		if err := fx.Coord().SplitCell(grandchild, true); err == nil {
			t.Fatal("expected error splitting below min cell size")
		}
	})
}

func TestSplitCell_TopologyCorrect(t *testing.T) {
	forEachTopology(t, partitionFixtureConfig(), func(t *testing.T, fx clusterFixture) {
		// Topology is maintained on the coord-role ControlPlane via the
		// hostRegistry ownership-changed callback (Task 5.2). Works in
		// both colocated and distributed topologies.

		cell := CellID{X: 0, Y: 0}
		if err := fx.Coord().SplitCell(cell, true); err != nil {
			t.Fatalf("SplitCell failed: %v", err)
		}

		children := cell.Children()
		coord := fx.Coord()

		// Right children should neighbor the depth-0 cell to the right.
		rightNeighbor := CellID{X: 1, Y: 0}
		for _, child := range children {
			if child.X == 1 { // right-side children
				found := false
				for _, n := range coord.Control.Topology.Neighbors[child] {
					if n == rightNeighbor {
						found = true
					}
				}
				if !found {
					t.Errorf("child %s should neighbor %s", child, rightNeighbor)
				}
			}
		}

		// All children should be neighbors of each other.
		for _, a := range children {
			siblingCount := 0
			for _, n := range coord.Control.Topology.Neighbors[a] {
				for _, b := range children {
					if n == b {
						siblingCount++
					}
				}
			}
			if siblingCount != 3 {
				t.Errorf("child %s has %d sibling neighbors, want 3", a, siblingCount)
			}
		}
	})
}

func TestSplitCell_NonexistentCell(t *testing.T) {
	forEachTopology(t, partitionFixtureConfig(), func(t *testing.T, fx clusterFixture) {
		if err := fx.Coord().SplitCell(CellID{X: 99, Y: 99}, true); err == nil {
			t.Fatal("expected error splitting nonexistent cell")
		}
	})
}

func TestSplitCell_DisabledPartitioning(t *testing.T) {
	// This test deliberately builds a Process WITHOUT going through
	// the fixture harness because New auto-installs
	// DefaultPartitionConfig when the field is nil. The test covers the
	// no-fixture startup path; fixture coverage for the enabled case is
	// provided by the other TestSplitCell_* tests.
	coords.SetCellSize(8192)
	c := New(Config{
		CellsX:      2,
		CellsY:      2,
		CellSize:    8192,
		ConnManager: net.NewConnManager(),
		Logger:      logger.New(),
		// DynamicPartitioning is nil -> auto-installed to DefaultPartitionConfig
		OnInit: func(w *Stage) {},
	})
	c.Build()
	t.Cleanup(c.Shutdown)

	// No cell game loops are started, so the executor serialize step
	// times out and SplitCell returns a rollback error — the expected
	// no-op behavior for a coordinator without running cells.
	if err := c.SplitCell(CellID{X: 0, Y: 0}, true); err == nil {
		t.Fatal("expected error when cell loops are not running")
	}
}

func TestMergeCell_Basic(t *testing.T) {
	forEachTopology(t, partitionFixtureConfig(), func(t *testing.T, fx clusterFixture) {
		cell := CellID{X: 0, Y: 0}

		// Split first.
		if err := fx.Coord().SplitCell(cell, true); err != nil {
			t.Fatalf("SplitCell failed: %v", err)
		}
		if got := len(fx.Coord().snapshotCellOwnership()); got != 7 {
			t.Fatalf("expected 7 cells after split, got %d", got)
		}

		// Merge back — use any child.
		child := cell.Children()[0]
		if err := fx.Coord().MergeCell(child, true); err != nil {
			t.Fatalf("MergeCell failed: %v", err)
		}

		// Parent should be back in the ownership view.
		if owner := fx.CellOwner(MeshCellID(cell)); owner == "" {
			t.Error("parent cell should be owned after merge")
		}

		// Children should be gone from the ownership view.
		for _, ch := range cell.Children() {
			if owner := fx.CellOwner(MeshCellID(ch)); owner != "" {
				t.Errorf("child %s should have no owner after merge (still on %q)", ch, owner)
			}
		}
	})
}

func TestMergeCell_CannotMergeDepth0(t *testing.T) {
	forEachTopology(t, partitionFixtureConfig(), func(t *testing.T, fx clusterFixture) {
		// MergeCell returns the error before any orchestration, so this
		// exercises only the precondition gate — no hosts touched.
		if err := fx.Coord().MergeCell(CellID{X: 0, Y: 0}, true); err == nil {
			t.Fatal("expected error merging depth-0 cell")
		}
	})
}

func TestSplitMerge_RoundTrip(t *testing.T) {
	forEachTopology(t, partitionFixtureConfig(), func(t *testing.T, fx clusterFixture) {
		cell := CellID{X: 1, Y: 1}

		// Split.
		if err := fx.Coord().SplitCell(cell, true); err != nil {
			t.Fatalf("SplitCell failed: %v", err)
		}

		// Merge.
		child := cell.Children()[2]
		if err := fx.Coord().MergeCell(child, true); err != nil {
			t.Fatalf("MergeCell failed: %v", err)
		}

		// Should have same number of cells in the ownership view as the
		// pre-split cluster.
		if got := len(fx.Coord().snapshotCellOwnership()); got != 4 {
			t.Errorf("expected 4 cells after round-trip, got %d", got)
		}

		// Parent cell should be back.
		if owner := fx.CellOwner(MeshCellID(cell)); owner == "" {
			t.Error("cell (1,1) should be owned after round-trip")
		}
	})
}

func TestSplitCell_Recursive(t *testing.T) {
	forEachTopology(t, partitionFixtureConfig(), func(t *testing.T, fx clusterFixture) {
		// Split depth 0 -> depth 1.
		root := CellID{X: 0, Y: 0}
		if err := fx.Coord().SplitCell(root, true); err != nil {
			t.Fatalf("depth-0 split failed: %v", err)
		}

		// Split one depth-1 child -> depth 2.
		child := root.Children()[0] // {0, 0, 1}
		if err := fx.Coord().SplitCell(child, true); err != nil {
			t.Fatalf("depth-1 split failed: %v", err)
		}

		// Should have depth-2 grandchildren owned by some host.
		grandchildren := child.Children()
		for _, gc := range grandchildren {
			if gc.Depth != 2 {
				t.Errorf("grandchild %s has depth %d, want 2", gc, gc.Depth)
			}
			if fx.CellOwner(MeshCellID(gc)) == "" {
				t.Errorf("grandchild %s has no owner", gc)
			}
		}

		// Depth-1 child should be gone.
		if owner := fx.CellOwner(MeshCellID(child)); owner != "" {
			t.Errorf("depth-1 cell %s should be gone after recursive split (still on %q)", child, owner)
		}
	})
}

func TestCooldown_AutoSplitBlocked(t *testing.T) {
	forEachTopology(t, partitionFixtureConfig(), func(t *testing.T, fx clusterFixture) {
		cell := CellID{X: 0, Y: 0}

		// Split (bypassing cooldown).
		if err := fx.Coord().SplitCell(cell, true); err != nil {
			t.Fatalf("SplitCell failed: %v", err)
		}

		// Children should have cooldowns set on the coord's partition state.
		// partState is a coord-local structure so it's populated identically
		// in both topologies — cooldowns are set by applyCellTransferCommit
		// on the coord side after every split.
		for _, child := range cell.Children() {
			if !fx.Coord().partState.onCooldown(child) {
				t.Errorf("child %s should be on cooldown after split", child)
			}
		}
	})
}

func TestDefaultPartitionConfig(t *testing.T) {
	pc := DefaultPartitionConfig()
	if pc.SplitThreshold != 0.75 {
		t.Errorf("SplitThreshold = %v, want 0.75", pc.SplitThreshold)
	}
	if pc.MergeThreshold != 0.20 {
		t.Errorf("MergeThreshold = %v, want 0.20", pc.MergeThreshold)
	}
	if pc.MinCellSize != 0 {
		t.Errorf("MinCellSize = %v, want 0 (resolved in Build)", pc.MinCellSize)
	}
}
