package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/quantize"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// TestEngineBindings3D_Layout pins the 3D profile's wire layout the way
// TestPhase1_TwoDBindingWidthsAreUnchanged pins the 2D one. Until phase 3
// generates an SDK against it, this and the schema test below are the only
// things holding the layout still.
func TestEngineBindings3D_Layout(t *testing.T) {
	world := ecs.NewWorld()
	group := EngineBindingsFor(Dimension3D).Bindings(world, 1000, 500, 2000)
	got := group.snapshotFields()

	// worldX/Y/Z (f32) + velX/Y/Z (qvel) + radius/width/height/depth (qsize)
	// + rot (qquat, ONE field).
	want := []int{4, 4, 4, 2, 2, 2, 2, 2, 2, 2, quantize.QuatWireSize}

	if len(got) != len(want) {
		t.Fatalf("3D engine bindings emit %d fields (%v), want %d (%v)", len(got), got, len(want), want)
	}
	total := 0
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("field %d width = %d, want %d", i, got[i], want[i])
		}
		total += got[i]
	}
	if total != 33 {
		t.Errorf("3D engine snapshot is %d bytes, want 33", total)
	}
}

// TestEngineBindings3D_DoesNotDisturbTheTwoDSet is the byte-invariance
// guarantee restated where a 3D change would break it: adding the 3D profile
// must not move the 2D layout by one field or one byte.
func TestEngineBindings3D_DoesNotDisturbTheTwoDSet(t *testing.T) {
	world := ecs.NewWorld()
	got := EngineBindingsFor(Dimension2D).Bindings(world, 1000, 500, 2000).snapshotFields()
	want := []int{4, 4, 2, 2, 2, 2, 2}
	if len(got) != len(want) {
		t.Fatalf("2D engine bindings emit %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("2D engine bindings emit %v, want %v", got, want)
		}
	}
}

// TestEngineBindings3D_SchemaFieldNames pins the names the generated decoders
// will bind to. A rename here is a silent client break: sdkgen emits these as
// property names.
func TestEngineBindings3D_SchemaFieldNames(t *testing.T) {
	world := ecs.NewWorld()
	group := EngineBindingsFor(Dimension3D).Bindings(world, 1000, 500, 2000).(*bindingGroup)

	var names, encodings []string
	for _, b := range group.bindings {
		for _, f := range b.schema().Fields {
			names = append(names, f.Name)
			encodings = append(encodings, f.Encoding)
		}
	}
	wantNames := []string{"worldX", "worldY", "worldZ", "velX", "velY", "velZ",
		"radius", "width", "height", "depth", "rot"}
	wantEnc := []string{"f32", "f32", "f32", "qvel", "qvel", "qvel",
		"qvel", "qvel", "qvel", "qvel", "qquat"}

	if len(names) != len(wantNames) {
		t.Fatalf("field names = %v, want %v", names, wantNames)
	}
	for i := range wantNames {
		if names[i] != wantNames[i] {
			t.Errorf("field %d name = %q, want %q", i, names[i], wantNames[i])
		}
		if encodings[i] != wantEnc[i] {
			t.Errorf("field %d encoding = %q, want %q", i, encodings[i], wantEnc[i])
		}
	}
}

// TestProvidesOrientation covers the marker that BuildReplicators uses to
// refuse a double-emitted orientation.
func TestProvidesOrientation(t *testing.T) {
	world := ecs.NewWorld()
	rotMap := ecs.NewMap1[component.Rotation](world)
	colliderMap := ecs.NewMap1[component.Collider](world)

	if !ProvidesOrientation(QQuat(rotMap)) {
		t.Error("QQuat does not report orientation")
	}
	if !ProvidesOrientation(QAngle(rotMap)) {
		t.Error("QAngle does not report orientation")
	}
	if ProvidesOrientation(QSize(colliderMap, 100)) {
		t.Error("QSize reports orientation")
	}
	// Grouped, which is how the engine set arrives.
	if !ProvidesOrientation(EngineBindingsFor(Dimension3D).Bindings(world, 1000, 500, 2000)) {
		t.Error("the 3D engine set does not report orientation")
	}
	if ProvidesOrientation(EngineBindingsFor(Dimension2D).Bindings(world, 1000, 500, 2000)) {
		t.Error("the 2D engine set reports orientation — it must leave it to the game")
	}
}

// TestQQuatBinding_AbsentRotationIsIdentity — the framework zero-fills
// components, and an entity with no Rotation must replicate as identity rather
// than as a zero quaternion, which is not a rotation at all.
func TestQQuatBinding_AbsentRotationIsIdentity(t *testing.T) {
	world := ecs.NewWorld()
	rotMap := ecs.NewMap1[component.Rotation](world)
	b := QQuat(rotMap).(*qQuatBinding)

	entity := world.NewEntity()
	buf := make([]byte, 16)
	w := quantize.NewSnapshotWriter(buf)
	b.snapshot(entity, w, nil, spatial.Entry{})

	r := quantize.NewSnapshotReader(w.Bytes())
	x, y, z, wq := r.UnQQuat()
	if x != 0 || y != 0 || z != 0 || wq != 1 {
		t.Fatalf("absent Rotation encoded as %v,%v,%v,%v, want identity", x, y, z, wq)
	}
}
