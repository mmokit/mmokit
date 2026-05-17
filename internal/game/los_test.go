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
	if !hasLOSOnGrid(g, vec2(0, 0), vec2(500, 0), ecs.Entity{}) {
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
	if hasLOSOnGrid(g, vec2(0, 0), vec2(500, 0), ecs.Entity{}) {
		t.Fatal("expected blocked LOS through wall")
	}
}

// TestLOS_ShipColliderDoesNotSelfBlock is the regression test for the
// LayerPlayer↔LayerStatic bit-collision bug. Before the fix, ships and
// NPCs were spawned with Collider.Layer=LayerPlayer (numerically =1),
// which matched the LayerStatic mask (1<<0 = 1). The raycast's first
// bucket contains the caster's own collider at the ray origin, so
// hasLOSOnGrid always returned blocked → NPCs never aggro'd through clear
// space and the selection LOS check broke locks within 1s of every cast.
//
// The fix migrates ships/NPCs to spatial.LayerEntity (1<<2 = 4) so they
// no longer match the LayerStatic mask, AND extends hasLOSOnGrid to take
// a self-entity to skip (defense in depth: future code that does match
// the entity layer still has a clean way to ignore the caster).
func TestLOS_ShipColliderDoesNotSelfBlock(t *testing.T) {
	g := spatial.NewHashGrid(100)
	w := ecs.NewWorld()
	caster := w.NewEntity()
	// Register a "ship" entity at the ray origin. LayerEntity must NOT
	// match a LayerStatic mask — and even if a future change reintroduces
	// such a collision, the self-skip param keeps the caster's own
	// collider from blocking.
	g.Register(spatial.Entry{
		Entity: caster, X: 0, Y: 0,
		Radius: 20, Width: 40, Height: 20, Rotation: 0,
		Shape: spatial.ShapeRect, Layer: spatial.LayerEntity,
	})
	if !hasLOSOnGrid(g, vec2(0, 0), vec2(500, 0), caster) {
		t.Fatal("expected clear LOS — caster's own LayerEntity collider must not self-block sight check")
	}

	// Also verify hasShotLOSOnGrid (broader mask = LayerStatic|LayerProp)
	// doesn't self-block when source is passed.
	target := w.NewEntity()
	if !hasShotLOSOnGrid(g, vec2(0, 0), vec2(500, 0), caster, target) {
		t.Fatal("expected clear shot LOS — caster's own collider must not self-block beam check")
	}
}

// TestLOS_AsteroidPropDoesNotBlockSight verifies that asteroids (LayerProp)
// don't block plain sight (hasLOSOnGrid uses LayerStatic only). This pins
// the asteroid sight-transparency invariant — the dungeon-POI design
// expects NPCs to see and aggro through asteroid fields.
func TestLOS_AsteroidPropDoesNotBlockSight(t *testing.T) {
	g := spatial.NewHashGrid(100)
	w := ecs.NewWorld()
	asteroid := w.NewEntity()
	g.Register(spatial.Entry{
		Entity: asteroid, X: 250, Y: 0,
		Radius: 40, Shape: spatial.ShapeCircle, Layer: spatial.LayerProp,
	})
	if !hasLOSOnGrid(g, vec2(0, 0), vec2(500, 0), ecs.Entity{}) {
		t.Fatal("asteroid (LayerProp) must not block plain sight")
	}
	// But it DOES block shots (hasShotLOSOnGrid uses LayerStatic|LayerProp).
	target := w.NewEntity()
	if hasShotLOSOnGrid(g, vec2(0, 0), vec2(500, 0), ecs.Entity{}, target) {
		t.Fatal("asteroid (LayerProp) must block shots when it is not the target")
	}
}
