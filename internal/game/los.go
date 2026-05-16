package game

import "github.com/zenion/mmoserver/pkg/spatial"

// vec2 is a local shorthand for converting (x,y) pairs to spatial.Vec2.
func vec2(x, y float32) spatial.Vec2 { return spatial.Vec2{X: x, Y: y} }

// hasLOSOnGrid reports whether a straight line of sight exists between
// `from` and `to` against `g`, considering only LayerStatic colliders.
//
// Wraps spatial.HashGrid.Raycast for the common "sight / lock / aggro"
// case. For projectile/beam checks (which also block on props), call
// Raycast directly with LayerStatic|LayerProp.
func hasLOSOnGrid(g *spatial.HashGrid, from, to spatial.Vec2) bool {
	_, _, _, hit := g.Raycast(from, to, spatial.LayerStatic)
	return !hit
}
