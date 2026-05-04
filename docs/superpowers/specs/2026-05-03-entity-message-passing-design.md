# Entity & Message-Passing Redesign — Design

**Status:** Draft for review
**Author:** Josh Stout (with Claude)
**Date:** 2026-05-03
**Related memories:** `project_opensource_ready`, `feedback_no_backward_compat`, `feedback_refactor_over_stopgaps`, `feedback_mmokit_facade_only`

## 1. Summary

The current model leaks cross-cell infrastructure into game code. To deal damage to a target that may be on a neighboring cell, a system handler today writes:

```go
case item.AbilityTypePulseLaser, ...:
    if gw.eng.ECS.Alive(lock.TargetEntity) {
        if s.isReplica(lock.TargetEntity) {
            s.sendCrossNodeDamage(action.casterNetID, lock.TargetEntity,
                params.Damage, action.slot, uint8(params.Type))
            sentCrossNode = true
        } else {
            damageDealt = gw.ApplyDamage(lock.TargetEntity, params.Damage, action.casterNetID)
        }
        targetNetID = lock.TargetNetID
    }
```

That snippet exposes `gw.eng.ECS` (four-deep accessor chain), forces a manual `isReplica` branch, hand-rolls a marshaller (`sendCrossNodeDamage` → `MarshalDamageAction`), splits the apply path between two cells (`HandleCrossCellAction` and `HandleActionResult`), and requires the caller to remember to enqueue an `AbilityCastResultMsg` on both cells (which we just spent a day debugging because the second enqueue was missing).

This spec redesigns interactions around **first-class entity handles** and **typed message passing**. The same scenario becomes:

```go
case item.AbilityTypePulseLaser, ...:
    if target.Alive() {
        target.Send(FirePulseLaser{Caster: caster, Target: target, Slot: slot})
    }
```

Cross-cell routing, codec, AoI broadcast, replica vs live distinction — all inside the framework. The class of bug we just fixed becomes structurally impossible: there is no place in game code where a developer can "forget to broadcast on the other cell."

## 2. Goals

- **Hide cells from game code.** No `isReplica`, no `gw.NetIDToEntity`, no `Bridge.SendAction`, no per-action marshaller. Game code writes intent; framework routes it.
- **Reduce concept count.** A game framework should expose ≤ 6 game-facing concepts (entities, components, messages, handlers, systems, spawns). Today there are ~12.
- **Surface state transitions as composable events.** Death is not a weapon concern; weapons damage, Health-going-zero is what kills. Loot, kill rewards, killmails, achievements all listen to `Killed` independently.
- **One wire path for all interactions.** The same typed struct flows server-to-server (cross-cell action), server-to-client (visual/animation broadcast), and client-to-server (input). One codec, one dispatch model.
- **Opensource-ready.** Documentation can teach the framework in one sitting; common bugs (forgetting to broadcast, mismatched codecs) are not expressible.

## 3. Non-goals

- **Backward compatibility.** Existing game code in `internal/game/` will be rewritten. There are no shim layers.
- **Eliminating raw ECS access entirely.** Raw `ecs.World`, `ecs.Map1[T]`, and `ecs.NewMap1` remain reachable as an *escape hatch* for performance-critical or unusual iteration patterns. They are not the default. The default for per-tick work is framework-provided per-entity tick callbacks (§4.6); reaching for raw ECS is a deliberate choice the framework makes possible but does not encourage.
- **Generic distributed-actor runtime.** Actors-with-mailboxes is the conceptual influence; the implementation does not include per-entity mailboxes, supervisor trees, or fault tolerance beyond what the existing cell mesh provides.
- **Position-anchored broadcasts as a primitive.** Rare cases (a meteor strike at coordinates with no associated projectile) spawn an ephemeral entity rather than introducing a position-anchor primitive. Keeps the broadcast model uniform.

## 4. The five primitives

The entire game-facing framework reduces to these five primitives plus components and systems.

### 4.1 `Entity` — value handle, cross-cell aware

