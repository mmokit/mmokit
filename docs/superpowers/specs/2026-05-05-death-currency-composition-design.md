# Death / Currency Composition + ECS-Access Sweep — Design

**Status:** Approved 2026-05-05.
**Predecessor specs:** `docs/superpowers/specs/2026-05-03-entity-message-passing-design.md` (the foundation; this design realizes its §5 composition example).
**Predecessor plans:** A+B (mmokit foundation), C (Damage + Mining + Process-level wrappers), D (StatusEffect + legacy surface removal). All landed on `feat/mmokit-entity-message-api`.

## 1. Summary

Realize the spec §5 composition pattern in real game code, fix the Plan-D-surfaced cross-cell currency reward regression along the way, and finish the mechanical ECS-access sweep that ties internal/game to the legacy `gw.eng.ECS` / `gw.C.X.Get` / `gw.NetIDToEntity` patterns.

The death / loot / kill-credit chain becomes four typed-message hops with one observer, mirroring spec §5 exactly:

```
ApplyDamage subtracts Health                                     (no death dispatch — just math)
        ↓
OnTickEachAll[struct{ H *Health }] death observer                (per-cell, framework-iterated)
   if H.Current <= 0 && !H.DeathFired:
     H.DeathFired = true
     e.Send(Killed{Killer: H.LastDamagedBy})
        ↓
Handle[Killed] (runs on dying entity's authoritative cell)        (new — verb_death.go)
   - kind branch:
       Player:  send GSE_PLAYER_DIED, transition StateDead, capture inventory+equipment
                as PendingLootDrop
       NPC:     roll drops, separate currency from items,
                enqueue PendingLootDrop for non-currency
   - for each currency drop: msg.Killer.Send(KillCredit{Currency, Amount})
   - MarkForRemoval(e)
        ↓
Handle[KillCredit] (runs on killer's authoritative cell — serverOnly)   (new — verb_death.go)
   - credit currency to player's bank
   - send GSE_CURRENCY_UPDATE to client
```

`KillCredit` implements the `serverOnly()` marker — no AoI broadcast, pure server-internal routing. Cross-cell delivery is automatic via mmokit's `Send` (the killer might be a replica on the dying entity's cell; mmokit routes the message back to the killer's authoritative cell). **This closes the Plan-D-flagged regression where `gw.SideEffects.Emit(SideEffectCurrency, ...)` had no drainer.**

The ECS-access full sweep replaces every `gw.eng.ECS.X` / `gw.C.X.Get/Has/HasAll/Add` / `gw.NetIDToEntity[id]` site across `internal/game/` with the equivalent mmokit facade calls, propagating function signatures from `ecs.Entity` to `mmokit.Entity` along the way. After the sweep: `gw.C` (the typed-mapper cache), `NewComponents`, and `gw.NetIDToEntity` are all deleted.

## 2. Goals

- Realize the spec §5 composition pattern: four messages, one death observer, no per-verb death/loot/credit branches scattered across helpers.
- Fix the cross-cell currency reward regression introduced by Plan C's Damage migration and surfaced in Plan D's review.
- Eliminate `SideEffectCollector` / `SideEffectRegistry` / `side_effects.go` / `combat_helpers.go` / `MarkPlayerDeath` / `MarkNPCDeath` / `processDeaths` / the `PlayerDeath` queue / `gw.SideEffects` field — every legacy death-attribution-and-reward seam in the game.
- Finish the ECS-access mechanical sweep across `internal/game/`: signatures move from `ecs.Entity` to `mmokit.Entity`; `gw.C`, `NewComponents`, and `gw.NetIDToEntity` are deleted.
- Consolidate the file layout so the death / currency / damage code sits in three obvious files (`verb_damage.go`, `verb_death.go`, `hooks.go`) instead of being spread across `combat_helpers.go` / `lifecycle.go` / `world.go` / `side_effects.go`.

## 3. Non-goals

