package universe

import (
	"testing"
)

func TestComputeTopology_2x2(t *testing.T) {
	cells := []CellID{
		{0, 0, 0}, {1, 0, 0},
		{0, 1, 0}, {1, 1, 0},
	}
	topo := ComputeTopology(cells, 8192)

	// Center-ish: each cell in a 2x2 has 3 neighbors
	for _, c := range cells {
		if len(topo.Neighbors[c]) != 3 {
			t.Errorf("cell %v has %d neighbors, want 3", c, len(topo.Neighbors[c]))
		}
	}
}

func TestComputeTopology_3x3Center(t *testing.T) {
	var cells []CellID
	for y := int32(0); y < 3; y++ {
		for x := int32(0); x < 3; x++ {
			cells = append(cells, CellID{x, y, 0})
		}
	}
	topo := ComputeTopology(cells, 8192)

	center := CellID{1, 1, 0}
	if len(topo.Neighbors[center]) != 8 {
		t.Errorf("center has %d neighbors, want 8", len(topo.Neighbors[center]))
	}

	corner := CellID{0, 0, 0}
	if len(topo.Neighbors[corner]) != 3 {
		t.Errorf("corner has %d neighbors, want 3", len(topo.Neighbors[corner]))
	}
}

func TestTopology_UpdateAfterSplit(t *testing.T) {
	base := float32(8192)

	// Start with a 2x2 grid
	cells := []CellID{
		{0, 0, 0}, {1, 0, 0},
		{0, 1, 0}, {1, 1, 0},
	}
	topo := ComputeTopology(cells, base)

	// Split cell (0,0,0) into 4 children
	parent := CellID{0, 0, 0}
	children := parent.Children()
	topo.UpdateAfterSplit(parent, children, base)

	// Parent should be gone
	if _, ok := topo.Neighbors[parent]; ok {
		t.Error("parent should be removed from topology")
	}

	// All 4 children should exist
	for _, c := range children {
		if _, ok := topo.Neighbors[c]; !ok {
			t.Errorf("child %v should be in topology", c)
		}
	}

	// Children should be neighbors of each other (all 4 in a 2x2 are adjacent)
	for _, c := range children {
		siblingCount := 0
		for _, n := range topo.Neighbors[c] {
			for _, s := range children {
				if n == s {
					siblingCount++
				}
			}
		}
		if siblingCount != 3 {
			t.Errorf("child %v has %d sibling neighbors, want 3", c, siblingCount)
		}
	}

	// depth-1 child {1,0,1} (right half, bottom) should neighbor depth-0 cell {1,0,0}
	rightBottom := children[1] // {1, 0, 1}
	hasNeighbor := false
	for _, n := range topo.Neighbors[rightBottom] {
		if n == (CellID{1, 0, 0}) {
			hasNeighbor = true
		}
	}
	if !hasNeighbor {
		t.Errorf("child %v should neighbor (1,0,0), neighbors: %v", rightBottom, topo.Neighbors[rightBottom])
	}

	// depth-0 cell {1,0,0} should now neighbor the depth-1 children that touch it
	neighborCell := CellID{1, 0, 0}
	childNeighborCount := 0
	for _, n := range topo.Neighbors[neighborCell] {
		for _, c := range children {
			if n == c {
				childNeighborCount++
			}
		}
	}
	// {1,0,1} and {1,1,1} touch the right edge of the split cell
	if childNeighborCount != 2 {
		t.Errorf("cell (1,0,0) has %d child neighbors, want 2, neighbors: %v", childNeighborCount, topo.Neighbors[neighborCell])
	}

	// (1,0,0) should NOT still reference the parent
	for _, n := range topo.Neighbors[neighborCell] {
		if n == parent {
			t.Error("cell (1,0,0) should not reference removed parent")
		}
	}
}

func TestTopology_UpdateAfterMerge(t *testing.T) {
	base := float32(8192)

	// Start with a 2x2 grid, split (0,0,0), then merge it back
	cells := []CellID{
		{0, 0, 0}, {1, 0, 0},
		{0, 1, 0}, {1, 1, 0},
	}
	topo := ComputeTopology(cells, base)

	parent := CellID{0, 0, 0}
	children := parent.Children()

	// Split
	topo.UpdateAfterSplit(parent, children, base)

	// Merge back
	topo.UpdateAfterMerge(children, parent, base)

	// Parent should be back
	if _, ok := topo.Neighbors[parent]; !ok {
		t.Error("parent should be back in topology after merge")
	}

	// Children should be gone
	for _, c := range children {
		if _, ok := topo.Neighbors[c]; ok {
			t.Errorf("child %v should be removed after merge", c)
		}
	}

	// Parent should have 3 neighbors (same as original 2x2)
	if len(topo.Neighbors[parent]) != 3 {
		t.Errorf("parent has %d neighbors after merge, want 3", len(topo.Neighbors[parent]))
	}

	// Neighbor (1,0,0) should reference parent, not children
	neighborCell := CellID{1, 0, 0}
	hasParent := false
	for _, n := range topo.Neighbors[neighborCell] {
		if n == parent {
			hasParent = true
		}
		for _, c := range children {
			if n == c {
				t.Errorf("neighbor (1,0,0) still references child %v", c)
			}
		}
	}
	if !hasParent {
		t.Error("neighbor (1,0,0) should reference parent after merge")
	}
}

func TestTopology_SplitMerge_RoundTrip(t *testing.T) {
	base := float32(8192)

	cells := []CellID{
		{0, 0, 0}, {1, 0, 0},
		{0, 1, 0}, {1, 1, 0},
	}
	original := ComputeTopology(cells, base)

	parent := CellID{0, 0, 0}
	children := parent.Children()

	topo := ComputeTopology(cells, base)
	topo.UpdateAfterSplit(parent, children, base)
	topo.UpdateAfterMerge(children, parent, base)

	// After round-trip, topology should match original
	for _, c := range cells {
		if len(topo.Neighbors[c]) != len(original.Neighbors[c]) {
			t.Errorf("cell %v: %d neighbors after round-trip, want %d",
				c, len(topo.Neighbors[c]), len(original.Neighbors[c]))
		}
	}
}
