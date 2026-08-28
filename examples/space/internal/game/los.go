package game

import (
	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// vec2 is a local shorthand for converting (x,y) pairs to a world position.
// The game is 2D, so Z is the ground plane.
func vec2(x, y float32) mmokit.Vec3 { return mmokit.Vec3{X: x, Y: y} }

// ignoring returns a ray filter that excludes the given entities.
//
// EXCLUDING DURING THE WALK IS THE ONLY CORRECT VERSION, and both helpers
// below used to do it afterwards. A raycast returns the NEAREST hit, so
// `hit == self` only works while self is never actually the nearest hit —
// which held here by accident, because ships are LayerEntity and these masks
// are LayerStatic and LayerStatic|LayerProp. Put a caster on a masked layer
// and it becomes the nearest hit at the ray's origin, the check reports a
// clear line, and EVERY WALL BEHIND IT IS MASKED. Latent, not theoretical.
func ignoring(handles ...mmokit.EntityHandle) spatial.RayFilter {
	return func(e *spatial.Entry) bool {
		for _, h := range handles {
			if h != (mmokit.EntityHandle{}) && e.Entity == h {
				return false
			}
		}
		return true
	}
}

// hasLOSOnGrid reports whether a straight line of sight exists between `from`
// and `to` against `g`, considering only LayerStatic colliders.
//
// `self` is the entity casting the check — its own collider sits at the ray
// origin and must be excluded, or the ray self-blocks. Pass the zero handle
// when there is no caster (pathfinding LOS smoothing).
//
// For projectile and beam checks, which also block on props, use
// hasShotLOSOnGrid.
func hasLOSOnGrid(g *spatial.HashGrid, from, to mmokit.Vec3, self mmokit.EntityHandle) bool {
	_, hit := g.Raycast(from, to, spatial.LayerStatic, ignoring(self))
	return !hit
}

// hasShotLOSOnGrid reports whether a beam or hitscan shot can reach `target`
// from `from`, against walls and props both.
//
// Source and target are both excluded during the walk: the source's collider
// is at the origin, and the target being hit is the shot landing rather than
// being blocked.
func hasShotLOSOnGrid(g *spatial.HashGrid, from, to mmokit.Vec3, source, target mmokit.EntityHandle) bool {
	_, hit := g.Raycast(from, to, spatial.LayerStatic|spatial.LayerProp, ignoring(source, target))
	return !hit
}
