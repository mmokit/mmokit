package mmokit

import (
	"iter"

	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// Nearby yields every entity within radius r of (x, y) on the stage's
// spatial grid. Includes both Live and Replica entities — game code does
// not need to distinguish.
func Nearby(stage *pkguniverse.Stage, x, y, r float32) iter.Seq[Entity] {
	return func(yield func(Entity) bool) {
		grid := stage.SpatialGrid()
		if grid == nil {
			return
		}
		for _, entry := range grid.QueryRadius(x, y, r, nil) {
			e := EntityFromECS(stage, entry.Entity)
			if !yield(e) {
				return
			}
		}
	}
}

// NearbyWith yields nearby entities that have component T.
func NearbyWith[T any](stage *pkguniverse.Stage, x, y, r float32) iter.Seq[Entity] {
	return func(yield func(Entity) bool) {
		for e := range Nearby(stage, x, y, r) {
			if Has[T](e) {
				if !yield(e) {
					return
				}
			}
		}
	}
}
