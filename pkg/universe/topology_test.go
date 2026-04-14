package universe

import (
	"math/rand"
	"sort"
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

// TestRebuildNeighborsFor_MatchesFullRebuild asserts that the incremental
// neighbor rewire produces the same Topology.Neighbors map as a full
// ComputeTopology rebuild, for a variety of random topologies generated
// by a fixed-seed random walk of splits and merges.
//
// The incremental path is called by the cell-transfer commit helpers to
// avoid O(N) rewires. If it ever diverges from the full rebuild, the mesh
// can silently drop edges and break border replication — hence this
// property test.
func TestRebuildNeighborsFor_MatchesFullRebuild(t *testing.T) {
	const base float32 = 8192
	const trials = 50

	// Canonical starting topology: 4x4 depth-0 grid.
	initialCells := func() []CellID {
		out := make([]CellID, 0, 16)
		for y := int32(0); y < 4; y++ {
			for x := int32(0); x < 4; x++ {
				out = append(out, CellID{X: x, Y: y, Depth: 0})
			}
		}
		return out
	}

	rng := rand.New(rand.NewSource(1729))

	for trial := 0; trial < trials; trial++ {
		cells := initialCells()
		topo := ComputeTopology(cells, base)

		// Randomly split a handful of cells to produce a mixed-depth
		// topology.
		splits := rng.Intn(6) + 1
		for i := 0; i < splits; i++ {
			keys := topo.AllCells()
			if len(keys) == 0 {
				break
			}
			target := keys[rng.Intn(len(keys))]
			// Only split depth-0 and depth-1 cells, to avoid creating
			// a pathologically deep tree.
			if target.Depth >= 2 {
				continue
			}
			children := target.Children()
			topo.UpdateAfterSplit(target, children, base)

			// Verify: after a full rebuild with the current key set,
			// RebuildNeighborsFor(affected) produces the same result.
			allKeys := topo.AllCells()
			fullTopo := ComputeTopology(allKeys, base)

			affected := children[:]
			// Use a fresh copy of the incremental topo seeded with
			// empty neighbor slots for every key (mimicking a caller
			// that only knows the key set, not the edges). Then fill
			// via RebuildNeighborsFor.
			incremental := Topology{Neighbors: make(map[CellID][]CellID, len(allKeys))}
			for _, k := range allKeys {
				incremental.Neighbors[k] = nil
			}
			incremental.RebuildNeighborsFor(allKeys, base)
			assertNeighborsEqual(t, trial, "post-split", fullTopo.Neighbors, incremental.Neighbors)

			// Also assert the real topo (which had UpdateAfterSplit
			// applied) plus incremental RebuildNeighborsFor(affected)
			// lands on the same fullTopo.
			topo.RebuildNeighborsFor(affected, base)
			assertNeighborsEqual(t, trial, "incremental-split", fullTopo.Neighbors, topo.Neighbors)
		}
	}
}

func assertNeighborsEqual(t *testing.T, trial int, tag string, want, got map[CellID][]CellID) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("trial=%d %s: neighbor map size mismatch: want %d keys, got %d",
			trial, tag, len(want), len(got))
		return
	}
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Errorf("trial=%d %s: cell %v missing from incremental map", trial, tag, k)
			continue
		}
		if !sameCellSet(wv, gv) {
			wantSorted := append([]CellID(nil), wv...)
			gotSorted := append([]CellID(nil), gv...)
			sort.Slice(wantSorted, func(i, j int) bool { return cellLess(wantSorted[i], wantSorted[j]) })
			sort.Slice(gotSorted, func(i, j int) bool { return cellLess(gotSorted[i], gotSorted[j]) })
			t.Errorf("trial=%d %s: cell %v neighbors differ\n  want: %v\n  got:  %v",
				trial, tag, k, wantSorted, gotSorted)
		}
	}
}

func sameCellSet(a, b []CellID) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[CellID]int, len(a))
	for _, c := range a {
		seen[c]++
	}
	for _, c := range b {
		seen[c]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}

func cellLess(a, b CellID) bool {
	if a.Depth != b.Depth {
		return a.Depth < b.Depth
	}
	if a.X != b.X {
		return a.X < b.X
	}
	return a.Y < b.Y
}
