# Typed SystemBase[W] + Auto-Bound Queries

## Context

The system DI dance for game-touching ECS systems is awkward. Every system that
calls into the game world stashes a typed `gw *World` field, populates it in
`Init()` via `WorldOf[*World](s)`, and inits each query field with `q.Init(s, opts...)`
where `s` is just a handle to reach the ECS world. The boilerplate is bad enough
that `internal/game/` invented its own `gwFromSystem(s.SystemBase)` helper —
every consumer of mmokit will reinvent that bandaid.

Today (one of ~14 systems with the same shape):

```go
type BotSystem struct {
    mmokit.SystemBase
    gw   *World
    bots mmokit.Query[BotBundle]
}

func (s *BotSystem) Init() {
    s.gw = mmokit.WorldOf[*World](s)
    s.bots.Init(s, mmokit.IncludeAll())
}

func (s *BotSystem) Update(dt float32) {
    origin := s.gw.Cell()
    // ...
}
```

Three things are wrong:

1. **`gw` field is dead weight.** It exists only because the system needed a
   typed handle and `SystemBase.GameWorld()` returns `any`.
2. **`WorldOf[*World](s)` is non-obvious.** New game devs hit it and ask "why
   am I passing myself to a function?"
3. **`q.Init(s, ...)` repeats the same trick** — `s` is just a way to reach
   `s.ECSWorld()`. The framework already has the ECS at SetDeps time.

This design eliminates all three by making `SystemBase` generic over the world
type and auto-binding `Query[T]` fields during SetDeps. Init() shrinks to
*only* declaring non-default query options, and disappears entirely for the
~50% of systems that use defaults.

## Before / After

**Before:**

```go
type BotSystem struct {
    mmokit.SystemBase
    gw   *World
    bots mmokit.Query[BotBundle]
}

func (s *BotSystem) Init() {
    s.gw = mmokit.WorldOf[*World](s)
    s.bots.Init(s, mmokit.IncludeAll())
}

func (s *BotSystem) Update(dt float32) {
    origin := s.gw.Cell()
    // ...
}
```

**After:**

```go
type BotSystem struct {
    mmokit.SystemBase[*World]
    bots mmokit.Query[BotBundle]
}

func (s *BotSystem) Init() {
    s.bots.With(mmokit.IncludeAll())
}

func (s *BotSystem) Update(dt float32) {
    origin := s.World().Cell()
    // ...
}
```

For systems with default query options (Wander, ShieldRegen, etc.), `Init()`
disappears entirely:

```go
type WanderSystem struct {
    mmokit.SystemBase[*World]
    entities mmokit.Query[WanderBundle]
}

func (s *WanderSystem) Update(dt float32) { ... }
```

## Goals

- Eliminate the `gw *World` stash field and the `WorldOf[*World](s)` call.
- Drop the `s` parameter from query setup.
- Allow trivial systems to omit `Init()` entirely.
- Preserve the typed, LSP-friendly query option API — no struct tags, no
  reflection over option strings.
- Make engine-side bundled systems (`pkg/system/PhysicsSystem`, etc.) work
  unchanged for any game.

## Non-Goals

- Functional / Bevy-style system registration (Option A from brainstorming).
- Generic `Coordinator[W]` (Option B). Coordinator stays untyped.
- Auto-discovery of system dependencies beyond the world type and queries.
- Removing the `WorldOf[W]` and `WorldOfCell[W]` helpers — they remain useful
  outside the system context (cmdsys handlers, console commands, ad-hoc
  utilities).

## Design

### Generic SystemBase[W]

Replace the existing non-generic `engine.SystemBase` (and its `mmokit.SystemBase`
re-export) with a generic version parameterized on the world type:

```go
// pkg/engine/system.go

type SystemBase[W any] struct {
    ecsWorld *ecs.World
    eng      *Engine
    world    W
    queries  []queryBinder // discovered Query[T] fields, populated on SetDeps
}

func (b *SystemBase[W]) ECSWorld() *ecs.World { return b.ecsWorld }
func (b *SystemBase[W]) Engine() *Engine      { return b.eng }
func (b *SystemBase[W]) GameWorld() any       { return b.world }
func (b *SystemBase[W]) World() W             { return b.world }

func (b *SystemBase[W]) Init() {} // default no-op

func (b *SystemBase[W]) SetDeps(ecs *ecs.World, eng *Engine, gw any) {
    b.ecsWorld, b.eng = ecs, eng
    typed, ok := gw.(W)
    if !ok {
        var zero W
        panic(fmt.Sprintf("SystemBase[%T]: GameWorld is %T, not assignable", zero, gw))
    }
    b.world = typed
    // Discovery happens here — see "SetDeps lifecycle" below.
}
```

