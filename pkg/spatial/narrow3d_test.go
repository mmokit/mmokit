package spatial

import (
	"math"
	"math/rand"
	"testing"

	"github.com/mmokit/mmokit/pkg/component"
)

func box(x, y, z, w, h, d float32, rot component.Rotation) Entry {
	return Entry{
		X: x, Y: y, Z: z,
		Width: w, Height: h, Depth: d,
		Radius: float32(math.Sqrt(float64(w*w+h*h+d*d)) / 2),
		Rot:    rot, Shape: component.ShapeBox,
	}
}

func sphere(x, y, z, r float32) Entry {
	return Entry{X: x, Y: y, Z: z, Radius: r, Rot: component.RotationIdentity(), Shape: component.ShapeSphere}
}

// capsule takes TOTAL tip-to-tip height, matching Collider.Depth's contract.
func capsule(x, y, z, r, totalHeight float32, rot component.Rotation) Entry {
	return Entry{
		X: x, Y: y, Z: z, Radius: r, Depth: totalHeight,
		Rot: rot, Shape: component.ShapeCapsule,
	}
}

// TestBoxSphere_IsThreeDimensional is the defect the planar test had: it
// projected onto the yaw plane and ignored Z entirely, so a sphere directly
// above a box collided with it at any height.
func TestBoxSphere_IsThreeDimensional(t *testing.T) {
	b := box(0, 0, 0, 10, 10, 10, component.RotationIdentity())

	if overlapBoxSphere3(&b, ptr(sphere(0, 0, 100, 2))) {
		t.Error("a sphere 100 units above a 10-unit box collides — the test is still planar")
	}
	if !overlapBoxSphere3(&b, ptr(sphere(0, 0, 6, 2))) {
		t.Error("a sphere just above the box's top face does not collide")
	}
	if !overlapBoxSphere3(&b, ptr(sphere(0, 0, 0, 1))) {
		t.Error("a sphere inside the box does not collide")
	}
	// Corner: the nearest point is the box's corner, at distance sqrt(3)*... .
	if overlapBoxSphere3(&b, ptr(sphere(10, 10, 10, 1))) {
		t.Error("a sphere well past the corner collides")
	}
}

// TestBoxBox_SeparatedOnEachFaceAxis walks the six face normals. A face axis
// is exact — no epsilon is involved — so a failure here is an arithmetic bug
// rather than a tolerance question.
func TestBoxBox_SeparatedOnEachFaceAxis(t *testing.T) {
	id := component.RotationIdentity()
	a := box(0, 0, 0, 10, 10, 10, id)
	for _, c := range []struct {
		name    string
		b       Entry
		overlap bool
	}{
		{"+X clear", box(11, 0, 0, 10, 10, 10, id), false},
		{"+X touching", box(9, 0, 0, 10, 10, 10, id), true},
		{"-Y clear", box(0, -11, 0, 10, 10, 10, id), false},
		{"+Z clear", box(0, 0, 11, 10, 10, 10, id), false},
		{"+Z touching", box(0, 0, 9, 10, 10, 10, id), true},
		{"coincident", box(0, 0, 0, 10, 10, 10, id), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := overlapBoxBox3(&a, &c.b); got != c.overlap {
				t.Errorf("overlap = %v, want %v", got, c.overlap)
			}
		})
	}
}

// TestBoxBox_EdgeCrossAxisSeparates is the case the nine cross-product axes
// exist for: two boxes whose face normals all overlap in projection but which
// are separated by an edge-edge axis. Without those nine tests this reports a
// false overlap.
//
// The z values are not guessed. My first attempt used 3.2 and asserted
// separation; the corner-projection oracle in TestBoxBox_MatchesCornerOracle
// showed the boxes genuinely overlap there and separate between 3.2 and 4, so
// the fixture was wrong and the code was right. That is the whole reason the
// oracle exists.
func TestBoxBox_EdgeCrossAxisSeparates(t *testing.T) {
	a := box(0, 0, 0, 20, 2, 2, component.RotationIdentity())
	rot := component.RotationFromAxisAngle(1, 0, 0, math.Pi/4).
		Mul(component.RotationFromAxisAngle(0, 0, 1, math.Pi/4))

	if overlapBoxBox3(&a, ptr(box(0, 0, 4, 20, 2, 2, rot))) {
		t.Error("boxes the oracle proves disjoint report overlapping")
	}
	// And close in, they must overlap — or the assertion above would pass on
	// an implementation that separates everything.
	if !overlapBoxBox3(&a, ptr(box(0, 0, 3.2, 20, 2, 2, rot))) {
		t.Error("boxes the oracle proves overlapping report separated")
	}
}

