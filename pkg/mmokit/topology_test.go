package mmokit

import (
	"testing"
)

// fakeTopoWorld implements just enough for BuildCellTopologyMsg.
type fakeTopoWorld struct {
	cells []ClusterCellInfo
	gridX uint32
	gridY uint32
	cs    float32
}

func (f *fakeTopoWorld) Topology() []ClusterCellInfo { return f.cells }
func (f *fakeTopoWorld) GridDimensions() (uint32, uint32, float32) {
	return f.gridX, f.gridY, f.cs
}

func TestBuildCellTopologyMsg_EmitsAllCells(t *testing.T) {
	w := &fakeTopoWorld{
		gridX: 2, gridY: 2, cs: 1000,
		cells: []ClusterCellInfo{
			{Cell: CellID{X: 0, Y: 0, Depth: 0}, HostID: "h0"},
			{Cell: CellID{X: 1, Y: 1, Depth: 0}, HostID: "h1"},
		},
	}
	msg := BuildCellTopologyMsg(w)
	if msg.GridW != 2 || msg.GridH != 2 {
		t.Fatalf("grid = %dx%d, want 2x2", msg.GridW, msg.GridH)
	}
	if len(msg.Cells) != 2 {
		t.Fatalf("len(Cells) = %d, want 2", len(msg.Cells))
	}
	if msg.Cells[0].NodeId != "h0" {
		t.Errorf("first cell host = %q, want h0", msg.Cells[0].NodeId)
	}
}

// TestHashTopology_StableAcrossOrderings pins the broadcaster's key
// invariant: the same cluster topology produces the same hash regardless
// of input slice order. ClusterCells() iterates a Go map and so returns
// elements in non-deterministic order; without the in-place sort in
// hashTopology, every tick would compute a fresh hash and SE_CELL_TOPOLOGY
// would be re-sent at 20 Hz to every active player.
func TestHashTopology_StableAcrossOrderings(t *testing.T) {
	cells1 := []ClusterCellInfo{
		{Cell: CellID{X: 0, Y: 0, Depth: 0}, HostID: "h0"},
		{Cell: CellID{X: 1, Y: 1, Depth: 0}, HostID: "h1"},
		{Cell: CellID{X: 0, Y: 1, Depth: 0}, HostID: "h0"},
	}
	cells2 := []ClusterCellInfo{
		{Cell: CellID{X: 1, Y: 1, Depth: 0}, HostID: "h1"},
		{Cell: CellID{X: 0, Y: 1, Depth: 0}, HostID: "h0"},
		{Cell: CellID{X: 0, Y: 0, Depth: 0}, HostID: "h0"},
	}
	h1 := hashTopology(cells1)
	h2 := hashTopology(cells2)
	if h1 != h2 {
		t.Errorf("hashTopology should be order-independent, got h1=%x h2=%x", h1, h2)
	}
}

// TestHashTopology_DistinguishesHostChange pins that a host change
// produces a different hash. The broadcaster relies on this to detect
// host-reassignment after a cell migration.
func TestHashTopology_DistinguishesHostChange(t *testing.T) {
	cells1 := []ClusterCellInfo{
		{Cell: CellID{X: 0, Y: 0, Depth: 0}, HostID: "h0"},
	}
	cells2 := []ClusterCellInfo{
		{Cell: CellID{X: 0, Y: 0, Depth: 0}, HostID: "h1"},
	}
	if hashTopology(cells1) == hashTopology(cells2) {
		t.Error("hashTopology must distinguish host changes")
	}
}
