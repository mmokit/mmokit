package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
)

// TestPlayerViewerSource_CarriesHeight is the only thing in the tree that can
// catch an omitted Z here, and it exists because omitting it is invisible in
// every other way.
//
// ViewerInfo is built at exactly one place. If that literal sets X and Y and
// not Z, it compiles, `go vet` is silent, all 58 ViewerInfo fixtures in the
// tree are at Z=0 either way, and schema-check is green because none of this
// is wire-visible. Every viewer would then measure area of interest from the
// ground plane instead of from its own eye height, so the effective horizontal
// radius silently becomes sqrt(R^2 - z^2) — correct-looking, and shrinking as
// the world gets taller.
func TestPlayerViewerSource_CarriesHeight(t *testing.T) {
	world := ecs.NewWorld()
	posMap := ecs.NewMap1[component.Position](world)

	pm := engine.NewPlayerManager()
	pm.RegisterPlayer(1, "flyer")
	sess := pm.ByConnID(1)
	if sess == nil {
		t.Fatal("RegisterPlayer: no session")
	}

	entity := world.NewEntity()
	posMap.Add(entity, &component.Position{X: 1, Y: 2, Z: 3})
	sess.Entity = entity
	if err := pm.Transition(sess, engine.StateActive); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	viewers := NewPlayerViewerSource(world, pm, engine.StateActive).ActiveViewers()
	if len(viewers) != 1 {
		t.Fatalf("got %d viewers, want 1", len(viewers))
	}
	v := viewers[0]
	if v.X != 1 || v.Y != 2 {
		t.Errorf("viewer XY = (%v, %v), want (1, 2)", v.X, v.Y)
	}
	if v.Z != 3 {
		t.Errorf("viewer Z = %v, want 3 — area of interest would be measured from the ground plane", v.Z)
	}
	if got := v.Center(); got != (component.Vec3{X: 1, Y: 2, Z: 3}) {
		t.Errorf("Center() = %+v, want {1 2 3}", got)
	}
}

// A 2D game never sets Z, and its viewers must read as being on the plane —
// which is what makes the spherical area-of-interest test that follows
// observationally identical for it.
func TestPlayerViewerSource_TwoDViewerIsOnThePlane(t *testing.T) {
	world := ecs.NewWorld()
	posMap := ecs.NewMap1[component.Position](world)

	pm := engine.NewPlayerManager()
	pm.RegisterPlayer(1, "walker")
	sess := pm.ByConnID(1)
	entity := world.NewEntity()
	posMap.Add(entity, &component.Position{X: 500, Y: 500})
	sess.Entity = entity
	if err := pm.Transition(sess, engine.StateActive); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	viewers := NewPlayerViewerSource(world, pm, engine.StateActive).ActiveViewers()
	if len(viewers) != 1 || viewers[0].Z != 0 {
		t.Fatalf("2D viewer Z = %v, want 0", viewers[0].Z)
	}
}