- TargetLock + Dock-request migrations (Plan F).
- AoI auto-broadcast for typed messages (spec §4.5; Plan G). `Killed` and `KillCredit` keep manually enqueueing `GSE_PLAYER_DIED` / `GSE_CURRENCY_UPDATE` for now — the ergonomic upgrade lands when AoI auto-broadcast does.
- Input-handler migration (`OnInput*` → typed `Handle`; Plan H).
- Splitting up the giant `NewGameWorld` constructor; reorganizing the `system_*.go` family; reorganizing the `entity_*.go` family. None of these are required to land this plan.
- Touching `pkg/universe` beyond deleting the now-orphan `side_effect.go` and the `mmokit.go` re-exports.

## 4. The composition

### 4.1 New typed messages (`internal/game/verb_death.go`)

```go
// Killed is dispatched by the death observer when an entity's Health.Current
// drops to zero and the observer hasn't yet fired. The handler runs on the
// dying entity's authoritative cell and is responsible for cleanup (loot,
// client notification, removal) and for forwarding currency rewards back to
// the killer via KillCredit.
//
// Killer may be the zero Entity if the entity died of unattributed damage
// (environmental, /kill admin command). The handler treats that as a no-op
// for kill-credit purposes.
type Killed struct {
    Killer mmokit.Entity
}

// KillCredit awards a currency drop to the killer. Server-internal — no AoI
// broadcast (the killer's GSE_CURRENCY_UPDATE is enqueued from the handler).
// Cross-cell aware via mmokit.Send: when the killer is a replica on the
// dying entity's cell, the message routes to the killer's authoritative cell.
type KillCredit struct {
    Currency uint32
    Amount   int64
}

func (KillCredit) serverOnly() {}
```

`Killer` is an `mmokit.Entity` value, not an `ecs.Entity` — survives cell transfer through the NetID resolution baked into `Entity`.

### 4.2 Health component additions

```go
// internal/component/components.go
type Health struct {
    Current            float32 `net:"f32"`
    Max                float32 `net:"f32"`
    LastDamagedByNetID uint32  // not replicated to clients; serialized in transfer codec
    DeathFired         bool    // observer idempotence flag; serialized in transfer codec
}
```

Both new fields are server-side only (no `net:` tag) but must travel with the entity across cell transfers. Stored as `uint32 NetID` (rather than `mmokit.Entity`) because the reflect-marshal codec used for cell-to-cell transfer skips `ecs.Entity` fields and rejects `mmokit.Entity` (which embeds a `*Stage`); `uint32` is marshaling-friendly and the killer is re-resolved to an `mmokit.Entity` at observer-fire time via `mmokit.EntityByNetID(stage, h.LastDamagedByNetID)`. **Principle (decided in brainstorming):** any state associated with a player must survive cell transfer.

### 4.3 Death observer (registered in `factory.GameSetup`)

```go
RegisterDeathVerbs(coord)

// Single observer fires Killed exactly once per entity per damage drop-to-zero.
mmokit.OnTickEachAll[struct{ H *gamecomp.Health }](coord, func(e mmokit.Entity, b *struct{ H *gamecomp.Health }, _ float32) {
    if b.H.Current > 0 || b.H.DeathFired {
        return
    }
    b.H.DeathFired = true
    e.Send(&Killed{Killer: b.H.LastDamagedBy})
})
```

### 4.4 Killed handler (`internal/game/verb_death.go`)

```go
func killedHandler(target mmokit.Entity, msg *Killed) {
    gw := gameWorldOfEntity(target)
    if gw == nil {
        return
    }
    gw.eng.Log.Log(CatCombatKill, "killed: target=%d killer=%d",
        target.NetID(), msg.Killer.NetID())

    if conn := mmokit.Get[gamecomp.PlayerConn](target); conn != nil {
        // Player kind. Send the death cue, capture loot, transition state.
        gw.handlePlayerKilled(target, conn, msg.Killer)
    } else {
        // NPC kind. Roll drops, route currency through KillCredit, queue
        // non-currency loot crate.
        gw.handleNPCKilled(target, msg.Killer)
    }

    gw.MarkForRemoval(target.Handle())
}
```

