package spatial

import (
	"math"
	"testing"

	"github.com/mlange-42/ark/ecs"
)

func TestRaycast_CircleHit(t *testing.T) {
	g := NewHashGrid(100)
	w := ecs.NewWorld()
	e := w.NewEntity()
	g.Register(Entry{
		Entity: e, X: 200, Y: 0,
		Radius: 50, Shape: ShapeCircle, Layer: LayerStatic,
	})
	// ray from origin straight along +X
	hit, hitPt, dist, ok := g.Raycast(Vec2{0, 0}, Vec2{500, 0}, LayerStatic)
	if !ok {
		t.Fatal("expected hit, got miss")
	}
	if hit != e {
		t.Fatalf("expected hit entity %v, got %v", e, hit)
	}
	// surface of circle at X=200,R=50 → first intersect at X=150
	if math.Abs(float64(hitPt.X-150)) > 0.5 {
		t.Fatalf("hit X expected ~150, got %.2f", hitPt.X)
	}
	if math.Abs(float64(dist-150)) > 0.5 {
		t.Fatalf("dist expected ~150, got %.2f", dist)
	}
}

func TestRaycast_LayerMaskFiltering(t *testing.T) {
	g := NewHashGrid(100)
	w := ecs.NewWorld()
	// entity is LayerProp; we query LayerStatic → must miss
	e := w.NewEntity()
	g.Register(Entry{
		Entity: e, X: 200, Y: 0,
		Radius: 50, Shape: ShapeCircle, Layer: LayerProp,
	})
	_, _, _, ok := g.Raycast(Vec2{0, 0}, Vec2{500, 0}, LayerStatic)
	if ok {
		t.Fatal("expected miss when layer mask excludes entity")
	}
}

func TestRaycast_Miss(t *testing.T) {
	g := NewHashGrid(100)
	_, _, _, ok := g.Raycast(Vec2{0, 0}, Vec2{500, 0}, LayerStatic)
	if ok {
		t.Fatal("expected miss on empty grid")
	}
}
