package pathfinding

import "testing"

// TestAStar_StraightLine: empty 10×10 grid, start (0,0) → goal (9,0).
// Expect 10 waypoints monotonically increasing in X.
func TestAStar_StraightLine(t *testing.T) {
	g := NewNavGrid(Vec2{0, 0}, 1, 10, 10)
	path := AStar(g, Vec2{0.5, 0.5}, Vec2{9.5, 0.5})
	if len(path) == 0 {
		t.Fatal("expected non-empty path")
	}
	// First and last waypoints should be at start and goal cell centers.
	if path[0].X != 0.5 || path[0].Y != 0.5 {
		t.Fatalf("start waypoint expected (0.5,0.5), got %v", path[0])
	}
	if path[len(path)-1].X != 9.5 || path[len(path)-1].Y != 0.5 {
		t.Fatalf("goal waypoint expected (9.5,0.5), got %v", path[len(path)-1])
	}
}

// TestAStar_AvoidsWall: a vertical wall at x=2 forces a detour.
func TestAStar_AvoidsWall(t *testing.T) {
	g := NewNavGrid(Vec2{0, 0}, 1, 5, 5)
	for y := 0; y < 4; y++ {
		g.Block(2, y) // wall from (2,0) to (2,3); (2,4) is the gap
	}
	path := AStar(g, Vec2{0.5, 0.5}, Vec2{4.5, 0.5})
	if path == nil {
		t.Fatal("expected reachable path through the gap")
	}
	// No waypoint should be at (2,0)..(2,3) (the blocked cells).
	for _, w := range path {
		cx, cy, _ := g.WorldToCell(w)
		if cx == 2 && cy < 4 {
			t.Fatalf("path crosses blocked cell (2,%d)", cy)
		}
	}
}

// TestAStar_Unreachable: goal walled in on all sides → nil.
func TestAStar_Unreachable(t *testing.T) {
	g := NewNavGrid(Vec2{0, 0}, 1, 5, 5)
	g.Block(1, 2)
	g.Block(2, 1)
	g.Block(2, 3)
	g.Block(3, 2)
	g.Block(1, 1)
	g.Block(1, 3)
	g.Block(3, 1)
	g.Block(3, 3)
	path := AStar(g, Vec2{0.5, 0.5}, Vec2{2.5, 2.5})
	if path != nil {
		t.Fatalf("expected nil path to walled-in goal, got %v", path)
	}
}
