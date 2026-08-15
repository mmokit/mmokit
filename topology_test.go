package mmokit

import (
	"testing"
)

// fakeTopoWorld implements just enough for BuildCellTopology.
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

func TestBuildCellTopology_EmitsAllCells(t *testing.T) {
	w := &fakeTopoWorld{
		gridX: 2, gridY: 2, cs: 1000,
		cells: []ClusterCellInfo{
			{Cell: CellID{X: 0, Y: 0, Depth: 0}, HostID: "h0"},
			{Cell: CellID{X: 1, Y: 1, Depth: 0}, HostID: "h1"},
		},
	}
	msg := BuildCellTopology(w)
	if msg.GridW != 2 || msg.GridH != 2 {
		t.Fatalf("grid = %dx%d, want 2x2", msg.GridW, msg.GridH)
	}
	if len(msg.Cells) != 2 {
		t.Fatalf("len(Cells) = %d, want 2", len(msg.Cells))
	}
	if msg.Cells[0].NodeID != "h0" {
		t.Errorf("first cell host = %q, want h0", msg.Cells[0].NodeID)
	}
}
