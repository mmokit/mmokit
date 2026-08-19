package system

import (
	"reflect"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
)

// Phase 1's acceptance criterion, made executable.
//
// The core types are 3D-capable — Position and Velocity carry Z, Rotation is a
// quaternion, Collider carries Depth — and the 2D wire output did not move by
// one byte. The repository-level proof of the second half is that regenerating
// both SDKs and every golden yields an empty diff; this is the unit-level
// statement of WHY, so a future change that would break it fails here rather
// than in a regeneration nobody runs locally.
//
// The invariance is structural, not coincidental: each engine binding names the
// source fields it emits as separate identifiers, so widening the SOURCE struct
// cannot change the EMITTED schema. The three things that would break it are
// named in docs/roadmap.md §7.5.4 — moving QAngle into the binding set, adding
// Depth to QSize, or making Vec3 a nested field.
func TestPhase1_CoreTypesAreWidened(t *testing.T) {
	for _, c := range []struct {
		name, field string
		typ         reflect.Type
	}{
		{"Position", "Z", reflect.TypeFor[component.Position]()},
		{"Velocity", "Z", reflect.TypeFor[component.Velocity]()},
		{"Collider", "Depth", reflect.TypeFor[component.Collider]()},
	} {
		if _, ok := c.typ.FieldByName(c.field); !ok {
			t.Errorf("%s has no %s field — phase 1 widened it", c.name, c.field)
		}
	}

	// Rotation is a quaternion, not a scalar angle.
	rt := reflect.TypeFor[component.Rotation]()
	if _, ok := rt.FieldByName("Angle"); ok {
		t.Error("Rotation still has an Angle field — phase 1 replaced it with a quaternion")
	}
	for _, f := range []string{"X", "Y", "Z", "W"} {
		if _, ok := rt.FieldByName(f); !ok {
			t.Errorf("Rotation has no %s field", f)
		}
	}
}

// The 2D engine binding set must emit exactly the widths it emitted before the
// widening. This is the byte-level half of the criterion, independent of the
// committed goldens.
func TestPhase1_TwoDBindingWidthsAreUnchanged(t *testing.T) {
	world := ecs.NewWorld()
	group := EngineBindingsFor(Dimension2D).Bindings(world, 1000, 500, 2000)
	got := group.snapshotFields()
	// worldX, worldY (f32) + velX, velY (qvel) + radius, width, height (qsize).
	// No Z, no depth: a 2D profile carries the widened types and emits neither.
	want := []int{4, 4, 2, 2, 2, 2, 2}

	if len(got) != len(want) {
		t.Fatalf("2D engine bindings emit %d fields (%v), want %d (%v) — "+
			"a widened core type reached the wire", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("2D field %d is %d bytes, want %d", i, got[i], want[i])
		}
	}
}

// Depth must stay out of QSize. Adding it there is one of the three named ways
// to break the acceptance criterion, and it would be an easy, plausible edit.
func TestPhase1_QSizeDoesNotEmitDepth(t *testing.T) {
	world := ecs.NewWorld()
	col := ecs.NewMap1[component.Collider](world)
	if got := QSize(col, 500).snapshotFields(); len(got) != 3 {
		t.Errorf("QSize emits %v (%d fields), want 3 — radius, width, height. "+
			"Depth must not reach the 2D wire", got, len(got))
	}
}