`SystemBase[W]` retains all of today's accessors (`ECSWorld`, `Engine`,
`GameWorld`) plus a new typed `World()` — this preserves the `interface{
ECSWorld() *ecs.World }` contract that `Query.With` and any other callers can
rely on.

### SetDeps lifecycle

The framework's existing `depsInjectable` interface check at
`pkg/universe/coordinator.go:1369` stays unchanged — `*SystemBase[W]` still
satisfies it. The lifecycle the coordinator drives becomes:

1. **`SetDeps(ecs, eng, gw)`** — called by the framework. SystemBase caches
   the typed world and reflects over the embedding system's fields once,
   recording every `*Query[T]` field it finds. Queries are *not* built yet;
   they're left in their zero "config" state.
2. **`Init()`** — called next. Default impl is no-op. User overrides to call
   `q.With(opts...)` on any query that needs non-default options.
3. **Build phase** — after Init returns, SystemBase walks the recorded queries
   and calls each one's internal `build(ecs)` using whatever options were
   accumulated (defaults if Init didn't touch the query).

The discovery in step 1 uses `reflect.TypeOf(outer).Elem()`, where `outer` is
the embedding system. SystemBase needs a way to find the outer struct — this
is solved by having the coordinator pass the system pointer through a new
`SetDepsOuter` interface, or by having the coordinator call a separate
`bindQueries(sys, ecs)` step after `SetDeps`. **Detail to nail down in
implementation**, but mechanically it's a one-shot reflect over exported
fields whose type is `mmokit.Query[T]` for any T.

### Query.With(opts...)

Replace `Query.Init(sys, opts...)` with `Query.With(opts...)`:

```go
// pkg/query/query.go

// With configures the query with the given options. Options accumulate across
// calls. Must be called during the system's Init() — calling With after the
// framework's build phase panics.
func (q *Query[T]) With(opts ...QueryOption) *Query[T] {
    if q.built {
        panic("query.Query: With called after build (only call inside system Init)")
    }
    q.opts = append(q.opts, opts...)
    return q
}
```

`Query.Init(sys, opts...)` is **removed**. Existing call sites in
`internal/game/`, `pkg/system/`, and `examples/` all migrate to `Query.With`.

`NewQuery[T](sys, opts...)` stays for ad-hoc construction outside the system
context (test code, console commands).

The internal `build(ecs)` is package-private and called by `SystemBase[W]`
after Init.

### Engine-side bundled systems

`pkg/system/PhysicsSystem` and friends can't import the game's `*World`. They
use `engine.SystemBase[any]` if they don't need world methods, or
`engine.SystemBase[someInterface]` if they do:

```go
// pkg/system/physics.go
type PhysicsSystem struct {
    engine.SystemBase[any]    // doesn't touch the world
    entities query.Query[...]
}
```

```go
// pkg/system/spatial_system.go
type SpatialSystem struct {
    engine.SystemBase[interface{ SpatialGrid() *spatial.HashGrid }]
    // ...
}
```

The engine's own bundled systems are an opaque library — game devs don't see
their internals. They register them via `mmokit.NewPhysicsSystem()` etc., same
as today.

### `gwFromSystem` helper

`internal/game/system_util.go:gwFromSystem` is deleted. The 10+ systems in
`internal/game/system_*.go` migrate from `s.gw = gwFromSystem(s.SystemBase)` and
`s.gw.X()` to `SystemBase[*GameWorld]` and `s.World().X()`.

## Migration

Mechanical per-system edits:

1. Change embed: `mmokit.SystemBase` → `mmokit.SystemBase[*World]` (or
   game-specific world type).
2. Delete `gw *World` field.
3. In `Init()`:
   - Delete `s.gw = mmokit.WorldOf[*World](s)` (or `gwFromSystem` variant).
   - Change `s.bots.Init(s, opts...)` → `s.bots.With(opts...)`.
   - If Init body is now empty, delete the method entirely.
4. In `Update()` and other methods: `s.gw.X()` → `s.World().X()`.
5. Engine-side systems: pick `SystemBase[any]` or
   `SystemBase[someInterface]`.
6. Delete `internal/game/system_util.go:gwFromSystem`.

The migration is a sweep — every system file gets one focused edit. No
downstream API changes for anyone calling `Update`, `coord.AddSystem`, or
`mmokit.NewSystem`.

## Edge Cases

- **`Query.With` called outside Init** (e.g. in Update): panics with "called
  after build". Same shape as today's "Init called twice" panic.
- **System has no Init override**: SystemBase's default no-op Init runs;
  framework's build phase still fires; queries get default options.
- **System embeds `SystemBase[*Wrong]` for the wrong world type**: `SetDeps`
  panics with `"SystemBase[*Wrong]: GameWorld is *Right, not assignable"`.
  Replaces today's `WorldOf[*Wrong](s)` panic with the same message shape.
- **Mixed default-and-custom queries on the same system**: each `With` call
  affects only that one query field. Untouched queries get defaults.
- **System with no queries at all**: build phase is a no-op walk. Fine.
- **Unexported `Query[T]` fields**: idiomatic — every existing system has
  unexported queries (`bots`, `entities`, etc.). Discovery must reach them
  via `reflect.Value.UnsafePointer()` + field offset arithmetic, the same
  unsafe-pointer pattern `query.build` already uses for bundle population.
  Standard `reflect.Value.Set` won't work; this is by design.

## Compatibility

This is a breaking API change for every system in the codebase. There is no
deprecation period and no shim — the existing `SystemBase` type is replaced.
Per project memory ([No backward compat](memory/feedback_no_backward_compat.md)
and [Refactor over stopgaps](memory/feedback_refactor_over_stopgaps.md)) this is
the right call: clean break, all call sites updated in one sweep.

External callers (none today, but design for opensource readiness):

- `WorldOf[W](sys)` and `WorldOfCell[W](cell)` remain. They're useful in
  cmdsys handlers and console commands where you have a system-shaped value
  but aren't writing a system.
- `Query.Init(sys, opts...)` is removed. `Query.With(opts...)` replaces it.
  `NewQuery[T](sys, opts...)` stays for one-shot ad-hoc construction.

## Testing

Unit tests in `pkg/mmokit/`:

- `TestSystemBase_TypedWorld`: embeds `SystemBase[*stubWorld]`, asserts
  `World()` returns the typed value after `SetDeps`.
- `TestSystemBase_TypeMismatch_Panics`: embeds `SystemBase[*wrongType]`,
  asserts `SetDeps` panics with a clear message.
- `TestSystemBase_AutoBindsQueries`: embeds two `Query[T]` fields, asserts
  both are built (rangeable) after the build phase.
- `TestSystemBase_DefaultExclusions`: query with no `With` call excludes
  Ghost + Replica.
- `TestQuery_With_AfterBuild_Panics`: calling `With` from inside Update
  panics.
- `TestQuery_With_Accumulates`: multiple `With` calls combine options.

Integration test via existing 4node-basic smoke:

- `just dev` boots, BotSystem migrates correctly, `bot spawn 30 0_0` produces
  wandering bots that cross cell boundaries (existing test path).
- `examples/4node-basic/just test-distributed` (multi-host smoke) passes
  unchanged.

## Risks

- **Reflection at cell boot.** Each system reflects over its own struct fields
  once at SetDeps. ~14 systems × negligible per-system cost = sub-millisecond
  per cell. Not on hot path.
- **Discovery via field offsets is unsafe-pointer territory.** Already used
  by `query.build` for bundle population; new discovery code reuses the same
  patterns. Risk is bounded by the existing test surface.
- **Generic SystemBase locks every system to a single world type.** A test
  that wants to share a system across two world types would need adapter
  shims. No real-world case for this today; YAGNI.
- **Build-phase ordering.** If a system's Init body itself iterates a query
  (rare), it'll panic — query isn't built yet. Mitigation: doc that Init is
  for *configuring* options, not *using* the query. Move iteration to Update.

## Open Questions

- **How does `SystemBase[W]` learn its outer struct's address for field
  reflection?** Two options: (1) coordinator passes the system pointer
  through a separate `BindQueries(sys)` call after `SetDeps`; (2) `SetDeps`
  signature gains the system pointer. Option 1 is less invasive — it adds a
  capability without breaking the existing `SetDeps` shape. Decide during
  implementation.
- **Naming: `With` vs. `Configure` vs. `Options`.** `With` is short, fluent,
  and reads as "with these options." `Configure` is more explicit but
  longer. Recommend `With` and revisit if it reads ambiguously.