// TestBoxBox_ParallelEdgesDoNotFalselySeparate is what satEpsilon is for.
//
// When two boxes share an axis, three of the nine cross products are the zero
// vector. Testing a zero axis compares 0 > 0+0, which is false, so it does not
// separate on its own — but the projections it feeds are numerically
// meaningless, and without the epsilon in AbsR the accumulated float error can
// make one of them separate. A false SEPARATION is an object passing through
// a wall, which is why the guard is generous rather than tight.
func TestBoxBox_ParallelEdgesDoNotFalselySeparate(t *testing.T) {
	// Axis-aligned boxes: every cross product of a shared axis with itself is
	// zero, which is the worst case for the guard.
	id := component.RotationIdentity()
	for _, yaw := range []float32{0, 0.5, 1, math.Pi / 4, math.Pi / 2, 2, 3} {
		r := component.RotationFromYaw(yaw)
		a := box(0, 0, 0, 10, 10, 10, r)
		b := box(1, 1, 1, 10, 10, 10, r) // same orientation: all edges parallel
		if !overlapBoxBox3(&a, &b) {
			t.Errorf("yaw %v: two overlapping boxes with parallel edges report separated", yaw)
		}
	}
	// And the degenerate identity case with a shared axis.
	a := box(0, 0, 0, 10, 10, 10, id)
	b := box(2, 0, 0, 10, 10, 10, component.RotationFromAxisAngle(0, 0, 1, math.Pi/2))
	if !overlapBoxBox3(&a, &b) {
		t.Error("a box rotated a quarter turn about a shared axis reports separated")
	}
}

// TestBoxBox_IsSymmetric: QueryCollisions iterates a Go map, so the argument
// order is not stable between runs. An asymmetric answer is a bug that only
// appears sometimes.
func TestBoxBox_IsSymmetric(t *testing.T) {
	rots := []component.Rotation{
		component.RotationIdentity(),
		component.RotationFromYaw(0.7),
		component.RotationFromAxisAngle(0.3, -0.7, 0.2, 2.4),
	}
	for i, ra := range rots {
		for j, rb := range rots {
			for _, d := range []float32{0, 3, 7, 9.5, 11, 20} {
				a := box(0, 0, 0, 10, 6, 4, ra)
				b := box(d, d/2, d/3, 8, 8, 8, rb)
				if overlapBoxBox3(&a, &b) != overlapBoxBox3(&b, &a) {
					t.Errorf("rots %d/%d at d=%v: asymmetric", i, j, d)
				}
			}
		}
	}
}

// TestCapsuleSphere covers the shape's whole range, including the degenerate
// case where Depth <= 2*Radius makes it a sphere.
func TestCapsuleSphere(t *testing.T) {
	id := component.RotationIdentity()
	// Standing capsule: total height 20, radius 3, so the segment runs
	// z = -7 .. +7 and the caps reach z = -10 .. +10.
	c := capsule(0, 0, 0, 3, 20, id)

	for _, tc := range []struct {
		name string
		s    Entry
		want bool
	}{
		{"beside the shaft, touching", sphere(5, 0, 0, 2.5), true},
		{"beside the shaft, clear", sphere(9, 0, 0, 2), false},
		{"above the top cap, touching", sphere(0, 0, 11, 2), true},
		{"above the top cap, clear", sphere(0, 0, 14, 2), false},
		{"inside", sphere(0, 0, 0, 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := overlapCapsuleSphere(&c, &tc.s); got != tc.want {
				t.Errorf("overlap = %v, want %v", got, tc.want)
			}
		})
	}

	// Depth <= 2*Radius degenerates to a sphere of the cap radius: the
	// segment has zero length and the routine must still be correct.
	squat := capsule(0, 0, 0, 5, 4, id)
	if !overlapCapsuleSphere(&squat, ptr(sphere(0, 0, 6, 2))) {
		t.Error("a degenerate capsule does not behave as a sphere of its cap radius")
	}
	if overlapCapsuleSphere(&squat, ptr(sphere(0, 0, 9, 2))) {
		t.Error("a degenerate capsule reaches further than its cap radius")
	}
}