`handlePlayerKilled` and `handleNPCKilled` are plain `(*GameWorld)` methods that consolidate today's `MarkPlayerDeath` and `MarkNPCDeath` bodies into the typed-message handler — no cross-cell logic, no SideEffects emit, just the local cleanup work. Loot crate spawn still goes through the deferred `PendingLootDrop` queue (drained in `postFlush`) because spawning during OnTickEach iteration is unsafe.

For each currency drop on an NPC kill:

```go
msg.Killer.Send(&KillCredit{Currency: itemID, Amount: int64(qty)})
```

Cross-cell routing is automatic. No `gw.SideEffects.Emit`. No marshallers.

### 4.5 KillCredit handler

```go
func killCreditHandler(killer mmokit.Entity, msg *KillCredit) {
    gw := gameWorldOfEntity(killer)
    if gw == nil {
        return
    }
    conn := mmokit.Get[gamecomp.PlayerConn](killer)
    if conn == nil {
        return
    }
    s := gw.Players.ByConnID(conn.ConnID)
    if s == nil || s.Username == "" {
        return
    }
    pdata := gw.PlayerDB.GetOrCreate(s.Username)
    pdata.AddCurrency(msg.Currency, msg.Amount)
    gw.PlayerDB.MarkDirty(s.Username)

    gw.eng.Log.Log(CatEconomyLoot, "kill credit: player=%s currency=%d amount=%d balance=%d",
        s.Username, msg.Currency, msg.Amount, pdata.GetCurrency(msg.Currency))

    gw.ServerEvents().Send(gw.eng.ConnMgr, conn.ConnID,
        uint32(gamepb.GameServerEventCode_GSE_CURRENCY_UPDATE),
        &gamepb.CurrencyUpdateMsg{
            CurrencyId: msg.Currency,
            Balance:    pdata.GetCurrency(msg.Currency),
            Earned:     msg.Amount,
        })
}
```

This is the canonical currency-reward path post-Plan-E. The single helper `RewardCurrencyToLocal` no longer exists — its body is inlined here.

### 4.6 ApplyDamage shrinks

```go
// verb_damage.go (after Plan E — ApplyDamage moved here from combat_helpers.go)
func (gw *GameWorld) ApplyDamage(target ecs.Entity, damage float32, attacker mmokit.Entity) float32 {
    e := gw.entityOf(target) // helper that wraps an ecs.Entity into mmokit.Entity
    if !e.Alive() {
        return 0
    }
    h := mmokit.Get[gamecomp.Health](e)
    if h == nil {
        return 0
    }
    if mmokit.Has[gamecomp.Dormant](e) {
        return 0
    }

    // Fortified, shield, damage math (unchanged) ...
    h.Current -= damage
    h.LastDamagedBy = attacker  // <-- the new attribution write

    // No death dispatch here. The death observer takes over.
    return totalDamage
}
```

The `if health.Current <= 0 { gw.MarkXxxDeath(...) }` block is **gone** — superseded by the observer. `damageHandler` (already in `verb_damage.go` from Plan C) calls this and passes its own `msg.Source` as the attacker.

## 5. What gets deleted

### 5.1 Game side

