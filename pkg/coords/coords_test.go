package coords

import (
	"math"
	"testing"
)

func TestSectorSize(t *testing.T) {
	if SectorSize != 8192.0 {
		t.Fatalf("SectorSize = %v, want 8192.0", SectorSize)
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
			in:   WorldPos{SX: 1, SY: 2, LX: 100, LY: 200},
			want: WorldPos{SX: 1, SY: 2, LX: 100, LY: 200},
		},
		{
			name: "positive overflow wraps SX++",
			in:   WorldPos{SX: 0, SY: 0, LX: SectorSize + 100, LY: 50},
			want: WorldPos{SX: 1, SY: 0, LX: 100, LY: 50},
		},
		{
			name: "negative wraps SX--",
			in:   WorldPos{SX: 0, SY: 0, LX: -100, LY: 50},
			want: WorldPos{SX: -1, SY: 0, LX: SectorSize - 100, LY: 50},
		},
		{
			name: "multi-sector overflow",
			in:   WorldPos{SX: 0, SY: 0, LX: SectorSize*3 + 500, LY: -SectorSize*2 - 300},
			want: WorldPos{SX: 3, SY: -3, LX: 500, LY: SectorSize - 300},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in
			Normalize(&got)
			if got.SX != tt.want.SX || got.SY != tt.want.SY {
				t.Errorf("sector = (%d,%d), want (%d,%d)", got.SX, got.SY, tt.want.SX, tt.want.SY)
			}
			if math.Abs(float64(got.LX-tt.want.LX)) > 0.01 || math.Abs(float64(got.LY-tt.want.LY)) > 0.01 {
				t.Errorf("local = (%.2f,%.2f), want (%.2f,%.2f)", got.LX, got.LY, tt.want.LX, tt.want.LY)
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
			name:   "same sector same point",
			from:   WorldPos{SX: 0, SY: 0, LX: 100, LY: 100},
			to:     WorldPos{SX: 0, SY: 0, LX: 100, LY: 100},
			wantDX: 0,
			wantDY: 0,
		},
		{
			name:   "adjacent sector offset equals SectorSize",
			from:   WorldPos{SX: 0, SY: 0, LX: 0, LY: 0},
			to:     WorldPos{SX: 1, SY: 0, LX: 0, LY: 0},
			wantDX: SectorSize,
			wantDY: 0,
		},
		{
			name:   "diagonal",
			from:   WorldPos{SX: 0, SY: 0, LX: 100, LY: 200},
			to:     WorldPos{SX: 1, SY: 1, LX: 100, LY: 200},
			wantDX: SectorSize,
			wantDY: SectorSize,
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
			a:    WorldPos{SX: 0, SY: 0, LX: 50, LY: 50},
			b:    WorldPos{SX: 0, SY: 0, LX: 50, LY: 50},
			want: 0,
		},
		{
			name: "3-4-5 triangle",
			a:    WorldPos{SX: 0, SY: 0, LX: 0, LY: 0},
			b:    WorldPos{SX: 0, SY: 0, LX: 3, LY: 4},
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
			wp := FromFlat(tt.x, tt.y)
			gotX, gotY := wp.ToFlat()
			if math.Abs(gotX-tt.x) > 0.01 || math.Abs(gotY-tt.y) > 0.01 {
				t.Errorf("round-trip (%.4f,%.4f) → (%.4f,%.4f)", tt.x, tt.y, gotX, gotY)
			}
		})
	}
}
