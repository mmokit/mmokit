package pathfinding

import "testing"

// LOSChecker is a minimal interface so smooth doesn't depend on pkg/spatial.
// Real callers wrap spatial.HashGrid.Raycast in a closure that returns
// true on clear LOS, false if blocked.
type fakeLOS struct {
	blocks []struct{ a, b Vec2 }
}

func (f fakeLOS) Clear(a, b Vec2) bool {
	for _, blk := range f.blocks {
		if blk.a == a && blk.b == b {
			return false
		}
	}
	return true
}

// All-clear LOS collapses a 3-point straight line to its endpoints.
func TestSmoothLOS_CollapsesStraightLine(t *testing.T) {
	in := []Vec2{{0, 0}, {1, 0}, {2, 0}, {3, 0}}
	out := SmoothLOS(in, fakeLOS{})
	if len(out) != 2 {
		t.Fatalf("expected 2 waypoints (collapsed), got %d: %v", len(out), out)
	}
	if out[0] != in[0] || out[1] != in[len(in)-1] {
		t.Fatalf("expected start/end preserved, got %v", out)
	}
}

// When LOS from 0 → 3 is blocked but 0 → 2 is clear, mid stop is at index 2.
func TestSmoothLOS_PreservesCorner(t *testing.T) {
	in := []Vec2{{0, 0}, {1, 0}, {2, 0}, {2, 1}}
	los := fakeLOS{blocks: []struct{ a, b Vec2 }{
		{a: Vec2{0, 0}, b: Vec2{2, 1}},
		{a: Vec2{0, 0}, b: Vec2{2, 0}}, // can't see (2,0) either
	}}
	// smoother walks: from (0,0), try (2,1) → blocked, try (2,0) → blocked,
	// accept (1,0) as next stop. From (1,0) try (2,1) → assume clear.
	out := SmoothLOS(in, los)
	if len(out) != 3 {
		t.Fatalf("expected 3 waypoints (corner preserved), got %d: %v", len(out), out)
	}
}

// Idempotency: smoothing a smooth path returns the same path.
func TestSmoothLOS_Idempotent(t *testing.T) {
	in := []Vec2{{0, 0}, {2, 2}}
	out1 := SmoothLOS(in, fakeLOS{})
	out2 := SmoothLOS(out1, fakeLOS{})
	if len(out2) != len(out1) {
		t.Fatalf("expected idempotent smoothing")
	}
}
