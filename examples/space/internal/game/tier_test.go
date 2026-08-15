package game

import (
	"testing"

	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/pkg/coords"
)

func TestTierForDist_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		dist float32
		want uint8
	}{
		{"origin", 0, 1},
		{"just inside T1", 100, 1},
		{"just below T2 boundary", 16383, 1},
		{"exactly T2 boundary", 16384, 2},
		{"middle of T2", 25000, 2},
		{"just below T3 boundary", 32767, 2},
		{"exactly T3 boundary", 32768, 3},
		{"deep T3", 100000, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tierForDist(tt.dist)
			if got != tt.want {
				t.Fatalf("tierForDist(%v) = %d, want %d", tt.dist, got, tt.want)
			}
		})
	}
}

func TestDistFromStation_AdjacentCells(t *testing.T) {
	station := mmokit.CellCoord{CellX: 0, CellY: 0}
	stationCenterX := coords.CellSize / 2

	got := distFromStation(mmokit.CellCoord{CellX: 1, CellY: 0}, stationCenterX, coords.CellSize/2, station)
	want := coords.CellSize
	if absf(got-want) > 1 {
		t.Fatalf("distFromStation eastNeighbor = %v, want ~%v", got, want)
	}
}

func TestTierForCellCenter_StationAndNeighbors(t *testing.T) {
	station := mmokit.CellCoord{CellX: 0, CellY: 0}

	if got := TierForCellCenter(station, station); got != 1 {
		t.Fatalf("station cell tier = %d, want 1", got)
	}
	if got := TierForCellCenter(mmokit.CellCoord{CellX: 1, CellY: 0}, station); got != 1 {
		t.Fatalf("immediate neighbor tier = %d, want 1", got)
	}
	if got := TierForCellCenter(mmokit.CellCoord{CellX: 3, CellY: 0}, station); got != 2 {
		t.Fatalf("3-cells-out tier = %d, want 2", got)
	}
	if got := TierForCellCenter(mmokit.CellCoord{CellX: 5, CellY: 0}, station); got != 3 {
		t.Fatalf("5-cells-out tier = %d, want 3", got)
	}
}

func absf(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
