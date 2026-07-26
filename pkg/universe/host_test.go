package universe

import "testing"

func TestHost_AddRemoveCell(t *testing.T) {
	h := NewHost("host-01")
	if h.ID != "host-01" {
		t.Fatalf("ID = %q, want host-01", h.ID)
	}
	if len(h.Cells) != 0 {
		t.Fatal("new host should have zero cells")
	}

	cell := NewCell("cell_0_0", CellID{X: 0, Y: 0})
	h.AddCell(CellID{X: 0, Y: 0}, cell)
	if len(h.Cells) != 1 {
		t.Fatalf("after AddCell: len = %d, want 1", len(h.Cells))
	}
	if h.Cells[CellID{X: 0, Y: 0}] != cell {
		t.Fatal("cell not found by CellID key")
	}

	h.RemoveCell(CellID{X: 0, Y: 0})
	if len(h.Cells) != 0 {
		t.Fatal("after RemoveCell: should be empty")
	}
}

func TestHost_IsLocal(t *testing.T) {
	h := NewHost("host-01")
	if !h.IsLocal("host-01") {
		t.Fatal("should be local for own ID")
	}
	if h.IsLocal("host-02") {
		t.Fatal("should not be local for other ID")
	}
}
