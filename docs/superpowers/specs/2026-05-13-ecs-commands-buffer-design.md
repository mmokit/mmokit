# ECS Commands Buffer — Design

**Status:** Approved for implementation
**Author:** Josh Stout
**Date:** 2026-05-13

## Problem

Every system that mutates ECS state structurally — adding/removing components, despawning entities — has had to invent its own deferral pattern to avoid ark's "cannot modify a locked world" panic. The codebase has 5+ confirmed incidents of this panic class (commits `6a2a01a`, `7f55080`, `211d7b3`, plus two more during the May 2026 combat work) and the workaround pattern (`PendingDeathMarker` → drain in postFlush, `pendingLeashClears` slice → drain at end of Update, `ecs.NewMap1[T](w)` priming in Init) is now duplicated in 8+ places.

Game code in `internal/game/` imports `github.com/mlange-42/ark/ecs` directly in 28 files. Most use it only as a type alias (`ecs.Entity`); some do raw query construction; two do `Map.Remove` calls that have to be carefully deferred. The pattern is correct each time, but it's not enforced — every new system has to remember.

The locked-world constraint is intrinsic to every archetype ECS (Bevy, flecs, EnTT, Unity DOTS all have the same hazard); ark just doesn't ship the standard mitigation. Replacing ark would not eliminate the constraint and would trade a known cost for a multi-week rewrite. The right fix is to build the missing layer inside mmokit.

## Goals

- Eliminate the "locked-world panic" bug class structurally: game code can't call into ark's structural-mutation APIs because it doesn't import ark/ecs anymore.
- Replace 8+ bespoke "collect-into-slice, drain-after-iteration" patterns with one primitive.
- Remove the `Pending*` struct + `gw.Queue` machinery entirely. Direct deferred closures replace typed payload queues.
- Hide ark/ecs from `internal/game/` completely. The only ark consumer is mmokit itself.
- Keep ark underneath as the storage engine — same perf, same generics ergonomics. The new API is a deferral wrapper, not a replacement.

## Non-goals

- Replacing ark or changing the storage model. Ark stays.
- Wrapping ark in `pkg/system/` (hot replication / spatial / network paths). Those keep direct ark for perf and proximity to engine internals.
- Adding observer / hook / event systems. Commands is deferred mutation only; cross-system communication happens through state mutation, not message passing.
- Multi-tick deferral. Commands flushes every tick. Timer-based "do this in N seconds" stays in domain code (e.g. the existing `autoRespawnAt` map).

## API surface

Lives in `pkg/mmokit/commands.go`. Single type:

```go
// Commands is a per-stage deferred ECS mutation buffer. The engine flushes
// it after every system's Update so ops queued in System N are visible to
// System N+1 within the same tick. Outside system Update, ops still queue
// and flush at the next system boundary (or explicitly during integration
// test calls to stage.TickOne).
type Commands struct {
    stage *Stage
    ops   []pendingOp
}

// Despawn marks the entity for immediate destruction at next flush.
// Subsequent ops on this entity within the same submit batch become no-ops.
func (c *Commands) Despawn(e Entity)

// Defer schedules an arbitrary closure to run during next flush, ordered
// with other queued ops by submit time. Use for game-action logic that
// doesn't reduce to a single ECS primitive (spawning a loot crate with
// inventory setup, starting a docking sequence, cross-cell respawn routing).
func (c *Commands) Defer(fn func())
```

Component add/remove can't be methods due to Go's generic-method limitation, so they're free functions matching the existing `mmokit.Get[T] / Has[T] / Set` pattern:

```go
// AddComponent queues a component add/overwrite at next flush. T is
// inferred from val. Overwrites if the component is already present.
func AddComponent[T any](c *Commands, e Entity, val T)

// RemoveComponent queues a component removal at next flush. T must be
// explicit (no value to infer from). No-op if the component isn't present
// or the entity is already dead.
func RemoveComponent[T any](c *Commands, e Entity)
```

**Access from systems:** every `SystemBase`-embedding system gets a `Commands()` shortcut that returns its stage's buffer:

```go
func (s *SystemBase) Commands() *Commands { return s.stage.Commands() }
```

**Access from non-system code (hooks, handlers, helpers):** `stage.Commands()` directly.

**Entity type alias:** `pkg/mmokit/entity_alias.go` exports `type Entity = ecs.Entity` so game code never has to mention ark. The `mmokit.Entity` wrapper type (the richer struct used by SDK helpers) is unchanged and continues to exist for cases that need methods.

## Query wrappers

Lives in `pkg/mmokit/queries.go`. Drop-in helpers replacing raw `ecs.NewFilter*` for one-shot lookups. All auto-close, all safe to call from anywhere:

