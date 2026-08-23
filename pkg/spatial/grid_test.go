package spatial

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
)

func makeWorld() *ecs.World {
	return ecs.NewWorld(64)
}

func newEntity(w *ecs.World) ecs.Entity {
	return w.NewEntity()
}

func TestRegister_QueryRadius(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	e := newEntity(w)
	g.Register(Entry{Entity: e, X: 50, Y: 50, Radius: 5, Layer: 1, Shape: component.ShapeSphere})

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
			results := g.QueryRadius(component.Vec3{X: tt.cx, Y: tt.cy}, tt.radius, nil)
			if len(results) != tt.wantLen {
				t.Fatalf("got %d results, want %d", len(results), tt.wantLen)
			}
		})
	}
}

func TestQueryRadius_EmptyGrid(t *testing.T) {
	g := NewHashGrid(100)
	results := g.QueryRadius(component.Vec3{X: 0, Y: 0}, 500, nil)
	if len(results) != 0 {
		t.Fatalf("expected 0 results on empty grid, got %d", len(results))
	}
}

func TestQueryRadius_MultipleEntries(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	g.Register(Entry{Entity: newEntity(w), X: 10, Y: 10, Radius: 5, Shape: component.ShapeSphere})
	g.Register(Entry{Entity: newEntity(w), X: 20, Y: 20, Radius: 5, Shape: component.ShapeSphere})
	g.Register(Entry{Entity: newEntity(w), X: 500, Y: 500, Radius: 5, Shape: component.ShapeSphere})

	results := g.QueryRadius(component.Vec3{X: 15, Y: 15}, 20, nil)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
}

func TestQueryRadius_CellBoundary(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	e := newEntity(w)
	g.Register(Entry{Entity: e, X: 99.9, Y: 50, Radius: 5, Shape: component.ShapeSphere})

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
			results := g.QueryRadius(component.Vec3{X: tt.cx, Y: tt.cy}, tt.radius, nil)
			if len(results) != tt.wantLen {
				t.Fatalf("got %d results, want %d", len(results), tt.wantLen)
			}
		})
	}
}

func TestQueryRadius_LargeRadius(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	count := 0
	for x := float32(0); x < 1000; x += 150 {
		for y := float32(0); y < 1000; y += 150 {
			g.Register(Entry{Entity: newEntity(w), X: x, Y: y, Radius: 5, Shape: component.ShapeSphere})
			count++
		}
	}

	results := g.QueryRadius(component.Vec3{X: 500, Y: 500}, 5000, nil)
	if len(results) != count {
		t.Fatalf("large radius: got %d results, want %d", len(results), count)
	}
}

func TestUpdate_SameCell(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	e := newEntity(w)
	g.Register(Entry{Entity: e, X: 10, Y: 10, Radius: 5, Shape: component.ShapeSphere})

	changed := g.Update(Entry{Entity: e, X: 15, Y: 15, Radius: 10, Shape: component.ShapeSphere})
	if changed {
		t.Fatal("expected no cell change")
	}

	results := g.QueryRadius(component.Vec3{X: 15, Y: 15}, 1, nil)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Radius != 10 {
		t.Fatalf("entry not updated: radius=%f, want 10", results[0].Radius)
	}
}

func TestUpdate_CrossCell(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	e := newEntity(w)
	g.Register(Entry{Entity: e, X: 10, Y: 10, Radius: 5, Shape: component.ShapeSphere})

	changed := g.Update(Entry{Entity: e, X: 210, Y: 210, Radius: 5, Shape: component.ShapeSphere})
	if !changed {
		t.Fatal("expected cell change")
	}

	// Should not be at old position.
	results := g.QueryRadius(component.Vec3{X: 10, Y: 10}, 5, nil)
	if len(results) != 0 {
		t.Fatalf("old position: got %d results, want 0", len(results))
	}

	// Should be at new position.
	results = g.QueryRadius(component.Vec3{X: 210, Y: 210}, 5, nil)
	if len(results) != 1 {
		t.Fatalf("new position: got %d results, want 1", len(results))
	}
}