// TestCapsuleCapsule includes the parallel case, which is where a
// segment-segment closed form divides by a zero denominator.
func TestCapsuleCapsule(t *testing.T) {
	id := component.RotationIdentity()
	a := capsule(0, 0, 0, 2, 20, id)

	if !overlapCapsuleCapsule(&a, ptr(capsule(3, 0, 0, 2, 20, id))) {
		t.Error("two parallel capsules 3 apart with radii 2+2 do not overlap")
	}
	if overlapCapsuleCapsule(&a, ptr(capsule(5, 0, 0, 2, 20, id))) {
		t.Error("two parallel capsules 5 apart with radii 2+2 overlap")
	}
	// Crossed: one standing, one lying along X at the same height.
	lying := capsule(0, 0, 0, 2, 20, component.RotationFromAxisAngle(0, 1, 0, math.Pi/2))
	if !overlapCapsuleCapsule(&a, &lying) {
		t.Error("crossed capsules through the same point do not overlap")
	}
	// Same crossed pair, moved apart along Y.
	lying.Y = 5
	if overlapCapsuleCapsule(&a, &lying) {
		t.Error("crossed capsules 5 apart with radii 2+2 overlap")
	}
	// End-to-end along the shared axis: caps reach ±10 each, so centers 21
	// apart clear by 1.
	if overlapCapsuleCapsule(&a, ptr(capsule(0, 0, 21, 2, 20, id))) {
		t.Error("stacked capsules 21 apart (reach 10 each) overlap")
	}
	if !overlapCapsuleCapsule(&a, ptr(capsule(0, 0, 19, 2, 20, id))) {
		t.Error("stacked capsules 19 apart (reach 10 each) do not overlap")
	}
}

// TestCapsuleSegment_UsesTipToTipHeight pins the convention, because the two
// common ones differ by exactly 2*Radius and picking the wrong one is a bug
// that reads as a tuning problem.
func TestCapsuleSegment_UsesTipToTipHeight(t *testing.T) {
	c := capsule(0, 0, 0, 3, 20, component.RotationIdentity())
	p0, p1, r := capsuleSegment(&c)
	if r != 3 {
		t.Errorf("cap radius = %v, want 3", r)
	}
	// half-length = Depth/2 - Radius = 10 - 3 = 7
	if math.Abs(float64(p1.Z-7)) > 1e-5 || math.Abs(float64(p0.Z+7)) > 1e-5 {
		t.Errorf("segment = %v..%v, want z = -7..+7 (Depth/2 - Radius)", p0.Z, p1.Z)
	}
	// A capsule shorter than its own diameter is a sphere: zero-length segment.
	squat := capsule(0, 0, 0, 5, 4, component.RotationIdentity())
	q0, q1, _ := capsuleSegment(&squat)
	if q0 != q1 {
		t.Errorf("a capsule with Depth <= 2*Radius has a non-degenerate segment %v..%v", q0, q1)
	}
}

func ptr(e Entry) *Entry { return &e }

// ---------------------------------------------------------------------------
// Differential test against an independent oracle
// ---------------------------------------------------------------------------

