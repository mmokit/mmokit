package spatial

import (
	"math"
	"testing"

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
	if got.Radius != 5 || got.Width != 8 || got.Height != 9 {
		t.Errorf("extents = (%v, %v, %v), want (5, 8, 9)", got.Radius, got.Width, got.Height)
	}
	if got.Layer != LayerStatic {
		t.Errorf("Layer = %v, want %v — a zero layer is invisible to every masked query", got.Layer, LayerStatic)
	}
	if got.Shape != component.ShapeBox {
		t.Errorf("Shape = %v, want box — a zero shape collides as a sphere", got.Shape)
	}
}

// A nil rotation must read as identity rather than panicking: Stage.Spawn does
// not add a Rotation, and a 2D game need never carry one.
func TestEntryFrom_NilRotationIsIdentity(t *testing.T) {
	w := ecs.NewWorld()
	got := EntryFrom(w.NewEntity(), &component.Position{}, &component.Collider{}, nil)
	if got.Yaw != 0 {
		t.Errorf("Yaw = %v, want 0 for a nil rotation", got.Yaw)
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
		if got.Yaw != rot.Yaw() {
			t.Errorf("angle %v: Yaw = %v, want rot.Yaw() = %v", angle, got.Yaw, rot.Yaw())
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
