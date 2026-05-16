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

// Raycast must work for entities at negative coordinates — this was a
// latent bug where bucketKey used int32() truncation and bucketsAlongRay
// used math.Floor, putting negative-coord entities in a different bucket
// than the ray actually walked.
func TestRaycast_NegativeCoordinates(t *testing.T) {
	g := NewHashGrid(100)
	w := ecs.NewWorld()
	e := w.NewEntity()
	g.Register(Entry{
		Entity: e, X: -200, Y: 0,
		Radius: 50, Shape: ShapeCircle, Layer: LayerStatic,
	})
	hit, hitPt, dist, ok := g.Raycast(Vec2{0, 0}, Vec2{-500, 0}, LayerStatic)
	if !ok {
		t.Fatal("expected hit at negative-X circle, got miss")
	}
	if hit != e {
		t.Fatalf("expected hit entity %v, got %v", e, hit)
	}
	if math.Abs(float64(hitPt.X-(-150))) > 0.5 {
		t.Fatalf("hit X expected ~-150, got %.2f", hitPt.X)
	}
	if math.Abs(float64(dist-150)) > 0.5 {
		t.Fatalf("dist expected ~150, got %.2f", dist)
	}
}

func TestRaycast_AxisAlignedRect(t *testing.T) {
	g := NewHashGrid(100)
	w := ecs.NewWorld()
	e := w.NewEntity()
	// Rect at (200,0) with width=80 forward, height=40 side, no rotation.
	// OBB extents are (40,20); surface along ray at X=160.
	g.Register(Entry{
		Entity: e, X: 200, Y: 0,
		Radius: 50, Width: 80, Height: 40, Rotation: 0,
		Shape: ShapeRect, Layer: LayerStatic,
	})
	_, hitPt, dist, ok := g.Raycast(Vec2{0, 0}, Vec2{500, 0}, LayerStatic)
	if !ok {
		t.Fatal("expected hit")
	}
	if math.Abs(float64(hitPt.X-160)) > 0.5 {
		t.Fatalf("hit X expected ~160, got %.2f", hitPt.X)
	}
	if math.Abs(float64(dist-160)) > 0.5 {
		t.Fatalf("dist expected ~160, got %.2f", dist)
	}
}

func TestRaycast_RotatedRect(t *testing.T) {
	g := NewHashGrid(100)
	w := ecs.NewWorld()
	e := w.NewEntity()
	// 90° rotated rect at (200,0): width-axis now points +Y, so the rect
	// along X is 40 wide (the original Height). Surface at X=180.
	g.Register(Entry{
		Entity: e, X: 200, Y: 0,
		Radius: 50, Width: 80, Height: 40,
		Rotation: float32(math.Pi / 2),
		Shape:    ShapeRect, Layer: LayerStatic,
	})
	_, hitPt, _, ok := g.Raycast(Vec2{0, 0}, Vec2{500, 0}, LayerStatic)
	if !ok {
		t.Fatal("expected hit")
	}
	if math.Abs(float64(hitPt.X-180)) > 0.5 {
		t.Fatalf("hit X expected ~180, got %.2f", hitPt.X)
	}
}
