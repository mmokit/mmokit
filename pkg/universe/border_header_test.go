package universe

import (
	"math"
	"testing"

	"github.com/mmokit/mmokit/pkg/component"
)

// TestBorderHeader2DWidthIsUnchanged is the 2D-pays-nothing guarantee, stated
// where a 3D change would break it. The 2D border header has always been 26
// bytes and phase 2 must not move it: a 2D cluster pays no wire byte for a
// feature it cannot use, on the mesh as much as on the client wire.
func TestBorderHeader2DWidthIsUnchanged(t *testing.T) {
	if borderHeaderSize(Dimension2D) != 26 {
		t.Fatalf("2D border header is %d bytes, want 26", borderHeaderSize(Dimension2D))
	}
	got := appendBorderHeader(nil, Dimension2D, 1, 2, 3, 4, 5, 6, 7,
		component.RotationFromYaw(0.5), 999)
	if len(got) != 26 {
		t.Fatalf("2D header encoded to %d bytes, want 26", len(got))
	}
}

// TestBorderHeader3DWidth pins the 3D layout: three coordinates, three
// velocity axes, a 7-byte quaternion.
func TestBorderHeader3DWidth(t *testing.T) {
	if borderHeaderSize(Dimension3D) != 37 {
		t.Fatalf("3D border header is %d bytes, want 37", borderHeaderSize(Dimension3D))
	}
	got := appendBorderHeader(nil, Dimension3D, 1, 2, 3, 4, 5, 6, 7,
		component.RotationIdentity(), 999)
	if len(got) != 37 {
		t.Fatalf("3D header encoded to %d bytes, want 37", len(got))
	}
}