// TestBoxBox_MatchesCornerOracle is the assertion that actually validates the
// 15-axis test, and it is worth more than every hand-written fixture above.
//
// overlapBoxBox3 uses Ericson's formulation: a rotation matrix between the two
// frames, half-extents projected through its absolute value, and an epsilon
// folded into that matrix. It is fast and it is a thicket of index
// permutations — i1, i2, j1, j2 — where a single transposed subscript gives an
// answer that is right for most configurations and wrong for a few.
//
// The oracle computes the same theorem by a completely different arithmetic
// path: it materialises all eight corners of each box in world space and
// projects them onto each candidate axis directly. No rotation matrix, no
// half-extent algebra, no epsilon. Finding a separating axis PROVES the boxes
// are disjoint, so where the oracle separates and the SAT does not, the SAT is
// wrong.
//
// Fixed seed: a randomized test that cannot be re-run identically is a test
// whose failures cannot be investigated.
func TestBoxBox_MatchesCornerOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(20260824))
	const cases = 3000
	mismatches, overlaps := 0, 0

	for i := range cases {
		a := randomBox(rng)
		b := randomBox(rng)
		got := overlapBoxBox3(&a, &b)
		disjoint := findSeparatingAxis(&a, &b, rng)
		if got {
			overlaps++
		}
		if got == disjoint {
			mismatches++
			if mismatches <= 3 {
				t.Errorf("case %d: SAT says overlap=%v, corner oracle says disjoint=%v\n  a=%+v\n  b=%+v",
					i, got, disjoint, a, b)
			}
		}
	}
	if mismatches > 0 {
		t.Errorf("%d of %d cases disagreed with the corner oracle", mismatches, cases)
	}
	// Non-vacuity: a generator that never produces an overlap would pass the
	// loop above trivially.
	if overlaps < cases/20 {
		t.Errorf("only %d of %d cases overlapped — the generator is not exercising contact", overlaps, cases)
	}
}

// randomBox produces a box with a random pose, extents and full 3D rotation.
func randomBox(rng *rand.Rand) Entry {
	w := float32(1 + rng.Float64()*9)
	h := float32(1 + rng.Float64()*9)
	d := float32(1 + rng.Float64()*9)
	// Positions clustered near the origin so a useful fraction of pairs touch.
	return box(
		float32(rng.NormFloat64()*4), float32(rng.NormFloat64()*4), float32(rng.NormFloat64()*4),
		w, h, d,
		component.RotationFromAxisAngle(
			float32(rng.NormFloat64()), float32(rng.NormFloat64()), float32(rng.NormFloat64()),
			float32(rng.Float64()*2*math.Pi)),
	)
}

// corners materialises a box's eight world-space corners.
func corners(e *Entry) [8]component.Vec3 {
	half, axis := obbAxes(e)
	c := centerOf(e)
	var out [8]component.Vec3
	i := 0
	for _, sx := range []float64{-1, 1} {
		for _, sy := range []float64{-1, 1} {
			for _, sz := range []float64{-1, 1} {
				out[i] = c.
					Add(axis[0].Scale(float32(sx * half[0]))).
					Add(axis[1].Scale(float32(sy * half[1]))).
					Add(axis[2].Scale(float32(sz * half[2])))
				i++
			}
		}
	}
	return out
}

func projRange(cs [8]component.Vec3, ax component.Vec3) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, c := range cs {
		p := float64(c.Dot(ax))
		lo = math.Min(lo, p)
		hi = math.Max(hi, p)
	}
	return lo, hi
}

// findSeparatingAxis reports whether any of the 15 canonical axes separates
// the two boxes, by direct corner projection.
//
// The separating-axis theorem says those 15 are sufficient for two convex
// boxes, so this is exact rather than a sample. The tolerance is one-sided:
// it declares separation only when the gap exceeds it, so a borderline pair is
// reported as overlapping and cannot produce a spurious mismatch.
func findSeparatingAxis(a, b *Entry, _ *rand.Rand) bool {
	ca, cb := corners(a), corners(b)
	_, aa := obbAxes(a)
	_, ab := obbAxes(b)

	axes := make([]component.Vec3, 0, 15)
	axes = append(axes, aa[0], aa[1], aa[2], ab[0], ab[1], ab[2])
	for i := range 3 {
		for j := range 3 {
			axes = append(axes, aa[i].Cross(ab[j]))
		}
	}
	for _, ax := range axes {
		// A near-zero cross product is a degenerate axis — the same case
		// satEpsilon guards in the routine under test — and carries no
		// information, so the oracle skips it rather than normalising noise.
		if ax.LenSq() < 1e-10 {
			continue
		}
		n := ax.Normalize()
		alo, ahi := projRange(ca, n)
		blo, bhi := projRange(cb, n)
		if ahi < blo-1e-4 || bhi < alo-1e-4 {
			return true
		}
	}
	return false
}

