package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
)

func TestBorderDispatcher_TickNoCandidatesNoPanic(t *testing.T) {
	d := NewBorderDispatcher(nil, nil)
	d.Tick(0)
}

func TestBorderDispatcher_TickSkipsWithoutNeighbors(t *testing.T) {
	// Even if base is non-nil, an empty neighbor map is a no-op.
	// This test uses a nil base intentionally — the Phase 4 stub
	// must handle nil safely so Phase 5 and 6 integration tests
	// don't have to stand up a full mesh just to exercise tick code.
	d := NewBorderDispatcher(nil, map[string]*NodeViewer{})
	d.Tick(42)
}

func TestBorderDispatcher_TickIgnoresNilNeighbors(t *testing.T) {
	// A nil neighbor entry should be skipped, not panic.
	viewers := map[string]*NodeViewer{
		"node_1_0": nil,
	}
	d := NewBorderDispatcher(nil, viewers)
	d.Tick(1)
}

// TestBorderDispatcher_CornerEntityReachesAllNeighbors is a regression
// test for an asymmetric visibility bug observed in the space game: an
// entity near the shared corner of cells (0,0)/(1,0)/(0,1)/(1,1) was
// only reaching the diagonal neighbor, never the two adjacent cardinal
// neighbors.
//
// Root cause: the shared replication.Dispatcher.Walk applies two
// proximity filters in series:
//  1. BorderDispatcher.entityNearNeighborEdge — "is the entity in the
//     AoI-margin strip along the shared edge?" This is correct.
//  2. replication.InsideRadius — "is the entity within tier.Radius of
//     the viewer's *point* position?" The NodeViewer's position is the
//     midpoint of the shared edge, and the old default tier radius was
//     1000 units.
//
// For a cell of size 8192 and an entity near the corner at (8177, 8177)
// in cell (0,0), distance-to-edge-midpoint is:
//
//	right cardinal (1,0):   midpoint (8192, 4096) → ~4081
//	down cardinal (0,1):    midpoint (4096, 8192) → ~4081
//	diagonal (1,1):         midpoint (8192, 8192) → ~21
//
// So the 1000-unit disc filter passed only the diagonal, rejecting both
// cardinals even though the entity legitimately belongs in both their
// border strips. The fix extends NodeViewer's default tier radius to
// cover the source cell's diagonal so InsideRadius never drops an
// entity that passed entityNearNeighborEdge.
func TestBorderDispatcher_CornerEntityReachesAllNeighbors(t *testing.T) {
	// Use the production cell size so the bug is reproducible. The
	// 1000-unit default tier radius only fails to reach cardinal
	// neighbors when the edge is long enough that the corner sits
	// outside a 1000-radius disc around the edge midpoint — which
	// requires cellSize > ~2000.
	base := newTestWorldBase(t, CellID{X: 0, Y: 0})
	coords.SetCellSize(8192)
	defer coords.SetCellSize(1024) // restore the default other tests expect

	world := base.ECSWorld()
	posMap := ecs.NewMap1[component.Position](world)
	velMap := ecs.NewMap1[component.Velocity](world)
	nidMap := ecs.NewMap1[component.NetworkID](world)
	kindMap := ecs.NewMap1[component.EntityKind](world)
	colMap := ecs.NewMap1[component.Collider](world)

	// Asteroid sits 15 units inside the (+X, +Y) corner of cell (0,0).
	// With AoI margin 100 this passes nearRight AND nearTop, making it
	// a candidate for all three neighbors: (1,0), (0,1), and (1,1).
	cs := coords.CellSize
	corner := cs - 15
	ent := world.NewEntity()
	posMap.Add(ent, &component.Position{X: corner, Y: corner})
	velMap.Add(ent, &component.Velocity{})
	nidMap.Add(ent, &component.NetworkID{ID: 1, Epoch: 0})
	kindMap.Add(ent, &component.EntityKind{Type: 1})
	colMap.Add(ent, &component.Collider{Radius: 5})

	bd := NewBorderDispatcher(base, nil)

	cases := []struct {
		name   string
		dx, dy int32
	}{
		{"right cardinal", 1, 0},
		{"down cardinal", 0, 1},
		{"diagonal", 1, 1},
	}
	for _, tc := range cases {
		bx, by := neighborBoundaryMidpoint(CellID{X: 0, Y: 0}, tc.dx, tc.dy)
		nv := NewNodeViewer("neighbor", NodeViewerID("neighbor"), bx, by, nil, nil, nil)
		nv.SetDirection(tc.dx, tc.dy)

		// Drive Walk directly so we can inspect the produced frame
		// without needing a real destination node for NodeViewer.Send.
		cands := bd.candidatesFor(nv)
		frame := bd.disp.Walk(nv, 1, cands)

		if len(frame.Entries) == 0 {
			t.Errorf("%s neighbor: BorderDispatcher dropped the corner entity that should be visible to it", tc.name)
			continue
		}
		if got := frame.Entries[0].NetID.ID; got != 1 {
			t.Errorf("%s neighbor: unexpected netID in frame: got %d, want 1", tc.name, got)
		}
	}
}