func TestDeregister(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	e := newEntity(w)
	g.Register(Entry{Entity: e, X: 50, Y: 50, Radius: 5, Shape: component.ShapeSphere})

	g.Deregister(e)

	results := g.QueryRadius(component.Vec3{X: 50, Y: 50}, 100, nil)
	if len(results) != 0 {
		t.Fatalf("after deregister: got %d results, want 0", len(results))
	}
	if g.IsRegistered(e) {
		t.Fatal("entity still registered after Deregister")
	}
	if g.TrackedCount() != 0 {
		t.Fatalf("TrackedCount=%d, want 0", g.TrackedCount())
	}
}

func TestDeregister_Unknown(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	// Should not panic.
	g.Deregister(newEntity(w))
}

func TestDeregister_SwapDeleteCorrectness(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)

	// Register 3 entities in the same cell.
	e1 := newEntity(w)
	e2 := newEntity(w)
	e3 := newEntity(w)
	g.Register(Entry{Entity: e1, X: 10, Y: 10, Radius: 5, Shape: component.ShapeSphere})
	g.Register(Entry{Entity: e2, X: 20, Y: 20, Radius: 5, Shape: component.ShapeSphere})
	g.Register(Entry{Entity: e3, X: 30, Y: 30, Radius: 5, Shape: component.ShapeSphere})

	// Remove the first — triggers swap-delete.
	g.Deregister(e1)

	if g.TrackedCount() != 2 {
		t.Fatalf("TrackedCount=%d, want 2", g.TrackedCount())
	}

	// e2 and e3 should still be findable.
	results := g.QueryRadius(component.Vec3{X: 20, Y: 20}, 5, nil)
	if len(results) != 1 {
		t.Fatalf("e2 query: got %d results, want 1", len(results))
	}
	results = g.QueryRadius(component.Vec3{X: 30, Y: 30}, 5, nil)
	if len(results) != 1 {
		t.Fatalf("e3 query: got %d results, want 1", len(results))
	}

	// e2 and e3 should still be updatable.
	g.Update(Entry{Entity: e2, X: 25, Y: 25, Radius: 5, Shape: component.ShapeSphere})
	g.Update(Entry{Entity: e3, X: 35, Y: 35, Radius: 5, Shape: component.ShapeSphere})

	results = g.QueryRadius(component.Vec3{X: 25, Y: 25}, 1, nil)
	if len(results) != 1 {
		t.Fatalf("e2 after update: got %d results, want 1", len(results))
	}
}

func TestInsertTransient_ClearTransient(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)

	// Register a tracked entity.
	e := newEntity(w)
	g.Register(Entry{Entity: e, X: 50, Y: 50, Radius: 5, Shape: component.ShapeSphere})

	// Add transient entries.
	g.InsertTransient(Entry{X: 55, Y: 55, Radius: 3, Shape: component.ShapeSphere})
	g.InsertTransient(Entry{X: 60, Y: 60, Radius: 3, Shape: component.ShapeSphere})

	// QueryRadius should find both tracked and transient.
	results := g.QueryRadius(component.Vec3{X: 50, Y: 50}, 20, nil)
	if len(results) != 3 {
		t.Fatalf("before clear: got %d results, want 3", len(results))
	}

	// ClearTransient should remove only transient entries.
	g.ClearTransient()
	results = g.QueryRadius(component.Vec3{X: 50, Y: 50}, 20, nil)
	if len(results) != 1 {
		t.Fatalf("after ClearTransient: got %d results, want 1", len(results))
	}

	// TrackedCount should be unchanged.
	if g.TrackedCount() != 1 {
		t.Fatalf("TrackedCount=%d, want 1", g.TrackedCount())
	}
}

func TestReset(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	g.Register(Entry{Entity: newEntity(w), X: 10, Y: 10, Radius: 5, Shape: component.ShapeSphere})
	g.InsertTransient(Entry{X: 20, Y: 20, Radius: 5, Shape: component.ShapeSphere})

	g.Reset()

	results := g.QueryRadius(component.Vec3{X: 0, Y: 0}, 5000, nil)
	if len(results) != 0 {
		t.Fatalf("after Reset: got %d results, want 0", len(results))
	}
	if g.TrackedCount() != 0 {
		t.Fatalf("after Reset: TrackedCount=%d, want 0", g.TrackedCount())
	}
}

