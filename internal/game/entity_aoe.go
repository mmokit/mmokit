package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// AoEMarkerBundle declares the components an AoEMarker carries. The
// AoESpec describes the damage payload; Lifetime ticks the cast/delay
// window; on Lifetime expiry AoESystem applies damage and despawns.
//
// Position is omitted: the framework owns Position as a transfer-core
// component and RegisterKind rejects it as a bundle field.
type AoEMarkerBundle struct {
	Lifetime *mmokit.Lifetime
	Spec     *gamecomp.AoESpec
}

// SpawnAoEMarker creates an AoEMarker at (x, y) with the given cast
// duration and damage payload. Returns the spawned entity so callers
// (e.g. Artillery cast) can store the handle for cancellation.
//
// For instant-resolve markers (Kamikaze detonate, projectile splash),
// pass lifetime = 0 — AoESystem resolves on the next pass within the
// same tick (registered after the spawner system in factory.go).
func (gw *GameWorld) SpawnAoEMarker(
	x, y float32,
	lifetime float32,
	radius, damage float32,
	ownerNetID uint32,
	factionMask uint8,
	abilityType uint8,
) mmokit.Entity {
	return gw.stage.Spawn(
		mmokit.EntityKind{Type: gamecomp.KindAoEMarker},
		mmokit.Position{X: x, Y: y},
		mmokit.Lifetime{Remaining: lifetime},
		// Collider matches the AoE radius so SpatialSystem indexes the
		// marker. Without a Collider, the entity is invisible to the
		// spatial grid → not replicated → client never sees the
		// telegraph. Layer is informational; Shape=Circle keeps the
		// math consistent with the AoESystem radius check.
		mmokit.Collider{Radius: radius, Width: radius * 2, Height: radius * 2, Shape: mmokit.ShapeCircle},
		gamecomp.AoESpec{
			Radius:      radius,
			Damage:      damage,
			OwnerNetID:  ownerNetID,
			FactionMask: factionMask,
			DamageType:  0,
			AbilityType: abilityType,
		},
	)
}
