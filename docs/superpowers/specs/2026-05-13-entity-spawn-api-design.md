# Entity Spawn API — Design

**Status:** Approved for implementation
**Author:** Josh Stout
**Date:** 2026-05-13

## Problem

`Stage.SpawnEntity(pos, opts...)` and its game-side wrappers (`gw.SpawnNPC`, `gw.SpawnLootCrate`, `gw.SpawnPOI`, `gw.SpawnStation`, `gw.spawnAsteroid`) carry several warts that compound into hard-to-read, error-prone spawn code:

1. **Two-phase init.** `SpawnEntity` allocates an ECS entity and attaches zero-valued components from the registered Bundle (via the `WithComponents()` marker). Callers then run a wall of `mmokit.Set(entity, Comp{...})` calls to fill in real values. A 10-component NPC spawn ends up at ~50 lines, most of it scaffolding.

2. **Asymmetric component args.** `Position` is positional; `Collider`, `Rotation`, `EntityKind` come through ad-hoc option helpers (`WithCollider(r)`, `WithRotation(a)`, `WithEntityKind(t)`); every other component (`Health`, `Shield`, `Inventory`, `NPCAI`, `TargetLock`, …) has no option helper and must be filled with `mmokit.Set` after the spawn returns. There is no principle behind which component lives at which level.

3. **Wrong return type.** `SpawnEntity` returns `ecs.Entity` (the raw ark handle). Every caller immediately does `mmokit.EntityFromECS(stage, handle)` to get the rich wrapper that supports `.NetID()`, `.Send()`, `.Alive()`, etc.

4. **`WithComponents()` is a magic marker** with no arguments and no documentation at call sites. It triggers auto-attach of zero-valued kind components, which silently swallows "forgot to Set" bugs (e.g., an NPC spawned with `Health{Current: 0, Max: 0}` is permanently dead-on-arrival).

5. **Get-then-mutate dance for Collider.** `WithCollider(radius)` only sets the bounding radius. Width, Height, Layer, Shape must be patched via `if col := mmokit.Get[mmokit.Collider](entity); col != nil { col.Width = ... }`. Half the entity-spawn helpers repeat this pattern.

The Bundle struct registered via `mmokit.RegisterKind[T]` (e.g., `NPCBundle`, `ShipBundle`) declares which components a kind has — but it's a *schema* used by replication and transfer, not a value carrier. There's no way to populate component values through it at spawn time.

## Goals

- Replace the two-phase spawn-then-Set pattern with a single declarative call where every component value lives at the call site.
- Eliminate the framework-vs-game-component asymmetry: all components are equal citizens in the spawn arg list.
- Return `mmokit.Entity` (the rich wrapper) so callers can immediately call methods on the result without an `EntityFromECS` wrap.
- Delete the magic `WithComponents()` marker and the ad-hoc `WithX` option helpers; the new API doesn't need them.
- Shrink game-side spawn helpers from ~50 lines to ~20, mostly by removing scaffolding.

## Non-goals

- Replacing ark or changing the storage model.
- Restructuring `RegisterKind[T]` or the Bundle struct's role in replication/transfer. The Bundle stays a schema; only the spawn API changes.
- Compile-time enforcement of "this kind requires Health populated." Go's type system can't express that on a variadic-any signature; runtime invariants in dev (`InvariantPanic` mode) catch the case instead.
- Touching the cell-transfer respawn path (`SpawnFromTransfer`). That stays as its own primitive — it deserializes a network frame into an entity, which is a different operation.

## API surface

A single new method on `*Stage`, plus a sibling `SpawnPlayer` for the player case:

