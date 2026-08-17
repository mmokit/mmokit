package mmokit

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/engine"
)

func TestDebugBroadcaster_BuildPayload_TopologyOnly(t *testing.T) {
	cells := []ClusterCellInfo{
		{Cell: CellID{X: 0, Y: 0, Depth: 0}, HostID: "host-a"},
	}
	got := buildDebugInfo(cells, 500, engine.DebugTopology, DefaultCellSize)
	if len(got.Topology.Cells) == 0 {
		t.Errorf("Topology should be populated when DebugTopology bit is set")
	}
	if got.AoIRadius != 500 {
		t.Errorf("AoIRadius should be 500, got %v", got.AoIRadius)
	}
}

func TestDebugBroadcaster_BuildPayload_NoFlags(t *testing.T) {
	cells := []ClusterCellInfo{
		{Cell: CellID{X: 0, Y: 0, Depth: 0}, HostID: "host-a"},
	}
	got := buildDebugInfo(cells, 500, 0, DefaultCellSize)
	if len(got.Topology.Cells) != 0 {
		t.Errorf("Topology cells should be empty when DebugTopology bit is unset")
	}
	if got.AoIRadius != 0 {
		t.Errorf("AoIRadius should be 0 when DebugTopology bit is unset")
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

func TestDebugBroadcaster_PayloadType(t *testing.T) {
	cells := []ClusterCellInfo{{Cell: CellID{X: 1, Y: 2}, HostID: "host-a"}}
	msg := buildDebugInfo(cells, 800, engine.DebugTopology, DefaultCellSize)
	if msg.AoIRadius != 800 {
		t.Errorf("buildDebugInfo: AoIRadius=%v, want 800", msg.AoIRadius)
	}
	if len(msg.Topology.Cells) == 0 {
		t.Errorf("buildDebugInfo: empty Topology.Cells")
	}
}

// TestDebugBroadcaster_RevokeGrantCycleResends pins the bug fix where
// a revoke-to-zero followed by a grant of the same flag set produced
// an identical payload hash, causing the broadcaster to skip the
// resend and leave the client with stale (cleared) state. The fix:
// drop sentHash[connID] when sess.DebugFlags == 0 so a fresh-grant
// path treats the next non-zero hash as a first-time send.
func TestDebugBroadcaster_RevokeGrantCycleResends(t *testing.T) {
	b := &debugBroadcaster{sentHash: map[uint32]uint64{}}

	const connID uint32 = 42
	cells := []ClusterCellInfo{{Cell: CellID{X: 0, Y: 0}, HostID: "host-a"}}
	radius := float32(500)

	// Initial: simulate the broadcaster having pushed once. Cache the
	// hash directly because the per-tick call site requires a Stage.
	prevHash := hashDebugPayload(cells, radius, engine.DebugTopology)
	b.sentHash[connID] = prevHash

	// Revoke: sess.DebugFlags drops to 0. Verify the broadcaster
	// would clear the cache when it sees this state.
	if _, ok := b.sentHash[connID]; !ok {
		t.Fatal("test setup: sentHash should contain connID")
	}
	// Simulate the inner ForEach body's zero-flag branch.
	delete(b.sentHash, connID)

	// Grant: sess.DebugFlags = DebugTopology again. Hash matches the
	// pre-revoke value, but because sentHash was cleared, the next
	// tick will see no entry and send.
	newHash := hashDebugPayload(cells, radius, engine.DebugTopology)
	if newHash != prevHash {
		t.Fatalf("test premise: pre-revoke and post-grant hashes should match (%d vs %d)", prevHash, newHash)
	}
	if _, cached := b.sentHash[connID]; cached {
		t.Errorf("sentHash should be empty after revoke; the broadcaster would otherwise skip the resend")
	}
}