```go
package mmokit

// Entity is the game-facing handle. Value type. Cheap to pass.
// Wraps NetID + lazily-resolved local ECS handle + world ref.
// Methods are safe on zero / dead entities.
type Entity struct{ /* unexported */ }

func (e Entity) Alive() bool                     // true iff resolves locally and is alive
func (e Entity) NetID() uint32
func (e Entity) Pos() (x, y float32)             // zero if dead
func (e Entity) Local() bool                     // rare; for tooling/diagnostics
func (e Entity) Send(msg any)                    // see §4.5
```

**Wire codec.** `Entity` encodes as `uint32` (its NetID). Receiving code resolves it lazily on first method call. The cached ECS handle is local-only state, never shipped.

**Replaces.** Raw `ecs.Entity` in game-facing code, the parallel `lock.TargetEntity` + `lock.TargetNetID` field pairs, and direct `gw.NetIDToEntity[id]` map lookups.

### 4.2 Generic component access

```go
func Get[T any](e Entity) *T                     // nil if absent or dead
func Has[T any](e Entity) bool
func Set[T any](e Entity, v T)                   // adds if missing, overwrites if present
```

Free functions, not methods on Entity. Go forbids generic methods, and free functions compose better with type inference at call sites.

**Replaces.** `gw.C.Health.Get(e)` / `gw.C.Health.HasAll(e)` / `gw.C.Health.Add(e, &v)` — the per-game `C` namespace and its `*ecs.Map1[T]` exposure disappear.

### 4.3 Spawn

```go
func Spawn(w *World, kind KindID, pos Pos, components ...any) Entity
func Despawn(e Entity)
```

Variadic components allow specifying overrides. The kind's registered defaults fill in the rest. Pos is a small inline struct (`type Pos struct{ X, Y float32 }`), not a component, because every entity has one.

**Kind registration** happens once at startup:

```go
mmokit.RegisterKind(w, KindID(KindShip), "Ship",
    mmokit.WithComponents[Health, Shield, PilotName, Equipment, /* ... */](),
    mmokit.WithReplicated[Health, Shield, PilotName](), // wire-replicated to clients
)
```

The `[T...]` generic types both register the components and seed the kind's component-set. No separate per-component `RegisterComponent` step.

### 4.4 Spatial queries

```go
func Nearby(w *World, x, y, r float32) iter.Seq[Entity]
func NearbyWith[T any](w *World, x, y, r float32) iter.Seq[Entity]   // entities with component T
```

Queries return `Entity` values. They can be range'd directly. Crossing cell boundaries within the radius is transparent — the engine consults the local spatial grid plus border replicas.

### 4.5 Send / Handle

```go
// Send routes msg to whichever cell currently owns e (the authoritative cell).
//   Same cell: direct invocation of the registered handler. Synchronous.
//   Different cell: wire-encoded, delivered next tick on the destination cell.
//
// Engine also fans msg out to AoI clients of any Entity-typed field on msg
// (auto-anchors), unless msg implements ServerOnly.
//
// Handler may mutate *msg in place; the broadcast uses the post-handler value
// (so result fields like DealtDamage land in the visual).
func (e Entity) Send(msg any)

// Handle registers the handler for messages of type M. One handler per type,
// world-wide. Handler receives the local Entity for the target plus a pointer
// to the message (so it can fill in result fields).
func Handle[M any](w *World, fn func(target Entity, msg *M))
```

**Sync local, async remote.** `Send` for a local target invokes the handler immediately and returns; for a remote target, queues a wire frame and returns. Cost model matches expectation.

**Auto-anchors.** When the engine fans `msg` out to AoI clients, every `Entity`-typed field of `msg` contributes one anchor. The broadcast reaches every client whose AoI overlaps any anchor's position. If multiple Entity fields refer to the same entity, anchors dedup.

**Server-only opt-out.** A message type implementing `ServerOnly` skips the AoI broadcast and only routes to the handler:

```go
type ServerOnly interface{ serverOnly() }

type KillCredit struct{ Killed Entity; Reward int32 }
func (KillCredit) serverOnly() {}
```

The marker is a method, not a struct field, so it lives in the type system and adds no wire bytes.

### 4.6 Tick callbacks — the default for per-tick work

Most game logic is reactive (responds to `Send`s) or per-entity periodic (regen, AI, status-effect ticks, collision-driven hazards). The framework gives this its own primitive so game code never has to write a `Query[T]` / `range .Iter` loop by default:

