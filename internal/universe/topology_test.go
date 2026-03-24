package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/coords"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

func makeGrid3x3() []coords.SectorCoord {
	var sectors []coords.SectorCoord
	for x := int32(-1); x <= 1; x++ {
		for y := int32(-1); y <= 1; y++ {
			sectors = append(sectors, coords.SectorCoord{SX: x, SY: y})
		}
	}
	return sectors
}

func TestTopology_CenterHas8Neighbors(t *testing.T) {
	topo := pkguniverse.ComputeTopology(makeGrid3x3())
	center := coords.SectorCoord{SX: 0, SY: 0}
	n := topo.Neighbors[center]
	if len(n) != 8 {
		t.Fatalf("center (0,0) should have 8 neighbors, got %d", len(n))
	}
}

func TestTopology_CornerHas3Neighbors(t *testing.T) {
	topo := pkguniverse.ComputeTopology(makeGrid3x3())
	corner := coords.SectorCoord{SX: -1, SY: -1}
	n := topo.Neighbors[corner]
	if len(n) != 3 {
		t.Fatalf("corner (-1,-1) should have 3 neighbors, got %d", len(n))
	}
}

func TestTopology_EdgeHas5Neighbors(t *testing.T) {
	topo := pkguniverse.ComputeTopology(makeGrid3x3())
	edge := coords.SectorCoord{SX: 0, SY: -1}
	n := topo.Neighbors[edge]
	if len(n) != 5 {
		t.Fatalf("edge (0,-1) should have 5 neighbors, got %d", len(n))
	}
}

func TestTopology_SingleSectorNoNeighbors(t *testing.T) {
	topo := pkguniverse.ComputeTopology([]coords.SectorCoord{{SX: 0, SY: 0}})
	n := topo.Neighbors[coords.SectorCoord{SX: 0, SY: 0}]
	if len(n) != 0 {
		t.Fatalf("single sector should have 0 neighbors, got %d", len(n))
	}
}

func TestSectorID_Format(t *testing.T) {
	tests := []struct {
		coord coords.SectorCoord
		want  string
	}{
		{coords.SectorCoord{SX: 0, SY: 0}, "node_0_0"},
		{coords.SectorCoord{SX: 1, SY: 2}, "node_1_2"},
		{coords.SectorCoord{SX: -1, SY: 0}, "node_-1_0"},
		{coords.SectorCoord{SX: -3, SY: -7}, "node_-3_-7"},
	}
	for _, tt := range tests {
		got := pkguniverse.SectorID(tt.coord)
		if got != tt.want {
			t.Errorf("pkguniverse.SectorID(%v) = %q, want %q", tt.coord, got, tt.want)
		}
	}
}
