package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// hasLOSOnGrid wraps spatial.HashGrid.Raycast with the LayerStatic mask.
// This test exercises the wrapper without needing a full GameWorld — we
// build the grid directly and place a wall in the path.
func TestHasLOS_ClearAndBlocked(t *testing.T) {
	g := spatial.NewHashGrid(100)
	if !hasLOSOnGrid(g, vec2(0, 0), vec2(500, 0)) {
		t.Fatal("expected clear LOS on empty grid")
	}
	// Place a static wall in the line.
	w := ecs.NewWorld()
	e := w.NewEntity()
	g.Register(spatial.Entry{
		Entity: e, X: 250, Y: 0,
		Radius: 60, Width: 80, Height: 40, Rotation: 0,
		Shape: spatial.ShapeRect, Layer: spatial.LayerStatic,
	})
	if hasLOSOnGrid(g, vec2(0, 0), vec2(500, 0)) {
		t.Fatal("expected blocked LOS through wall")
	}
}