```go
// Any reports whether any entity has component T.
func Any[T any](stage *Stage) bool

// FindOne returns the first entity carrying T. Order is implementation-defined.
func FindOne[T any](stage *Stage) (Entity, bool)

// ForEach1 iterates every entity with T. Queueing Commands ops inside
// the closure is safe — they flush after this iteration completes.
func ForEach1[T any](stage *Stage, fn func(Entity, *T))

// ForEach2 / ForEach3 — same with multiple required components.
func ForEach2[T1, T2 any](stage *Stage, fn func(Entity, *T1, *T2))
func ForEach3[T1, T2, T3 any](stage *Stage, fn func(Entity, *T1, *T2, *T3))
```

**`mmokit.Query[Bundle]` (the sticky-query pattern used in hot systems) stays as-is.** It's the right abstraction for systems iterating every tick over a fixed component set. `ForEachN` is the right abstraction for one-shot lookups (the `hasStation` use case, finding a singleton, ad-hoc filters in helpers).

## Flush semantics

Triggered by the engine's game loop, between each system's Update and the next:

```go
// pkg/engine/loop.go — phase 6 (system execution) becomes:
for _, sys := range gl.systems {
    sys.Update(dt)
    stage.Commands().flush()
}
```

`flush()` is **package-private** to `mmokit`. Game code can never call it. Only test utilities reach it via `stage.TickOne(sys)`.

**Flush order:**
1. Iterate queued ops in submit order.
2. For each op, check if the target entity is still alive (`world.Alive`). If not, no-op silently. This handles the AddComponent-after-Despawn case automatically.
3. Apply: AddComponent → `mmokit.Set` semantics under the hood; RemoveComponent → `Map[T].Remove`; Despawn → immediate destruction via the same code path step 8 (`FlushRemovals`) uses, including all `OnEntityRemoved` callbacks (NetIDIndex cleanup, spatial Deregister, replication farewells).
4. Closures (`Defer`) execute inline. They may call `mmokit.AddComponent` / etc. on Commands again — those ops queue for the **next** system's flush. Single-pass per flush (no convergence loop). One-tick lag for chained ops is acceptable.
5. Clear the buffer.

**Despawn behavior diverges from `stage.MarkForRemoval`.** `MarkForRemoval` is the existing pattern — it queues the entity for destruction during phase 8 (`FlushRemovals`) at end of tick. `cmds.Despawn` destroys the entity at the next flush boundary, so System N's Despawns are gone to System N+1 in the same tick. This is the intentional behavior change ("Option A" in brainstorm). Phase 8's `FlushRemovals` still exists for non-Commands paths (anyone calling `stage.MarkForRemoval` directly).

**Performance.** 15 systems × per-system flush × ~1-3 ops average ≈ 50 ns × 50 = 2.5 µs/tick. Below the noise floor on a 20 Hz loop. The buffer is reused tick-to-tick (capacity grows, length resets), so no per-tick allocation.

## What gets deleted

After migration, the following exist nowhere in the codebase:

