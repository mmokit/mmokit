# Hot-Swappable WASM Game Systems

**Date:** 2026-06-04
**Status:** Design — approved for Phase 0 implementation
**Author:** Josh Stout (with Claude)

## Motivation

Today a game system is a Go type implementing `System.Update(dt float32)`, registered
via `AddSystem`, compiled into the binary, and run sequentially every tick on the game-loop
goroutine ([pkg/engine/loop.go](../../../pkg/engine/loop.go) L137). Changing *any* game logic
means recompiling and restarting the server.

The goal is to express game-logic systems as **self-contained modules that can be built
separately and loaded, unloaded, and hot-swapped at runtime** — so most game changes (and
adding/removing systems entirely) ship with **zero downtime** and a fast dev-iteration loop.

### Primary use cases

- **(A) Dev-loop hot-reload** — rebuild one system, see it take effect on a running server.
- **(C) Zero-downtime production deploys** — ship game-logic changes to a live cluster
  without disconnecting players.

These are the dominant motivators. Multi-language authoring (the SpacetimeDB "reducers in any
language" angle) is **explicitly a non-goal** — systems are authored in Go, forever.

## Constraints & First Principles

1. **True load AND unload is a hard requirement.** Systems may be added or removed entirely
   at runtime, not just swapped in place. This **eliminates Go plugins** (`.so` / the `plugin`
   package): they cannot be unloaded — it is a permanent property of the mechanism, not a wart.
2. **Trusted code.** These are the project's own game systems, not untrusted third-party mods.
   Sandboxing is a *bonus*, not a driving requirement.
3. **No meaningful performance degradation.** The 20 Hz fixed-timestep loop iterating thousands
   of entities must not regress.

### The governing trade-off

> **True unload requires an isolation boundary. The only mechanism with zero boundary cost is
> native in-process code — which is precisely the thing that cannot unload. Unload ⟺ boundary.**

There is no Go-forever mechanism that delivers literally-zero overhead *and* clean load/unload.
The design therefore does not try to avoid the boundary; it makes the boundary cost
**O(systems · ticks)** instead of **O(entities · ticks)** by batching ECS access at the column
level. This is exactly what SpacetimeDB does (WASM + batched host-call data access) and is the
basis for the chosen approach.

### Why this is affordable here

The architecture is split into a **stable native core** and a **swappable game layer**
(see "Scope" below). The hot inner-loop systems that touch thousands of entities at 20 Hz
(Physics, Spatial, Replication, Collision) **stay native and never cross the boundary**. Only
game-logic systems — which touch tens-to-low-hundreds of entities per cell — are swappable, so
the per-tick column copy is single-digit-kilobyte and single-digit-microsecond against a 50 ms
budget, and **does not scale with the hot-path entity counts**.

## Scope

**In scope (hot-swappable):** custom game-logic systems only — e.g. Docking, TargetLock,
ShipControl, Mining, Economy, Equipment, Ability, StatusEffect, Wander, ShieldRegen, AoE,
Supercruise, NPC AI.

**Out of scope (stays native, compiled into the binary):** the engine core — Physics, Spatial,
Replication, Collision, the game loop, transport, the ECS itself. Changes to these are ABI
changes (see §2) and ship via a full host redeploy using the existing cell-**migrate** machinery.

## Chosen Approach: WASM modules via wazero

A swappable system is authored in Go and compiled to `GOOS=wasip1 GOARCH=wasm`. The host embeds
the **wazero** pure-Go WASM runtime (no cgo, sandboxed, ships in the single binary). The host
owns the ark world and the native core; the module owns only its systems' logic and internal
state (in WASM linear memory).

### Authoring model — comparison

A swappable system declares its component query, then loops over plain Go slices that the host
has bulk-mapped into linear memory. The inner loop is essentially identical to today's native
code. Using `ShieldRegenSystem` ([internal/game/system_shieldregen.go](../../../internal/game/system_shieldregen.go))
as the reference:

**Today (native):**

```go
type ShieldRegenSystem struct {
    mmokit.SystemBase
    entities mmokit.Query[struct {
        Shield *gamecomp.Shield
    }]
}

func (s *ShieldRegenSystem) Update(dt float32) {
    for _, b := range s.entities.Iter {
        shield := b.Shield
        if shield.DamageCooldown > 0 {
            shield.DamageCooldown -= dt
            continue
        }
        if shield.Current < shield.Max {
            shield.Current = min(shield.Current+shield.RegenRate*dt, shield.Max)
        }
    }
}
```

**Proposed (compiled to `wasip1/wasm`, hot-loadable/unloadable):**

```go
package main

import (
    "github.com/zenion/mmoserver/pkg/wasmsys"            // authoring SDK, compiled INTO the module
    gamecomp "github.com/zenion/mmoserver/internal/component" // SHARED component structs = frozen ABI
)

type ShieldRegen struct{}

// Replaces the embedded Query[T] field. Tells the host which columns to
// bulk-map into linear memory each tick, and the access mode.
func (ShieldRegen) Query() wasmsys.Query {
    return wasmsys.ReadWrite[gamecomp.Shield]()
}

func (ShieldRegen) Update(ctx *wasmsys.Ctx, dt float32) {
    shields := wasmsys.Column[gamecomp.Shield](ctx) // []gamecomp.Shield view over linear mem
    for i := range shields {
        s := &shields[i]
        if s.DamageCooldown > 0 {
            s.DamageCooldown -= dt
            continue
        }
        if s.Current < s.Max {
            s.Current = min(s.Current+s.RegenRate*dt, s.Max)
        }
    }
}

func main() { wasmsys.Register(ShieldRegen{}) } // exports init/update/snapshot/restore
```

**What changed:** `Query[T]` field → `Query()` method (a declaration the host reads to know which
columns to copy); no `SystemBase`/live ECS handle (the world stays host-side); `Column[T](ctx)`
returns a `[]T` aliasing the host-copied buffer; `main()`+`Register` export the ABI entrypoints.
The arithmetic and control flow are verbatim.

**On the explicit `Query()` (and why it is generated later, not now):** the host must copy the
declared columns into linear memory *before* calling `Update`, so the column set must be known
ahead of execution — runtime/probe discovery is unreliable for conditionally-accessed columns
(`if rare { Column[Health](ctx) }` would not reveal `Health` until that branch first fires, by
which point the column needed to already be mapped). The set *can* be inferred, but only by
**build-time static analysis** (codegen), not from runtime calls. Phase 0 keeps `Query()`
hand-written (zero tooling); Phase 1 promotes it to a generated manifest (see Phasing).

## Architecture

### 1. The two sides

- **Host (native binary):** ark world; native core systems; game loop; wazero runtime; the
  **column bridge**; the Commands flush; the **module lifecycle manager**.
- **Module (`.wasm`):** one or more game-logic systems, each `Query()` + `Update` + optional
  `snapshot`/`restore`; plus the `wasmsys` SDK compiled in.

### 2. The frozen ABI

Three things are shared and version-stamped between host and module:

1. **Component struct layouts** — a shared Go package compiled into both. POD (value-type)
   components map directly via memcpy.
2. **The `wasmsys` SDK contract** — the exported entrypoint signatures and the host-import
   function signatures.
3. **An ABI version hash** — over component layouts + contract. On load the host compares the
   module's embedded hash to its own; **mismatch → load rejected.** This is the lockstep
   guarantee: a module built against stale component layouts physically cannot load into a newer
   host. Therefore **core/component changes require a host redeploy** (existing migrate path);
   **game-logic changes hot-swap** against the frozen ABI.

### 3. The column bridge (performance core)

Per swappable system, per tick:

1. Host resolves the system's `Query()` to the matching ark archetypes/columns.
2. Host writes a **batch header** into the module's linear-memory arena: entity count, per-column
   base offsets, and the entity netID/handle array (so Commands can refer to entities by index).
3. Host bulk-copies each declared component column (ark stores SoA/archetype-column) into the
   arena at the agreed offsets. `Read` columns are copied in only; `ReadWrite` columns are
   copied back after `Update`.
4. Host calls `update(dt, batchPtr)`.
5. The module's `Column[T]` returns a `[]T` over the buffer; the loop runs.
6. On return the host copies `ReadWrite` columns back into ark and drains queued Commands.

**Boundary crossed twice per tick per system, never per entity.** The arena is host-owned,
grown as needed, and **reused across ticks** (no per-tick allocation). The module exposes an
arena pointer (e.g. an exported `batchBuffer(minSize uint32) uint32`) so the host writes columns
into a region the module's allocator will not clobber; wazero gives the host read/write access
to the module's full linear memory.

### 4. Structural mutation & cross-entity access (host imports)

The module never touches ark directly. Host-import functions mirror the existing primitives:

- `cmd_despawn(entityIndex)`, `cmd_add_component(entityIndex, typeID, ptr, len)`,
  `cmd_remove_component(entityIndex, typeID)`, `cmd_spawn(blobPtr, blobLen)` → queue into the
  **existing per-stage `Commands` buffer**, flushed at the same system boundary as native systems
  (`engine.Hooks.AfterSystem`).
- `lookup_component(netID, typeID, outPtr) bool` → random-access read of another entity (a
  boundary crossing; for reference-chasing systems, used sparingly).
- `send_event(connID, code, msgPtr, msgLen)` → mirrors `Stage.SendEvent`.
- `log(catID, msgPtr, msgLen)` → mirrors `gw.Log.Log`.

**Honest casualty:** `Commands().Defer(closure)` has no cross-boundary equivalent — arbitrary Go
closures cannot cross. Multi-step game actions get re-expressed as structured ops inside
`Update` (sequences of the host-import commands above).

### 5. State model & lifecycle

- **Durable per-entity state** lives in ark components (host-side) → survives load/unload
  untouched. This is the ECS-orthodox default and covers the common case for free.
- **Module internal state** lives in WASM linear memory; the module exports `snapshot() (ptr,len)`
  (called before unload) and `restore(ptr, len, version)` (called after load, with a version tag
  for shape migration). A fresh **add** calls `restore` with an empty payload.
- **Global non-entity state** (order book, spatial indices) lives **host-side as resources**,
  exposed via host-import verbs — *not* inside a swappable module, since the native core touches
  it too.
- **Ephemeral scratch** is rebuilt in `init()`.

**Lifecycle operations** (all at a tick boundary):

- **Add:** load `.wasm` → ABI-check → instantiate → `init()` → splice into tick order → runs next tick.
- **Remove:** `snapshot()` (optional) → drop the instance (**true unload**, memory reclaimed) → unsplice.
- **Swap:** add-new + remove-old at one boundary, piping `snapshot` → `restore` with migration.

**Tick ordering:** a module declares an anchor (e.g. `After("ShipControl")` / `Before("Physics")`,
or a numeric priority within the game-logic phase). Native core systems are fixed anchors, so
insertion is deterministic when systems are added or removed.

### 6. Atomicity — staged

- **Single-process (PoC + most of dev):** the swap runs between ticks on the loop goroutine via
  `engine.RunOnLoop` — atomic for free.
- **Cluster-wide (deferred to Phase 3):** a new commit kind through the existing orchestrator —
  all hosts load+instantiate+migrate in the *Ready* phase, ack, and commit at one agreed
  `ClusterClock` tick. Reuses the commit-plan, invariants, and commit-log machinery already built
  for split/merge/migrate.

## Known constraints & limitations

- **POD components only on the cheap path.** Components with slices/maps/pointers (`Inventory`,
  `AbilitySet`, `StatusEffects`) cannot be flat-copied. They require per-entity marshaling (more
  expensive) or the systems that touch them stay native. Phase 2 decides the strategy
  (marshal vs. a formal "stays-native" list).
- **Cross-entity random access changes shape.** A system that chases references (e.g. "my
  target's Health by netID") either includes that data in its declared query or pays a
  `lookup_component` host crossing — it cannot make an ad-hoc ark call.
- **No `Defer(closure)`** across the boundary (see §4).
- **ABI lockstep.** Any change to a shared component layout invalidates existing modules by
  design; they must be rebuilt against the new host. This is the safety property, not a defect.
- **Not literally zero overhead.** There is a small, bounded, per-tick constant cost
  (column copy + WASM call). It is independent of hot-path entity counts. Literal zero is only
  achievable with native in-process code, which cannot unload.

## Phasing

### Phase 0 — the "limited system" PoC (this milestone)

Single-process. Validate the authoring model and the performance characteristics end-to-end.

- Build the `wasmsys` authoring SDK (`Query`/`ReadWrite`/`Read`/`Column`/`Register`/`Ctx`).
- Build the host: wazero integration, the column bridge, the module lifecycle manager,
  the ABI version handshake, and `snapshot`/`restore`.
- Port **ShieldRegen** (read-write, POD `Shield` component) to a `.wasm` module.
- Wire **add / remove / swap** on the loop goroutine via `RunOnLoop`.
- **Benchmark:** native ShieldRegen vs. wasm ShieldRegen at N = 100 / 1 000 / 10 000 entities;
  confirm the boundary cost is flat per-tick (not per-entity) and quantify the crossover.

**Out of Phase 0:** Commands host-imports, cross-entity lookup, cluster atomicity, multiple
coexisting modules, pointerful components.

**Phase 0 done = validated.** The PoC proves load/unload/swap + the perf benchmark; that is the
agreed bar for "limited system."

### Phase 1
Commands host-imports (spawn/despawn/add/remove), `lookup_component`, `send_event`, `log`,
multi-component queries; port a second, *mutating* system.

**Codegen-derived query manifest.** Replace the hand-written `Query()` with a `//go:generate
wasmsys-gen` build step that statically analyzes the module package (`x/tools/go/packages`),
collects every `Column[T]` (read-write) and `View[T]` (read-only) call site, and emits the
column manifest as exported module metadata the host reads at load. Access mode rides on the
accessor name (`Column` = RW, `View` = RO). Constraints: concrete type args at each call site
only (no `Column[T]` where `T` is a type parameter); the analyzer walks the package call graph
so accessors in helper functions are still discovered. An explicit override remains available for
the rare dynamically-determined column.

### Phase 2
Tick-order declarations + multiple coexisting modules + host-side resources; decide the
pointerful-component strategy (marshal vs. formally stays-native).

### Phase 3
Cluster-wide atomic add/remove/swap via the commit-plan / `ClusterClock` orchestrator.

## Open questions (deferred, not blocking Phase 0)

- Exact host-side resource API for global state (Phase 2).
- Pointerful-component marshaling format vs. stays-native classification (Phase 2).
- Standard-Go-wasip1 vs. TinyGo for module builds — start with standard Go (full language);
  evaluate TinyGo later purely for module size / instantiation latency.
- Module packaging/distribution (where `.wasm` artifacts live, how a deploy delivers them).
