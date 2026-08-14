package game

import (
	"github.com/zenion/mmokit/pkg/mmokit"
	"github.com/zenion/mmokit/pkg/spatial"
)

// vec2 is a local shorthand for converting (x,y) pairs to spatial.Vec2.
func vec2(x, y float32) spatial.Vec2 { return spatial.Vec2{X: x, Y: y} }

// hasLOSOnGrid reports whether a straight line of sight exists between
// `from` and `to` against `g`, considering only LayerStatic colliders.
//
// `self` is the entity casting the LOS check — its own collider, if
// present at the ray origin, must be ignored or the raycast self-blocks
// (the ray's very first bucket contains the caster's own collider). Pass
// the zero mmokit.EntityHandle if there's no caller entity to ignore
// (e.g. pathfinding LOS smoothing).
//
// Wraps spatial.HashGrid.Raycast for the common "sight / lock / aggro"
// case. For projectile/beam checks (which also block on props), call
// hasShotLOSOnGrid (wider mask) or Raycast directly.
func hasLOSOnGrid(g *spatial.HashGrid, from, to spatial.Vec2, self mmokit.EntityHandle) bool {
	hitE, _, _, hit := g.Raycast(from, to, spatial.LayerStatic)
	if !hit {
		return true
	}
	return hitE == self
}

// hasShotLOSOnGrid reports whether a beam/hitscan shot can reach `target`
// from `from`. The raycast uses the wider LayerStatic|LayerProp mask
// (walls + asteroids). If the first hit along the ray IS the target
// itself OR the source itself, that's not blockage — the shot lands.
// Any other hit means the shot is occluded.
//
// `source` is the casting entity; like hasLOSOnGrid, the source's own
// collider at the ray origin must be ignored (otherwise every shot
// "blocks" on the caster). Pass the zero mmokit.EntityHandle if no
// source entity applies.
//
// Use this for sustained-beam and hitscan damage gating; for the
// generic sight check (aggro / lock), use hasLOSOnGrid.
func hasShotLOSOnGrid(g *spatial.HashGrid, from, to spatial.Vec2, source, target mmokit.EntityHandle) bool {
	hitE, _, _, hit := g.Raycast(from, to, spatial.LayerStatic|spatial.LayerProp)
	if !hit {
		return true
	}
	return hitE == target || hitE == source
}
