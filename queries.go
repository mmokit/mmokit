package mmokit

import pkguniverse "github.com/mmokit/mmokit/pkg/universe"

// The query helpers below are thin forwarders. The implementations live in
// pkg/universe so that framework-internal systems — notably the wasm adapter
// in internal/wasmctl — can iterate a stage without importing this package,
// which would be an import cycle. Generic functions cannot be aliased in Go,
// so these are forwarders rather than `var X = pkguniverse.X`.

// Any reports whether any entity in the stage carries component T. Closes the
// underlying query automatically, so the ark world lock is always released.
func Any[T any](stage *Stage) bool { return pkguniverse.Any[T](stage) }

// FindOne returns the first entity carrying T, if any. Order is
// implementation-defined (ark archetype-iteration order).
func FindOne[T any](stage *Stage) (Entity, bool) { return pkguniverse.FindOne[T](stage) }

// ForEach1 iterates every entity carrying T, invoking fn for each. Queueing
// Commands ops inside the closure is safe — they flush after the calling
// system's Update returns.
func ForEach1[T any](stage *Stage, fn func(Entity, *T)) { pkguniverse.ForEach1(stage, fn) }

// ForEach2 iterates every entity carrying both T1 and T2.
func ForEach2[T1, T2 any](stage *Stage, fn func(Entity, *T1, *T2)) {
	pkguniverse.ForEach2(stage, fn)
}

// ForEach3 iterates every entity carrying T1, T2 and T3.
func ForEach3[T1, T2, T3 any](stage *Stage, fn func(Entity, *T1, *T2, *T3)) {
	pkguniverse.ForEach3(stage, fn)
}
