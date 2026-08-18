package coords

import (
	"math"
	"testing"
)

// FromFlat is what survives of this package's coordinate maths after CE-010.
//
// Normalize, RelativeOffset, Distance and WorldPos.ToFlat were deleted rather
// than converted: each would have needed a cell-size parameter threaded through
// it, and none had a single caller outside this file's own tests. Their tests
// went with them — a round-trip test whose other half no longer exists is not
// coverage, it is a reason to keep dead code alive.
func TestFromFlat(t *testing.T) {
	const cellSize float32 = 8192

	tests := []struct {
		name       string
		x, y       float64
		wantCellX  int32
		wantCellY  int32
		wantLocalX float32
		wantLocalY float32
	}{
		{"origin", 0, 0, 0, 0, 0, 0},
		{"inside the first cell", 12345.678, 9876.543, 1, 1, 4153.678, 1684.543},
		// Negative coordinates floor toward minus infinity rather than
		// truncating toward zero, so the local offset stays in [0, cellSize).
		{"negative", -5000.5, -12000.25, -1, -2, 3191.5, 4383.75},
		{"exact cell boundary", 8192, 16384, 1, 2, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromFlat(tt.x, tt.y, cellSize)
			if got.CellX != tt.wantCellX || got.CellY != tt.wantCellY {
				t.Fatalf("cell = (%d,%d), want (%d,%d)", got.CellX, got.CellY, tt.wantCellX, tt.wantCellY)
			}
			if math.Abs(float64(got.LocalX-tt.wantLocalX)) > 0.01 ||
				math.Abs(float64(got.LocalY-tt.wantLocalY)) > 0.01 {
				t.Fatalf("local = (%.4f,%.4f), want (%.4f,%.4f)",
					got.LocalX, got.LocalY, tt.wantLocalX, tt.wantLocalY)
			}
			// The local offset must always land inside the cell, which is the
			// invariant the floor-vs-truncate distinction exists to preserve.
			if got.LocalX < 0 || got.LocalX >= cellSize || got.LocalY < 0 || got.LocalY >= cellSize {
				t.Fatalf("local offset (%.4f,%.4f) outside [0,%v)", got.LocalX, got.LocalY, cellSize)
			}
		})
	}
}

// The size is a parameter, so two callers can use different geometries in one
// process — the property CE-010 exists to provide.
func TestFromFlat_HonoursTheSizeItIsGiven(t *testing.T) {
	at8192 := FromFlat(10000, 0, 8192)
	at1024 := FromFlat(10000, 0, 1024)
	if at8192.CellX != 1 {
		t.Fatalf("cellX at 8192 = %d, want 1", at8192.CellX)
	}
	if at1024.CellX != 9 {
		t.Fatalf("cellX at 1024 = %d, want 9", at1024.CellX)
	}
}