- `pkg/engine/tickqueue.go` — the `TickQueue` type itself.
- `pkg/mmokit/mmokit.go`: the `TickQueue` alias (~line 115), `Enqueue[T]` and `Drain[T]` free functions (~lines 1098-1106).
- `internal/game/gameworld.go`: `PendingLootDrop`, `PendingLootAll`, `PendingRespawn`, `PendingDeathMarker`, `DockingProgress` (kept — has timer state), `PendingDockRequest`, `PendingUndockRequest`. The `gw.Queue *mmokit.TickQueue` field.
- `internal/game/system_npc_ai.go`: `pendingLeashClears []ecs.Entity` field and its drain loop.
- All `ecs.NewMap1[T](w)` priming calls in `internal/game/` (handled by Commands' internal cache).
- All `ark/ecs` imports in `internal/game/` (verified by `grep`).

## Phased migration

Each phase compiles and passes tests independently. Phase 1 adds only; Phases 2-6 are net-negative LoC.

### Phase 1: Land the new API

- New: `pkg/mmokit/commands.go`, `pkg/mmokit/queries.go`, `pkg/mmokit/entity_alias.go`.
- Engine integration: `stage.Commands()` accessor, `SystemBase.Commands()` shortcut, per-system flush in `pkg/engine/loop.go`.
- Test helpers: `stage.TickOne(sys)` and `stage.FlushCommands()` for tests that bypass the loop.
- Unit tests: ops ordering, no-op-on-dead-entity, Defer captures, ForEachN coverage, flush isolation between systems.
- No game-code changes. Everything still uses `gw.Queue` and `ecs.NewMap1`.

### Phase 2: Migrate panic-class structural mutations

- `internal/game/system_npc_ai.go`: replace `pendingLeashClears` with `mmokit.RemoveComponent[Leashing]` calls; delete the field and drain loop.
- `internal/game/game.go::dieKeepEntity`: replace `Enqueue(PendingDeathMarker)` with `mmokit.AddComponent(stage.Commands(), entity, Dormant{})`. Delete the Pending struct, delete the postFlush drain.
- `internal/game/hooks.go::processUndocks`: replace the raw `dormantMap.Remove(s.Entity)` call with `mmokit.RemoveComponent[Dormant](stage.Commands(), entity)`.
- Delete priming calls (`ecs.NewMap1[Leashing/Dormant/POIAnchor](w)`) from `system_npc_ai.go`, `system_targetlock.go`, `system_poi.go`, `gameworld.go`, `hooks.go`.

### Phase 3: Migrate game actions to Defer

- `PendingLootDrop`: replace queue + drain with `cmds.Defer(func() { gw.SpawnLootCrate(x, y, items) })` at the call sites in the death observer.
- `PendingDockRequest`, `PendingUndockRequest`: replace with `cmds.Defer(...)` at the input handler call sites.
- `PendingRespawn`: replace with `cmds.Defer(...)` at the timer fire in postTick.
- `PendingLootAll`: replace with `cmds.Defer(...)`.

### Phase 4: Delete the old queue

- Delete all `Pending*` structs from `internal/game/gameworld.go`.
- Delete `gw.Queue` field.
- Delete `pkg/engine/tickqueue.go` and the mmokit `TickQueue` alias + `Enqueue`/`Drain` helpers.
- `grep -r "PendingLootDrop\|PendingDeathMarker\|PendingDockRequest\|PendingUndockRequest\|PendingRespawn\|PendingLootAll" internal/` returns nothing.
- `grep -r "mmokit.Enqueue\|mmokit.Drain" .` returns nothing.

### Phase 5: Migrate query construction

- `internal/game/entity_station.go`: `ecs.NewFilter3[Station, Position, CellCoord]` → `mmokit.ForEach3[gamecomp.Station, mmokit.Position, mmokit.CellCoord](stage, fn)`.
- `internal/game/entity_poi.go`: `ecs.NewFilter1[POIAnchor]` → `mmokit.ForEach1[gamecomp.POIAnchor]`.
- `internal/game/op_bank.go`: `ecs.NewFilter2[Station, Position]` → `mmokit.ForEach2[gamecomp.Station, mmokit.Position]`.
- `internal/game/gameworld.go::hasStation`: `ecs.NewFilter1[Station] + Next()` → `mmokit.Any[gamecomp.Station](gw.stage)`.

### Phase 6: Erase remaining ark imports from internal/game/

- The 6 type-alias-only files (`entity_npc.go`, `entity_ship.go`, `factory.go`, `game.go`, `system_ability.go`, `system_network.go`, `transfer.go`): swap `ecs.Entity` → `mmokit.Entity`, drop the `ark/ecs` import.
- Hard verification: `grep -rn "mlange-42/ark/ecs" internal/game/` returns nothing.

### Phase 7: Documentation + enforcement

- Update `CLAUDE.md`: document Commands API, document the "no `ark/ecs` imports in `internal/game/`" rule, document the `mmokit.Set` (immediate) vs `Commands.AddComponent` (deferred) distinction.
- Optional: add a CI grep / `go vet` analyzer that fails the build if `internal/game/` re-imports ark.

## Risks and open questions

1. **Tests calling `sys.Update(dt)` directly will miss the per-system flush** unless they switch to `stage.TickOne(sys)`. Mitigation: do this as part of Phase 2 alongside the production migration so the diff is contiguous.
2. **Cross-cell entity transfer + queued Commands.** If a transferring entity has pending ops queued in the source cell, they'll no-op once the entity is removed (covered by the "no-op on dead entity" rule). Verified path; no special handling needed.
3. **`mmokit.Set` vs `Commands.AddComponent` coexist.** Set stays for direct/immediate writes from safe contexts (hooks, postFlush handlers, command verbs). AddComponent is the deferred form. Document the rule in CLAUDE.md: inside a `System.Update` loop, never call `Set` directly — always go through Commands.
4. **`pkg/system/` keeps direct ark.** Replication, spatial, physics, network — all framework-internal, all perf-critical, all carefully written by engine maintainers. The wrapper is for game-side correctness, not engine internals. Asymmetry is intentional.
5. **Defer closure traceability.** Closures show up as `func1.go:127` in stack traces, less greppable than typed `Pending` structs. Mitigation: helper functions (`gw.SpawnLootCrate`) keep their named identity inside the closure, so `grep SpawnLootCrate` still finds all callers. If we ever grow to 50+ Defer sites and want better introspection, we can add Bevy-style `cmds.Add(MyCommand{})` typed-command interface later — non-breaking addition.

## Success criteria

- `grep -rn "mlange-42/ark/ecs" internal/game/` returns no results.
- `grep -rn "PendingLootDrop\|PendingDeathMarker\|PendingDockRequest\|PendingUndockRequest\|PendingRespawn\|PendingLootAll\|pendingLeashClears" .` returns no results.
- `grep -rn "mmokit.Enqueue\|mmokit.Drain\|TickQueue" .` returns no results outside the deletion commit.
- All existing tests pass without modification beyond the `TickOne` swap.
- No new locked-world panics in any smoke run (4node-basic + space game).
- New systems written after this lands cannot accidentally introduce the panic — the only ECS structural mutation API reachable from game code is `Commands`, which always defers.
