package game

import (
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/pkg/mmokit"
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
		// Collider is required for SpatialSystem to index the entity —
		// without indexing, the replication system's AoI culling never
		// finds it and the client never sees the projectile. A small
		// 1u radius is enough for indexing; non-terrain layer keeps it
		// out of CollisionSystem's player↔terrain checks.
		mmokit.Collider{Radius: 1, Width: 2, Height: 2, Shape: mmokit.ShapeCircle},
		spec,
	)
}