// TestBorderHeader3DRoundTrip is the unit's reason to exist: Z and full
// orientation survive a border push. Before this, the header carried neither —
// a 3D replica sat at Z=0 and its pitch and roll were flattened to yaw at
// every cell line.
func TestBorderHeader3DRoundTrip(t *testing.T) {
	rot := component.RotationFromAxisAngle(0.3, 0.7, 1.1, 1.234)
	buf := appendBorderHeader(nil, Dimension3D,
		100.5, -200.25, 42.75, 3.5, 11, -22, 33, rot, 123456)
	buf = append(buf, 0, 0) // minimum tail

	h, tail, ok := parseBorderHeader(buf, Dimension3D)
	if !ok {
		t.Fatal("parse failed")
	}
	if len(tail) != 2 {
		t.Fatalf("tail is %d bytes, want 2", len(tail))
	}
	if h.WorldX != 100.5 || h.WorldY != -200.25 || h.WorldZ != 42.75 {
		t.Errorf("position = %v,%v,%v, want 100.5,-200.25,42.75", h.WorldX, h.WorldY, h.WorldZ)
	}
	if h.Radius != 3.5 {
		t.Errorf("radius = %v, want 3.5", h.Radius)
	}
	// Velocity is quantized; check within a quantum rather than exactly.
	for _, c := range []struct {
		name      string
		got, want float32
	}{{"vx", h.VX, 11}, {"vy", h.VY, -22}, {"vz", h.VZ, 33}} {
		if math.Abs(float64(c.got-c.want)) > 0.1 {
			t.Errorf("%s = %v, want ~%v", c.name, c.got, c.want)
		}
	}
	if h.ProducedAtMs != 123456 {
		t.Errorf("producedAtMs = %d, want 123456", h.ProducedAtMs)
	}

	// The whole quaternion, not just its yaw. A yaw-only comparison would
	// pass against the flattening this unit removes.
	for _, c := range []struct {
		name      string
		got, want float32
	}{{"X", h.Rot.X, rot.X}, {"Y", h.Rot.Y, rot.Y}, {"Z", h.Rot.Z, rot.Z}, {"W", h.Rot.W, rot.W}} {
		if math.Abs(float64(c.got-c.want)) > 1e-4 {
			t.Errorf("rotation %s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if math.Abs(float64(h.Rot.X)) < 1e-3 || math.Abs(float64(h.Rot.Y)) < 1e-3 {
		t.Fatal("fixture rotation has no pitch or roll — it cannot detect yaw flattening")
	}
}

// TestBorderHeader2DRoundTrip — the 2D path still carries yaw, unchanged.
func TestBorderHeader2DRoundTrip(t *testing.T) {
	rot := component.RotationFromYaw(1.75)
	buf := appendBorderHeader(nil, Dimension2D, 10, 20, 999, 4, 1, 2, 999, rot, 77)
	buf = append(buf, 0, 0)

	h, _, ok := parseBorderHeader(buf, Dimension2D)
	if !ok {
		t.Fatal("parse failed")
	}
	if h.WorldZ != 0 || h.VZ != 0 {
		t.Errorf("2D header decoded Z state (%v, %v); it carries none", h.WorldZ, h.VZ)
	}
	if math.Abs(float64(h.Rot.Yaw()-1.75)) > 1e-3 {
		t.Errorf("yaw = %v, want 1.75", h.Rot.Yaw())
	}
}

// TestBorderHeaderRejectsShortBuffer — a truncated frame must be skipped, not
// decoded out of adjacent memory.
func TestBorderHeaderRejectsShortBuffer(t *testing.T) {
	for _, d := range []Dimension{Dimension2D, Dimension3D} {
		short := make([]byte, borderHeaderSize(d)+borderMinTail-1)
		if _, _, ok := parseBorderHeader(short, d); ok {
			t.Errorf("%s: accepted a buffer one byte short of the minimum", d)
		}
	}
}

// TestUpsertBorderReplicaPreservesFullRotation pins the receive-side half.
//
// upsertBorderReplica used to call SetYaw, which rebuilds the quaternion from
// yaw alone and so discarded pitch and roll on every border update. That is
// invisible in a 2D profile — yaw is all there is — and total orientation loss
// at every cell line in a 3D one. The assertion is dimension-independent
// because the function takes a Rotation either way, so a 2D fixture is enough.
func TestUpsertBorderReplicaPreservesFullRotation(t *testing.T) {
	stage := newTestStage(t)

	const netID uint32 = 4242
	create := component.RotationFromAxisAngle(1, 0.5, 0.25, 0.9)
	stage.upsertBorderReplica(netID, 1, 3, 10, 20, 30, 4, 1, 2, 3, create, "cell_1_0", 100, nil)

	ent, ok := stage.replicaNetIDs[netID]
	if !ok {
		t.Fatal("replica was not created")
	}
	if got := *stage.rotMap.Get(ent); !rotationsClose(got, create) {
		t.Fatalf("created replica rotation = %+v, want %+v", got, create)
	}

	// Now UPDATE the same replica — this is the branch that flattened.
	update := component.RotationFromAxisAngle(0.2, 1, 0.75, 2.1)
	stage.upsertBorderReplica(netID, 1, 3, 11, 21, 31, 4, 1, 2, 3, update, "cell_1_0", 200, nil)

	got := *stage.rotMap.Get(ent)
	if !rotationsClose(got, update) {
		t.Fatalf("updated replica rotation = %+v, want %+v", got, update)
	}
	// Non-vacuity guard: the fixture must actually carry pitch and roll, or a
	// SetYaw implementation would pass this test.
	if math.Abs(float64(update.X)) < 1e-3 || math.Abs(float64(update.Y)) < 1e-3 {
		t.Fatal("fixture rotation has no pitch or roll")
	}

	// Z on both position and velocity must survive too.
	if pos := stage.posMap.Get(ent); pos.Z != 31 {
		t.Errorf("replica Position.Z = %v, want 31", pos.Z)
	}
	if vel := stage.velMap.Get(ent); vel.Z != 3 {
		t.Errorf("replica Velocity.Z = %v, want 3", vel.Z)
	}
}

func rotationsClose(a, b component.Rotation) bool {
	return math.Abs(float64(a.X-b.X)) < 1e-5 &&
		math.Abs(float64(a.Y-b.Y)) < 1e-5 &&
		math.Abs(float64(a.Z-b.Z)) < 1e-5 &&
		math.Abs(float64(a.W-b.W)) < 1e-5
}
