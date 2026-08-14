package mmokit

import (
	"github.com/mlange-42/ark/ecs"

	pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

// Any reports whether any entity in the stage carries component T.
// Replaces the `ecs.NewFilter1[T] + query.Next()` idiom for one-shot
// existence checks. Closes the underlying query automatically before
// returning, so the ark world lock is always released.
func Any[T any](stage *pkguniverse.Stage) bool {
	filter := ecs.NewFilter1[T](stage.ECSWorld())
	q := filter.Query()
	defer q.Close()
	return q.Next()
}

// FindOne returns the first entity carrying T, if any. Order is
// implementation-defined (ark archetype-iteration order). Used for
// singleton lookups like "find the station entity." Closes the underlying
// query automatically before returning, including on the no-match path.
func FindOne[T any](stage *pkguniverse.Stage) (Entity, bool) {
	filter := ecs.NewFilter1[T](stage.ECSWorld())
	q := filter.Query()
	defer q.Close()
	if !q.Next() {
		return Entity{}, false
	}
	return EntityFromECS(stage, q.Entity()), true
}

// ForEach1 iterates every entity carrying T, invoking fn for each.
// The closure receives the wrapped Entity and a pointer to T. Closes
// the underlying query automatically when iteration completes (or on
// panic — defer).
//
// Queueing Commands ops inside the closure is safe — they flush after
// this iteration completes (after the calling system's Update returns).
func ForEach1[T any](stage *pkguniverse.Stage, fn func(Entity, *T)) {
	filter := ecs.NewFilter1[T](stage.ECSWorld())
	q := filter.Query()
	defer q.Close()
	for q.Next() {
		t := q.Get()
		fn(EntityFromECS(stage, q.Entity()), t)
	}
}

// ForEach2 iterates entities carrying both T1 and T2.
func ForEach2[T1, T2 any](stage *pkguniverse.Stage, fn func(Entity, *T1, *T2)) {
	filter := ecs.NewFilter2[T1, T2](stage.ECSWorld())
	q := filter.Query()
	defer q.Close()
	for q.Next() {
		t1, t2 := q.Get()
		fn(EntityFromECS(stage, q.Entity()), t1, t2)
	}
}

// ForEach3 iterates entities carrying T1, T2, and T3.
func ForEach3[T1, T2, T3 any](stage *pkguniverse.Stage, fn func(Entity, *T1, *T2, *T3)) {
	filter := ecs.NewFilter3[T1, T2, T3](stage.ECSWorld())
	q := filter.Query()
	defer q.Close()
	for q.Next() {
		t1, t2, t3 := q.Get()
		fn(EntityFromECS(stage, q.Entity()), t1, t2, t3)
	}
}
