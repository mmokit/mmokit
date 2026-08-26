package spatial

import (
	"math"
	"math/rand"
	"testing"

	"github.com/mmokit/mmokit/pkg/component"
)

// TestContact_NormalPushesAApartFromB pins the direction convention, which is
// the single most consequential thing in this file.
//
// Normal points from B toward A, so moving A by Normal*Depth separates them.
// Get the sign wrong and push-out drives a character INTO the wall — and it
// does so consistently, which reads as the wall having negative thickness
// rather than as a sign error.
//
// Asserted by CONSTRUCTION rather than by inspecting the vector: apply the
// push and check the pair no longer overlaps.
func TestContact_NormalPushesAApartFromB(t *testing.T) {
	rng := rand.New(rand.NewSource(31337))
	checked := 0

	for range 4000 {
		a, b := randomShape(rng), randomShape(rng)
		if !overlap(&a, &b) {
			continue
		}
		c, ok := contact(&a, &b)
		if !ok {
			// Box-box has no manifold by scope decision; everything else must
			// produce one when it overlaps.
			if a.Shape == component.ShapeBox && b.Shape == component.ShapeBox {
				continue
			}
			t.Fatalf("%v vs %v overlap but produced no contact\n a=%+v\n b=%+v", a.Shape, b.Shape, a, b)
		}
		checked++

		if n := math.Sqrt(float64(c.Normal.LenSq())); math.Abs(n-1) > 1e-4 {
			t.Fatalf("normal is not unit length: %v", n)
		}
		if c.Depth <= 0 {
			t.Fatalf("contact depth %v is not positive", c.Depth)
		}

		// Apply the push with a hair of slack for float error, and they must
		// separate. This is the whole convention, tested end to end.
		moved := a
		push := c.Normal.Scale(c.Depth * 1.001)
		moved.X += push.X
		moved.Y += push.Y
		moved.Z += push.Z
		if overlap(&moved, &b) {
			t.Fatalf("%v vs %v: pushing A by Normal*Depth did not separate them\n"+
				" normal=%+v depth=%v\n a=%+v\n b=%+v", a.Shape, b.Shape, c.Normal, c.Depth, a, b)
		}
	}
	if checked < 200 {
		t.Fatalf("only %d overlapping pairs exercised — the generator is not producing contact", checked)
	}
	t.Logf("verified push-out on %d overlapping pairs", checked)
}

// TestContact_IsAntisymmetric: the same pair seen from either side must give
// opposing normals and the same depth. QueryCollisions iterates a Go map, so
// the argument order is not stable between runs.
func TestContact_IsAntisymmetric(t *testing.T) {
	rng := rand.New(rand.NewSource(5150))
	for range 4000 {
		a, b := randomShape(rng), randomShape(rng)
		ca, oka := contact(&a, &b)
		cb, okb := contact(&b, &a)
		if oka != okb {
			t.Fatalf("%v vs %v: contact exists in one direction only", a.Shape, b.Shape)
		}
		if !oka {
			continue
		}
		if math.Abs(float64(ca.Depth-cb.Depth)) > 1e-3 {
			t.Fatalf("%v vs %v: depths differ by direction: %v vs %v", a.Shape, b.Shape, ca.Depth, cb.Depth)
		}
		sum := ca.Normal.Add(cb.Normal)
		if math.Sqrt(float64(sum.LenSq())) > 1e-4 {
			t.Fatalf("%v vs %v: normals are not opposed: %+v and %+v", a.Shape, b.Shape, ca.Normal, cb.Normal)
		}
	}
}

// TestContact_SphereSphereExactly pins the arithmetic on the one pair simple
// enough to check by hand, so the property tests above have an anchor.
func TestContact_SphereSphereExactly(t *testing.T) {
	a := sphere(0, 0, 0, 3)
	b := sphere(4, 0, 0, 3) // centres 4 apart, radii sum 6, so depth 2
	c, ok := contact(&a, &b)
	if !ok {
		t.Fatal("no contact")
	}
	if math.Abs(float64(c.Depth-2)) > 1e-5 {
		t.Errorf("depth = %v, want 2", c.Depth)
	}
	// Normal from B toward A is -X.
	if math.Abs(float64(c.Normal.X+1)) > 1e-5 || math.Abs(float64(c.Normal.Y)) > 1e-5 {
		t.Errorf("normal = %+v, want (-1, 0, 0)", c.Normal)
	}
	// Contact point midway through the overlap: A's surface is at x=3, B's at
	// x=1, so the midpoint is x=2.
	if math.Abs(float64(c.Point.X-2)) > 1e-5 {
		t.Errorf("point.X = %v, want 2", c.Point.X)
	}
}

// A sphere whose centre is INSIDE a box is the case a clamp cannot answer:
// the closest point is the sphere's own centre, giving a zero distance and no
// direction. It must exit through the nearest face.
func TestContact_SphereCentreInsideBox(t *testing.T) {
	b := box(0, 0, 0, 10, 10, 100, component.RotationIdentity()) // half 5,5,50
	// Just inside the +X face: nearest exit is +X, 1 unit away.
	s := sphere(4, 0, 0, 2)
	c, ok := contact(&s, &b)
	if !ok {
		t.Fatal("a sphere inside a box produced no contact")
	}
	if math.Abs(float64(c.Normal.X-1)) > 1e-4 {
		t.Errorf("normal = %+v, want +X (the nearest face)", c.Normal)
	}
	// Depth is the radius plus how far the centre is past the face plane.
	if math.Abs(float64(c.Depth-3)) > 1e-4 {
		t.Errorf("depth = %v, want 3 (radius 2 + 1 inside the face)", c.Depth)
	}
	// And the push must actually work.
	moved := s
	moved.X += c.Normal.X * c.Depth * 1.001
	moved.Y += c.Normal.Y * c.Depth * 1.001
	moved.Z += c.Normal.Z * c.Depth * 1.001
	if overlap(&moved, &b) {
		t.Error("pushing the sphere out by the reported depth left it overlapping")
	}
}

// randomShape produces a sphere, box or capsule with a random pose, clustered
// near the origin so a useful fraction of pairs overlap.
func randomShape(rng *rand.Rand) Entry {
	kinds := []component.ShapeKind{component.ShapeSphere, component.ShapeBox, component.ShapeCapsule}
	k := kinds[rng.Intn(len(kinds))]
	rot := component.RotationFromAxisAngle(
		float32(rng.NormFloat64()), float32(rng.NormFloat64()), float32(rng.NormFloat64()),
		float32(rng.Float64()*2*math.Pi))
	c := randVec(rng, 3)
	switch k {
	case component.ShapeSphere:
		return sphere(c.X, c.Y, c.Z, float32(1+rng.Float64()*3))
	case component.ShapeBox:
		return box(c.X, c.Y, c.Z,
			float32(1+rng.Float64()*5), float32(1+rng.Float64()*5), float32(1+rng.Float64()*5), rot)
	default:
		r := float32(0.5 + rng.Float64()*2)
		return capsule(c.X, c.Y, c.Z, r, r*2+float32(rng.Float64()*6), rot)
	}
}
