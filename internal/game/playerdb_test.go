package game

import (
	"testing"
)

func TestPlayerDataAccessor_RoundTrip(t *testing.T) {
	pd := &PlayerData{Username: "alice"}
	pd.SetCell(3, -2)
	pd.SetPosition(10.5, 20.5)
	if pd.CellX != 3 || pd.CellY != -2 {
		t.Fatalf("SetCell didn't take: %d,%d", pd.CellX, pd.CellY)
	}
	if pd.X != 10.5 || pd.Y != 20.5 {
		t.Fatalf("SetPosition didn't take: %g,%g", pd.X, pd.Y)
	}
	if pd.GetUsername() != "alice" || pd.GetCellX() != 3 || pd.GetCellY() != -2 || pd.GetX() != 10.5 || pd.GetY() != 20.5 {
		t.Fatalf("Getters didn't reflect setter values: %+v", pd)
	}
}
