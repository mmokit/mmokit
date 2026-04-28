# Engine debug component + bindings cleanup

**Date:** 2026-04-28
**Status:** Design

## Motivation

Two related awkwardnesses in the current entity-kind registration API:

1. **`EngineBindingsConfig` is per-kind but every value in the codebase is uniform.** Across two games and seven `RegisterKind` sites, `SizeQuantScale: 500` and `IncludeMeshState: true` are set identically every time; `VelQuantScale: 2000` is set identically on every kind that moves. Making each game declare these values per kind is boilerplate without expressive value.

2. **`IncludeMeshState: true` conflates two unrelated concepts.** "The engine knows mesh state" (always true — derivable from `Ghost`/`Replica`/`CellCoord`) and "the client should see mesh state" (a game choice). The flag tries to control wire exposure but lives next to quantization scales, which is unrelated.

Today, 4node-basic also hand-rolls a `DebugInfo` component + `DebugInfoSystem` to ship `AoIRadius` to the client for the AoI overlay. That's a separate per-entity mechanism that exists purely for debug rendering. The engine has no equivalent for mesh-state debug info — it bypasses the bundle/component system entirely via the `IncludeMeshState` flag.

## Design

### A single engine-provided debug component

```go
// pkg/component/debug_info.go (re-exported as mmokit.DebugInfo)
type DebugInfo struct {
    Presence  uint8   `net:"u8"`   // EMS_LOCAL / EMS_REPLICA / EMS_GHOST
    OwnerHost uint8   `net:"u8"`   // host index in cluster topology
    AoIRadius float32 `net:"f32"`  // viewer's effective AoI radius
}
```

Lives in `pkg/component` alongside other engine components (Position, Velocity, Collider, …). Re-exported via `mmokit.DebugInfo` for game-code convenience.

### Engine writer system

A new builtin system, auto-added during `Process.Build()`, walks every entity that has `*DebugInfo` each tick and writes all three fields:

