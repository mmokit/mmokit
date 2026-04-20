package universe

import (
	"strings"
	"testing"
)

func TestInvariant_CoordMapsConsistent_OK(t *testing.T) {
	c := &Process{
		Cells:     make(map[string]*Cell),
		CellOwner: make(map[CellID]string),
	}
	cell := CellID{X: 0, Y: 0}
	c.Cells["cell_0_0"] = &Cell{Cell: cell, ID: "cell_0_0"}
	c.CellOwner[cell] = "cell_0_0"

	if err := invCoordMapsConsistent.Check(c); err != nil {
		t.Fatalf("expected OK, got %v", err)
	}
}

func TestInvariant_CoordMapsConsistent_MissingCellOwner(t *testing.T) {
	c := &Process{
		Cells:     make(map[string]*Cell),
		CellOwner: make(map[CellID]string),
	}
	cell := CellID{X: 0, Y: 0}
	c.Cells["cell_0_0"] = &Cell{Cell: cell, ID: "cell_0_0"}
	// Deliberately leave CellOwner empty.

	err := invCoordMapsConsistent.Check(c)
	if err == nil {
		t.Fatal("expected violation, got nil")
	}
	if !strings.Contains(err.Error(), "cell_0_0") {
		t.Fatalf("error should mention the offending cell, got %v", err)
	}
}
