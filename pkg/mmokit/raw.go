package mmokit

// raw.go: escape hatch for direct ECS access. Use ONLY when OnTickEach / Get
// / Has / Set cannot express the iteration or perf-critical pattern. Naming
// this RawWorld (vs ECSWorld or World) makes escape-hatch usage trivially
// grep-able in code review.

import (
	"github.com/mlange-42/ark/ecs"
	pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

// RawWorld returns the underlying ark ECS world for the given stage.
//
// This is a deliberately unsafe escape hatch — direct ECS access bypasses the
// mmokit primitives (Get/Has/Set, Spawn, Nearby, Send, OnTickEach). Prefer
// those primitives for normal game logic. Reach for RawWorld only when:
//
//   - A query shape cannot be expressed via OnTickEach[Bundle] or Nearby.
//   - A perf-critical hot loop demands direct Map1[T] / FilterN access.
//   - Bridging an ark API not yet wrapped by mmokit.
//
// The function name is intentionally distinctive so that all escape-hatch
// usages are trivially grep-able at code review time.
func RawWorld(stage *pkguniverse.Stage) *ecs.World {
	return stage.ECSWorld()
}
