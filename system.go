package mmokit

import "github.com/mmokit/mmokit/pkg/universe"

// SystemBase is the canonical base for all game systems. Embed it to get:
//   - engine.SystemBase methods (ECSWorld, Engine, Init, default Update,
//     BindQueries, BuildQueries, SetDeps)
//   - Stage() — direct access to the per-cell *universe.Stage
//   - Commands() — the per-stage deferred-mutation buffer
//
// Game systems do not parameterize on a typed game world; typed per-cell state
// is fetched explicitly via mmokit.State[T](s.Stage()) and cached in Init().
//
// The type lives in pkg/universe because the universe layer owns stage
// lifecycle (it calls InitStage) and because the built-in wasm systems under
// internal/wasmctl embed it without being able to import this package.
type SystemBase = universe.SystemBase