| File / symbol | Disposition |
|---|---|
| `internal/game/combat_helpers.go` (entire file) | Delete. `ApplyDamage` moves to `verb_damage.go`. `MarkNPCDeath`, `rewardCurrency`, `RewardCurrencyToLocal`, `SideEffectCurrency`, `MarshalCurrencyReward`, `UnmarshalCurrencyReward` all gone (logic absorbed into Killed/KillCredit handlers). |
| `internal/game/side_effects.go` (entire file) | Delete. `buildSideEffectRegistry` body and the file itself. |
| `internal/game/lifecycle.go::processDeaths` | Delete. Replaced by `Killed` handler. |
| `internal/game/world.go::MarkPlayerDeath` | Delete. Logic absorbed into `Killed` handler's player branch. |
| `internal/game/world.go::PlayerDeath` queue type | Delete. |
| `internal/game/world.go::SideEffects *mmokit.SideEffectCollector` field | Delete. |
| `internal/game/world.go::sideEffectRegistry` field | Delete. |
| `internal/game/factory.go::buildSideEffectRegistry(gw)` call | Delete. |
| `internal/game/testutil_test.go` setup of `sideEffectRegistry` | Delete. |
| `gw.C` (`*Components`) field on `GameWorld` | Delete after sweep. |
| `gw.NetIDToEntity` field + the SpatialSystem hook that populates it | Delete after sweep. |
| `internal/game/components.go::NewComponents` (the typed-mapper cache builder) | Delete after sweep. |

### 5.2 Universe side

| File / symbol | Disposition |
|---|---|
| `pkg/universe/side_effect.go` (entire file) | Delete. `SideEffectType`, `SideEffect`, `SideEffectCollector`, `SideEffectRegistry`, `MarshalSideEffects`, `UnmarshalSideEffects`. |
| `pkg/mmokit/mmokit.go` re-exports of those types | Delete (per `feedback_no_backward_compat`). |

## 6. File reshuffle

| Action | File | New location |
|---|---|---|
| Delete | `internal/game/combat_helpers.go` | `ApplyDamage` → `verb_damage.go` |
| Delete | `internal/game/side_effects.go` | n/a — orphan |
| New | `internal/game/verb_death.go` | `Killed` + `KillCredit` typed messages, handlers, `RegisterDeathVerbs`, `gw.handlePlayerKilled`, `gw.handleNPCKilled` |
| Rename | `internal/game/lifecycle.go` → `internal/game/hooks.go` | All hook-body content (postFlush, postTick, clearTickState, processRespawns, processUndocks, processDockCompletions, hasStation, GetNetID) collected here. `Hooks()`, `Init()`, `Shutdown()` move in from `game.go`. |
| Trim | `internal/game/game.go` | After moving lifecycle methods out, `game.go` is just the `NewGameWorld` constructor body + the player-state-machine setup. |
| Rename | `internal/game/world.go` → `internal/game/gameworld.go` | The struct it defines is `GameWorld` — file name should match. Avoids visual collision with `pkg/universe/world.go` (the `GameWorld` interface). |

The `verb_*.go` family now owns all four typed verbs:
- `verb_damage.go` — `Damage` + `ApplyDamage`
- `verb_mining.go` — `MineExtract`
- `verb_status.go` — `Status`
- `verb_death.go` — `Killed` + `KillCredit`

## 7. ECS-access mechanical sweep

### 7.1 Replacements

| Pattern | Replacement | Notes |
|---|---|---|
| `gw.eng.ECS.Alive(e)` | `e.Alive()` | `e` is `mmokit.Entity` |
| `gw.eng.ECS.RemoveEntity(e)` | `mmokit.Despawn(e)` | |
| `gw.C.X.Get(e)` | `mmokit.Get[X](e)` | returns `*X` or nil |
| `gw.C.X.HasAll(e)` | `mmokit.Has[X](e)` | |
| `gw.C.X.Add(e, &v)` | `mmokit.Set(e, v)` | value-style, adds-or-overwrites |
| `gw.NetIDToEntity[id]` | `mmokit.EntityByNetID(gw.Stage, id)` | |
| `gw.C.NetworkID.Get(e).ID` | `e.NetID()` | |

### 7.2 Signature propagation

Every helper that currently takes `entity ecs.Entity` becomes `entity mmokit.Entity`. Approximately 40 call sites by initial grep, mostly localized — the chain doesn't fan out wildly because the bulk of game code already operates on the same set of helpers.

