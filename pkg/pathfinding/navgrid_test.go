package pathfinding

import "testing"

func TestNavGrid_CoordRoundTrip(t *testing.T) {
	g := NewNavGrid(Vec2{0, 0}, 10, 5, 5)
	for x := 0; x < 5; x++ {
		for y := 0; y < 5; y++ {
			wp := g.CellToWorld(x, y)
			cx, cy, ok := g.WorldToCell(wp)
			if !ok {
				t.Fatalf("cell (%d,%d) → world %v → not ok", x, y, wp)
			}
			if cx != x || cy != y {
				t.Fatalf("round-trip mismatch: (%d,%d) → (%d,%d)", x, y, cx, cy)
			}
		}
	}
}

func TestNavGrid_OutOfBounds(t *testing.T) {
	g := NewNavGrid(Vec2{0, 0}, 10, 5, 5)
	_, _, ok := g.WorldToCell(Vec2{-5, 0})
	if ok {
		t.Fatal("expected out-of-bounds for negative x")
	}
	_, _, ok = g.WorldToCell(Vec2{100, 100})
	if ok {
		t.Fatal("expected out-of-bounds beyond grid extent")
	}
}

func TestNavGrid_BlockUnblock(t *testing.T) {
	g := NewNavGrid(Vec2{0, 0}, 10, 5, 5)
	if !g.Walkable(2, 2) {
		t.Fatal("default cell must be walkable")
	}
	g.Block(2, 2)
	if g.Walkable(2, 2) {
		t.Fatal("expected (2,2) to be blocked")
	}
}
