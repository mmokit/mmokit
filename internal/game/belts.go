package game

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// AsteroidBelt defines a cluster of asteroids within a cell. Retained
// as a private helper type — Task 8 will use it as the internal belt
// description while the world manifest carries the operator-facing
// definition (mmokit.WorldBelt).
type AsteroidBelt struct {
	CenterX, CenterY float32
	Radius           float32
	ResourceItemIDs  []uint32 // 1-2 dominant resource item IDs
	Count            int
}

// SpawnBelt is the world-manifest entry point. Task 8 replaces this
// stub with the real implementation that scatters asteroids inside the
// def.Radius using def.Density (and resource-type selection). The stub
// panics so the per-cell bootstrap loop fails loudly when
// world/belts.json is non-empty before the implementation lands.
func (gw *GameWorld) SpawnBelt(localX, localY float32, def mmokit.WorldBelt) {
	_ = localX
	_ = localY
	_ = def
	panic("Task 8: implement SpawnBelt(localX, localY, def mmokit.WorldBelt)")
}
