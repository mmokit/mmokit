package universe

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
	netpkg "github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// TestSpawn_RegistersTheWholeCollider is the gap Stage.Spawn shipped with: it
// registered a spatial entry carrying Entity, X, Y and Radius, and left Layer
// and Shape at zero.
//
// A zero Layer is invisible to EVERY layer-masked query — Raycast filters on
// Layer&mask before it tests anything — so a freshly spawned wall blocked
// nothing until the next SpatialSystem tick overwrote the entry, and blocked
// nothing forever in a process that registers no SpatialSystem.
//
// The raycast is the assertion rather than a field-by-field comparison because
// it is the behaviour that was actually broken, and it stays true if the entry
// is ever built some other way.
func TestSpawn_RegistersTheWholeCollider(t *testing.T) {
	stage := newSpawnGridStage(t)

	stage.Spawn(
		component.Position{X: 250, Y: 0},
		component.Collider{
			Radius: 60,
			Width:  80,
			Height: 40,
			Layer:  spatial.LayerStatic,
			Shape:  component.ShapeBox,
		},
	)

	// No tick has run. This is the window the bug lived in.
	_, hit := stage.SpatialGrid().Raycast(
		component.Vec3{X: 0, Y: 0},
		component.Vec3{X: 500, Y: 0},
		spatial.LayerStatic,
		nil,
	)
	if !hit {
		t.Fatal("a wall spawned on LayerStatic does not block a LayerStatic ray before the first tick — " +
			"Stage.Spawn registered it with a zero Layer")
	}
}

// The shape matters separately: a zero Shape is ShapeSphere, so a spawned box
// is broad-phase-visible but narrow-phase-tested as a circle of its bounding
// radius. That over-reports hits rather than under-reporting them, which is
// why it survived unnoticed alongside the Layer bug.
func TestSpawn_RegistersTheShapeDiscriminant(t *testing.T) {
	stage := newSpawnGridStage(t)

	e := stage.Spawn(
		component.Position{X: 100, Y: 100},
		component.Collider{Radius: 10, Width: 20, Height: 4, Layer: spatial.LayerProp, Shape: component.ShapeBox},
	)

	var found *spatial.Entry
	for _, entry := range stage.SpatialGrid().QueryRadius(component.Vec3{X: 100, Y: 100}, 50, nil) {
		if entry.Entity == e.Handle() {
			found = &entry
			break
		}
	}
	if found == nil {
		t.Fatal("spawned collider is not in the grid at all")
	}
	if found.Shape != component.ShapeBox {
		t.Errorf("Shape = %v, want box", found.Shape)
	}
	if found.Layer != spatial.LayerProp {
		t.Errorf("Layer = %v, want %v", found.Layer, spatial.LayerProp)
	}
	if found.Width != 20 || found.Height != 4 {
		t.Errorf("extents = (%v, %v), want (20, 4)", found.Width, found.Height)
	}
}

// newSpawnGridStage builds the smallest stage that has a spatial grid.
// NewStage does not attach one — the coordinator does, at
// coordinator.go:2545 — so a test that forgets SetSpatialGrid asserts
// nothing and passes.
func newSpawnGridStage(t *testing.T) *Stage {
	t.Helper()
	eng := engine.New(engine.DefaultConfig(), netpkg.NewConnManager(), logger.New())
	cellID, err := ParseCellID("cell_0_0")
	if err != nil {
		t.Fatalf("ParseCellID: %v", err)
	}
	stage := NewStage(eng, cellID, 300, nil, NewWireRegistry())
	stage.SetSpatialGrid(spatial.NewHashGrid(100))
	if stage.SpatialGrid() == nil {
		t.Fatal("stage has no spatial grid — the assertions below would be vacuous")
	}
	return stage
}
