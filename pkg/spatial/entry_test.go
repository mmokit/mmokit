package spatial

import (
	"math"
	"testing"
	"unsafe"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
)

// TestEntryFrom_CarriesEveryColliderField is the assertion the three
// hand-written call sites this replaced could not make about each other.
// Stage.Spawn's variant filled four fields and left Layer and Shape at zero,
// which made a freshly spawned wall invisible to every layer-masked query.
func TestEntryFrom_CarriesEveryColliderField(t *testing.T) {
	w := ecs.NewWorld()
	e := w.NewEntity()

	got := EntryFrom(e,
		&component.Position{X: 10, Y: 20, Z: 30},
		&component.Collider{Radius: 5, Width: 8, Height: 9, Depth: 7, Layer: LayerStatic, Shape: component.ShapeBox},
		nil,
	)

	if got.Entity != e {
		t.Errorf("Entity = %v, want %v", got.Entity, e)
	}
	if got.X != 10 || got.Y != 20 || got.Z != 30 {
		t.Errorf("position = (%v, %v, %v), want (10, 20, 30)", got.X, got.Y, got.Z)
	}
	// Depth included: it has been on the wire since phase 1 and no spatial
	// code consumed it until phase 4b needed a box to have a third extent.
	if got.Radius != 5 || got.Width != 8 || got.Height != 9 || got.Depth != 7 {
		t.Errorf("extents = (%v, %v, %v, %v), want (5, 8, 9, 7)",
			got.Radius, got.Width, got.Height, got.Depth)
	}
	if got.Layer != LayerStatic {
		t.Errorf("Layer = %v, want %v — a zero layer is invisible to every masked query", got.Layer, LayerStatic)
	}
	if got.Shape != component.ShapeBox {
		t.Errorf("Shape = %v, want box — a zero shape collides as a sphere", got.Shape)
	}
}

// The yaw must be exactly what component.Rotation reports, not a reimplemented
// extraction — the quaternion-to-yaw conversion is the kind of thing that gets
// re-derived slightly differently in a second place.
func TestEntryFrom_YawMatchesTheRotation(t *testing.T) {
	w := ecs.NewWorld()
	for _, angle := range []float32{0, 0.5, math.Pi / 2, 3, -1.25} {
		rot := component.RotationFromAxisAngle(0, 0, 1, angle)
		got := EntryFrom(w.NewEntity(), &component.Position{}, &component.Collider{}, &rot)
		if got.Rot.Yaw() != rot.Yaw() {
			t.Errorf("angle %v: Yaw = %v, want rot.Yaw() = %v", angle, got.Rot.Yaw(), rot.Yaw())
		}
	}
}

// Nil position and nil collider are not reachable from the current call sites,
// but EntryFrom is exported through the facade and a game may reach a
// collider-less entity. Returning a zero entry beats panicking on the tick path.
func TestEntryFrom_NilComponents(t *testing.T) {
	w := ecs.NewWorld()
	e := w.NewEntity()
	got := EntryFrom(e, nil, nil, nil)
	if got != (Entry{Entity: e}) {
		t.Errorf("EntryFrom with nil components = %+v, want a zero entry", got)
	}
}

// TestEntrySize pins the struct's width, because every widening of it is a
// decision and none should happen by accident.
//
// 56 bytes: Entity 8, XYZ 12, Radius/Width/Height/Depth 16, Rot 16, Layer and
// Shape 1 each, 2 padding. Phase 4b measured the cost of going from 40 to 56
// and found it in the noise — see pkg/system/replication_entry_bench_test.go —
// so this is a change-detector, not a budget.
func TestEntrySize(t *testing.T) {
	if got := unsafe.Sizeof(Entry{}); got != 56 {
		t.Errorf("sizeof(Entry) = %d, want 56 — widening it is a decision; "+
			"re-measure with BenchmarkReplicationSystem_Update before changing this", got)
	}
}

// TestEntryFrom_NilRotationIsIdentity pins the convention a 3D narrow phase
// has to respect.
//
// component.Rotation's zero value IS identity, but only through the
// accessors: normalized() maps a zero-norm quaternion to {0,0,0,1}. Read the
// fields directly and {0,0,0,0} yields a basis of three zero vectors, on which
// a three-axis SAT separates every pair — silently, and only for entities that
// never set a rotation, which is most of them.
func TestEntryFrom_NilRotationIsIdentity(t *testing.T) {
	w := makeWorld()
	got := EntryFrom(w.NewEntity(), &component.Position{}, &component.Collider{}, nil)
	if got.Rot != component.RotationIdentity() {
		t.Errorf("EntryFrom with nil rotation = %+v, want identity", got.Rot)
	}
	if got.Rot.Yaw() != 0 {
		t.Errorf("identity yaw = %v, want 0", got.Rot.Yaw())
	}
}

// Bound is derived rather than stored, so a hand-built Entry cannot have one
// that disagrees with its radius. A stored bound left at zero would be
// invisible to every broad-phase test, silently.
func TestEntryBound_IsDerivedFromRadius(t *testing.T) {
	handBuilt := Entry{Radius: 12} // no EntryFrom, no validation — the hazard
	if got := handBuilt.Bound(); got != 12 {
		t.Errorf("Bound() = %v, want 12", got)
	}
	var empty Entry
	if got := empty.Bound(); got != 0 {
		t.Errorf("zero Entry Bound() = %v, want 0", got)
	}
}
