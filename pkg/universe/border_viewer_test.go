package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/replication"
)

func TestNodeViewer_SatisfiesInterface(t *testing.T) {
	var _ replication.Viewer = (*NodeViewer)(nil)
}

func TestNodeViewer_Position(t *testing.T) {
	v := NewNodeViewer("node_1_0", 123, 50, 75, nil, nil, nil)
	if v.ID() != 123 {
		t.Fatalf("ID: got %d want 123", v.ID())
	}
	x, y := v.Position()
	if x != 50 || y != 75 {
		t.Fatalf("Position: got (%v,%v) want (50,75)", x, y)
	}
}

func TestNodeViewer_DefaultTier(t *testing.T) {
	v := NewNodeViewer("node_1_0", 123, 0, 0, nil, nil, nil)
	tier := v.Tier(0)
	if tier.Radius == 0 {
		t.Fatal("default tier should have non-zero radius")
	}
}

func TestNodeViewer_BaselinesAllocated(t *testing.T) {
	v := NewNodeViewer("node_1_0", 123, 0, 0, nil, nil, nil)
	if v.Baselines() == nil {
		t.Fatal("NodeViewer.Baselines() should return a pre-allocated store, never nil")
	}
}

func TestNodeViewerID_StableAndCollisionFree(t *testing.T) {
	// Player connection IDs are small uint32 values. NodeViewerID must
	// tag neighbor IDs with the high bit so they can never collide
	// with a zero-extended player conn ID.
	id := NodeViewerID("node_1_0")
	if id>>63 != 1 {
		t.Fatalf("NodeViewerID(%q) must have the high bit set; got %#x", "node_1_0", id)
	}

	// Same input yields same output.
	if NodeViewerID("node_1_0") != id {
		t.Fatal("NodeViewerID must be deterministic")
	}

	// Different inputs yield different outputs.
	if NodeViewerID("node_0_1") == id {
		t.Fatal("NodeViewerID should differ for different node IDs")
	}
}
