package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// ProjectileBundle declares the components a Projectile carries. The
// ProjectileSpec drives behavior; Lifetime despawns the projectile if
// no impact occurs before timeout.
//
// Position and Velocity are framework-owned transfer-core components:
// they're passed by value to Spawn() but must NOT appear as bundle
// fields — RegisterKind rejects them (see entity_aoe.go for the same
// pattern).
type ProjectileBundle struct {
	Lifetime *mmokit.Lifetime
	Spec     *gamecomp.ProjectileSpec
}

// SpawnProjectile creates a Projectile at (x, y) with initial velocity
// (vx, vy) and the given spec. Lifetime is computed from range/speed by
// the caller (typically the ability handler). Returns the entity handle.
func (gw *GameWorld) SpawnProjectile(
	x, y, vx, vy float32,
	lifetime float32,
	spec gamecomp.ProjectileSpec,
) mmokit.Entity {
	return gw.stage.Spawn(
		mmokit.EntityKind{Type: gamecomp.KindProjectile},
		mmokit.Position{X: x, Y: y},
		mmokit.Velocity{X: vx, Y: vy},
		mmokit.Lifetime{Remaining: lifetime},
		spec,
	)
}