A small `gw.entityOf(handle ecs.Entity) mmokit.Entity` helper bridges the gap when game code interfaces with raw ECS-yielding code (e.g. iterator hooks from `pkg/spatial`). It wraps `mmokit.EntityFromHandle(stage, handle)` or equivalent — exact signature pinned in the implementation plan.

### 7.3 Sweep order

1. Add any missing mmokit facade methods discovered during scope exploration.
2. Sweep file-by-file in topological order: leaves first (`combat_helpers.go`, `verb_*.go`, `system_*.go`), then helpers (`world.go`, `lifecycle.go`), then containers (`game.go`, `factory.go`, `testutil_test.go`).
3. After every site is migrated: delete `gw.C` field, `NewComponents`, `gw.NetIDToEntity` map + the SpatialSystem hook that populated it.

### 7.4 Tests

Tests that construct entities via `gw.C.X.Add(...)` need a different path post-sweep — likely a `newTestEntity(gw, kind, ...components)` helper that wraps `mmokit.Spawn(gw.Stage, kind, pos, components...)`. This keeps existing test bodies tight while removing the direct mapper access.

## 8. Phase ordering

1. **Component additions + transfer codec.** Add `Health.LastDamagedBy mmokit.Entity` and `Health.DeathFired bool`. Wire transfer-codec serialization for both. Update tests that construct Health directly.
2. **`Killed` + `KillCredit` typed messages + handlers.** New `verb_death.go`. Register via `RegisterDeathVerbs(p)` in `factory.GameSetup`. Same-cell + cross-cell tests mirroring `verb_damage_test.go` pattern. **No callsite cutover yet** — handlers exist but nothing fires them.
3. **Death observer migration.** Install `OnTickEachAll[Health]` death observer in `factory.GameSetup`. Move `ApplyDamage`'s `if Current ≤ 0` death dispatch out — death is now observer-driven. Delete the two `MarkXxxDeath` methods. Loot/currency logic moves into the `Killed` handler.
4. **Currency cutover.** Replace `gw.SideEffects.Emit(SideEffectCurrency, ...)` (and the local-credit path) with `msg.Killer.Send(KillCredit{...})` in the `Killed` handler. Delete `rewardCurrency` / `RewardCurrencyToLocal` / `MarshalCurrencyReward` / `SideEffectCurrency` / `side_effects.go`. Verify cross-cell currency reward works in a 2-cell test.
5. **Universe-side cleanup.** Delete `pkg/universe/side_effect.go` + the `mmokit` re-exports + `gw.SideEffects` field + the doc-comments Plan D 2.5'd. The Plan D regression closes here.
6. **File reshuffle.** Delete `combat_helpers.go` (move `ApplyDamage` to `verb_damage.go`); rename `lifecycle.go` → `hooks.go` and pull `Hooks()`/`Init()`/`Shutdown()`/`postTick` in from `game.go`; rename `world.go` → `gameworld.go`.
7. **ECS-access full sweep.** Leaves first, helpers next, containers last. 5-8 commits. After every site migrated: delete `gw.C`, `NewComponents`, `gw.NetIDToEntity` + its SpatialSystem hook.
8. **Closeout.** Full `go vet` + `go test`; smoke-build `examples/4node-basic/`; update spec §10 marking step 4 (ECS sweep) as done, and add a Plan-E entry to the migration history; final report.

## 9. Testing strategy

- **Same-cell unit tests for `Killed` handler** (player kind, NPC kind) + `KillCredit` handler — `internal/game/verb_death_test.go`.
- **Same-cell death observer integration test** — spawn entity with Health, drop it to zero, drive ticks, assert `Killed` fired exactly once and not refired across subsequent ticks.
- **Cross-cell `KillCredit` smoke** — same-process two-cell setup; kill an NPC where the killer is a replica on the NPC's cell; assert currency lands on the killer's authoritative cell. **This is the regression-fix proof.**
- **Cross-cell handoff continuity** — kill an entity, hand off mid-death, assert the observer doesn't re-fire on the destination cell (DeathFired survives transfer).
- Existing tests (`pkg/universe`, `pkg/mmokit`, `internal/game`, `internal/marketplace`) must stay green throughout the sweep.

