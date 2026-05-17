package game

import (
	"math"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// DungeonWallBundle is the entity-kind bundle for a static cave wall.
//
// Walls are passive geometry — their orientation comes from the entity's
// Rotation component, which is replicated via the kind's QAngle extra
// binding (mirroring how Ship/NPC publish their facing).
type DungeonWallBundle struct {
	Wall *gamecomp.DungeonWall
}

// SpawnDungeonWall creates a rectangular wall collider at (x,y) with the
// given dimensions and rotation (radians). The collider is on
// LayerStatic so it blocks movement, sight, locks, and projectiles.
// Returns the wall entity's NetID.
//
// The Collider.Radius is set to the rect's half-diagonal so the broad-
// phase spatial hash still includes the wall in any radius query that
// could possibly intersect its OBB — narrow-phase (raycast / rect-rect)
// then uses Width/Height/Shape for the precise test.
func (gw *GameWorld) SpawnDungeonWall(x, y, width, height, rotation float32) uint32 {
	radius := float32(math.Hypot(float64(width)/2, float64(height)/2))
	e := gw.stage.Spawn(
		mmokit.Position{X: x, Y: y},
		mmokit.Rotation{Angle: rotation},
		mmokit.EntityKind{Type: gamecomp.KindDungeonWall},
		mmokit.Collider{
			Radius: radius,
			Width:  width,
			Height: height,
			Shape:  spatial.ShapeRect,
			Layer:  spatial.LayerStatic,
		},
		gamecomp.DungeonWall{Width: width, Height: height},
	)
	return e.NetID()
}

