package spatial

import (
	"fmt"

	"github.com/mmokit/mmokit/pkg/component"
)

// Shape dispatch.
//
// Both the narrow phase and the raycast pick an implementation by shape. They
// used to do it with if-chains that FELL THROUGH: checkCollision tested the
// sphere/sphere, box/sphere and sphere/box pairs and then ran the OBB routine
// for anything else, and the raycast skipped a shape it did not recognise. So
// adding a discriminant shipped an entity that collided as a degenerate box
// and was invisible to line of sight, with nothing to notice.
//
// These are tables sized to component.ShapeCount, and init() refuses to start
// the process if any slot is unfilled. Adding a shape is now a startup panic
// naming the missing pair rather than a silent wrong answer.
//
// A slot that is deliberately not implemented yet holds a NAMED function, not
// nil. Nil is always a mistake; a named function is a decision with a place to
// write the reason.

// overlapFunc reports whether two entries' shapes intersect. Both pointers are
// read-only.
type overlapFunc func(a, b *Entry) bool

// rayFunc reports the parametric distance along from→to at which the ray first
// meets e, and whether it does at all.
type rayFunc func(from, to Vec2, e *Entry) (float32, bool)

// overlapTable is indexed [a.Shape][b.Shape]. It must be symmetric in the
// sense that dispatching (a,b) and (b,a) gives the same answer; init() cannot
// check that exhaustively, so it checks that both slots are filled and each
// pair's two entries are documented as a matched set below.
var overlapTable [component.ShapeCount][component.ShapeCount]overlapFunc

// rayTable is indexed [e.Shape].
var rayTable [component.ShapeCount]rayFunc

func init() {
	overlapTable[component.ShapeSphere][component.ShapeSphere] = overlapSphereSphere
	overlapTable[component.ShapeBox][component.ShapeSphere] = overlapBoxSphere
	overlapTable[component.ShapeSphere][component.ShapeBox] = overlapSphereBox
	overlapTable[component.ShapeBox][component.ShapeBox] = overlapBoxBox

	// Capsule pairs are phase 4b unit 8. Named rather than nil so the gap is a
	// decision on the record: until the three-axis narrow phase lands, a
	// capsule does not collide, and it says so here rather than by falling
	// through to a box test that would answer confidently and wrongly.
	overlapTable[component.ShapeCapsule][component.ShapeSphere] = overlapCapsuleUnimplemented
	overlapTable[component.ShapeSphere][component.ShapeCapsule] = overlapCapsuleUnimplemented
	overlapTable[component.ShapeCapsule][component.ShapeBox] = overlapCapsuleUnimplemented
	overlapTable[component.ShapeBox][component.ShapeCapsule] = overlapCapsuleUnimplemented
	overlapTable[component.ShapeCapsule][component.ShapeCapsule] = overlapCapsuleUnimplemented

	rayTable[component.ShapeSphere] = rayCircleEntry
	rayTable[component.ShapeBox] = rayRectHit
	rayTable[component.ShapeCapsule] = rayCapsuleUnimplemented

	assertDispatchComplete()
}

// assertDispatchComplete panics at process start if any shape pair or ray
// slot is unfilled.
//
// A panic, not a log: an unfilled slot means a shape that silently does not
// collide or is silently invisible to sight, and both are worse in production
// than a process that refuses to start. It fires at init, so it cannot reach a
// running cluster — a build with a missing slot dies on the developer's first
// run and in CI, which is the whole point.
func assertDispatchComplete() {
	for a := range component.ShapeCount {
		for b := range component.ShapeCount {
			if overlapTable[a][b] == nil {
				panic(fmt.Sprintf(
					"spatial: no overlap implementation for %v vs %v — a shape was added to "+
						"component.ShapeKind without wiring its pairs in pkg/spatial/dispatch.go",
					component.ShapeKind(a), component.ShapeKind(b)))
			}
		}
		if rayTable[a] == nil {
			panic(fmt.Sprintf(
				"spatial: no ray implementation for %v — a shape was added to "+
					"component.ShapeKind without wiring its raycast in pkg/spatial/dispatch.go",
				component.ShapeKind(a)))
		}
	}
}

// overlap dispatches a shape pair. Shape is validated at EntryFrom and
// Stage.Spawn, so the indices are in range by construction.
func overlap(a, b *Entry) bool { return overlapTable[a.Shape][b.Shape](a, b) }

// --- implementations -------------------------------------------------------

// Two spheres that reached here already passed the broad-phase bounding-sphere
// test, which for a sphere IS the shape test.
func overlapSphereSphere(a, b *Entry) bool { return true }

func overlapBoxSphere(box, sphere *Entry) bool { return obbCircleCollision(box, sphere) }
func overlapSphereBox(sphere, box *Entry) bool { return obbCircleCollision(box, sphere) }
func overlapBoxBox(a, b *Entry) bool           { return obbOBBCollision(a, b) }

// overlapCapsuleUnimplemented is phase 4b unit 8's work.
//
// Returns false — a capsule collides with nothing until the three-axis narrow
// phase exists. Returning false rather than falling through to the box test is
// the point: a wrong "no collision" is a character walking through a wall,
// which a playtester finds in seconds, while a wrong "collision" from a box
// test on capsule extents is a character catching on nothing, which reads as
// jitter and gets attributed to the network.
func overlapCapsuleUnimplemented(a, b *Entry) bool { return false }

// rayCircleEntry adapts rayCircleHit, which takes loose scalars, to the table.
func rayCircleEntry(from, to Vec2, e *Entry) (float32, bool) {
	return rayCircleHit(from, to, e.X, e.Y, e.Radius)
}

// rayCapsuleUnimplemented is phase 4b unit 11's work. A capsule does not block
// a ray until then, and says so here rather than being skipped by a dispatch
// that does not recognise it.
func rayCapsuleUnimplemented(from, to Vec2, e *Entry) (float32, bool) { return 0, false }
