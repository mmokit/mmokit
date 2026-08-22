package game

import (
	"github.com/mmokit/mmokit"
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
)

// LineTelegraphBundle declares the components a LineTelegraph entity
// carries. Position and Rotation are framework-owned transfer-core
// components: they're passed by value to Spawn() but must NOT appear as
// bundle fields — RegisterKind rejects them (see entity_aoe.go and
// entity_projectile.go for the same pattern).
//
// Lifetime ticks the windup; the Spec carries length + width for the
// client renderer.
type LineTelegraphBundle struct {
	Lifetime *mmokit.Lifetime
	Spec     *gamecomp.LineTelegraphSpec
}

// SpawnLineTelegraph creates a LineTelegraph at (x, y) pointing in
// `dirAngle` radians. The marker self-expires after `windupTime`
// seconds via LifetimeSystem. The owner (Lancer or Brawler) also tracks
// the entity netID so it can despawn the marker early if the windup is
// interrupted.
func (gw *GameWorld) SpawnLineTelegraph(
	x, y, dirAngle, length, halfWidth, windupTime float32,
	ownerNetID uint32,
) mmokit.Entity {
	// Collider radius spans the full charge path so the spatial-grid
	// entry sits roughly centered on the lance line. Without a Collider
	// the SpatialSystem skips this entity entirely → replication never
	// finds it → client doesn't draw the telegraph. Layer 0 (non-terrain)
	// keeps it out of player↔terrain collision checks.
	gridRadius := length * 0.5
	if gridRadius < 2 {
		gridRadius = 2
	}
	return gw.stage.Spawn(
		mmokit.EntityKind{Type: gamecomp.KindLineTelegraph},
		mmokit.Position{X: x, Y: y},
		mmokit.RotationFromYaw(dirAngle),
		mmokit.Lifetime{Remaining: windupTime},
		mmokit.Collider{Radius: gridRadius, Width: gridRadius * 2, Height: gridRadius * 2, Shape: mmokit.ShapeSphere},
		gamecomp.LineTelegraphSpec{
			Length:     length,
			HalfWidth:  halfWidth,
			OwnerNetID: ownerNetID,
		},
	)
}
