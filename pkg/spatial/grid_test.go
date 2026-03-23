package spatial

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
)

func entity(id uint32) ecs.Entity {
	// Zero-value entities are fine for testing; we use different positions to distinguish.
	return ecs.Entity{}
}

func TestQueryRadius_SingleEntry(t *testing.T) {
	g := NewGrid(100)
	e := Entry{X: 50, Y: 50, Radius: 5, Layer: 1, Shape: ShapeCircle}
	g.Insert(e)

	tests := []struct {
		name    string
		cx, cy  float32
		radius  float32
		wantLen int
	}{
		{"within range", 50, 50, 10, 1},
		{"at edge of range", 50, 60, 10, 1},
		{"outside range", 50, 200, 10, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := g.QueryRadius(tt.cx, tt.cy, tt.radius, nil)
			if len(results) != tt.wantLen {
				t.Fatalf("got %d results, want %d", len(results), tt.wantLen)
			}
		})
	}
}

func TestQueryRadius_EmptyGrid(t *testing.T) {
	g := NewGrid(100)
	results := g.QueryRadius(0, 0, 500, nil)
	if len(results) != 0 {
		t.Fatalf("expected 0 results on empty grid, got %d", len(results))
	}
}

func TestQueryRadius_MultipleEntries(t *testing.T) {
	g := NewGrid(100)
	g.Insert(Entry{X: 10, Y: 10, Radius: 5, Shape: ShapeCircle})
	g.Insert(Entry{X: 20, Y: 20, Radius: 5, Shape: ShapeCircle})
	g.Insert(Entry{X: 500, Y: 500, Radius: 5, Shape: ShapeCircle})

	results := g.QueryRadius(15, 15, 20, nil)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
}

func TestQueryRadius_CellBoundary(t *testing.T) {
	g := NewGrid(100)
	// Place entity right at cell boundary (x=100 is boundary between cell 0 and cell 1)
	e := Entry{X: 99.9, Y: 50, Radius: 5, Shape: ShapeCircle}
	g.Insert(e)

	tests := []struct {
		name    string
		cx, cy  float32
		radius  float32
		wantLen int
	}{
		{"query from same cell side", 90, 50, 20, 1},
		{"query from other cell side", 110, 50, 20, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := g.QueryRadius(tt.cx, tt.cy, tt.radius, nil)
			if len(results) != tt.wantLen {
				t.Fatalf("got %d results, want %d", len(results), tt.wantLen)
			}
		})
	}
}

func TestQueryRadius_LargeRadius(t *testing.T) {
	g := NewGrid(100)
	// Scatter entries across many cells
	count := 0
	for x := float32(0); x < 1000; x += 150 {
		for y := float32(0); y < 1000; y += 150 {
			g.Insert(Entry{X: x, Y: y, Radius: 5, Shape: ShapeCircle})
			count++
		}
	}

	results := g.QueryRadius(500, 500, 5000, nil)
	if len(results) != count {
		t.Fatalf("large radius: got %d results, want %d", len(results), count)
	}
}

func TestClear(t *testing.T) {
	g := NewGrid(100)
	g.Insert(Entry{X: 10, Y: 10, Radius: 5, Shape: ShapeCircle})
	g.Insert(Entry{X: 200, Y: 200, Radius: 5, Shape: ShapeCircle})
	g.Clear()

	results := g.QueryRadius(0, 0, 5000, nil)
	if len(results) != 0 {
		t.Fatalf("after Clear: got %d results, want 0", len(results))
	}
}

func TestQueryCollisions_CircleCircle(t *testing.T) {
	g := NewGrid(100)

	// Collision matrix: layer 1 collides with layer 1
	matrix := map[uint8]uint8{1: 1}

	tests := []struct {
		name      string
		entries   []Entry
		wantPairs int
	}{
		{
			name: "overlapping pair",
			entries: []Entry{
				{X: 10, Y: 10, Radius: 20, Layer: 1, Shape: ShapeCircle},
				{X: 25, Y: 10, Radius: 20, Layer: 1, Shape: ShapeCircle},
			},
			wantPairs: 1,
		},
		{
			name: "non-overlapping pair",
			entries: []Entry{
				{X: 0, Y: 0, Radius: 5, Layer: 1, Shape: ShapeCircle},
				{X: 100, Y: 100, Radius: 5, Layer: 1, Shape: ShapeCircle},
			},
			wantPairs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g.Clear()
			for _, e := range tt.entries {
				g.Insert(e)
			}
			pairs := 0
			g.QueryCollisions(matrix, func(a, b Entry) {
				pairs++
			})
			if pairs != tt.wantPairs {
				t.Fatalf("got %d collision pairs, want %d", pairs, tt.wantPairs)
			}
		})
	}
}

func TestQueryCollisions_LayerMatrix(t *testing.T) {
	g := NewGrid(100)

	// Layer 1 collides with layer 2, but not with itself
	matrix := map[uint8]uint8{1: 2}

	// Two layer-1 entities overlapping — should NOT collide
	g.Insert(Entry{X: 10, Y: 10, Radius: 20, Layer: 1, Shape: ShapeCircle})
	g.Insert(Entry{X: 15, Y: 10, Radius: 20, Layer: 1, Shape: ShapeCircle})

	pairs := 0
	g.QueryCollisions(matrix, func(a, b Entry) {
		pairs++
	})
	if pairs != 0 {
		t.Fatalf("same-layer should not collide when matrix excludes it: got %d pairs", pairs)
	}

	// Now add a layer-2 entity overlapping with one of the layer-1 entities
	g.Insert(Entry{X: 12, Y: 10, Radius: 20, Layer: 2, Shape: ShapeCircle})

	pairs = 0
	g.QueryCollisions(matrix, func(a, b Entry) {
		pairs++
	})
	if pairs != 2 {
		t.Fatalf("cross-layer collisions: got %d pairs, want 2", pairs)
	}
}