```go
// Per-entity tick: framework iterates entities with component T, calls fn
// each tick of the simulation. fn receives a typed Entity (not ecs.Entity)
// and dt; inside fn the game uses Get/Has/Set/Send like everywhere else.
func OnTick[T any](w *World, fn func(e Entity, dt float32))

// Multi-component variant: bundle pointer fields auto-resolved.
//   type ShieldRegenBundle struct{ S *Shield; H *Health }
//   mmokit.OnTickEach[ShieldRegenBundle](w, func(e Entity, b *ShieldRegenBundle, dt float32) {...})
func OnTickEach[B any](w *World, fn func(e Entity, b *B, dt float32))

// World-level tick: runs once per tick, receives nothing entity-shaped.
//   For cross-cutting work like driving a global timer or batched bookkeeping.
func OnWorldTick(w *World, fn func(dt float32))
```

The DeathSystem from §5 becomes:

```go
mmokit.OnTickEach[struct{ H *Health }](w, func(e mmokit.Entity, b *struct{ H *Health }, dt float32) {
    if b.H.Current <= 0 && !b.H.DeathFired {
        b.H.DeathFired = true
        e.Send(Killed{Killer: b.H.LastDamagedBy})
    }
})
```

No `mmokit.Query[T]` field, no `.Iter` loop, no `SystemBase` embedding, no system struct at all unless the game wants one for organizational reasons. The framework owns iteration; the game owns per-entity logic.

**When does the game write a `System` type then?** Only when it needs persistent per-system state (e.g., a deferred-action queue, a frame-to-frame accumulator) that doesn't fit in components. Even then, the system's `Update(dt)` body uses `OnTickEach` internally — it's not iterating ECS directly.

**Escape hatch.** When `OnTickEach` doesn't fit (cross-bundle joins, custom filter logic, performance hot loops where allocation must be controlled), the game can drop to raw ECS:

```go
import "github.com/mlange-42/ark/ecs"
import "github.com/zenion/mmoserver/pkg/query"

// query.Query[Bundle].Iter is still available — explicit choice to use it.
// Direct ecs.Map1[T] access via mmokit.RawWorld(w) — also explicit.
```

`RawWorld(w)` is named to discourage casual use and to make code review trivially flag escape-hatch usage. In a healthy game codebase, ~95% of per-tick work is `OnTickEach`; raw ECS access is rare and concentrated in performance-critical engine-adjacent code.

## 5. Composition example: damage, death, kill credit

End-to-end, with no cross-cell awareness anywhere in game code.

### 5.1 Messages

```go
// Weapon-specific visual — purely "I fired a laser." Carries no damage number;
// damage numbers are carried by the separate Damage broadcast (§5.2).
type FirePulseLaser struct {
    Caster, Target mmokit.Entity
    Slot           uint8
}

// Generic damage event. Every damage source (weapons, DoT ticks, environmental
// hazards) funnels through this. Broadcast to AoI so clients render damage
// numbers floating up from the target. Dealt is filled in by the handler
// before the broadcast goes out.
type Damage struct {
    Amount float32
    Source mmokit.Entity
    Tag    DamageTag
    Dealt  float32                // result, mutated by handler
}

// Lifecycle event — emitted by DeathSystem, not by weapons.
type Killed struct{ Killer mmokit.Entity }

// Cross-cell side effect — server-only routing back to killer's cell.
type KillCredit struct{ Killed mmokit.Entity; Reward int32 }
func (KillCredit) serverOnly() {}
```

### 5.2 Handlers (registered once at startup)

