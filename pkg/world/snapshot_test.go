package world_test

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/world"
)

func TestBucketByCell_GroupsByWorldPos(t *testing.T) {
	prev := coords.CellSize
	defer func() { coords.CellSize = prev }()
	coords.CellSize = 8192

	s := &world.Snapshot{
		Stations: &world.Stations{Stations: []world.Station{
			{ID: "a", WorldPos: [2]float32{100, 200}},     // cell (0,0)
			{ID: "b", WorldPos: [2]float32{9000, 200}},    // cell (1,0)
			{ID: "c", WorldPos: [2]float32{-100, -100}},   // cell (-1,-1)
		}},
		POIs: &world.POIs{POIs: []world.POI{
			{ID: "p1", WorldPos: [2]float32{100, 200}, Tier: 1, Roster: "Starter"},
		}},
	}

	buckets := s.BucketByCell()

	if len(buckets[world.CellID{X: 0, Y: 0}].Stations) != 1 {
		t.Fatalf("cell (0,0) wanted 1 station, got %d", len(buckets[world.CellID{X: 0, Y: 0}].Stations))
	}
	if buckets[world.CellID{X: 0, Y: 0}].Stations[0].LocalPos != [2]float32{100, 200} {
		t.Fatalf("cell (0,0) station local pos wrong: %v", buckets[world.CellID{X: 0, Y: 0}].Stations[0].LocalPos)
	}
	if buckets[world.CellID{X: 1, Y: 0}].Stations[0].LocalPos[0] != 9000-8192 {
		t.Fatalf("cell (1,0) station local pos wrong: %v", buckets[world.CellID{X: 1, Y: 0}].Stations[0].LocalPos)
	}
	if buckets[world.CellID{X: -1, Y: -1}].Stations[0].LocalPos != [2]float32{8092, 8092} {
		t.Fatalf("cell (-1,-1) station local pos wrong: %v", buckets[world.CellID{X: -1, Y: -1}].Stations[0].LocalPos)
	}
	if len(buckets[world.CellID{X: 0, Y: 0}].POIs) != 1 {
		t.Fatalf("cell (0,0) wanted 1 POI, got %d", len(buckets[world.CellID{X: 0, Y: 0}].POIs))
	}
}

func TestBucketByCell_NilSlicesSafe(t *testing.T) {
	s := &world.Snapshot{}
	buckets := s.BucketByCell()
	if len(buckets) != 0 {
		t.Fatalf("empty snapshot should produce no buckets, got %d", len(buckets))
	}
}
