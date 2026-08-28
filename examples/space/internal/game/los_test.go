package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// hasLOSOnGrid wraps spatial.HashGrid.Raycast with the LayerStatic mask and a self-exclusion filter.
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
		Radius: 60, Width: 80, Height: 40, Rot: mmokit.RotationIdentity(),
		Shape: mmokit.ShapeBox, Layer: spatial.LayerStatic,
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
		Radius: 20, Width: 40, Height: 20, Rot: mmokit.RotationIdentity(),
		Shape: mmokit.ShapeBox, Layer: spatial.LayerEntity,
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
		Radius: 40, Shape: mmokit.ShapeSphere, Layer: spatial.LayerProp,
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

// TestHasLOS_CasterOnAMaskedLayerDoesNotMaskTheWall is the test the old
// implementation could not pass, and which nothing in this file covered
// because the defect was latent.
//
// hasLOSOnGrid used to cast without a filter and then check whether the hit
// WAS the caster. A raycast returns only the NEAREST hit, so that is correct
// exactly while the caster is never nearest — which held here by accident:
// ships are LayerEntity and this mask is LayerStatic. Put the caster on a
// masked layer, which is a one-line change in any game, and its own collider
// at the ray origin becomes the nearest hit, the function returns "clear",
// and every wall behind it is invisible to line of sight.
//
// The filter excludes during the walk instead, so the wall is still found.
func TestHasLOS_CasterOnAMaskedLayerDoesNotMaskTheWall(t *testing.T) {
	g := spatial.NewHashGrid(100)
	w := ecs.NewWorld()

	self := w.NewEntity()
	wall := w.NewEntity()
	// The caster, at the ray's origin, ON THE MASKED LAYER.
	g.Register(spatial.Entry{
		Entity: self, X: 0, Y: 0, Radius: 20,
		Shape: mmokit.ShapeSphere, Layer: spatial.LayerStatic,
		Rot: mmokit.RotationIdentity(),
	})
	// A wall further along the same ray.
	g.Register(spatial.Entry{
		Entity: wall, X: 250, Y: 0, Radius: 60,
		Shape: mmokit.ShapeSphere, Layer: spatial.LayerStatic,
		Rot: mmokit.RotationIdentity(),
	})

	if hasLOSOnGrid(g, vec2(0, 0), vec2(500, 0), self) {
		t.Error("line of sight reported clear through a wall — the caster's own collider " +
			"was the nearest hit and masked everything behind it")
	}
}

// The same shape for the shot variant, which excludes both source and target.
func TestHasShotLOS_SourceOnAMaskedLayerDoesNotMaskTheWall(t *testing.T) {
	g := spatial.NewHashGrid(100)
	w := ecs.NewWorld()

	source := w.NewEntity()
	target := w.NewEntity()
	wall := w.NewEntity()
	for _, e := range []struct {
		h ecs.Entity
		x float32
		r float32
	}{{source, 0, 20}, {wall, 250, 60}, {target, 500, 20}} {
		g.Register(spatial.Entry{
			Entity: e.h, X: e.x, Y: 0, Radius: e.r,
			Shape: mmokit.ShapeSphere, Layer: spatial.LayerStatic,
			Rot: mmokit.RotationIdentity(),
		})
	}

	if hasShotLOSOnGrid(g, vec2(0, 0), vec2(500, 0), source, target) {
		t.Error("shot reported unobstructed through a wall — the source's own collider " +
			"was the nearest hit")
	}
	// And with the wall gone, the shot lands: source and target are excluded,
	// so neither counts as blockage.
	g.Deregister(wall)
	if !hasShotLOSOnGrid(g, vec2(0, 0), vec2(500, 0), source, target) {
		t.Error("shot reported blocked with nothing between source and target")
	}
}