```go
// Weapon handler delegates damage math to Damage. Stays focused on visual.
mmokit.Handle[FirePulseLaser](w, func(target mmokit.Entity, msg *FirePulseLaser) {
    target.Send(Damage{
        Amount: baseDmg[msg.Slot],
        Source: msg.Caster,
        Tag:    TagLaser,
    })
})

// Single canonical damage formula — every source funnels through here.
// Vampirism / armor / status interactions hook in by reading msg / mutating it
// in their own handlers (chained handler support is a future addition; for now
// composition lives inside this one handler).
mmokit.Handle[Damage](w, func(target mmokit.Entity, msg *Damage) {
    h := mmokit.Get[Health](target); if h == nil { return }
    final := msg.Amount
    if s := mmokit.Get[Shield](target); s != nil && s.Current > 0 {
        a := min(final, s.Current); s.Current -= a; final -= a
    }
    h.Current -= final
    h.LastDamagedBy = msg.Source
    msg.Dealt = final
})

mmokit.Handle[Killed](w, func(killed mmokit.Entity, msg *Killed) {
    spawnLootCrate(killed.Pos())
    mmokit.Despawn(killed)
    if msg.Killer.Alive() {
        msg.Killer.Send(KillCredit{Killed: killed, Reward: 100})
    }
})

mmokit.Handle[KillCredit](w, func(killer mmokit.Entity, msg *KillCredit) {
    mmokit.Get[Currency](killer).Flux += msg.Reward
})
```

### 5.3 Death observer — per-entity tick callback

```go
// Per-entity tick: framework iterates entities with Health each tick, calls
// fn. No system struct, no Query field, no .Iter loop.
mmokit.OnTickEach[struct{ H *Health }](w, func(e mmokit.Entity, b *struct{ H *Health }, dt float32) {
    if b.H.Current <= 0 && !b.H.DeathFired {
        b.H.DeathFired = true
        e.Send(Killed{Killer: b.H.LastDamagedBy})
    }
})
```

### 5.4 Caller — the player presses Q

```go
// Client → server input arrives as a Send to the player entity.
mmokit.Handle[PressAbility](w, func(player mmokit.Entity, msg *PressAbility) {
    target := mmokit.EntityByNetID(w, msg.TargetNetID)
    if !target.Alive() { return }
    target.Send(FirePulseLaser{Caster: player, Target: target, Slot: msg.Slot})
})
```

The full path — input through visuals through death through reward — is **eight Sends and four Handlers**. No `isReplica`, no `Bridge.SendAction`, no `HandleCrossCellAction` switch, no manual marshallers, no twin animation enqueues.

## 6. Rationale for the major design calls

Each call below was made during brainstorming after weighing alternatives.

### 6.1 Why `Send` is sync-when-local, async-when-remote

Always-async is more uniform but adds latency to 99% of game code (single-host dev/test). Always-sync is impossible cross-cell. The split matches the cost model — `target.Send(...)` reads as a function call, *because for local targets it is one*. Cross-cell is the exception, signaled by being a wire round-trip.

The risk: a developer writes code assuming sync-style ordering ("I sent damage, target's Health is now reduced") that holds in tests but breaks under cross-cell load. Mitigation: handlers may not assume `Send` has completed; they only assume the message has been delivered to the engine. Result fields on the message are filled in *before* the local handler returns or *during* remote dispatch — both work uniformly if the calling handler reads `*msg.DealtDamage` only after `target.Send(msg)` returns.

### 6.2 Why auto-anchors over explicit anchor declarations

Auto-anchors (every `Entity`-typed field in the message) cover ~95% of cases — the relevant entities are already named in the struct as `Caster`, `Target`, etc. Explicit anchors require a side declaration that drifts from the struct. Auto-anchors are also typed (the compiler sees the `Entity` fields), so refactoring is safe.

For multi-target broadcasts (chain lightning, AoE) the handler does the spatial query and emits one `Send` per target. The broadcasts fan out naturally; no special multi-anchor primitive needed.

### 6.3 Why marker interface for server-only, not separate registration verb

A separate `HandleInternal[T]` requires the call site of `Send` to know which mode applies — but `Send` looks identical at the call site. The policy belongs on the message type, not on the caller. A marker interface (`serverOnly()` method) is type-system-resident and zero-byte-on-wire.

### 6.4 Why one global handler per message type, with in-handler kind dispatch

`Damage` to a Ship interacts with Shield; `Damage` to an Asteroid does not. Two ways:

- **One handler, dispatches by kind internally:** the handler reads `Get[Shield]` (returns nil for asteroids); the if/else-tree is local and inspectable.
- **Per-kind handlers (`Handle[Damage].For[Ship]`):** sugar for the first form, more declarative, but adds a sub-registration concept.

We start with the first. If unconditional handlers grow hairy, the per-kind sugar can land later non-breakingly.