## 10. Open risks and mitigations

- **Send-during-iteration race.** The death observer fires `e.Send(Killed{...})` synchronously during `OnTickEachAll` iteration; the `Killed` handler then runs synchronously (same-cell case) and if it spawned entities (loot crates) directly, that would crash the iteration (per CLAUDE.md: "Never spawn/remove entities during query iteration"). Mitigation: `Killed` enqueues `PendingLootDrop` and lets `postFlush` drain. Same pattern as today.
- **DeathFired survives transfer.** Transfer codec must serialize `DeathFired` as a single byte. Without this, an entity that crosses a cell boundary mid-death would have its observer re-fire on the destination cell, double-spawning loot or double-crediting kills.
- **LastDamagedBy may be stale across transfer.** When an entity transfers cells, `LastDamagedBy.NetID()` is serialized; the destination cell re-resolves it via its own NetID index. If the killer isn't visible on the destination cell (no replica, no border), `Killer.NetID() != 0` but `Killer.Alive() == false` on that cell — which is fine: the `Killed` handler's `if msg.Killer.Alive() { msg.Killer.Send(KillCredit{...}) }` short-circuits. The Send itself is cross-cell aware and can route to the killer's actual authoritative cell even without a local replica, so this is mostly a defensive note rather than a blocker. The implementation plan pins down whether `Killer.Alive()` returns true for a cross-cell-known-but-not-locally-replicated entity; if not, the handler should call `target.Send` unconditionally and let mmokit's routing handle "killer no longer exists" by dropping silently.
- **`gw.C` deletion and tests.** The mass-delete of `gw.C.X.Add` in test setup paths is the riskiest part of the sweep. Mitigation: add a `newTestEntity(gw, kind, ...components)` helper *before* the sweep; convert tests to use it; only then delete `gw.C`.
- **4node-basic example.** May reference `gw.C` directly. The sweep must include the example as well, or the example breaks at the deletion step.

## 11. Success criteria

- Spec §5's composition example exists in real code, with no `isReplica` checks, no `Bridge.SendAction`, no `MarshalXxxAction`.
- `combat_helpers.go`, `side_effects.go`, `pkg/universe/side_effect.go`, `MarkPlayerDeath`, `MarkNPCDeath`, `processDeaths`, `PlayerDeath` queue, `SideEffectCollector`, `SideEffectRegistry`, `gw.SideEffects`, `gw.sideEffectRegistry` are all deleted.
- A cross-cell currency reward integration test passes — proving the Plan D regression is fixed.
- A cross-cell handoff-during-death test passes — proving `DeathFired` and `LastDamagedBy` survive transfer.
- `internal/game/` no longer references `gw.eng.ECS`, `gw.C.<X>`, or `gw.NetIDToEntity`. `gw.C`, `NewComponents`, and `gw.NetIDToEntity` are deleted.
- `verb_*.go` family covers all four typed verbs: `verb_damage.go`, `verb_mining.go`, `verb_status.go`, `verb_death.go`.
- `lifecycle.go` is renamed to `hooks.go` and contains all hook bodies and lifecycle methods.
- Existing 4node-basic example continues to run; cross-host migration tests stay green.
- Full `go vet ./...` clean; `go test ./pkg/... ./internal/...` green.

## 12. Out of scope (carried forward)

- **TargetLock + Dock-request migrations** → Plan F.
- **AoI auto-broadcast (spec §4.5)** → Plan G. After it lands, `Killed`/`KillCredit` handlers' manual `GSE_PLAYER_DIED`/`GSE_CURRENCY_UPDATE` enqueues become declarative.
- **Input-handler migration** (`OnInput*` → typed `Handle` with from-client-trust marker) → Plan H.
- **`NewGameWorld` decomposition**, system-family reorg, entity-family reorg → not currently planned.