- `Presence` — derived from `HasAll(Ghost)` / `HasAll(Replica)` / neither, mapped to the existing `enginepb.EntityMeshState` enum values.
- `OwnerHost` — looked up via the coordinator's host-for-cell mapping (replaces today's flat `cellY * gridWidth + cellX` index, which doesn't survive dynamic-cell splitting).
- `AoIRadius` — pulled from `Config.AoIRadius`. (If/when per-entity AoI overrides become useful, that's a separate component or a writer-system override; out of scope here.)

The system is a no-op for any entity whose bundle doesn't declare `*DebugInfo`, so cost is purely opt-in per kind.

### Bundle integration replaces `IncludeMeshState`

Game opts in by including the field:

```go
type PlayerComponents struct {
    Name      *PlayerName
    DebugInfo *mmokit.DebugInfo   // engine writes; net tags drive wire format
    MoveTarget *mmokit.MoveTarget
}
```

If the field is absent, the engine never adds the component to that kind's entities, the writer system skips them, zero bytes hit the wire. No flag.

### `EngineBindingsConfig` dissolves

After removing `IncludeMeshState` and `GridWidth` (engine looks up host internally), the remaining fields are `VelQuantScale`, `SizeQuantScale`, and `CellSizeFn`:

- `VelQuantScale` and `SizeQuantScale` move to `Config` with defaults set to today's universal values:

  ```go
  type Config struct {
      // ...
      VelQuantScale  float32 // default: 2000 (max ~16 u/s, precision 0.0005)
      SizeQuantScale float32 // default: 500  (max ~65 units, precision 0.002)
  }
  ```

- `CellSizeFn` is currently never set non-default in the codebase — every caller falls through to `coords.CellSize`. Since entities always use base-cell coords (per the standing rule, regardless of quadtree depth), this is correct unconditionally. Field is removed; `EngineBindings()` always uses `coords.CellSize`.

`EngineBindingsConfig` is deleted entirely. `RegisterKind` signature simplifies:

```go
// Before
mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player",
    mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500, IncludeMeshState: true})

// After
mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player")
```

## Wire-format & change detection

Concern: a writer system that runs every tick sounds like it would ship every tick. It won't, because the replication pipeline already has four layers of change detection between component writes and wire output:

1. **Per-entity hash pre-check** ([replication.go:662-673](pkg/system/replication.go#L662-L673)) — every tick, hash diff-relevant fields. If unchanged, skip serialization.
2. **Dormancy** ([replication.go:655-660](pkg/system/replication.go#L655-L660)) — entity unchanged for `DormancyThreshold` ticks → skip even the hash work.
3. **Per-field delta on the wire** ([quantize/delta.go:65-113](pkg/quantize/delta.go#L65-L113)) — `DeltaEncoder` writes `[bitmask][changed-field-values]`. Unchanged fields contribute 0 bytes (their bits in the bitmask are 0).
4. **`net:"initial"` tag** — sent once at visibility-enter, never in deltas.

For a player whose `DebugInfo` flips at a cell-handoff: 6 bytes ship on the flip tick (1 + 1 + 4), 0 on every other tick beyond what the entity's other fields produce. For a stationary asteroid with `DebugInfo`: zero work and zero bytes after dormancy kicks in.

So `DebugInfo` as a 6-byte component is essentially free at runtime, and the writer system's per-tick work is bounded by entity count, not by network throughput.

## Migration / blast radius

**New / modified in `pkg/`:**

- New: `pkg/component/debug_info.go` — `DebugInfo` struct.
- New: `pkg/system/debug_info_writer.go` — builtin writer system.
- Modified: `pkg/mmokit/mmokit.go` — re-export `mmokit.DebugInfo`; add `VelQuantScale` / `SizeQuantScale` to `Config` with defaults.
- Modified: `pkg/system/auto_replicator.go` — delete `MeshState` synthetic binding, delete `EngineBindingsConfig`, change `EngineBindings()` to read scales from a `*Config`.
- Modified: `pkg/mmokit/kindreg.go` — `RegisterKind` signature drops the bindings argument.
- Modified: `pkg/universe/entity_kind.go` — delete the `EngineBindings *system.EngineBindingsConfig` field on `EntityKindDef`. Whether the kind ships debug info is derivable from bundle reflection alone (bundle declares `*DebugInfo` ⇔ engine adds the component ⇔ writer system runs ⇔ field bytes appear in the snapshot).
- Modified: `pkg/universe/process.go` (or wherever Build lives) — auto-register the `DebugInfoWriter` system.

**Game-side updates:**

- `examples/4node-basic/components.go` — delete `DebugInfo` struct; replace `Debug *DebugInfo` with `DebugInfo *mmokit.DebugInfo` in `PlayerComponents` (Bot bundle stays unchanged — bots don't expose debug info).
- `examples/4node-basic/system_debug_info.go` — delete file.
- `examples/4node-basic/main.go` — drop `playerBindings` local var, drop bindings args from both `RegisterKind` calls, drop `mmo.AddSystem(&DebugInfoSystem{})`.
- `examples/4node-basic/web/src/...` — update consumer to read `debugInfo.aoIRadius` (or the SDK-generated field name) instead of the old `debug.aoIRadius`.
- `internal/game/entity_kinds.go` — drop bindings args from all 7 `RegisterKind` calls; add `*mmokit.DebugInfo` to `ShipBundle` (and any other bundle the game wants client-visible debug info on).

**Schema regeneration:**

- `just build` re-emits the SDK with the new wire format. The codegen flattens net-tagged fields per entity (one TS field per `net:"..."` tag, regardless of source component), so `DebugInfo` produces three flat fields on the entity interface: `presence: number`, `ownerHost: number`, `aoiRadius: number`. The old `meshState` / `ownerNode` fields go away.
- Web client code that reads `entity.meshState` / `entity.ownerNode` migrates to `entity.presence` / `entity.ownerHost`. 4node-basic's existing `entity.aoIRadius` (from its hand-rolled `DebugInfo`) gets renamed to whatever the SDK emits for `mmokit.DebugInfo.AoIRadius` — likely `aoiRadius`.

**No backward compat:**

Per the standing project rule, no aliases or shims. All callers update to the new shape in one PR.

## Open questions resolved

- **Component name:** `mmokit.DebugInfo`. Generic enough to extend later (e.g. add per-tick perf metrics, last-handoff timestamp), specific enough that it lives in the engine's namespace.
- **Why one component instead of separate `MeshState` + `AoIInfo`:** every site that wants one wants both (debug overlay rendering). Splitting forces games to declare both fields and pay two component slots. If a future case wants just one, split then.
- **AoIRadius cost:** ~0 bytes/tick on the wire after the initial frame, per the four-layer change detection above.
- **Per-entity AoI overrides:** out of scope. Today AoI is per-process; the writer system reads `Config.AoIRadius`. When per-entity AoI is needed, the writer system grows a hook or `DebugInfo` gets an override field.

## Non-goals

- Changing the replication system or wire format beyond removing the synthetic `MeshState` binding.
- Adding a generic "debug-only fields" wire-tag concept (e.g. `net:"debug"` that only ships when a debug flag is set). The component-membership opt-in is sufficient.
- Per-viewer field shipping (e.g. "only ship `AoIRadius` to the entity's owner"). Not needed; field-level delta + dormancy already make it ~free.
