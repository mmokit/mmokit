package mmokit

import (
	"iter"

	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// Nearby yields every entity within radius r of center on the stage's spatial
// grid. Includes both Live and Replica entities — game code does not need to
// distinguish.
//
// The test is a sphere. In a 2D profile every Z is zero, so a Vec3 built from
// a Position behaves exactly as the old (x, y) pair did.
func Nearby(stage *pkguniverse.Stage, center Vec3, r float32) iter.Seq[Entity] {
	return func(yield func(Entity) bool) {
		grid := stage.SpatialGrid()
		if grid == nil {
			return
		}
		for _, entry := range grid.QueryRadius(center, r, nil) {
			e := EntityFromECS(stage, entry.Entity)
			if !yield(e) {
				return
			}
		}
	}
}

// NearbyWith yields nearby entities that have component T.
func NearbyWith[T any](stage *pkguniverse.Stage, center Vec3, r float32) iter.Seq[Entity] {
	return func(yield func(Entity) bool) {
		for e := range Nearby(stage, center, r) {
			if Has[T](e) {
				if !yield(e) {
					return
				}
			}
		}
	}
}
