package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/replication"
)

func TestCellViewer_SatisfiesInterface(t *testing.T) {
	var _ replication.Viewer = (*CellViewer)(nil)
}

func TestCellViewer_Position(t *testing.T) {
	v := NewCellViewer("cell_1_0", 123, 50, 75, nil, nil, nil)
	if v.ID() != 123 {
		t.Fatalf("ID: got %d want 123", v.ID())
	}
	x, y := v.Position()
	if x != 50 || y != 75 {
		t.Fatalf("Position: got (%v,%v) want (50,75)", x, y)
	}
}

func TestCellViewer_DefaultTier(t *testing.T) {
	v := NewCellViewer("cell_1_0", 123, 0, 0, nil, nil, nil)
	tier := v.Tier(0)
	if tier.Radius == 0 {
		t.Fatal("default tier should have non-zero radius")
	}
}

func TestCellViewer_BaselinesAllocated(t *testing.T) {
	v := NewCellViewer("cell_1_0", 123, 0, 0, nil, nil, nil)
	if v.Baselines() == nil {
		t.Fatal("CellViewer.Baselines() should return a pre-allocated store, never nil")
	}
}

func TestCellViewerID_StableAndCollisionFree(t *testing.T) {
	// Player connection IDs are small uint32 values. CellViewerID must
	// tag neighbor IDs with the high bit so they can never collide
	// with a zero-extended player conn ID.
	id := CellViewerID("cell_1_0")
	if id>>63 != 1 {
		t.Fatalf("CellViewerID(%q) must have the high bit set; got %#x", "cell_1_0", id)
	}

	// Same input yields same output.
	if CellViewerID("cell_1_0") != id {
		t.Fatal("CellViewerID must be deterministic")
	}

	// Different inputs yield different outputs.
	if CellViewerID("cell_0_1") == id {
		t.Fatal("CellViewerID should differ for different node IDs")
	}
}