func TestDoubleRegister_Panics(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	e := newEntity(w)
	g.Register(Entry{Entity: e, X: 10, Y: 10, Radius: 5, Shape: component.ShapeSphere})

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on double register")
		}
	}()
	g.Register(Entry{Entity: e, X: 20, Y: 20, Radius: 5, Shape: component.ShapeSphere})
}

func TestTrackedCount(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	if g.TrackedCount() != 0 {
		t.Fatalf("empty grid: TrackedCount=%d, want 0", g.TrackedCount())
	}

	e1 := newEntity(w)
	e2 := newEntity(w)
	g.Register(Entry{Entity: e1, X: 10, Y: 10, Radius: 5, Shape: component.ShapeSphere})
	g.Register(Entry{Entity: e2, X: 20, Y: 20, Radius: 5, Shape: component.ShapeSphere})
	if g.TrackedCount() != 2 {
		t.Fatalf("after 2 registers: TrackedCount=%d, want 2", g.TrackedCount())
	}

	g.Deregister(e1)
	if g.TrackedCount() != 1 {
		t.Fatalf("after deregister: TrackedCount=%d, want 1", g.TrackedCount())
	}
}

func TestQueryCollisions_BothLayers(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)

	matrix := map[uint8]uint8{1: 2}

	// Tracked: layer 1.
	g.Register(Entry{Entity: newEntity(w), X: 10, Y: 10, Radius: 20, Layer: 1, Shape: component.ShapeSphere})

	// Transient: layer 2, overlapping.
	g.InsertTransient(Entry{X: 15, Y: 10, Radius: 20, Layer: 2, Shape: component.ShapeSphere})

	pairs := 0
	g.QueryCollisions(matrix, func(a, b Entry) {
		pairs++
	})
	if pairs != 1 {
		t.Fatalf("got %d collision pairs, want 1", pairs)
	}
}

