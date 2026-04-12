package game

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/coords"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

func makeGrid3x3() []pkguniverse.CellID {
	var cells []pkguniverse.CellID
	for x := int32(-1); x <= 1; x++ {
		for y := int32(-1); y <= 1; y++ {
			cells = append(cells, pkguniverse.CellID{X: x, Y: y})
		}
	}
	return cells
}

func TestTopology_CenterHas8Neighbors(t *testing.T) {
	topo := pkguniverse.ComputeTopology(makeGrid3x3(), coords.CellSize)
	center := pkguniverse.CellID{X: 0, Y: 0}
	n := topo.Neighbors[center]
	if len(n) != 8 {
		t.Fatalf("center (0,0) should have 8 neighbors, got %d", len(n))
	}
}

func TestTopology_CornerHas3Neighbors(t *testing.T) {
	topo := pkguniverse.ComputeTopology(makeGrid3x3(), coords.CellSize)
	corner := pkguniverse.CellID{X: -1, Y: -1}
	n := topo.Neighbors[corner]
	if len(n) != 3 {
		t.Fatalf("corner (-1,-1) should have 3 neighbors, got %d", len(n))
	}
}

func TestTopology_EdgeHas5Neighbors(t *testing.T) {
	topo := pkguniverse.ComputeTopology(makeGrid3x3(), coords.CellSize)
	edge := pkguniverse.CellID{X: 0, Y: -1}
	n := topo.Neighbors[edge]
	if len(n) != 5 {
		t.Fatalf("edge (0,-1) should have 5 neighbors, got %d", len(n))
	}
}

func TestTopology_SingleCellNoNeighbors(t *testing.T) {
	topo := pkguniverse.ComputeTopology([]pkguniverse.CellID{{X: 0, Y: 0}}, coords.CellSize)
	n := topo.Neighbors[pkguniverse.CellID{X: 0, Y: 0}]
	if len(n) != 0 {
		t.Fatalf("single cell should have 0 neighbors, got %d", len(n))
	}
}

func TestCellID_Format(t *testing.T) {
	tests := []struct {
		coord pkguniverse.CellID
		want  string
	}{
		{pkguniverse.CellID{X: 0, Y: 0}, "cell_0_0"},
		{pkguniverse.CellID{X: 1, Y: 2}, "cell_1_2"},
		{pkguniverse.CellID{X: -1, Y: 0}, "cell_-1_0"},
		{pkguniverse.CellID{X: -3, Y: -7}, "cell_-3_-7"},
	}
	for _, tt := range tests {
		got := pkguniverse.MeshCellID(tt.coord)
		if got != tt.want {
			t.Errorf("pkguniverse.MeshCellID(%v) = %q, want %q", tt.coord, got, tt.want)
		}
	}
}
