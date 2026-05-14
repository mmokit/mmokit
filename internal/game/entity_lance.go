package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// LanceTelegraphBundle declares the components a LanceTelegraph entity
// carries. Position and Rotation are framework-owned transfer-core
// components: they're passed by value to Spawn() but must NOT appear as
// bundle fields — RegisterKind rejects them (see entity_aoe.go and
// entity_projectile.go for the same pattern).
//
// Lifetime ticks the windup; the Spec carries length + width for the
// client renderer.
type LanceTelegraphBundle struct {
	Lifetime *mmokit.Lifetime
	Spec     *gamecomp.LanceTelegraphSpec
}

// SpawnLanceTelegraph creates a LanceTelegraph at (x, y) pointing in
// `dirAngle` radians. The marker self-expires after `windupTime`
// seconds via LifetimeSystem. The Lancer also tracks the entity netID
// so it can despawn the marker early if the charge is interrupted.
func (gw *GameWorld) SpawnLanceTelegraph(
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
		mmokit.EntityKind{Type: gamecomp.KindLanceTelegraph},
		mmokit.Position{X: x, Y: y},
		mmokit.Rotation{Angle: dirAngle},
		mmokit.Lifetime{Remaining: windupTime},
		mmokit.Collider{Radius: gridRadius, Width: gridRadius * 2, Height: gridRadius * 2, Shape: mmokit.ShapeCircle},
		gamecomp.LanceTelegraphSpec{
			Length:     length,
			HalfWidth:  halfWidth,
			OwnerNetID: ownerNetID,
		},
	)
}