### 6.5 Why hooks for lifecycle, Sends for inter-entity ops

Player connect / disconnect / world-load fire at deterministic moments in the tick (before any input is processed). Sends are queue-and-drain. You cannot have player input arrive before the player entity exists, so spawning must be a hook with timing guarantees.

`mmokit.OnPlayerJoin(w, fn)`, `OnPlayerLeave(w, fn)`, `OnTick(w, fn)` are direct callbacks. The hook body does direct work — usually `Spawn(...)` or `Save(...)`. Hooks are not message types.

### 6.6 Why client input is a Send (not its own primitive)

Client input messages are typed wire payloads routed to a specific entity (the player's). That is exactly what `Send` does. Unifying them means one wire codec, one dispatch model, one handler-registration concept. Auth (the wire frame proves it came from this player) is metadata on the receive context, available to handlers that care:

```go
mmokit.Handle[PressAbility](w, func(player mmokit.Entity, msg *PressAbility) {
    // ctx not shown; engine attaches it. Internal Sends and trusted internal
    // sources are flagged differently from external client Sends.
})
```

Rate limiting and authentication are framework responsibilities, applied uniformly to all Sends entering from the client transport.

### 6.7 Why per-entity tick callbacks are the default, raw ECS is the escape hatch

Two pressures pull in opposite directions:

- Game devs want to write per-entity logic without thinking about iteration mechanics, query construction, or the ECS world. "When this entity has Health, run this code each tick" should be a one-liner.
- Performance-critical loops (collision broad-phase, spatial-grid rebuild, batched physics) need control over allocation, ordering, and bundle resolution that a per-entity callback can't always provide.

`OnTickEach[Bundle]` covers the first case for ~95% of gameplay logic. It costs the framework a function call per entity per tick — measured negligible against the work the handler itself does in any realistic game.

For the 5% case, `pkg/query.Query[T].Iter` and `mmokit.RawWorld(w)` remain reachable. Naming `RawWorld` instead of an innocuous accessor makes escape-hatch usage obvious in code review and grep-able in audits. A healthy game codebase has zero `RawWorld` callers in `internal/game/` and a small handful in deeply engine-adjacent code (collision, replication delta builder, etc.).

This is the same shape as `unsafe.Pointer` in Go's standard library: present, occasionally necessary, but the language defaults push you elsewhere.

## 7. What gets deleted

| Construct | Replaced by | Files affected |
| --- | --- | --- |
| `gw.eng.ECS.Alive(e)` | `e.Alive()` | every game file |
| `gw.NetIDToEntity[id]` | engine-internal; `mmokit.EntityByNetID(w, id)` for explicit lookup | `factory.go`, `system_*.go`, `combat_helpers.go` |
| `gw.C.<Comp>.Get(e)` | `mmokit.Get[Comp](e)` | every system |
| `lock.TargetEntity` + `lock.TargetNetID` pair | `lock.Target Entity` (single field) | `components.go`, `system_targetlock.go`, `system_network.go` |
| `s.isReplica(target)` branches | gone — `Send` routes | `system_ability.go` |
| `Bridge.SendAction` direct callers | gone — game never calls this | `system_ability.go` |
| `MarshalDamageAction` / `UnmarshalDamageAction` (and Status, Mining variants) | reflect codec on typed message struct | `action_codec.go` deleted |
| `HandleCrossCellAction` switch | engine dispatcher driven by `Handle[T]` registry | `game.go` |
| `HandleActionResult` switch | engine — `Send` from handler routes back transparently | `game.go` |
| `SideEffectRegistry` + sideeffect marshaling | symmetric `Send` — message routed to caster's cell | `combat_helpers.go` and related |
| Manual animation enqueue on attacker's cell | engine broadcast via auto-anchors | `system_ability.go` |
| Manual animation enqueue on victim's cell (the bug we fixed) | same broadcast, fans out to both cells | `game.go HandleCrossCellAction` |
| `RegisterKind[Bundle]` with bundle structs | `RegisterKind` with type-list | `entity_kinds.go`, `entity_*.go` |
| `type FooSystem struct{ entities Query[...] }` + `Update(dt)` boilerplate | `mmokit.OnTickEach[Bundle](w, fn)` (§4.6) — system structs only when persistent state requires them | most `system_*.go` |

Net deletion estimate: ~800-1200 lines of plumbing across `internal/game/`, replaced by ~200-300 lines of message types and handlers. The `pkg/universe/` cross-cell action infrastructure simplifies (the dispatcher becomes generic; the `CrossCellAction` and `ActionResult` opaque-payload types collapse into typed wire frames).

## 8. What stays unchanged

- Component shapes (`Health`, `Shield`, `Position`, `MoveTarget`, etc.) and their `net:""` struct tags for client wire replication.
- The simulation tick loop and system-ordering machinery in `pkg/engine/`. Per-entity tick callbacks (§4.6) are sugar over the same loop, registered once at startup.
- Spatial grid implementation (`pkg/spatial/`).
- Cell mesh, border replication, transfer protocol (`pkg/universe/`).
- Persistence layer (`pkg/persist/`).
- Replication system (`pkg/system/replication.go`) — it continues to drive client wire frames; it just consumes auto-replicator bindings derived from the new `RegisterKind` declarations.

Game-side ECS systems (PhysicsSystem, ShipDynamicsSystem, WanderSystem, etc.) **do** change shape — most collapse from a struct-with-Query-field into a single `OnTickEach` registration. The framework still drives them per tick; the boilerplate goes away.

The redesign is scoped to **the game-facing API surface**. The simulation tick, state replication, and cell mesh are unchanged below the API.

## 9. Open questions / deferred

- **Reply pattern.** This spec uses symmetric `Send` for reply flows (handler does `caster.Send(ResultMsg{...})`). A future `Ask[R](target, msg) Future[R]` could be sugar over symmetric Send if call-sites need inline result handling. Not blocking — defer until a real ergonomic complaint emerges.
- **Per-kind handler sugar.** §6.4 — defer until handlers grow hairy.
- **Position-anchored broadcasts.** §3 — rare, defer; spawn an entity if needed.
- **Wire codec edge cases.** Pointer fields, slices of complex types, maps. Initial scope: messages must be flat structs of primitives + `Entity` + nested structs + slices of those. Same constraint as today's `ReflectMarshal` for components. Maps and pointers rejected at registration time with a clear error.
- **Error handling.** Handler panic recovers and logs. Wire decode error drops and logs. Send to dead target drops silently (handler never runs). These are framework defaults; not a user-facing concern.

## 10. Migration plan

This is a structural rewrite of `internal/game/` against the new `pkg/mmokit` surface. Both move together. Approach:

1. **[done — 2026-05-04, branch `feat/mmokit-entity-message-api`]** **Land the new mmokit surface first** (Plan A+B, [docs/superpowers/plans/2026-05-04-mmokit-entity-message-api.md](../plans/2026-05-04-mmokit-entity-message-api.md)). `mmokit.Entity`, `Get/Has/Set`, `Spawn/Despawn`, `Nearby/NearbyWith`, `Handle/Send` (with cross-cell routing through the existing Bridge via a new `ActionTypedMessage` opcode), `OnWorldTick/OnTick[T]/OnTickEach[Bundle]`, `RawWorld` escape hatch. 25 commits, all tests green, integration test proves cross-cell wire round-trip.
2. **[done — 2026-05-04]** **Migrate one verb end-to-end as a proof.** Damage migration shipped in Plan C ([2026-05-04-mmokit-damage-mining-migration.md](../plans/2026-05-04-mmokit-damage-mining-migration.md)). `gw.Damage(caster, target, amount, bonus, slot, abilityType)` replaces the legacy `if isReplica { sendCrossNodeDamage } else { ApplyDamage }` branches; `damageHandler` runs on the authoritative cell via `mmokit.HandleAll`; `DamageAction`/`DamageResult` codec + `ActionDamage` dispatch cases deleted.
3. **Migrate remaining verbs.** Mining: **[done — 2026-05-04, Plan C]**. `gw.MineExtract(caster, asteroid, beam, requested)` replaces `sendCrossNodeMining`; `MiningAction`/`MiningResult` codec + `ActionMining` dispatch deleted; both `system_ability.go` (extract pulse) and `system_mining.go` (continuous beam) callsites migrated. StatusEffect: **[done — 2026-05-04, Plan D]**. `gw.ApplyStatus(caster, target, type, dur, val, slot, abilityType)` replaces `sendCrossNodeStatusEffect`; `StatusEffectAction` codec + `ActionStatusEffect` dispatch deleted; `system_ability.go` `AbilityTypeIonBurn` callsite migrated. **Remaining:** target lock, dock requests, currency transfers — one plan per verb (E onward). Currency transfers in particular have a latent regression: `combat_helpers.go` still emits `gw.SideEffects.Emit(SideEffectCurrency, ...)` on cross-cell kills, but the legacy drain path went through `HandleCrossCellAction` (now removed) — typed-message replacement pending.
4. **Migrate ECS access.** Replace `gw.eng.ECS.Alive`, `gw.C.X.Get`, `gw.NetIDToEntity` mechanically across systems.
5. **[done — 2026-05-04, Plan D]** **Delete the old API surfaces.** `internal/game/action_codec.go` deleted (entire file). `(*GameWorld).HandleCrossCellAction` and `(*GameWorld).HandleActionResult` methods deleted. The `HandleCrossCellAction` / `HandleActionResult` requirements removed from the `pkg/universe.GameWorld` interface and the corresponding `*Stage` no-op default impls deleted. `pkg/universe/cell.go` `MsgCrossCellAction` arm reduced to engine-internal-only (`Stage.HandleEngineAction` for `ActionTypedMessage` from `mmokit.Send`); `MsgActionResult` arm reduced to log-and-drop. `CrossCellAction` and `ActionResult` types continue to exist *internally* in `pkg/universe` as the carrier for `mmokit.Send`'s `ActionTypedMessage` wire frames, but no game code touches them. `SideEffectCollector` / `SideEffectRegistry` types remain wired but currently undrained — typed-message replacement for cross-cell currency rewards pending in the currency-transfer migration plan.
6. **Migrate input handling.** Convert `InputBindings` to be a special case of `Handle[T]` with a "from-client-trust" tag. Delete the parallel input plumbing.

Each step is independent and revertible. Steps 1-2-mining are landed; the remaining verbs, ECS-access sweep, and input migration can each ship as their own plan.

### Foundation deferrals

The Plan A+B final code review (commit `15ffc66`) flagged five issues addressed in-branch (Type.String wire keys, nil-safety in `Send`, race-safety on `tickCallbacks`, NoopBridge logging, dispatcher no-handler logging) and three items deferred to follow-up plans. Plan C resolved (a): `Process.OnStageInit` + `mmokit.HandleAll[M]` / `OnWorldTickAll` / `OnTickAll[T]` / `OnTickEachAll[B]` auto-replay registrations onto stages created by future cell splits. Plan D resolved (b): `pkg/mmokit/tick_integration_test.go::TestOnWorldTickAll_FiresThroughRealLoop` runs `Process.Start(ctx)` in a goroutine and asserts `OnWorldTickAll` callbacks fire through the actual `mergedHooks.PreFlush` wiring at `coordinator.go:2261-2263`. Still deferred: (c) optional `Set`-on-dead-entity debug log.

## 11. Success criteria

- `internal/game/` no longer references `gw.eng.ECS`, `gw.C.<X>`, `gw.NetIDToEntity`, `Bridge.SendAction`, `MarshalXxxAction`, or `isReplica`.
- `action_codec.go`, `HandleCrossCellAction`, `HandleActionResult`, `SideEffectRegistry` deleted.
- The bug class we fixed yesterday (forgetting to broadcast on the target's cell) is structurally impossible — there is no game-code touchpoint where an animation can be omitted from one side.
- A new game implementer reads `pkg/mmokit/doc.go` and can write a working damage / death / loot loop in under 100 lines, without ever encountering the words "cell," "replica," or "bridge."
- Per-tick game logic in `internal/game/` is overwhelmingly written as `OnTickEach[Bundle]` registrations, not custom system structs with `Query[T]` fields. Calls to `mmokit.RawWorld(w)` in `internal/game/` are zero or near-zero, and any that exist carry a comment explaining why the escape hatch was needed.
- Existing 4node-basic example continues to run with the redesigned surface; cross-host migration tests still pass.