// TestSATEpsilon_GuardsNearParallelEdges is the measurement that earns
// satEpsilon its value, rather than asserting one.
//
// The guard only matters in one narrow regime: two boxes whose edges are
// NEARLY parallel, at a separation near the contact boundary. Exactly parallel
// is safe without it — the cross product is exactly zero, so the test compares
// 0 > 0 and does not separate. Deep overlap is safe too, being far from the
// decision. It is the razor edge with a tiny non-zero cross product where the
// projections are noise of comparable magnitude to the answer.
//
// Swept against the corner oracle, 24000 configurations at a rotation
// difference of 1e-7 radians:
//
//	satEpsilon = 1e-6   0 disagreements
//	satEpsilon = 0     75 disagreements
//
// Every one of those 75 is a FALSE SEPARATION — boxes that overlap reporting
// disjoint — which is an object passing through a wall. That asymmetry is why
// the guard is generous: a false overlap would be caught by the face axes,
// which are exact.
func TestSATEpsilon_GuardsNearParallelEdges(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const nearParallel = 1e-7
	disagreements, n := 0, 0

	for i := range 400 {
		ang := float32(i) * 0.017
		ra := component.RotationFromAxisAngle(0.3, 0.5, 0.81, ang)
		rb := component.RotationFromAxisAngle(0.3, 0.5, 0.81, ang+nearParallel)
		// Sweep the separation finely through the contact distance; extents
		// are 10, so contact is around d = 10.
		for k := range 60 {
			d := 9.0 + float32(k)*0.02
			a := box(0, 0, 0, 10, 10, 10, ra)
			b := box(d, 0, 0, 10, 10, 10, rb)
			n++
			if overlapBoxBox3(&a, &b) == findSeparatingAxis(&a, &b, rng) {
				disagreements++
			}
		}
	}
	if disagreements > 0 {
		t.Errorf("%d of %d near-parallel configurations disagree with the corner oracle — "+
			"satEpsilon is too small; every disagreement here is a false separation, "+
			"i.e. an object passing through a wall", disagreements, n)
	}
}

// ---------------------------------------------------------------------------
// Capsule vs box
// ---------------------------------------------------------------------------

// TestSegmentBoxDist_MatchesSamplingOracle validates the exact solver against
// an independent method, and is the measurement that replaced an iterative
// design.
//
// The oracle walks the segment densely and takes the exact point-to-box
// distance at each sample — no convexity argument, no piecewise reasoning, a
// different algorithm entirely. It converges to the true distance from ABOVE,
// so the solver must never exceed it by more than the sampling resolution.
//
// The number this replaced is worth keeping: alternating projection — clamp
// onto the box, clamp back onto the segment, repeat — was the planned method,
// and at twelve iterations its worst overestimate over these same 4000
// configurations was 0.91 world units. The exact solver's is 6.8e-7, which is
// the oracle's own resolution. The error direction is what made that
// unacceptable rather than merely imprecise: an overestimate reports no
// overlap where there is one, i.e. a character passing through a wall.
func TestSegmentBoxDist_MatchesSamplingOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	worst := 0.0
	for range 4000 {
		half := [3]float64{1 + rng.Float64()*8, 1 + rng.Float64()*8, 1 + rng.Float64()*8}
		p0 := randVec(rng, 6)
		p1 := randVec(rng, 6)

		got := math.Sqrt(segmentBoxDistSq(p0, p1, half))
		want := oracleSegmentBoxDist(p0, p1, half, 4000)
		if err := got - want; err > worst {
			worst = err
		}
		// The solver must never UNDERSHOOT beyond float noise either: the
		// oracle is an upper bound, so a large negative error would mean the
		// solver found a distance that does not exist.
		if got-want < -1e-3 {
			t.Fatalf("solver %v is below the sampling oracle %v — it found a closer pair than exists", got, want)
		}
	}
	// Generous against the oracle's own sampling resolution, tight enough that
	// the 0.91 of alternating projection fails by six orders of magnitude.
	if worst > 1e-3 {
		t.Errorf("worst overestimate %.6g exceeds the oracle's resolution — "+
			"an overestimate is a false negative, i.e. a character through a wall", worst)
	}
}

