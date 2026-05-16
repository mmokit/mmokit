package game

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// vec2 is a local shorthand for converting (x,y) pairs to spatial.Vec2.
func vec2(x, y float32) spatial.Vec2 { return spatial.Vec2{X: x, Y: y} }

// hasLOSOnGrid reports whether a straight line of sight exists between
// `from` and `to` against `g`, considering only LayerStatic colliders.
//
// Wraps spatial.HashGrid.Raycast for the common "sight / lock / aggro"
// case. For projectile/beam checks (which also block on props), call
// hasShotLOSOnGrid (wider mask) or Raycast directly.
func hasLOSOnGrid(g *spatial.HashGrid, from, to spatial.Vec2) bool {
	_, _, _, hit := g.Raycast(from, to, spatial.LayerStatic)
	return !hit
}

// hasShotLOSOnGrid reports whether a beam/hitscan shot can reach `target`
// from `from`. The raycast uses the wider LayerStatic|LayerProp mask
// (walls + asteroids). If the first hit along the ray IS the target
// itself, that's not blockage — the shot lands. Any other hit means
// the shot is occluded.
//
// Use this for sustained-beam and hitscan damage gating; for the
// generic sight check (aggro / lock), use hasLOSOnGrid.
func hasShotLOSOnGrid(g *spatial.HashGrid, from, to spatial.Vec2, target ecs.Entity) bool {
	hitE, _, _, hit := g.Raycast(from, to, spatial.LayerStatic|spatial.LayerProp)
	if !hit {
		return true
	}
	return hitE == target
}
