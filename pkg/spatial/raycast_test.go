package spatial

import (
	"math"
	"math/rand"
	"testing"

	"github.com/mmokit/mmokit/pkg/component"
)

func v3(x, y, z float32) component.Vec3 { return component.Vec3{X: x, Y: y, Z: z} }

func TestRaycast_SphereHit(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	e := newEntity(w)
	g.Register(Entry{Entity: e, X: 200, Radius: 50,
		Shape: component.ShapeSphere, Layer: LayerStatic, Rot: component.RotationIdentity()})

	hit, ok := g.Raycast(v3(0, 0, 0), v3(500, 0, 0), LayerStatic, nil)
	if !ok {
		t.Fatal("expected a hit")
	}
	if hit.Entity != e {
		t.Fatalf("hit %v, want %v", hit.Entity, e)
	}
	// Surface of a sphere at X=200 R=50: first intersection at X=150.
	if math.Abs(float64(hit.Point.X-150)) > 0.5 {
		t.Errorf("hit X = %.2f, want ~150", hit.Point.X)
	}
	if math.Abs(float64(hit.Dist-150)) > 0.5 {
		t.Errorf("dist = %.2f, want ~150", hit.Dist)
	}
}

// The ray now has a Z, and a shape out of its plane must not block it. Under
// the old planar cast this passed for the wrong reason: Z did not exist.
func TestRaycast_IsThreeDimensional(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	// A sphere directly on the XY path, but 500 units up.
	g.Register(Entry{Entity: newEntity(w), X: 200, Z: 500, Radius: 50,
		Shape: component.ShapeSphere, Layer: LayerStatic, Rot: component.RotationIdentity()})

	if _, ok := g.Raycast(v3(0, 0, 0), v3(500, 0, 0), LayerStatic, nil); ok {
		t.Error("a sphere 500 units above the ray blocked it — the cast is still planar")
	}
	// Raise the ray to meet it.
	if _, ok := g.Raycast(v3(0, 0, 500), v3(500, 0, 500), LayerStatic, nil); !ok {
		t.Error("a ray through the sphere's own height missed it")
	}
}

func TestRaycast_LayerMaskFiltering(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	g.Register(Entry{Entity: newEntity(w), X: 100, Radius: 30,
		Shape: component.ShapeSphere, Layer: LayerProp, Rot: component.RotationIdentity()})

	if _, ok := g.Raycast(v3(0, 0, 0), v3(400, 0, 0), LayerStatic, nil); ok {
		t.Error("a LayerProp entry answered a LayerStatic cast")
	}
	if _, ok := g.Raycast(v3(0, 0, 0), v3(400, 0, 0), LayerProp, nil); !ok {
		t.Error("a LayerProp entry did not answer a LayerProp cast")
	}
}

// TestRaycast_FilterExcludesDuringTheWalk is the reason RayFilter exists.
//
// A raycast returns the NEAREST hit, so excluding an entity by comparing it
// against the result only works while that entity is never actually nearest.
// Here it IS nearest — it sits at the ray's origin, on the masked layer — and
// a post-hoc check would report a clear line while a wall stands behind it.
func TestRaycast_FilterExcludesDuringTheWalk(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	self := newEntity(w)
	wall := newEntity(w)
	g.Register(Entry{Entity: self, X: 0, Radius: 10,
		Shape: component.ShapeSphere, Layer: LayerStatic, Rot: component.RotationIdentity()})
	g.Register(Entry{Entity: wall, X: 200, Radius: 30,
		Shape: component.ShapeSphere, Layer: LayerStatic, Rot: component.RotationIdentity()})

	// Without the filter, the caster's own collider is the nearest hit.
	hit, ok := g.Raycast(v3(0, 0, 0), v3(400, 0, 0), LayerStatic, nil)
	if !ok || hit.Entity != self {
		t.Fatalf("expected the caster to be the nearest hit, got %v ok=%v", hit.Entity, ok)
	}
	// With it, the wall behind is found — which a post-hoc `hit == self`
	// check would have missed entirely.
	hit, ok = g.Raycast(v3(0, 0, 0), v3(400, 0, 0), LayerStatic,
		func(e *Entry) bool { return e.Entity != self })
	if !ok {
		t.Fatal("filtering the caster hid the wall behind it")
	}
	if hit.Entity != wall {
		t.Errorf("hit %v, want the wall %v", hit.Entity, wall)
	}
}

func TestRaycast_Miss(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	g.Register(Entry{Entity: newEntity(w), X: 100, Y: 500, Radius: 20,
		Shape: component.ShapeSphere, Layer: LayerStatic, Rot: component.RotationIdentity()})
	if _, ok := g.Raycast(v3(0, 0, 0), v3(400, 0, 0), LayerStatic, nil); ok {
		t.Error("expected a miss")
	}
}

func TestRaycast_NegativeCoordinates(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	e := newEntity(w)
	g.Register(Entry{Entity: e, X: -200, Y: -200, Radius: 40,
		Shape: component.ShapeSphere, Layer: LayerStatic, Rot: component.RotationIdentity()})
	hit, ok := g.Raycast(v3(-500, -500, 0), v3(0, 0, 0), LayerStatic, nil)
	if !ok || hit.Entity != e {
		t.Fatalf("negative-coordinate cast missed: ok=%v", ok)
	}
}

