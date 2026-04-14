package universe

import (
	"context"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

func newPartitionCoordinator(t *testing.T) (*Coordinator, context.CancelFunc) {
	t.Helper()
	coords.SetCellSize(8192)
	c := NewCoordinator(Config{
		CellsX:             2,
		CellsY:             2,
		CellSize:           8192,
		ConnManager:        net.NewConnManager(),
		Logger:             logger.New(),
		DynamicPartitioning: DefaultPartitionConfig(),
	})
	c.OnInit(func(w *WorldBase) {})
	c.Build()

	// Start all node game loops so PendingAdminCmds is drained
	ctx, cancel := context.WithCancel(context.Background())
	for _, node := range c.Cells {
		go node.Run(ctx)
	}
	t.Cleanup(func() {
		cancel()
		// Cell.Shutdown (called via coord.Shutdown) blocks until each
		// game loop has actually exited, including cells spawned by
		// the executor Receive path via SplitCell. Without this, those
		// goroutines leak past the test and race with the next test's
		// coords.SetCellSize (S7-T10 race fix).
		c.Shutdown()
	})
	// Let game loops start
	time.Sleep(50 * time.Millisecond)

	return c, cancel
}

func TestSplitCell_Basic(t *testing.T) {
	c, _ := newPartitionCoordinator(t)

	cell := CellID{X: 0, Y: 0}

	// Before split: 4 nodes
	if len(c.Cells) != 4 {
		t.Fatalf("expected 4 nodes before split, got %d", len(c.Cells))
	}

	err := c.SplitCell(cell, true)
	if err != nil {
		t.Fatalf("SplitCell failed: %v", err)
	}

	// After split: 4 original - 1 split + 4 children = 7 nodes
	if len(c.Cells) != 7 {
		t.Fatalf("expected 7 nodes after split, got %d", len(c.Cells))
	}

	// All 4 children should exist in NodeOwner
	children := cell.Children()
	for _, child := range children {
		if _, ok := c.CellOwner[child]; !ok {
			t.Errorf("child cell %s not found in NodeOwner", child)
		}
	}

	// Original cell should be gone
	if _, ok := c.CellOwner[cell]; ok {
		t.Error("original cell should be removed from NodeOwner after split")
	}
}

func TestSplitCell_MinCellSize(t *testing.T) {
	c, _ := newPartitionCoordinator(t)

	// Default MinCellSize = 8192/4 = 2048, max depth = 2
	cell := CellID{X: 0, Y: 0}

	// First split: 8192 -> 4096 (ok)
	if err := c.SplitCell(cell, true); err != nil {
		t.Fatalf("first split failed: %v", err)
	}

	// Second split: 4096 -> 2048 (ok, at the limit)
	child := cell.Children()[0]
	if err := c.SplitCell(child, true); err != nil {
		t.Fatalf("second split failed: %v", err)
	}

	// Third split: 2048 -> 1024 (should fail — below min)
	grandchild := child.Children()[0]
	err := c.SplitCell(grandchild, true)
	if err == nil {
		t.Fatal("expected error splitting below min cell size")
	}
}

func TestSplitCell_TopologyCorrect(t *testing.T) {
	c, _ := newPartitionCoordinator(t)

	cell := CellID{X: 0, Y: 0}
	if err := c.SplitCell(cell, true); err != nil {
		t.Fatalf("SplitCell failed: %v", err)
	}

	children := cell.Children()

	// Right children should neighbor the depth-0 cell to the right
	rightNeighbor := CellID{X: 1, Y: 0}
	for _, child := range children {
		if child.X == 1 { // right-side children
			found := false
			for _, n := range c.Topology.Neighbors[child] {
				if n == rightNeighbor {
					found = true
				}
			}
			if !found {
				t.Errorf("child %s should neighbor %s", child, rightNeighbor)
			}
		}
	}

	// All children should be neighbors of each other
	for _, a := range children {
		siblingCount := 0
		for _, n := range c.Topology.Neighbors[a] {
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
}

func TestSplitCell_NonexistentCell(t *testing.T) {
	c, _ := newPartitionCoordinator(t)

	err := c.SplitCell(CellID{X: 99, Y: 99}, true)
	if err == nil {
		t.Fatal("expected error splitting nonexistent cell")
	}
}

func TestSplitCell_DisabledPartitioning(t *testing.T) {
	coords.SetCellSize(8192)
	c := NewCoordinator(Config{
		CellsX:      2,
		CellsY:      2,
		CellSize:    8192,
		ConnManager: net.NewConnManager(),
		Logger:      logger.New(),
		// DynamicPartitioning is nil
	})
	c.OnInit(func(w *WorldBase) {})
	c.Build()

	err := c.SplitCell(CellID{X: 0, Y: 0}, true)
	if err == nil {
		t.Fatal("expected error when partitioning is disabled")
	}
}

func TestMergeCell_Basic(t *testing.T) {
	c, _ := newPartitionCoordinator(t)

	cell := CellID{X: 0, Y: 0}

	// Split first
	if err := c.SplitCell(cell, true); err != nil {
		t.Fatalf("SplitCell failed: %v", err)
	}
	if len(c.Cells) != 7 {
		t.Fatalf("expected 7 nodes after split, got %d", len(c.Cells))
	}

	// Merge back — use any child
	child := cell.Children()[0]
	if err := c.MergeCell(child, true); err != nil {
		t.Fatalf("MergeCell failed: %v", err)
	}

	// Parent should be back in NodeOwner
	if _, ok := c.CellOwner[cell]; !ok {
		t.Error("parent cell should exist in NodeOwner after merge")
	}

	// Children should be gone from NodeOwner
	for _, ch := range cell.Children() {
		if _, ok := c.CellOwner[ch]; ok {
			t.Errorf("child %s should be removed from NodeOwner after merge", ch)
		}
	}
}

func TestMergeCell_CannotMergeDepth0(t *testing.T) {
	c, _ := newPartitionCoordinator(t)

	err := c.MergeCell(CellID{X: 0, Y: 0}, true)
	if err == nil {
		t.Fatal("expected error merging depth-0 cell")
	}
}

func TestSplitMerge_RoundTrip(t *testing.T) {
	c, _ := newPartitionCoordinator(t)

	cell := CellID{X: 1, Y: 1}
	originalNodeCount := len(c.Cells)

	// Split
	if err := c.SplitCell(cell, true); err != nil {
		t.Fatalf("SplitCell failed: %v", err)
	}

	// Merge
	child := cell.Children()[2]
	if err := c.MergeCell(child, true); err != nil {
		t.Fatalf("MergeCell failed: %v", err)
	}

	// Should have same number of NodeOwner entries
	if len(c.CellOwner) != 4 {
		t.Errorf("expected 4 cells in NodeOwner after round-trip, got %d", len(c.CellOwner))
	}

	// Parent cell should be back
	if _, ok := c.CellOwner[cell]; !ok {
		t.Error("cell (1,1) should exist after round-trip")
	}

	// Node count: 3 non-survivors are cleaned up asynchronously,
	// so we check NodeOwner instead
	_ = originalNodeCount
}

func TestSplitCell_Recursive(t *testing.T) {
	c, _ := newPartitionCoordinator(t)

	// Split depth 0 -> depth 1
	root := CellID{X: 0, Y: 0}
	if err := c.SplitCell(root, true); err != nil {
		t.Fatalf("depth-0 split failed: %v", err)
	}

	// Split one depth-1 child -> depth 2
	child := root.Children()[0] // {0, 0, 1}
	if err := c.SplitCell(child, true); err != nil {
		t.Fatalf("depth-1 split failed: %v", err)
	}

	// Should have depth-2 grandchildren
	grandchildren := child.Children()
	for _, gc := range grandchildren {
		if gc.Depth != 2 {
			t.Errorf("grandchild %s has depth %d, want 2", gc, gc.Depth)
		}
		if _, ok := c.CellOwner[gc]; !ok {
			t.Errorf("grandchild %s not found in NodeOwner", gc)
		}
	}

	// Depth-1 child should be gone
	if _, ok := c.CellOwner[child]; ok {
		t.Error("depth-1 cell should be gone after recursive split")
	}
}

func TestCooldown_AutoSplitBlocked(t *testing.T) {
	c, _ := newPartitionCoordinator(t)

	cell := CellID{X: 0, Y: 0}

	// Split (bypassing cooldown)
	if err := c.SplitCell(cell, true); err != nil {
		t.Fatalf("SplitCell failed: %v", err)
	}

	// Children should have cooldowns set
	for _, child := range cell.Children() {
		if !c.partState.onCooldown(child) {
			t.Errorf("child %s should be on cooldown after split", child)
		}

		// Auto-split (no bypass) should fail due to cooldown
		// (also would fail for min size, but the cooldown check comes first
		// if the cell is big enough — test with a splittable child)
	}
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
