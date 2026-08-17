package coords

import (
	"math"
	"testing"
)

func TestCellSize(t *testing.T) {
	if CellSize != 8192.0 {
		t.Fatalf("CellSize = %v, want 8192.0", CellSize)
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   WorldPos
		want WorldPos
	}{
		{
			name: "already normalized no-op",
			in:   WorldPos{CellX: 1, CellY: 2, LocalX: 100, LocalY: 200},
			want: WorldPos{CellX: 1, CellY: 2, LocalX: 100, LocalY: 200},
		},
		{
			name: "positive overflow wraps CellX++",
			in:   WorldPos{CellX: 0, CellY: 0, LocalX: CellSize + 100, LocalY: 50},
			want: WorldPos{CellX: 1, CellY: 0, LocalX: 100, LocalY: 50},
		},
		{
			name: "negative wraps CellX--",
			in:   WorldPos{CellX: 0, CellY: 0, LocalX: -100, LocalY: 50},
			want: WorldPos{CellX: -1, CellY: 0, LocalX: CellSize - 100, LocalY: 50},
		},
		{
			name: "multi-cell overflow",
			in:   WorldPos{CellX: 0, CellY: 0, LocalX: CellSize*3 + 500, LocalY: -CellSize*2 - 300},
			want: WorldPos{CellX: 3, CellY: -3, LocalX: 500, LocalY: CellSize - 300},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in
			Normalize(&got)
			if got.CellX != tt.want.CellX || got.CellY != tt.want.CellY {
				t.Errorf("cell = (%d,%d), want (%d,%d)", got.CellX, got.CellY, tt.want.CellX, tt.want.CellY)
			}
			if math.Abs(float64(got.LocalX-tt.want.LocalX)) > 0.01 || math.Abs(float64(got.LocalY-tt.want.LocalY)) > 0.01 {
				t.Errorf("local = (%.2f,%.2f), want (%.2f,%.2f)", got.LocalX, got.LocalY, tt.want.LocalX, tt.want.LocalY)
			}
		})
	}
}

func TestRelativeOffset(t *testing.T) {
	tests := []struct {
		name   string
		from   WorldPos
		to     WorldPos
		wantDX float32
		wantDY float32
	}{
		{
			name:   "same cell same point",
			from:   WorldPos{CellX: 0, CellY: 0, LocalX: 100, LocalY: 100},
			to:     WorldPos{CellX: 0, CellY: 0, LocalX: 100, LocalY: 100},
			wantDX: 0,
			wantDY: 0,
		},
		{
			name:   "adjacent cell offset equals CellSize",
			from:   WorldPos{CellX: 0, CellY: 0, LocalX: 0, LocalY: 0},
			to:     WorldPos{CellX: 1, CellY: 0, LocalX: 0, LocalY: 0},
			wantDX: CellSize,
			wantDY: 0,
		},
		{
			name:   "diagonal",
			from:   WorldPos{CellX: 0, CellY: 0, LocalX: 100, LocalY: 200},
			to:     WorldPos{CellX: 1, CellY: 1, LocalX: 100, LocalY: 200},
			wantDX: CellSize,
			wantDY: CellSize,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dx, dy := RelativeOffset(tt.from, tt.to)
			if math.Abs(float64(dx-tt.wantDX)) > 0.01 || math.Abs(float64(dy-tt.wantDY)) > 0.01 {
				t.Errorf("RelativeOffset = (%.2f,%.2f), want (%.2f,%.2f)", dx, dy, tt.wantDX, tt.wantDY)
			}
		})
	}
}

func TestDistance(t *testing.T) {
	tests := []struct {
		name string
		a, b WorldPos
		want float32
	}{
		{
			name: "same point is 0",
			a:    WorldPos{CellX: 0, CellY: 0, LocalX: 50, LocalY: 50},
			b:    WorldPos{CellX: 0, CellY: 0, LocalX: 50, LocalY: 50},
			want: 0,
		},
		{
			name: "3-4-5 triangle",
			a:    WorldPos{CellX: 0, CellY: 0, LocalX: 0, LocalY: 0},
			b:    WorldPos{CellX: 0, CellY: 0, LocalX: 3, LocalY: 4},
			want: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Distance(tt.a, tt.b)
			if math.Abs(float64(got-tt.want)) > 0.01 {
				t.Errorf("Distance = %.4f, want %.4f", got, tt.want)
			}
		})
	}
}

func TestFromFlatToFlatRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		x, y float64
	}{
		{"positive", 12345.678, 9876.543},
		{"negative", -5000.5, -12000.25},
		{"large values", 100000.0, 200000.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wp := FromFlat(tt.x, tt.y, CellSize)
			gotX, gotY := wp.ToFlat()
			if math.Abs(gotX-tt.x) > 0.01 || math.Abs(gotY-tt.y) > 0.01 {
				t.Errorf("round-trip (%.4f,%.4f) → (%.4f,%.4f)", tt.x, tt.y, gotX, gotY)
			}
		})
	}
}
