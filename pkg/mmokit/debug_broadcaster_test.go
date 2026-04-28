package mmokit

import (
	"testing"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/engine"
)

func TestDebugBroadcaster_BuildPayload_TopologyOnly(t *testing.T) {
	cells := []ClusterCellInfo{
		{Cell: CellID{X: 0, Y: 0, Depth: 0}, HostID: "host-a"},
	}
	got := buildDebugInfoPayload(cells, 500, engine.DebugTopology)
	if got.Topology == nil {
		t.Errorf("Topology should be populated when DebugTopology bit is set")
	}
	if got.AoiRadius == nil || *got.AoiRadius != 500 {
		t.Errorf("AoiRadius should be 500, got %v", got.AoiRadius)
	}
}

func TestDebugBroadcaster_BuildPayload_NoFlags(t *testing.T) {
	cells := []ClusterCellInfo{
		{Cell: CellID{X: 0, Y: 0, Depth: 0}, HostID: "host-a"},
	}
	got := buildDebugInfoPayload(cells, 500, 0)
	if got.Topology != nil {
		t.Errorf("Topology should be nil when DebugTopology bit is unset")
	}
	if got.AoiRadius != nil {
		t.Errorf("AoiRadius should be nil when DebugTopology bit is unset")
	}
}

func TestDebugBroadcaster_HashStable(t *testing.T) {
	cells := []ClusterCellInfo{
		{Cell: CellID{X: 0, Y: 0, Depth: 0}, HostID: "host-a"},
		{Cell: CellID{X: 1, Y: 0, Depth: 0}, HostID: "host-b"},
	}
	a := hashDebugPayload(cells, 500, engine.DebugTopology)
	b := hashDebugPayload(cells, 500, engine.DebugTopology)
	if a != b {
		t.Errorf("hashes should be stable: %d vs %d", a, b)
	}
}

func TestDebugBroadcaster_HashChangesOnTopologyChange(t *testing.T) {
	cellsA := []ClusterCellInfo{
		{Cell: CellID{X: 0, Y: 0}, HostID: "host-a"},
	}
	cellsB := []ClusterCellInfo{
		{Cell: CellID{X: 0, Y: 0}, HostID: "host-b"},
	}
	a := hashDebugPayload(cellsA, 500, engine.DebugTopology)
	b := hashDebugPayload(cellsB, 500, engine.DebugTopology)
	if a == b {
		t.Errorf("hash should differ when topology hostID differs")
	}
}

func TestDebugBroadcaster_HashChangesOnAoIChange(t *testing.T) {
	cells := []ClusterCellInfo{{Cell: CellID{X: 0, Y: 0}, HostID: "host-a"}}
	a := hashDebugPayload(cells, 500, engine.DebugTopology)
	b := hashDebugPayload(cells, 1000, engine.DebugTopology)
	if a == b {
		t.Errorf("hash should differ when AoI radius differs")
	}
}

func TestDebugBroadcaster_HashChangesOnFlagsChange(t *testing.T) {
	cells := []ClusterCellInfo{{Cell: CellID{X: 0, Y: 0}, HostID: "host-a"}}
	a := hashDebugPayload(cells, 500, 0)
	b := hashDebugPayload(cells, 500, engine.DebugTopology)
	if a == b {
		t.Errorf("hash should differ when flag set differs")
	}
}

func TestDebugBroadcaster_PayloadProtoType(t *testing.T) {
	cells := []ClusterCellInfo{{Cell: CellID{X: 1, Y: 2}, HostID: "host-a"}}
	msg := buildDebugInfoPayload(cells, 800, engine.DebugTopology)
	if _, ok := any(msg).(*enginepb.DebugInfoMsg); !ok {
		t.Errorf("buildDebugInfoPayload return type is wrong")
	}
}