```go
// Spawn creates an entity carrying the given components. The framework
// walks the variadic args, dispatches each by Go type, and attaches it
// to the new entity. Components must be passed by VALUE (not pointer).
//
// Position must be present — Spawn panics if not. Every entity
// participates in spatial indexing, which requires a known position.
// Test fixtures that don't need spatial behavior pass Position{0, 0}
// explicitly.
//
// The same component type passed twice is a programmer error; Spawn
// panics. Order of args has no semantic effect — each component is
// attached independently of the others.
//
// Returns the rich Entity wrapper (mmokit.Entity), not the raw
// ecs.Entity handle. Callers can immediately use .NetID(), .Send(),
// .Alive(), etc.
func (b *Stage) Spawn(components ...any) Entity

// SpawnPlayer is the player-spawn variant. Binds session.ConnID into a
// PlayerConn component and tracks the entity in PlayerManager state.
// Otherwise identical to Spawn.
func (b *Stage) SpawnPlayer(session *engine.PlayerSession, components ...any) Entity
```

**Component conventions:**

- Pass values, not pointers: `mmokit.Position{X: 1, Y: 2}`, `gamecomp.Health{Current: 100, Max: 100}`.
- `EntityKind` is an ordinary component: `mmokit.EntityKind{Type: gamecomp.KindNPC}`. No special "kind option."
- `Position`, `Collider`, `Rotation` are ordinary components: `mmokit.Position{...}`, `mmokit.Collider{Width: w, Height: h, Layer: L, Shape: S}`, `mmokit.Rotation{Angle: a}`.
- Optional/conditional components (e.g., `POIAnchor` only when `poiNetID != 0`) are appended to a slice and spread: `gw.stage.Spawn(components...)`.

**What `Spawn` does internally:**

