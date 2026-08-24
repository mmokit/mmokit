package spatial

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/component"
)

// TestDispatchTablesAreComplete is the assertion init() makes at process
// start, restated as a test so a missing slot fails in CI with a name rather
// than as a panic in someone's first run.
//
// It is the structural replacement for a comment that asked people to remember
// two dispatch sites. The old if-chains fell through: an unrecognised shape
// collided as a degenerate box and was skipped entirely by the raycast, so it
// was simultaneously solid to collision and invisible to sight.
func TestDispatchTablesAreComplete(t *testing.T) {
	for a := range component.ShapeCount {
		for b := range component.ShapeCount {
			if overlapTable[a][b] == nil {
				t.Errorf("no overlap implementation for %v vs %v",
					component.ShapeKind(a), component.ShapeKind(b))
			}
		}
		if rayTable[a] == nil {
			t.Errorf("no ray implementation for %v", component.ShapeKind(a))
		}
	}
}

// TestDispatchTableIsSymmetric pins that a pair answers the same either way
// round. Asymmetry here is a bug that only appears for one iteration order,
// and QueryCollisions' order comes from a Go map.
func TestDispatchTableIsSymmetric(t *testing.T) {
	w := makeWorld()
	shapes := []component.ShapeKind{component.ShapeSphere, component.ShapeBox, component.ShapeCapsule}
	for _, sa := range shapes {
		for _, sb := range shapes {
			a := Entry{Entity: newEntity(w), X: 0, Y: 0, Radius: 10, Width: 20, Height: 20, Depth: 20, Shape: sa}
			b := Entry{Entity: newEntity(w), X: 5, Y: 0, Radius: 10, Width: 20, Height: 20, Depth: 20, Shape: sb}
			a.Rot = component.RotationIdentity()
			b.Rot = component.RotationIdentity()
			if got, want := overlap(&b, &a), overlap(&a, &b); got != want {
				t.Errorf("%v vs %v: overlap(a,b)=%v but overlap(b,a)=%v", sa, sb, want, got)
			}
		}
	}
}

// TestCapsulePairsAreUnimplemented records exactly which pairs phase 4b unit 8
// still owes, so that unit has a checklist and this test is what turns green
// when it lands.
//
// A capsule returning false is a deliberate wrong answer, not an accident: a
// character passes through a wall, which a playtester finds immediately.
// Falling through to the box test — which is what the old if-chain did — would
// answer confidently and wrongly, and read as jitter.
func TestCapsulePairsAreUnimplemented(t *testing.T) {
	shapes := []component.ShapeKind{component.ShapeSphere, component.ShapeBox, component.ShapeCapsule}
	for _, s := range shapes {
		if overlapTable[component.ShapeCapsule][s] == nil || overlapTable[s][component.ShapeCapsule] == nil {
			t.Fatalf("capsule vs %v is nil rather than a named placeholder", s)
		}
	}
	if rayTable[component.ShapeCapsule] == nil {
		t.Fatal("capsule ray slot is nil rather than a named placeholder")
	}
}

// TestBound_CapsuleUsesItsAxis is the one shape whose extent can exceed its
// authored radius, and the broad-phase gate measures Bound() for exactly that
// reason. A capsule gated on Radius alone is rejected before the narrow phase
// ever sees it.
func TestBound_CapsuleUsesItsAxis(t *testing.T) {
	tall := Entry{Shape: component.ShapeCapsule, Radius: 10, Depth: 60}
	if got := tall.Bound(); got != 30 {
		t.Errorf("tall capsule Bound() = %v, want 30 (half its tip-to-tip height)", got)
	}
	// Depth <= 2*Radius is a sphere, and its bound is the radius.
	squat := Entry{Shape: component.ShapeCapsule, Radius: 10, Depth: 15}
	if got := squat.Bound(); got != 10 {
		t.Errorf("squat capsule Bound() = %v, want 10 (the cap radius)", got)
	}
	// A box's bound is its authored radius whatever its depth — the contract
	// on component.Collider.Radius says the author makes it bound the shape.
	box := Entry{Shape: component.ShapeBox, Radius: 10, Depth: 60}
	if got := box.Bound(); got != 10 {
		t.Errorf("box Bound() = %v, want its authored radius 10", got)
	}
}
