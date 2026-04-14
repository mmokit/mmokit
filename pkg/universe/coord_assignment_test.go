package universe

import (
	"reflect"
	"sort"
	"testing"

	"github.com/zenion/mmoserver/pkg/logger"
)

// newAssignmentEngineForTest builds a minimal assignmentEngine suitable
// for exercising the pure read-only helpers (enumerateCells, rebalance
// input shaping) without bringing up a gRPC control server. The caller
// is responsible for seeding coord.cellToHostMap and coord.cfg.
func newAssignmentEngineForTest() *assignmentEngine {
	c := &Coordinator{
		cellToHostMap: make(map[string]string),
		Log:           logger.New(),
	}
	return &assignmentEngine{
		coord: c,
		log:   c.Log,
	}
}

// TestEnumerateCellsColdStart covers the cold-start path: when
// cellToHostMap is empty (no cells have been committed yet), the
// engine must fall back to walking the configured depth-0 grid so the
// first-ever assignment pass has something to hand out.
func TestEnumerateCellsColdStart(t *testing.T) {
	e := newAssignmentEngineForTest()
	e.coord.cfg = Config{CellsX: 2, CellsY: 2}

	got := e.enumerateCells()
	sort.Strings(got)

	want := []string{
		MeshCellID(CellID{X: 0, Y: 0}),
		MeshCellID(CellID{X: 0, Y: 1}),
		MeshCellID(CellID{X: 1, Y: 0}),
		MeshCellID(CellID{X: 1, Y: 1}),
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cold-start enumerateCells:\n  got=%v\n  want=%v", got, want)
	}
}

// TestEnumerateCellsReadsCellToHostMap covers the normal path: once
// cellToHostMap has been populated (by Build() or by a control-plane
// CellReady commit), enumerateCells must return exactly those keys —
// NOT the configured grid. This is the foundation of quadtree-aware
// rebalance: after a split the child cell IDs live in cellToHostMap
// and the depth-0 grid walk would miss them entirely.
func TestEnumerateCellsReadsCellToHostMap(t *testing.T) {
	e := newAssignmentEngineForTest()
	// Set a cfg that differs from the map contents — the map wins.
	e.coord.cfg = Config{CellsX: 4, CellsY: 4}

	want := []string{
		MeshCellID(CellID{X: 0, Y: 0}),
		MeshCellID(CellID{X: 1, Y: 0}),
	}
	for _, id := range want {
		e.coord.cellToHostMap[id] = "host-a"
	}
	got := e.enumerateCells()
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("enumerateCells should read cellToHostMap, not cfg grid:\n  got=%v\n  want=%v", got, want)
	}
}

// TestEnumerateCellsIncludesDepth1Children is the T2 regression guard.
// After a split commit, the parent's 4 children appear in
// cellToHostMap under their depth-1 MeshCellIDs ("cell_d1_..."). The
// rebalance pass must enumerate those child IDs so it can assign /
// rebalance them — the pre-T2 depth-0 grid walk silently dropped every
// post-split cell and left splits unassignable.
func TestEnumerateCellsIncludesDepth1Children(t *testing.T) {
	e := newAssignmentEngineForTest()
	e.coord.cfg = Config{CellsX: 2, CellsY: 2}

	// Simulate a split of CellID{0,0,0} committed into the map.
	parent := CellID{X: 0, Y: 0, Depth: 0}
	children := parent.Children()
	// The other three depth-0 cells are still whole.
	depth0Siblings := []CellID{
		{X: 1, Y: 0, Depth: 0},
		{X: 0, Y: 1, Depth: 0},
		{X: 1, Y: 1, Depth: 0},
	}

	for _, s := range depth0Siblings {
		e.coord.cellToHostMap[MeshCellID(s)] = "host-a"
	}
	for _, child := range children {
		e.coord.cellToHostMap[MeshCellID(child)] = "host-a"
	}

	got := e.enumerateCells()

	// Must contain all 4 depth-1 children (the split output).
	for _, child := range children {
		childID := MeshCellID(child)
		found := false
		for _, g := range got {
			if g == childID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("enumerateCells missing depth-1 child %s (split output); got=%v", childID, got)
		}
	}
	// Must contain the 3 intact depth-0 siblings.
	for _, s := range depth0Siblings {
		sID := MeshCellID(s)
		found := false
		for _, g := range got {
			if g == sID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("enumerateCells missing depth-0 sibling %s; got=%v", sID, got)
		}
	}
	// Must NOT contain the parent — splitting removes it from the live map.
	parentID := MeshCellID(parent)
	for _, g := range got {
		if g == parentID {
			t.Errorf("enumerateCells still contains split parent %s; got=%v", parentID, got)
		}
	}
	// Total count: 3 intact siblings + 4 children = 7.
	if len(got) != 7 {
		t.Errorf("enumerateCells count = %d, want 7 (3 siblings + 4 children); got=%v", len(got), got)
	}
}

// TestCellIDNeighborsDepth0 locks in the Moore-neighborhood layout used
// by the rendezvous locality bias. Eight neighbors, no duplicates, same
// depth, no wrap-around — if any of these invariants break the
// locality bonus silently stops finding adjacent cells.
func TestCellIDNeighborsDepth0(t *testing.T) {
	c := CellID{X: 5, Y: 5, Depth: 0}
	got := c.Neighbors()
	if len(got) != 8 {
		t.Fatalf("Neighbors len = %d, want 8", len(got))
	}
	want := map[CellID]bool{
		{X: 4, Y: 4, Depth: 0}: true,
		{X: 5, Y: 4, Depth: 0}: true,
		{X: 6, Y: 4, Depth: 0}: true,
		{X: 4, Y: 5, Depth: 0}: true,
		{X: 6, Y: 5, Depth: 0}: true,
		{X: 4, Y: 6, Depth: 0}: true,
		{X: 5, Y: 6, Depth: 0}: true,
		{X: 6, Y: 6, Depth: 0}: true,
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected neighbor %+v", n)
		}
		delete(want, n)
	}
	if len(want) != 0 {
		t.Errorf("missing neighbors: %v", want)
	}
}

// TestCellIDNeighborsDepth1 covers the depth-preserving property that
// makes the locality bias meaningful for split children: a depth-1
// cell's neighbors stay at depth 1, not depth 0. The rendezvous bias
// only matches on MeshCellID string equality, so depth mismatch would
// silently turn the bonus off for split children.
func TestCellIDNeighborsDepth1(t *testing.T) {
	c := CellID{X: 3, Y: 3, Depth: 1}
	for _, n := range c.Neighbors() {
		if n.Depth != 1 {
			t.Errorf("depth-1 neighbor depth = %d, want 1 (cell=%+v)", n.Depth, n)
		}
	}
}