func TestRaycast_Box(t *testing.T) {
	w := makeWorld()
	g := NewHashGrid(100)
	e := newEntity(w)
	g.Register(Entry{Entity: e, X: 200, Width: 100, Height: 100, Depth: 100, Radius: 87,
		Shape: component.ShapeBox, Layer: LayerStatic, Rot: component.RotationIdentity()})

	hit, ok := g.Raycast(v3(0, 0, 0), v3(500, 0, 0), LayerStatic, nil)
	if !ok {
		t.Fatal("axis-aligned box was missed")
	}
	// Half-width 50, so the near face is at X=150.
	if math.Abs(float64(hit.Point.X-150)) > 0.5 {
		t.Errorf("hit X = %.2f, want ~150", hit.Point.X)
	}
	// Rotated a quarter turn about Z: a 100x100 box is unchanged in plan, so
	// the near face stays at 150.
	g.Update(Entry{Entity: e, X: 200, Width: 100, Height: 100, Depth: 100, Radius: 87,
		Shape: component.ShapeBox, Layer: LayerStatic,
		Rot: component.RotationFromYaw(math.Pi / 2)})
	if hit, ok = g.Raycast(v3(0, 0, 0), v3(500, 0, 0), LayerStatic, nil); !ok {
		t.Fatal("rotated box was missed")
	}
	if math.Abs(float64(hit.Point.X-150)) > 0.5 {
		t.Errorf("rotated hit X = %.2f, want ~150", hit.Point.X)
	}
	// Tilted out of the plane: the ray along Z=0 must still enter the box.
	g.Update(Entry{Entity: e, X: 200, Width: 100, Height: 100, Depth: 100, Radius: 87,
		Shape: component.ShapeBox, Layer: LayerStatic,
		Rot: component.RotationFromAxisAngle(0, 1, 0, math.Pi/4)})
	if _, ok = g.Raycast(v3(0, 0, 0), v3(500, 0, 0), LayerStatic, nil); !ok {
		t.Error("box tilted about Y was missed by a ray through its centre line")
	}
}

// TestRayShapes_MatchTheNarrowPhase validates every per-shape ray test against
// an independent oracle built from ALREADY-VALIDATED code.
//
// The oracle walks the ray in fine steps and asks the narrow phase whether a
// zero-radius sphere at that point overlaps the shape — a completely different
// computation from the analytic root-finding under test, and one whose own
// correctness is established by the differential tests in narrow3d_test.go.
// The first step that overlaps brackets the true entry parameter.
//
// This is the check that matters: the sphere, box and capsule ray tests are
// three separate closed forms, each with its own sign conventions and
// degenerate branches, and none of them is obviously right by reading.
func TestRayShapes_MatchTheNarrowPhase(t *testing.T) {
	rng := rand.New(rand.NewSource(4711))
	const steps = 4000
	checked, hits := 0, 0

	for range 3000 {
		e := randomShape(rng)
		// Aimed rather than uniform: a ray from far out, through the shape's
		// own neighbourhood. Uniform endpoints hit about 3% of the time,
		// which exercises the miss path and almost nothing else.
		from := centerOf(&e).Add(randVec(rng, 14))
		to := centerOf(&e).Add(randVec(rng, 3))
		if to.Sub(from).LenSq() < 1e-6 {
			continue
		}
		checked++

		gotT, gotOK := rayTable[e.Shape](from, to, &e)

		// Oracle: the first sampled point that is inside the shape.
		wantOK, wantT := false, float32(0)
		d := to.Sub(from)
		for i := 0; i <= steps; i++ {
			s := float32(i) / float32(steps)
			p := sphere(from.X+d.X*s, from.Y+d.Y*s, from.Z+d.Z*s, 0)
			if overlap(&p, &e) {
				wantOK, wantT = true, s
				break
			}
		}

		if gotOK != wantOK {
			// A grazing ray can be found by one method and missed by the
			// other within the sampling resolution; only a clear
			// disagreement is a bug.
			if wantOK && !gotOK {
				// Confirm it is not a graze: is the midpoint of the reported
				// entry well inside?
				p := sphere(from.X+d.X*wantT, from.Y+d.Y*wantT, from.Z+d.Z*wantT, 0)
				if _, deep := contact(&p, &e); deep {
					t.Fatalf("%v: analytic ray missed a shape the narrow phase says it enters at t=%v\n from=%+v to=%+v e=%+v",
						e.Shape, wantT, from, to, e)
				}
			}
			continue
		}
		if !gotOK {
			continue
		}
		hits++
		// Entry parameters must agree to within the sampling step, generously.
		if math.Abs(float64(gotT-wantT)) > 4.0/steps {
			t.Fatalf("%v: ray enters at t=%v, oracle says t=%v\n from=%+v to=%+v e=%+v",
				e.Shape, gotT, wantT, from, to, e)
		}
	}
	if hits < 500 {
		t.Fatalf("only %d of %d casts hit — the generator is not exercising the ray tests", hits, checked)
	}
	t.Logf("verified %d ray hits against the narrow phase", hits)
}
