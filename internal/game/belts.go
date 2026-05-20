package game

import (
	"hash/fnv"
	"math"
	"math/rand/v2"

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

// beltBaseAsteroidCount is the default asteroid count for a belt at
// def.Density == 1.0. TODO: promote to GameConfig (e.g.
// AsteroidsPerBelt) once we have multiple belts in the world and want
// per-deployment tuning.
const beltBaseAsteroidCount = 12

// SpawnBelt creates the asteroid scatter for a hand-placed belt
// definition. The scatter is deterministic per def.ID — same id always
// produces the same asteroid layout — so renaming a belt re-rolls its
// asteroids but moving one does not.
//
// def.Density multiplies the baseline asteroid count. Resource
// selection is currently random (seeded by def.ID) from the global
// resource pool, matching the existing spawnAsteroid behavior.
func (gw *GameWorld) SpawnBelt(localX, localY float32, def mmokit.WorldBelt) {
	if def.Radius <= 0 {
		gw.eng.Log.Log(CatPlayerSpawn, "world: skip belt id=%s — non-positive radius", def.ID)
		return
	}
	count := int(float32(beltBaseAsteroidCount) * def.Density)
	if count <= 0 {
		return
	}
	rng := rand.New(rand.NewPCG(fnvBelt(def.ID), 0))
	for i := 0; i < count; i++ {
		angle := rng.Float32() * 2 * math.Pi
		r := def.Radius * float32(math.Sqrt(float64(rng.Float32())))
		ax := localX + r*float32(math.Cos(float64(angle)))
		ay := localY + r*float32(math.Sin(float64(angle)))
		gw.spawnAsteroidSeeded(ax, ay, rng)
	}
	gw.eng.Log.Log(CatPlayerSpawn, "belt spawned: id=%s pos=(%.1f,%.1f) radius=%.1f density=%.2f count=%d",
		def.ID, localX, localY, def.Radius, def.Density, count)
}

// fnvBelt hashes a belt's stable ID into a 64-bit seed for the
// scatter RNG.
func fnvBelt(id string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(id))
	return h.Sum64()
}
