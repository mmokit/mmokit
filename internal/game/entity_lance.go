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
	return gw.stage.Spawn(
		mmokit.EntityKind{Type: gamecomp.KindLanceTelegraph},
		mmokit.Position{X: x, Y: y},
		mmokit.Rotation{Angle: dirAngle},
		mmokit.Lifetime{Remaining: windupTime},
		gamecomp.LanceTelegraphSpec{
			Length:     length,
			HalfWidth:  halfWidth,
			OwnerNetID: ownerNetID,
		},
	)
}
