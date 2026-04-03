package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/coords"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

func makeGrid3x3() []coords.CellCoord {
	var cells []coords.CellCoord
	for x := int32(-1); x <= 1; x++ {
		for y := int32(-1); y <= 1; y++ {
			cells = append(cells, coords.CellCoord{CellX: x, CellY: y})
		}
	}
	return cells
}

func TestTopology_CenterHas8Neighbors(t *testing.T) {
	topo := pkguniverse.ComputeTopology(makeGrid3x3())
	center := coords.CellCoord{CellX: 0, CellY: 0}
	n := topo.Neighbors[center]
	if len(n) != 8 {
		t.Fatalf("center (0,0) should have 8 neighbors, got %d", len(n))
	}
}

func TestTopology_CornerHas3Neighbors(t *testing.T) {
	topo := pkguniverse.ComputeTopology(makeGrid3x3())
	corner := coords.CellCoord{CellX: -1, CellY: -1}
	n := topo.Neighbors[corner]
	if len(n) != 3 {
		t.Fatalf("corner (-1,-1) should have 3 neighbors, got %d", len(n))
	}
}

func TestTopology_EdgeHas5Neighbors(t *testing.T) {
	topo := pkguniverse.ComputeTopology(makeGrid3x3())
	edge := coords.CellCoord{CellX: 0, CellY: -1}
	n := topo.Neighbors[edge]
	if len(n) != 5 {
		t.Fatalf("edge (0,-1) should have 5 neighbors, got %d", len(n))
	}
}

func TestTopology_SingleCellNoNeighbors(t *testing.T) {
	topo := pkguniverse.ComputeTopology([]coords.CellCoord{{CellX: 0, CellY: 0}})
	n := topo.Neighbors[coords.CellCoord{CellX: 0, CellY: 0}]
	if len(n) != 0 {
		t.Fatalf("single cell should have 0 neighbors, got %d", len(n))
	}
}

func TestCellID_Format(t *testing.T) {
	tests := []struct {
		coord coords.CellCoord
		want  string
	}{
		{coords.CellCoord{CellX: 0, CellY: 0}, "node_0_0"},
		{coords.CellCoord{CellX: 1, CellY: 2}, "node_1_2"},
		{coords.CellCoord{CellX: -1, CellY: 0}, "node_-1_0"},
		{coords.CellCoord{CellX: -3, CellY: -7}, "node_-3_-7"},
	}
	for _, tt := range tests {
		got := pkguniverse.MeshNodeID(tt.coord)
		if got != tt.want {
			t.Errorf("pkguniverse.MeshNodeID(%v) = %q, want %q", tt.coord, got, tt.want)
		}
	}
}