func TestQueryCollisions_CircleCircle(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)

	matrix := map[uint8]uint8{1: 1}

	tests := []struct {
		name      string
		entries   []Entry
		wantPairs int
	}{
		{
			name: "overlapping pair",
			entries: []Entry{
				{X: 10, Y: 10, Radius: 20, Layer: 1, Shape: component.ShapeSphere},
				{X: 25, Y: 10, Radius: 20, Layer: 1, Shape: component.ShapeSphere},
			},
			wantPairs: 1,
		},
		{
			name: "non-overlapping pair",
			entries: []Entry{
				{X: 0, Y: 0, Radius: 5, Layer: 1, Shape: component.ShapeSphere},
				{X: 100, Y: 100, Radius: 5, Layer: 1, Shape: component.ShapeSphere},
			},
			wantPairs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g.Reset()
			for i, e := range tt.entries {
				e.Entity = newEntity(w)
				tt.entries[i] = e
				g.Register(e)
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
	w := makeWorld()
	g := NewHashGrid(100)

	// Layer 1 collides with layer 2, but not with itself.
	matrix := map[uint8]uint8{1: 2}

	g.Register(Entry{Entity: newEntity(w), X: 10, Y: 10, Radius: 20, Layer: 1, Shape: component.ShapeSphere})
	g.Register(Entry{Entity: newEntity(w), X: 15, Y: 10, Radius: 20, Layer: 1, Shape: component.ShapeSphere})

	pairs := 0
	g.QueryCollisions(matrix, func(a, b Entry) {
		pairs++
	})
	if pairs != 0 {
		t.Fatalf("same-layer should not collide: got %d pairs", pairs)
	}

	g.Register(Entry{Entity: newEntity(w), X: 12, Y: 10, Radius: 20, Layer: 2, Shape: component.ShapeSphere})

	pairs = 0
	g.QueryCollisions(matrix, func(a, b Entry) {
		pairs++
	})
	if pairs != 2 {
		t.Fatalf("cross-layer collisions: got %d pairs, want 2", pairs)
	}
}

func TestIsRegistered(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	e := newEntity(w)

	if g.IsRegistered(e) {
		t.Fatal("should not be registered before Register")
	}

	g.Register(Entry{Entity: e, X: 10, Y: 10, Radius: 5, Shape: component.ShapeSphere})
	if !g.IsRegistered(e) {
		t.Fatal("should be registered after Register")
	}

	g.Deregister(e)
	if g.IsRegistered(e) {
		t.Fatal("should not be registered after Deregister")
	}
}

func TestUpdate_CrossCell_MultipleEntities(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)

	e1 := newEntity(w)
	e2 := newEntity(w)
	g.Register(Entry{Entity: e1, X: 10, Y: 10, Radius: 5, Shape: component.ShapeSphere})
	g.Register(Entry{Entity: e2, X: 20, Y: 20, Radius: 5, Shape: component.ShapeSphere})

	// Move e1 to a different cell.
	g.Update(Entry{Entity: e1, X: 210, Y: 210, Radius: 5, Shape: component.ShapeSphere})

	// e2 should still be in original position.
	results := g.QueryRadius(component.Vec3{X: 20, Y: 20}, 5, nil)
	if len(results) != 1 {
		t.Fatalf("e2 should still be findable: got %d results", len(results))
	}

	// e1 should be at new position.
	results = g.QueryRadius(component.Vec3{X: 210, Y: 210}, 5, nil)
	if len(results) != 1 {
		t.Fatalf("e1 at new position: got %d results", len(results))
	}
}

// TestQueryRadius_IsSpherical is the assertion the whole phase exists for.
//
// The two entries MUST share an XY bucket. If they do not, the 2D bucket sweep
// excludes the far one on its own and the test passes even after the Z term is
// reverted — which makes it a test of the bucket key rather than of the
// distance test. Bucket size is 100 and both entries sit at XY (0,0), so they
// are in the same column by construction.
func TestQueryRadius_IsSpherical(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)

	onPlane := newEntity(w)
	overhead := newEntity(w)
	g.Register(Entry{Entity: onPlane, X: 0, Y: 0, Z: 0, Radius: 1})
	g.Register(Entry{Entity: overhead, X: 0, Y: 0, Z: 150, Radius: 1})

	got := g.QueryRadius(component.Vec3{}, 100, nil)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1 — a query at the origin with radius 100 must not reach Z=150", len(got))
	}
	if got[0].Entity != onPlane {
		t.Errorf("returned the entry at Z=150; the query is still an infinite cylinder")
	}

	// And it must still find it once the query rises to meet it.
	got = g.QueryRadius(component.Vec3{Z: 150}, 100, nil)
	if len(got) != 1 || got[0].Entity != overhead {
		t.Errorf("a query centered at Z=150 does not find the entry there")
	}
}

// TestQueryRadius_TwoDIsUnchanged pins the property the owner constraint rests
// on: with every Z at zero, the spherical test returns exactly what the
// cylindrical one did. Not "approximately" and not "for these radii" — the
// same entries, because dz is zero and a sum of squares is never negative zero.
func TestQueryRadius_TwoDIsUnchanged(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)

	// A spread that straddles bucket lines and the radius boundary.
	positions := []struct{ x, y float32 }{
		{0, 0}, {50, 0}, {99, 0}, {100, 0}, {101, 0},
		{0, 99}, {70, 70}, {71, 71}, {-40, -40}, {250, 250},
	}
	want := make(map[ecs.Entity]bool)
	for _, p := range positions {
		e := newEntity(w)
		g.Register(Entry{Entity: e, X: p.x, Y: p.y, Radius: 1})
		// The reference answer, computed the OLD way: 2D distance only.
		dx, dy := p.x, p.y
		if dx*dx+dy*dy <= 100*100 {
			want[e] = true
		}
	}

	got := g.QueryRadius(component.Vec3{}, 100, nil)
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d — the 2D result set changed", len(got), len(want))
	}
	for _, e := range got {
		if !want[e.Entity] {
			t.Errorf("entry %v is in the spherical result and was not in the cylindrical one", e.Entity)
		}
	}
}