// A degenerate capsule is a sphere, and must agree with the sphere-box test.
// The two routines share no code, so this is a real cross-check.
func TestCapsuleBox_DegeneratesToSphereBox(t *testing.T) {
	rng := rand.New(rand.NewSource(4242))
	for range 2000 {
		b := randomBox(rng)
		c := centerOf(&b).Add(randVec(rng, 8))
		r := float32(0.5 + rng.Float64()*4)

		sph := sphere(c.X, c.Y, c.Z, r)
		// Depth <= 2*Radius makes the segment zero-length: a sphere.
		cap := capsule(c.X, c.Y, c.Z, r, r, component.RotationFromAxisAngle(
			float32(rng.NormFloat64()), float32(rng.NormFloat64()), float32(rng.NormFloat64()),
			float32(rng.Float64()*6)))

		if got, want := overlapCapsuleBox(&cap, &b), overlapBoxSphere3(&b, &sph); got != want {
			t.Fatalf("degenerate capsule says %v, sphere says %v\n  box=%+v\n  at=%+v r=%v",
				got, want, b, c, r)
		}
	}
}

func TestCapsuleBox_Cases(t *testing.T) {
	id := component.RotationIdentity()
	b := box(0, 0, 0, 10, 10, 10, id) // half-extents 5
	// Standing capsule, radius 2, total height 20 -> segment z = -8..+8.
	for _, tc := range []struct {
		name string
		c    Entry
		want bool
	}{
		{"through the middle", capsule(0, 0, 0, 2, 20, id), true},
		{"beside, touching", capsule(6.5, 0, 0, 2, 20, id), true},
		{"beside, clear", capsule(8, 0, 0, 2, 20, id), false},
		{"above, cap touching", capsule(0, 0, 14, 2, 20, id), true},
		{"above, clear", capsule(0, 0, 20, 2, 20, id), false},
		// Along the box's diagonal the clearance is (x-5)*sqrt(2), so 6 is
		// 1.41 away and OVERLAPS a radius-2 capsule — my first version of
		// this case asserted false and was wrong. 8 clears by 4.24.
		{"diagonal, touching", capsule(6, 6, 6, 2, 20, id), true},
		{"diagonal, clear", capsule(8, 8, 6, 2, 20, id), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := overlapCapsuleBox(&tc.c, &b); got != tc.want {
				t.Errorf("overlap = %v, want %v", got, tc.want)
			}
		})
	}
}

// oracleSegmentBoxDist walks the segment densely, taking the exact
// point-to-box distance at each sample. Converges to the true distance from
// above; a different algorithm from the solver under test.
func oracleSegmentBoxDist(p0, p1 component.Vec3, half [3]float64, samples int) float64 {
	best := math.Inf(1)
	d := p1.Sub(p0)
	for i := 0; i <= samples; i++ {
		s := p0.Add(d.Scale(float32(i) / float32(samples)))
		if v := pointBoxDistSq(s, half); v < best {
			best = v
		}
	}
	return math.Sqrt(best)
}

func randVec(rng *rand.Rand, scale float64) component.Vec3 {
	return component.Vec3{
		X: float32(rng.NormFloat64() * scale),
		Y: float32(rng.NormFloat64() * scale),
		Z: float32(rng.NormFloat64() * scale),
	}
}