1. Walk the args once to find `Position`. Panic if missing. Use it for spatial indexing.
2. Walk the args once to find `EntityKind` (optional — kindless entities are legal). If present, look up the registered Bundle for the kind and stash it for the invariant check below.
3. Allocate an ECS entity via `ecs.NewEntity(world)`.
4. Walk the args a third time; for each arg, look up its `reflect.Type` in a per-process `componentAttachHandlers` map. Call the type-specific attach function which calls into ark's typed `Map[T].Add(handle, &arg)`.
5. Assign a `NetworkID` (via the engine's standard netID allocator) and register the live netID in the stage's index.
6. Register in spatial grid + presence index using the Position from step 1.
7. (Debug mode) If a kind was provided, check that every non-`mmokit:"local"` Bundle field has a corresponding component attached. If not, panic with a clear message like `"NPC spawn missing required component: Health"`.
8. Return the `mmokit.Entity` wrapper.

**`componentAttachHandlers` cache:** populated lazily on first-of-type spawn (cache miss → use `reflect.TypeOf` to build a closure that calls the right `ecs.NewMap1[T](w).Add(handle, &val)`). Stored in a `sync.Map` keyed by `reflect.Type`. After warmup, all subsequent spawns hit the cached path.

## Auto-attach behavior change

The current `WithComponents()` marker triggers auto-attach of *all* the kind's Bundle components at zero values. After spawn, callers Set non-zero values for the components they care about.

This is gone. The new behavior:

- **You must pass every component you want attached.** No silent zero-fills.
- **Debug-mode invariant** (`Config.InvariantMode == InvariantPanic`, which is dev/test default per CLAUDE.md): if a kinded entity is missing a non-`mmokit:"local"` Bundle field after spawn, `Spawn` panics with a clear message. Production (`InvariantOff`): no check.
- **The Bundle struct's role is unchanged**: it remains a *schema* for replication/transfer, declaring which components participate in cross-cell handoff. It is NOT a value carrier at spawn time.

This trades silent buggy spawns (forgot to Set Health after auto-attach → NPC permanently dead) for loud, immediate failures during smoke testing.

## Sample migration (NPC)

**Before** (50 lines, [entity_npc.go:28-77](internal/game/entity_npc.go#L28-L77)):

```go
func (gw *GameWorld) SpawnNPC(x, y float32, archetype uint8, poiNetID uint32) mmokit.EntityHandle {
    defaults := archetypeDefaults(gw.Config, archetype)
    br := boundingRadius(gw.Config.NpcWidth, gw.Config.NpcHeight)

    handle := gw.stage.SpawnEntity(
        mmokit.Position{X: x, Y: y},
        mmokit.WithEntityKind(gamecomp.KindNPC),
        mmokit.WithCollider(br),
        mmokit.WithRotation(0),
        mmokit.WithComponents(),
    )
    entity := mmokit.EntityFromECS(gw.stage, handle)

    if col := mmokit.Get[mmokit.Collider](entity); col != nil {
        col.Width = gw.Config.NpcWidth
        col.Height = gw.Config.NpcHeight
        col.Layer = gamecomp.LayerPlayer
        col.Shape = mmokit.ShapeRect
    }

    mmokit.Set(entity, gamecomp.Health{Current: defaults.HP, Max: defaults.HP})
    mmokit.Set(entity, gamecomp.Shield{
        Current:    defaults.Shield,
        Max:        defaults.Shield,
        RegenRate:  gw.Config.NpcShieldRegenRate,
        RegenDelay: gw.Config.NpcShieldRegenDelay,
    })
    mmokit.Set(entity, gamecomp.TargetLock{
        MaxSlots: gw.Config.LockMaxSlotsNPC,
        Range:    defaults.AggroRadius,
    })
    mmokit.Set(entity, gamecomp.NPCAI{ /* 10 fields */ })
    if poiNetID != 0 {
        mmokit.Set(entity, gamecomp.POIAnchor{POINetID: poiNetID})
    }

    gw.eng.Log.Log(CatPlayerSpawn, "npc spawned: ...")
    return handle
}
```

**After** (~25 lines):

```go
func (gw *GameWorld) SpawnNPC(x, y float32, archetype uint8, poiNetID uint32) mmokit.Entity {
    d := archetypeDefaults(gw.Config, archetype)
    components := []any{
        mmokit.Position{X: x, Y: y},
        mmokit.EntityKind{Type: gamecomp.KindNPC},
        mmokit.Collider{Width: gw.Config.NpcWidth, Height: gw.Config.NpcHeight, Layer: gamecomp.LayerPlayer, Shape: mmokit.ShapeRect},
        mmokit.Rotation{},
        gamecomp.Health{Current: d.HP, Max: d.HP},
        gamecomp.Shield{Current: d.Shield, Max: d.Shield, RegenRate: gw.Config.NpcShieldRegenRate, RegenDelay: gw.Config.NpcShieldRegenDelay},
        gamecomp.TargetLock{MaxSlots: gw.Config.LockMaxSlotsNPC, Range: d.AggroRadius},
        gamecomp.NPCAI{Archetype: archetype, /* ... */},
    }
    if poiNetID != 0 {
        components = append(components, gamecomp.POIAnchor{POINetID: poiNetID})
    }
    e := gw.stage.Spawn(components...)
    gw.eng.Log.Log(CatPlayerSpawn, "npc spawned: netID=%d archetype=%d pos=(%.0f,%.0f) anchor=%d",
        e.NetID(), archetype, x, y, poiNetID)
    return e
}
```

Net reduction: ~25 lines, no `EntityFromECS` wrap, no Collider Get-then-mutate, no `Set` calls, return type is the rich `mmokit.Entity`.

## Phased migration

**Phase 1 — Land the new API.**

- Add `Stage.Spawn(components ...any) Entity` to `pkg/universe/stage.go`. Mmokit re-exports via the existing `Stage` alias.
- Add `Stage.SpawnPlayer(session, components ...any) Entity` alongside it (analogous to the existing one but with the new arg shape).
- Implement the `componentAttachHandlers` cache (sync.Map keyed by reflect.Type).
- Add `mmokit.EntityKind` exposed as an ordinary component value (already exists as `component.EntityKind` — verify the mmokit re-export).
- Unit tests in `pkg/universe/spawn_test.go`: position-required panic, duplicate-component panic, kind missing required component panic (under InvariantPanic), wrapper return type, every component attached.
- Existing `SpawnEntity` and the `WithX` options stay. No game code changes yet.

**Phase 2 — Migrate game-side spawn helpers.**

One commit per helper, each a behavior-preserving refactor with tests still passing:

1. `gw.SpawnNPC` (`entity_npc.go`)
2. `gw.SpawnLootCrate` (`entity_lootcrate.go`)
3. `gw.SpawnPlayer` (`entity_ship.go`) — uses `Stage.SpawnPlayer`
4. `gw.SpawnPOI` (`entity_poi.go`)
5. `gw.SpawnStation` (`entity_station.go`)
6. `gw.spawnAsteroid` / `spawnAsteroidWithItem` (`entity_asteroid.go`)

Each becomes a thin wrapper around `gw.stage.Spawn(...)`. The return type changes from `mmokit.EntityHandle` (or void) to `mmokit.Entity` where it makes sense.

**Phase 3 — Migrate framework-internal callers.**

Survey `pkg/universe`, `pkg/system`, `pkg/mmokit` for `Stage.SpawnEntity` calls and migrate the ones that fit. Some may genuinely belong on `SpawnEntity` (e.g., the cell-transfer respawn path uses `SpawnFromTransfer`, not `SpawnEntity`, so no migration needed there).

**Phase 4 — Delete the old API.**

- Delete `Stage.SpawnEntity` and its option helpers (`WithEntityKind`, `WithCollider`, `WithRotation`, `WithComponents`, plus any other `WithX` not used by `Stage.Spawn`).
- Verify `grep "WithComponents\|WithEntityKind\|WithCollider\|WithRotation" --include="*.go" .` returns nothing.
- Verify `grep "stage.SpawnEntity\|Stage.SpawnEntity" --include="*.go" .` returns nothing.

**Phase 5 — Documentation.**

Update CLAUDE.md's spawn section to document `Stage.Spawn(components...)` and the no-auto-zero rule. Delete the existing references to `WithComponents` / `WithCollider` / etc.

Each phase compiles and passes tests independently. Phase 1 is additive (no deletions). Phases 2–4 are net-negative LoC.

## Risks and open questions

1. **Reflection cost.** Per-component overhead is ~30 ns once the cache is warm (one `reflect.TypeOf` + one `sync.Map` load + one indirect call). For an 8-component spawn, ~240 ns per call. At 100 spawns/sec under load, ~24 µs/sec ≈ 0.002% of CPU. Not a concern.

2. **First-of-type spawn is expensive** (cache miss path builds the attach closure, ~µs range). Mitigation: pre-warm at startup by iterating every component type known to RegisterKind. Even without pre-warming, the cache miss happens once per type per process lifetime.

3. **Lost compile-time check.** The current `mmokit.Set[T](entity, T{})` is type-checked at compile time. The new variadic-any path is not. Mitigation: `InvariantPanic` mode (dev default) catches missing-required-component cases immediately during smoke runs.

4. **`mmokit:"local"` tag on Bundle fields.** Fields marked local (`TargetLock`, `POIAnchor` on NPC) are not serialized during cross-cell transfer but ARE valid components to attach at spawn. The debug-mode invariant must skip `mmokit:"local"` fields when checking "required components" — the bundle declares them as non-transferred, not as optional. (Today's `WithComponents()` already excludes them; same logic applies.)

5. **`Stage.SpawnEntity` deletion.** Cell-transfer respawn uses a separate `SpawnFromTransfer` primitive; that stays. Other engine-internal callers of `SpawnEntity` need to be inventoried in Phase 3 before Phase 4 can land.

6. **Spawn helper return type changes.** Callers of `gw.SpawnNPC` that today take `mmokit.EntityHandle` may need to switch to `mmokit.Entity`. That's a one-line per-call change. The handle is still accessible via `e.Handle()` if needed.

## Success criteria

- `grep "WithComponents\|WithEntityKind\|WithCollider\|WithRotation" --include="*.go" .` returns nothing.
- `grep "stage.SpawnEntity\|Stage.SpawnEntity" --include="*.go" .` returns nothing.
- `grep "mmokit.EntityFromECS.*Spawn" --include="*.go" internal/game/` (the wrap-after-spawn pattern) returns nothing.
- All existing tests pass.
- A new spawn in any game helper is one declarative call with every component value at the call site.
- Forgetting a required component on a kinded spawn produces an immediate panic in dev (InvariantPanic mode) — not silently broken behavior.
