package spatial

import (
	"math"
	"reflect"
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

// TestEntryFrom_CoversEveryColliderField is the ViewerInfo-Z failure shape,
// caught structurally instead of by remembering.
//
// EntryFrom is a hand-written field list. Omit one line — entry.Depth =
// col.Depth, say — and it compiles, go vet is clean, every 2D fixture passes
// because the field is zero there anyway, and cube3d's aoi_test passes too
// because it reads only Z. Exactly how ViewerInfo shipped without its Z.
//
// So this walks Collider's fields REFLECTIVELY, sets each to a distinct
// non-zero value, and asserts the corresponding Entry field arrived. A field
// added to Collider and forgotten in EntryFrom fails here by name, without
// anyone having thought to write an assertion for it.
func TestEntryFrom_CoversEveryColliderField(t *testing.T) {
	// Field name on Collider -> field name on Entry. Same name unless noted.
	want := map[string]float32{
		"Radius": 3, "Width": 5, "Height": 7, "Depth": 11,
	}
	var col component.Collider
	cv := reflect.ValueOf(&col).Elem()
	for name, v := range want {
		f := cv.FieldByName(name)
		if !f.IsValid() {
			t.Fatalf("Collider has no field %s — update this test with the rename", name)
		}
		f.SetFloat(float64(v))
	}
	col.Layer = LayerStatic
	col.Shape = component.ShapeBox

	got := EntryFrom(makeWorld().NewEntity(), &component.Position{}, &col, nil)
	ev := reflect.ValueOf(got)
	for name, v := range want {
		f := ev.FieldByName(name)
		if !f.IsValid() {
			t.Errorf("Entry has no field %s, so Collider.%s reaches nothing", name, name)
			continue
		}
		if float32(f.Float()) != v {
			t.Errorf("Entry.%s = %v, want %v — EntryFrom does not carry Collider.%s",
				name, f.Float(), v, name)
		}
	}

	// Every float32 field on Collider must appear in the map above, so ADDING
	// one to Collider fails this test until someone decides what it means for
	// a spatial entry.
	ct := reflect.TypeOf(component.Collider{})
	for i := range ct.NumField() {
		f := ct.Field(i)
		if f.Type.Kind() != reflect.Float32 {
			continue
		}
		if _, covered := want[f.Name]; !covered {
			t.Errorf("Collider.%s is a new float32 field that this test does not cover — "+
				"decide whether EntryFrom should carry it, then add it here", f.Name)
		}
	}
}

// A drifted quaternion must be normalized on the way into the grid, not at
// every narrow-phase test.
func TestEntryFrom_NormalizesTheRotation(t *testing.T) {
	drifted := component.Rotation{W: 5}
	got := EntryFrom(makeWorld().NewEntity(), &component.Position{}, &component.Collider{}, &drifted)
	n := math.Sqrt(float64(got.Rot.X*got.Rot.X + got.Rot.Y*got.Rot.Y +
		got.Rot.Z*got.Rot.Z + got.Rot.W*got.Rot.W))
	if math.Abs(n-1) > 1e-6 {
		t.Errorf("|Rot| = %v, want 1 — a scale error reaches the projected half-extents", n)
	}
}
