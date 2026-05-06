# Death/Currency Composition + ECS-Access Sweep Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Realize spec §5's typed-message death/loot/currency composition in real game code, fix the Plan-D-flagged cross-cell currency reward regression, and finish the ECS-access mechanical sweep across `internal/game/`.

**Architecture:** Death observer (`OnTickEachAll[Health]`) replaces the imperative death dispatch in `ApplyDamage`; emits typed `Killed{Killer}` to the dying entity's authoritative cell. The `Killed` handler absorbs all `MarkPlayerDeath`/`MarkNPCDeath` logic and routes currency rewards via a separate typed `KillCredit` message (`serverOnly()` marker; cross-cell aware via mmokit). `gw.SideEffects` / `SideEffectRegistry` / `combat_helpers.go` / `side_effects.go` all delete. ECS-access sweep then converts every `gw.eng.ECS.X` / `gw.C.X.Get/Has/HasAll/Add` / `gw.NetIDToEntity[id]` site to the `mmokit.*` facade, propagating `ecs.Entity` → `mmokit.Entity` signatures across `internal/game/`. After the sweep, `gw.C`, `NewComponents`, and `gw.NetIDToEntity` are deleted.

**Tech Stack:** Go 1.24, `pkg/mmokit` facade (Plan A+B), `pkg/universe` server meshing, `OnTickEachAll` (Plan C), `mmokit.HandleAll` typed-message dispatcher (Plan C+D).

**Spec:** `docs/superpowers/specs/2026-05-05-death-currency-composition-design.md`

**Predecessor plans (all on `feat/mmokit-entity-message-api`):**

- `2026-05-04-mmokit-entity-message-api.md` (foundation)
- `2026-05-04-mmokit-damage-mining-migration.md` (Damage + Mining)
- `2026-05-04-mmokit-statuseffect-migration-cleanup.md` (StatusEffect + legacy surface removal)

**Branch:** `feat/mmokit-entity-message-api` (continue on this branch — single ongoing dev branch per the solo-developer convention).

---

## Project memory to apply throughout

- `feedback_no_unnecessary_type_args` — drop generic params Go can infer (`mmokit.HandleAll(p, fn)`, not `mmokit.HandleAll[*Killed](p, fn)`).
- `feedback_no_backward_compat` — change consistently, no shim layers, no aliases.
- `feedback_mmokit_facade_only` — game code uses `mmokit.*`, never `pkg/` subpaths. Add aliases if missing.
- `feedback_logging` — log significant state changes. The `damageHandler` / `mineExtractHandler` / `statusHandler` patterns show what to do.
- `feedback_no_cell_field_shadow` — never name a field `Cell` on a `WorldBase`-embedder.
- IDE diagnostics may be stale — trust `go vet` + `go test` output, not the IDE diagnostic panel.

---

## File structure

**New files (`internal/game/`):**

- `verb_death.go` — `Killed`, `KillCredit` typed messages; `killedHandler`, `killCreditHandler`; `RegisterDeathVerbs(p)`; `(*GameWorld).handlePlayerKilled`, `(*GameWorld).handleNPCKilled` private helpers.
- `verb_death_test.go` — same-cell `Killed` (player kind, NPC kind) + `KillCredit` unit tests.

**New files (`pkg/mmokit/` or `internal/game/`, depending on placement):**

- `internal/game/death_observer_test.go` — death observer + cross-cell handoff-during-death integration tests.
- `pkg/mmokit/integration_killcredit_test.go` — cross-cell `KillCredit` regression-fix smoke test.

**Renamed files:**

- `internal/game/lifecycle.go` → `internal/game/hooks.go` (after pulling `Hooks()`/`Init()`/`Shutdown()`/`postTick` from `game.go`).
- `internal/game/world.go` → `internal/game/gameworld.go`.

**Deleted files:**

- `internal/game/combat_helpers.go` — `ApplyDamage` moves to `verb_damage.go`; rest is gone.
- `internal/game/side_effects.go` — orphan after Killed/KillCredit cutover.
- `pkg/universe/side_effect.go` — orphan after `gw.SideEffects` field deletion.

**Modified files (`internal/component/`):**

- `components.go` — `Health` gains `LastDamagedByNetID uint32` and `DeathFired bool`.

**Modified files (`internal/game/`):**

- `factory.go` — `RegisterDeathVerbs(coord)` + death observer registration; remove `buildSideEffectRegistry(gw)` call.
- `verb_damage.go` — gains `ApplyDamage` from old `combat_helpers.go`; `damageHandler` writes `LastDamagedByNetID` and stops dispatching to `MarkXxxDeath`.
- `gameworld.go` (renamed from `world.go`) — `MarkPlayerDeath` deleted; `PlayerDeath` queue type deleted; `SideEffects` and `sideEffectRegistry` fields deleted; eventually `gw.C` field and `gw.NetIDToEntity` map deleted.
- `hooks.go` (renamed from `lifecycle.go`) — `processDeaths` deleted; gains `Hooks()`/`Init()`/`Shutdown()`/`postTick` from `game.go`.
- `game.go` — trimmed to `NewGameWorld` constructor + state-machine setup.
- `testutil_test.go` — `sideEffectRegistry` setup deleted; `newTestEntity` helper added.
- `verb_*.go`, `entity_*.go`, `system_*.go` (all files) — ECS-access sweep.

**Modified files (`pkg/mmokit/`):**

- `mmokit.go` — re-exports of `SideEffect*` types deleted.

**Modified files (`pkg/universe/`):**

- `stage.go` — possibly the SpatialSystem-fed NetIDToEntity hook becomes unreferenced; clean up if so.

**Modified files (`examples/4node-basic/`):**

- Sweep any `gw.C.X.Get`-style usage (the example shares package conventions with `internal/game`).

---

## Phase 1: Component foundations

### Task 1.1: Add Health.LastDamagedByNetID + DeathFired

**Files:**

- Modify: `internal/component/components.go`
- Verify: tests for any code that constructs Health directly

- [ ] **Step 1: Inspect current Health**

```bash
grep -n "type Health struct" internal/component/components.go
sed -n '23,28p' internal/component/components.go
```

Expected: 4-line struct with `Current float32 net:"f32"`, `Max float32 net:"f32"`.

- [ ] **Step 2: Extend Health**

Edit `internal/component/components.go` Health struct:

```go
// Health represents hit points.
type Health struct {
    Current            float32 `net:"f32"`
    Max                float32 `net:"f32"`
    LastDamagedByNetID uint32  // not replicated; survives cell transfer for kill attribution
    DeathFired         bool    // observer idempotence flag; survives cell transfer
}
```

No `net:` tags on the new fields — server-side only. The reflect-marshal codec (`pkg/universe/reflect_marshal.go`) handles `uint32` and `bool` natively.

- [ ] **Step 3: Verify**

```bash
go vet ./...
go test ./internal/...
```

Existing tests should still pass — Go zero-values the new fields. If any test does `Health{Current: x, Max: y}` positionally, switch it to keyed fields. If any test pattern-matches on Health by `==`, the new fields default to zero so equality survives.

- [ ] **Step 4: Commit**

```bash
git add internal/component/components.go
git commit -m "feat(component): Health.LastDamagedByNetID + DeathFired (transfer-codec serialized)"
```

---

## Phase 2: Killed + KillCredit verbs (handlers exist; no callsites yet)

### Task 2.1: Create verb_death.go with messages, handlers, registration

**Files:**

- Create: `internal/game/verb_death.go`
- Modify: `internal/game/factory.go`

- [ ] **Step 1: Look at the existing verb pattern**

```bash
cat internal/game/verb_damage.go
```

Mirror its structure: typed message, handler that mutates ECS state on the authoritative cell, `RegisterXxxVerb(p)`, `(*GameWorld).VerbHelper(...)` if applicable.

- [ ] **Step 2: Create the file**

Write `internal/game/verb_death.go`:

```go
package game

import (
    gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
    gamecomp "github.com/zenion/mmoserver/internal/component"
    "github.com/zenion/mmoserver/pkg/mmokit"
)

// Killed is dispatched by the death observer when an entity's Health.Current
// drops to zero. The handler runs on the dying entity's authoritative cell
// and is responsible for cleanup (loot, client notification, removal) and
// for forwarding currency rewards back to the killer via KillCredit.
//
// Killer may be the zero Entity if the entity died of unattributed damage
// (environmental, /kill admin command). The handler treats that as a no-op
// for kill-credit purposes.
type Killed struct {
    Killer mmokit.Entity
}

// KillCredit awards a currency drop to the killer. Server-internal — no AoI
// broadcast (the killer's GSE_CURRENCY_UPDATE is enqueued from the handler).
// Cross-cell aware: when the killer is a replica on the dying entity's cell,
// the message routes to the killer's authoritative cell.
type KillCredit struct {
    Currency uint32
    Amount   int64
}

// serverOnly marks KillCredit as a server-internal message — skips AoI broadcast.
func (KillCredit) serverOnly() {}

// killedHandler runs on the dying entity's authoritative cell. Branches on
// kind, sends per-currency KillCredit to the killer, schedules loot, marks
// the entity for removal.
func killedHandler(target mmokit.Entity, msg *Killed) {
    gw := gameWorldOfEntity(target)
    if gw == nil {
        return
    }
    gw.eng.Log.Log(CatCombatKill, "killed: target=%d killer=%d",
        target.NetID(), msg.Killer.NetID())

    if mmokit.Has[gamecomp.PlayerConn](target) {
        gw.handlePlayerKilled(target, msg.Killer)
    } else {
        gw.handleNPCKilled(target, msg.Killer)
    }

    gw.MarkForRemoval(target.Handle())
}

// killCreditHandler runs on the killer's authoritative cell. Credits the
// currency to the player's bank and pushes a CurrencyUpdate event to their
// client.
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

// RegisterDeathVerbs wires the killedHandler and killCreditHandler onto every
// Stage owned by p. Call once at startup (typically from GameSetup).
func RegisterDeathVerbs(p *mmokit.Process) {
    mmokit.HandleAll(p, killedHandler)
    mmokit.HandleAll(p, killCreditHandler)
}

// handlePlayerKilled is the per-kind body for player deaths. Sends the death
// cue to the player's client, captures inventory + equipment as a loot drop,
// and transitions the player session to StateDead.
func (gw *GameWorld) handlePlayerKilled(target mmokit.Entity, killer mmokit.Entity) {
    conn := mmokit.Get[gamecomp.PlayerConn](target)
    if conn == nil {
        return
    }
    connID := conn.ConnID

    gw.ServerEvents().Send(gw.eng.ConnMgr, connID,
        uint32(gamepb.GameServerEventCode_GSE_PLAYER_DIED),
        &gamepb.PlayerDiedMsg{KillerId: killer.NetID()})

    if s := gw.Players.ByConnID(connID); s != nil {
        if s.Username != "" {
            // Clear saved state so respawn places them near the station
            pdata := gw.PlayerDB.GetOrCreate(s.Username)
            pdata.Cargo = nil
            pdata.Equipment = EquipmentSave{}
            pdata.HasSave = false
            gw.PlayerDB.MarkDirty(s.Username)
        }
        gw.Players.Transition(s, StateDead)
    }

    // Capture inventory + equipment for loot crate drop
    pos := mmokit.Get[mmokit.Position](target)
    if pos == nil {
        return
    }
    var items map[uint32]int32

    if inv := mmokit.Get[gamecomp.Inventory](target); inv != nil && !inv.IsEmpty() {
        items = inv.Clear()
    }
    if eq := mmokit.Get[gamecomp.Equipment](target); eq != nil {
        for _, eqID := range []uint32{eq.Weapon1, eq.Weapon2, eq.Shield, eq.Thruster} {
            if eqID != 0 {
                if items == nil {
                    items = make(map[uint32]int32)
                }
                items[eqID] += 1
            }
        }
        eq.Weapon1 = 0
        eq.Weapon2 = 0
        eq.Shield = 0
        eq.Thruster = 0
    }
    if len(items) > 0 {
        mmokit.Enqueue(gw.Queue, PendingLootDrop{X: pos.X, Y: pos.Y, Items: items})
    }
}

// handleNPCKilled is the per-kind body for NPC deaths. Rolls drops; routes
// each currency item via KillCredit (cross-cell-aware); queues the
// non-currency remainder as a loot crate. Currency-only kills produce no
// loot crate.
func (gw *GameWorld) handleNPCKilled(target mmokit.Entity, killer mmokit.Entity) {
    pos := mmokit.Get[mmokit.Position](target)
    kind := mmokit.Get[mmokit.EntityKind](target)
    if pos == nil || kind == nil {
        return
    }
    table, ok := NPCDropTables[kind.Type]
    if !ok {
        return
    }
    items := RollDrops(table)
    if len(items) == 0 {
        return
    }

    // Currency drops route to killer via KillCredit (cross-cell aware).
    for itemID, qty := range items {
        if !item.IsCurrency(itemID) {
            continue
        }
        if killer.Alive() {
            killer.Send(&KillCredit{Currency: itemID, Amount: int64(qty)})
        } else {
            gw.eng.Log.Log(CatEconomyLoot, "currency drop: dropped (no live killer): currency=%d amount=%d", itemID, qty)
        }
        delete(items, itemID)
    }

    // Non-currency items go into a loot crate.
    if len(items) > 0 {
        mmokit.Enqueue(gw.Queue, PendingLootDrop{X: pos.X, Y: pos.Y, Items: items})
    }
}
```

**Important imports note:** the `item` package and `NPCDropTables` / `RollDrops` already exist in `internal/game/`. Add the `item` import as needed:

```go
import "github.com/zenion/mmoserver/internal/item"
```

The `mmokit.Position` and `mmokit.EntityKind` references work if they're exported from mmokit's facade — verify with `grep -n "Position\|EntityKind" pkg/mmokit/components.go pkg/mmokit/mmokit.go`. If they're not aliased, use `gamecomp.Position` / `gamecomp.EntityKind` (the existing internal/component aliases) — adjust the imports accordingly. Match what the `verb_damage.go` / `verb_status.go` files do today.

- [ ] **Step 3: Wire registration in GameSetup**

```bash
grep -n "RegisterDamageVerb\|RegisterStatusVerb" internal/game/factory.go
```

Edit `internal/game/factory.go`, add a sibling line right after `RegisterStatusVerb(coord)`:

```go
RegisterDamageVerb(coord)
RegisterMiningVerb(coord)
RegisterStatusVerb(coord)
RegisterDeathVerbs(coord)
```

- [ ] **Step 4: Verify**

```bash
go vet ./...
go test ./internal/game/...
```

Existing tests must stay green — no callsite uses `Killed` / `KillCredit` yet.

- [ ] **Step 5: Commit**

```bash
git add internal/game/verb_death.go internal/game/factory.go
git commit -m "feat(game): RegisterDeathVerbs (Killed + KillCredit handlers; no callsites yet)"
```

---

### Task 2.2: Same-cell unit tests for Killed + KillCredit

**Files:**

- Create: `internal/game/verb_death_test.go`

- [ ] **Step 1: Look at the existing verb test pattern**

```bash
cat internal/game/verb_status_test.go internal/game/verb_damage_test.go
```

Mirror them. Especially `newTestShip` (in `verb_damage_test.go`) — extend it if needed for `PlayerConn` / `Inventory` / `Equipment` components on player kills, or add a `newTestNPC` for NPC drop logic.

- [ ] **Step 2: Write tests**

```go
// internal/game/verb_death_test.go
package game

import (
    "testing"

    gamecomp "github.com/zenion/mmoserver/internal/component"
    "github.com/zenion/mmoserver/pkg/mmokit"
)

// TestKilled_NPC_NoDropsIsSafe verifies the Killed handler short-circuits
// cleanly when the dying NPC has no drop table entry.
func TestKilled_NPC_NoDropsIsSafe(t *testing.T) {
    gw, _ := newTestGameWorld()
    gw.Stage.SetGameWorld(gw)
    mmokit.Handle(gw.Stage, killedHandler)
    mmokit.Handle(gw.Stage, killCreditHandler)

    // newTestShip already sets NetworkID + Position + Health + Shield.
    // For the NPC path we need EntityKind too — extend or add a helper.
    target := newTestShipWithKind(t, gw, 101, gamecomp.TypeNPC) // see Step 3 helper note
    killer := newTestShip(t, gw, 202, 100, 0)

    targetE := mmokit.EntityByNetID(gw.Stage, target)
    killerE := mmokit.EntityByNetID(gw.Stage, killer)

    targetE.Send(&Killed{Killer: killerE})

    // No panic, no drops, no loot crate enqueued.
    drops := mmokit.Peek[PendingLootDrop](gw.Queue)
    if len(drops) != 0 {
        t.Fatalf("PendingLootDrop queue: got %d, want 0 (no drop table for TypeNPC)", len(drops))
    }
}

// TestKillCredit_SameCell_CreditsCurrency verifies the KillCredit handler
// credits the killer's account and enqueues a CurrencyUpdate event.
func TestKillCredit_SameCell_CreditsCurrency(t *testing.T) {
    gw, _ := newTestGameWorld()
    gw.Stage.SetGameWorld(gw)
    mmokit.Handle(gw.Stage, killCreditHandler)

    // newTestShip alone doesn't set PlayerConn — extend or add helper.
    killer := newTestPlayerShip(t, gw, 303, "alice") // see Step 3 helper note
    killerE := mmokit.EntityByNetID(gw.Stage, killer)

    killerE.Send(&KillCredit{Currency: 1, Amount: 50})

    pdata := gw.PlayerDB.GetOrCreate("alice")
    if got := pdata.GetCurrency(1); got != 50 {
        t.Fatalf("currency balance: got %d, want 50", got)
    }
}
```

- [ ] **Step 3: Add small test helpers if needed**

Extend `verb_damage_test.go`'s helper family in the SAME PR or in `verb_death_test.go`:

```go
// newTestShipWithKind composes on newTestShip and adds an EntityKind component
// (needed by the NPC drop path).
func newTestShipWithKind(t *testing.T, gw *GameWorld, netID uint32, kind uint32) uint32 {
    t.Helper()
    id := newTestShip(t, gw, netID, 100, 0)
    entity := gw.NetIDToEntity[id]
    gw.C.EntityKind.Add(entity, &mmokit.EntityKind{Type: kind})
    return id
}

// newTestPlayerShip composes on newTestShip and adds PlayerConn for the
// kill-credit path. Registers the player session in PlayerDB so the
// killCreditHandler can find it via Players.ByConnID.
func newTestPlayerShip(t *testing.T, gw *GameWorld, netID uint32, username string) uint32 {
    t.Helper()
    connID := uint32(netID) // arbitrary mapping for tests
    id := newTestShip(t, gw, netID, 100, 0)
    entity := gw.NetIDToEntity[id]
    gw.C.PlayerConn.Add(entity, &gamecomp.PlayerConn{ConnID: connID})
    gw.Players.RegisterPlayer(connID, username)
    return id
}
```

NOTE: these helpers use the *current* `gw.C.X.Add` and `gw.NetIDToEntity[]` patterns. After Phase 7's sweep these will be `mmokit.Set` and `mmokit.EntityByNetID(gw.Stage, id)` — the sweep includes test files. Keep the existing pattern in this Phase 2 task; the sweep updates them later.

- [ ] **Step 4: Run + commit**

```bash
go test ./internal/game/ -run "TestKilled\|TestKillCredit" -v
git add internal/game/verb_death_test.go internal/game/verb_damage_test.go
git commit -m "test(game): same-cell Killed + KillCredit unit tests"
```

---

### Task 2.3: Cross-cell smoke test for KillCredit

**Files:**

- Create: `pkg/mmokit/integration_killcredit_test.go`

- [ ] **Step 1: Look at the existing cross-cell integration test pattern**

```bash
cat pkg/mmokit/integration_damage_test.go
```

Mirror it: build two stages connected by `newTwoCellLoopback` (in `pkg/mmokit/testutil_test.go`), run handlers on each, verify `Send` from cell A reaches the handler on cell B.

- [ ] **Step 2: Write the test**

This test is in `pkg/mmokit/` (not `internal/game/`) because `KillCredit` and the routing fabric belong to the framework — but the test message can be a tiny mock if we don't want to import `internal/game` from `pkg/mmokit`. Use the `mmokit_test` package isolation pattern.

```go
// pkg/mmokit/integration_killcredit_test.go
package mmokit_test

import (
    "testing"
    "time"

    "github.com/zenion/mmoserver/pkg/mmokit"
)

// killCreditMsg is a stand-in for internal/game.KillCredit — same shape, but
// declared inside this test package so we don't have to import internal/game
// from pkg/mmokit (a layering violation). Demonstrates the same routing.
type killCreditMsg struct {
    Currency uint32
    Amount   int64
}

func (killCreditMsg) serverOnly() {}

// TestIntegration_KillCredit_CrossCell verifies that a serverOnly typed
// message Send'd to a cross-cell killer routes to the killer's authoritative
// cell — the regression-fix proof for the cross-cell currency-reward path.
func TestIntegration_KillCredit_CrossCell(t *testing.T) {
    cellA, cellB, drain := newTwoCellLoopback(t)
    cellA.SetGameWorld(testWorld{})
    cellB.SetGameWorld(testWorld{})

    var receivedAt string
    var receivedAmount int64
    mmokit.Handle(cellB, func(killer mmokit.Entity, msg *killCreditMsg) {
        receivedAt = "B"
        receivedAmount = msg.Amount
    })

    // Killer's authoritative cell is B. Spawn the killer on B...
    killerNetID := uint32(101)
    spawnTestEntityOn(t, cellB, killerNetID)
    // ... and a replica on A.
    pushBorderReplicaTo(t, cellB, cellA, killerNetID)

    // From cell A's perspective, the killer is a replica. Send routes back to B.
    killerOnA := mmokit.EntityByNetID(cellA, killerNetID)
    killerOnA.Send(&killCreditMsg{Currency: 1, Amount: 50})

    drain(50 * time.Millisecond)

    if receivedAt != "B" || receivedAmount != 50 {
        t.Fatalf("KillCredit didn't route to authoritative cell: at=%q amount=%d", receivedAt, receivedAmount)
    }
}
```

NOTE: `spawnTestEntityOn`, `testWorld`, `newTwoCellLoopback`, `pushBorderReplicaTo` are existing helpers in `pkg/mmokit/testutil_test.go`. Verify their exact signatures with `grep -n "^func" pkg/mmokit/testutil_test.go` before writing the test; adapt to whatever they look like today.

- [ ] **Step 3: Run + commit**

```bash
go test ./pkg/mmokit/ -run TestIntegration_KillCredit_CrossCell -v
git add pkg/mmokit/integration_killcredit_test.go
git commit -m "test(mmokit): cross-cell KillCredit routing integration test"
```

---

## Phase 3: Death observer migration

### Task 3.1: Install death observer + integration test

**Files:**

- Modify: `internal/game/factory.go`
- Create: `internal/game/death_observer_test.go`

- [ ] **Step 1: Wire the death observer in GameSetup**

In `internal/game/factory.go`, add after `RegisterDeathVerbs(coord)`:

```go
// Death observer: fires Killed exactly once per entity per drop-to-zero.
mmokit.OnTickEachAll[deathObserverBundle](coord, deathObserver)
```

Define the bundle and observer in a small section of `verb_death.go`:

```go
// deathObserverBundle is the per-entity bundle the death observer iterates.
type deathObserverBundle struct {
    H *gamecomp.Health
}

// deathObserver fires Killed when Health.Current drops to zero. Idempotent
// via Health.DeathFired so cross-cell handoff during death doesn't double-fire.
// Killer is resolved at fire time from Health.LastDamagedByNetID via the
// stage's NetID index — survives cell transfer.
func deathObserver(e mmokit.Entity, b *deathObserverBundle, _ float32) {
    if b.H.Current > 0 || b.H.DeathFired {
        return
    }
    b.H.DeathFired = true
    killer := mmokit.EntityByNetID(e.Stage(), b.H.LastDamagedByNetID)
    e.Send(&Killed{Killer: killer})
}
```

- [ ] **Step 2: Write the integration test**

```go
// internal/game/death_observer_test.go
package game

import (
    "testing"

    gamecomp "github.com/zenion/mmoserver/internal/component"
    "github.com/zenion/mmoserver/pkg/mmokit"
)

// TestDeathObserver_FiresOnceWhenHealthZero verifies the observer fires
// Killed exactly once when Health.Current crosses to zero, and doesn't
// re-fire on subsequent ticks (DeathFired idempotence).
func TestDeathObserver_FiresOnceWhenHealthZero(t *testing.T) {
    gw, _ := newTestGameWorld()
    gw.Stage.SetGameWorld(gw)

    var killedFires int
    mmokit.Handle(gw.Stage, func(target mmokit.Entity, msg *Killed) {
        killedFires++
    })
    mmokit.OnTickEach[deathObserverBundle](gw.Stage, deathObserver)

    target := newTestShip(t, gw, 101, 100, 0)
    targetE := mmokit.EntityByNetID(gw.Stage, target)
    h := mmokit.Get[gamecomp.Health](targetE)
    if h == nil {
        t.Fatal("Health missing on test ship")
    }
    h.Current = 0
    h.LastDamagedByNetID = 0 // unattributed death

    // Drive 3 ticks; Killed should fire on tick 1 and not refire.
    runTicks(t, gw.Stage, 3)

    if killedFires != 1 {
        t.Fatalf("Killed fired %d times, want exactly 1", killedFires)
    }
    if !h.DeathFired {
        t.Fatal("Health.DeathFired not set after observer fired")
    }
}
```

NOTE: `newTestGameWorld` already exists; it sets up `gw.Stage` etc. `runTicks` lives in `pkg/mmokit/testutil_test.go` — but this test file is in `internal/game`, so verify there's an equivalent helper or call `gw.Stage.TickCallbacks()` directly (the same primitive).

- [ ] **Step 3: Run + commit**

```bash
go test ./internal/game/ -run TestDeathObserver -v
git add internal/game/factory.go internal/game/verb_death.go internal/game/death_observer_test.go
git commit -m "feat(game): install OnTickEach death observer + integration test"
```

---

### Task 3.2: ApplyDamage writes LastDamagedByNetID; remove death dispatch

**Files:**

- Modify: `internal/game/combat_helpers.go`
- Modify: `internal/game/verb_damage.go` (damageHandler signature note)

- [ ] **Step 1: Inspect ApplyDamage**

```bash
sed -n '14,72p' internal/game/combat_helpers.go
```

Find:
- The line `health.Current -= damage`
- The block `if health.Current <= 0 { gw.MarkPlayerDeath(...) or gw.MarkNPCDeath(...) }`

- [ ] **Step 2: Edit ApplyDamage**

Add `h.LastDamagedByNetID = attackerNetID` immediately after `health.Current -= damage`. Delete the entire `if health.Current <= 0 { ... }` block — death is now observer-driven.

After edit, ApplyDamage should look approximately like:

```go
func (gw *GameWorld) ApplyDamage(target ecs.Entity, damage float32, attackerNetID uint32) float32 {
    if !gw.eng.ECS.Alive(target) || !gw.C.Health.HasAll(target) {
        return 0
    }
    if gw.C.Dormant.HasAll(target) {
        return 0
    }

    if gw.C.StatusEffects.HasAll(target) {
        se := gw.C.StatusEffects.Get(target)
        if eff := se.Get(component.StatusFortified); eff != nil {
            damage *= (1.0 - eff.Value)
        }
    }

    health := gw.C.Health.Get(target)
    totalDamage := damage
    shieldAbsorbed := float32(0)

    if gw.C.Shield.HasAll(target) {
        shield := gw.C.Shield.Get(target)
        shield.DamageCooldown = shield.RegenDelay
        if shield.Current > 0 {
            shieldAbsorbed = min(shield.Current, damage)
            shield.Current -= shieldAbsorbed
            damage -= shieldAbsorbed
        }
    }

    health.Current -= damage
    health.LastDamagedByNetID = attackerNetID

    targetNetID := uint32(0)
    if gw.C.NetworkID.HasAll(target) {
        targetNetID = gw.C.NetworkID.Get(target).ID
    }
    gw.eng.Log.Log(CatCombatHit, "hit: attacker=%d -> target=%d damage=%.1f (shield=%.1f) hp=%.1f/%.1f",
        attackerNetID, targetNetID, totalDamage, shieldAbsorbed, health.Current, health.Max)

    return totalDamage
}
```

The death-dispatch block is **gone**. The `CatCombatKill` log line is also gone — the killed handler logs its own message.

- [ ] **Step 3: Verify build**

```bash
go vet ./internal/game/...
```

Note: `internal/game/commands/kill.go` (the `/kill` admin command) calls `gw.MarkPlayerDeath(target.Online.Entity, 0)`. After this step `MarkPlayerDeath` STILL EXISTS (Task 3.3 deletes it), so the build is green here.

- [ ] **Step 4: Run + commit**

```bash
go test ./internal/game/...
git add internal/game/combat_helpers.go
git commit -m "refactor(game): ApplyDamage writes LastDamagedByNetID; death dispatch moves to observer"
```

The commit may produce test failures if any test assumed `ApplyDamage` triggered death — investigate and update if so. Search with `grep -rn "ApplyDamage" internal/game/`.

---

### Task 3.3: Delete MarkPlayerDeath / MarkNPCDeath; update /kill admin command

**Files:**

- Modify: `internal/game/world.go` (delete `MarkPlayerDeath`)
- Modify: `internal/game/combat_helpers.go` (delete `MarkNPCDeath`)
- Modify: `internal/game/commands/kill.go` (replace `MarkPlayerDeath` call)

- [ ] **Step 1: Find callers**

```bash
grep -rn "MarkPlayerDeath\|MarkNPCDeath" internal/ examples/ cmd/
```

Expected:
- `world.go` defines `MarkPlayerDeath`
- `combat_helpers.go` defines `MarkNPCDeath`
- `commands/kill.go:41` calls `gw.MarkPlayerDeath(target.Online.Entity, 0)`

That's it after Task 3.2 (ApplyDamage no longer calls them).

- [ ] **Step 2: Update /kill command**

In `internal/game/commands/kill.go`, replace the call:

```go
// Before:
gw.MarkPlayerDeath(target.Online.Entity, 0)
```

With direct Health mutation (the observer takes over from there):

```go
// After: zero the player's Health; the death observer will fire Killed next tick.
h := mmokit.Get[gamecomp.Health](mmokit.EntityFromECS(gw.Stage, target.Online.Entity))
if h != nil {
    h.Current = 0
    h.LastDamagedByNetID = 0 // admin-killed: unattributed
}
```

If the existing imports don't include `mmokit` and `gamecomp`, add them. Match the existing import style.

- [ ] **Step 3: Delete MarkPlayerDeath**

In `internal/game/world.go`, delete the entire `MarkPlayerDeath` function (around lines 214-275 per earlier inspection). Also delete the `PlayerDeath` type (around lines 13-19) and any other now-unreferenced exports — verify with grep before deleting:

```bash
grep -rn "PlayerDeath\b" internal/ examples/ cmd/
```

If only the queue type and its now-deleted enqueue site reference it, delete the type too.

- [ ] **Step 4: Delete MarkNPCDeath**

In `internal/game/combat_helpers.go`, delete the entire `MarkNPCDeath` function. Don't delete `rewardCurrency` / `RewardCurrencyToLocal` yet — Phase 4 does that.

- [ ] **Step 5: Delete processDeaths**

In `internal/game/lifecycle.go`, delete the `processDeaths` function entirely. Then in `internal/game/game.go`, delete the call to `gw.processDeaths()` from the PreFlush hook body (around line 178). The hook body becomes:

```go
PreFlush: func() {
    gw.processDockCompletions()
},
```

- [ ] **Step 6: Verify**

```bash
go vet ./...
go test ./internal/...
```

The build must be green. Tests that exercised `MarkPlayerDeath` directly may need updating — search:

```bash
grep -rn "MarkPlayerDeath\|MarkNPCDeath\|processDeaths" internal/
```

Should return zero hits in non-test code.

- [ ] **Step 7: Commit**

```bash
git add internal/game/world.go internal/game/combat_helpers.go internal/game/lifecycle.go internal/game/game.go internal/game/commands/kill.go
git commit -m "refactor(game): delete MarkXxxDeath methods + processDeaths hook (logic moved to Killed handler)"
```

---

### Task 3.4: Cross-cell handoff-during-death test

**Files:**

- Create: `internal/game/death_observer_test.go` (extend with new test)

- [ ] **Step 1: Find an existing cross-cell test scaffold**

```bash
grep -rn "newTwoCellLoopback\|TwoCell" internal/game/ pkg/mmokit/testutil_test.go
```

If `newTwoCellLoopback` is in `pkg/mmokit/testutil_test.go` only, you'll need to either:

- Move it (or a near-copy) to `internal/game/testutil_test.go`
- Or write the test as a `pkg/mmokit/`-style integration test using the killCreditMsg pattern from Task 2.3

Pick whichever requires less new test infrastructure.

- [ ] **Step 2: Write the test**

The test must demonstrate: spawn an entity on cell A with Health.Current already at zero and DeathFired = true. Transfer to cell B. Run a tick on B. Verify the observer DOES NOT fire (DeathFired survived transfer).

Pseudocode:

```go
func TestDeathObserver_DeathFiredSurvivesTransfer(t *testing.T) {
    cellA, cellB, drain := /* two-cell setup */

    // ...wire observer + Killed handler on both cells.

    var killedFires int
    /* register Killed handler on both cells, increment killedFires */

    // Spawn pre-killed entity on A: Health{Current:0, DeathFired:true}.
    netID := spawnPreKilledEntity(t, cellA, 101)

    // First tick on A: observer should already see DeathFired=true and skip.
    runTicks(t, cellA, 1)

    // Transfer A → B.
    transferEntity(t, cellA, cellB, netID)

    // Tick on B: observer must still skip (DeathFired survived transfer).
    runTicks(t, cellB, 3)

    if killedFires != 0 {
        t.Fatalf("Killed fired %d times after transfer; expected 0 (DeathFired must survive)", killedFires)
    }
}
```

Implementation details (`spawnPreKilledEntity`, `transferEntity`) depend on the test scaffolding available — write them as small helpers in the test file. The transfer codec serializes Health via reflect-marshal; verify by inspection that `DeathFired bool` round-trips.

If the existing test scaffolding doesn't support entity transfer end-to-end, the alternative is a marshal-roundtrip unit test:

```go
func TestHealth_DeathFired_SurvivesTransferCodec(t *testing.T) {
    in := gamecomp.Health{Current: 0, Max: 100, LastDamagedByNetID: 42, DeathFired: true}
    data := pkguniverse.ReflectMarshal(&in)
    var out gamecomp.Health
    pkguniverse.ReflectUnmarshal(data, &out)
    if out != in {
        t.Fatalf("Health roundtrip: got %+v, want %+v", out, in)
    }
}
```

This is a strict subset (it doesn't exercise the observer behavior across stages) but proves the codec carries the field. Pick whichever option fits the existing test infrastructure.

- [ ] **Step 3: Run + commit**

```bash
go test ./internal/game/ -run "TestDeathObserver\|TestHealth_DeathFired" -v
git add internal/game/death_observer_test.go
git commit -m "test(game): cross-cell handoff during death (DeathFired survives transfer)"
```

---

## Phase 4: Currency cutover (regression fix)

### Task 4.1: Replace SideEffects.Emit with KillCredit Send

**Files:**

- Modify: `internal/game/combat_helpers.go` (delete `rewardCurrency` + the `MarkNPCDeath` body's currency loop)
- Already done in Task 2.1: `handleNPCKilled` uses `killer.Send(&KillCredit{...})`

- [ ] **Step 1: Verify the new path is wired**

```bash
grep -n "killer.Send" internal/game/verb_death.go
grep -n "SideEffects.Emit\|rewardCurrency" internal/game/
```

The first should hit `handleNPCKilled`. The second should still hit `combat_helpers.go::rewardCurrency` (we delete it next).

- [ ] **Step 2: Delete rewardCurrency, RewardCurrencyToLocal, SideEffectCurrency, marshallers**

In `internal/game/combat_helpers.go`, delete:

- `func (gw *GameWorld) rewardCurrency(...)` (around line 162)
- `func (gw *GameWorld) RewardCurrencyToLocal(...)` (around line 109)
- `const SideEffectCurrency mmokit.SideEffectType = 1` (around line 142)
- `func MarshalCurrencyReward(...)` (around line 145)
- `func UnmarshalCurrencyReward(...)` (around line 153)

- [ ] **Step 3: Delete side_effects.go**

```bash
git rm internal/game/side_effects.go
```

That file's content was `buildSideEffectRegistry(gw)` — orphan after currency cutover.

- [ ] **Step 4: Remove buildSideEffectRegistry call from factory.go**

In `internal/game/factory.go`, remove the line `gw.sideEffectRegistry = buildSideEffectRegistry(gw)` from `WorldFactory`.

- [ ] **Step 5: Verify build**

```bash
go vet ./...
```

Expected errors at this stage: `gw.sideEffectRegistry` field access is now unreferenced (still defined on the GameWorld struct in `world.go`), and the `mmokit.SideEffectType` import is gone — clean those imports up. `internal/game/testutil_test.go:46` also calls `buildSideEffectRegistry` — delete that line too.

- [ ] **Step 6: Run tests + commit**

```bash
go test ./pkg/... ./internal/...
git add internal/game/combat_helpers.go internal/game/factory.go internal/game/testutil_test.go
git rm internal/game/side_effects.go
git commit -m "refactor(game): cross-cell currency reward via KillCredit Send (regression fix)"
```

---

### Task 4.2: Cross-cell currency reward integration test

**Files:**

- Create: `internal/game/cross_cell_kill_credit_test.go` (or extend existing integration tests)

This is the **regression-fix proof**.

- [ ] **Step 1: Identify the right scaffold**

Check what's available:

```bash
grep -rn "newTwoCellLoopback\|TestKilled\|TestCrossCell" internal/game/ pkg/mmokit/
```

If `internal/game/` doesn't have a two-cell test scaffold, port one from `pkg/mmokit/testutil_test.go` (or call into it via test-helper export, depending on package boundaries).

- [ ] **Step 2: Write the test**

End-to-end: kill an NPC on cell A whose killer is a player on cell B (replica on A). Verify the killer's currency balance increments on cell B.

```go
func TestCrossCell_KillCredit_DeliversCurrencyToKiller(t *testing.T) {
    cellA, cellB, drain := /* two-cell setup */

    // Wire the death observer + handlers on both cells.
    // (RegisterDeathVerbs + OnTickEachAll typically run via GameSetup;
    // for a unit test, register manually on each Stage.)

    // Spawn an NPC on A (the dying entity), a player on B (the killer),
    // and a replica of B's player on A.
    npcID := spawnTestNPC(t, cellAGameWorld, 101, npcKindWithCurrencyDrop)
    playerID := spawnTestPlayer(t, cellBGameWorld, 202, "alice")
    pushBorderReplicaTo(t, cellB, cellA, playerID)

    // Drop the NPC's Health to zero, attribute to the player.
    npcE := mmokit.EntityByNetID(cellA, npcID)
    h := mmokit.Get[gamecomp.Health](npcE)
    h.Current = 0
    h.LastDamagedByNetID = playerID

    // Drive ticks: observer fires Killed on A; A's handler Sends KillCredit
    // to the (cross-cell) killer; KillCredit lands on B; B's handler credits
    // currency.
    runTicks(t, cellA, 1)
    drain(50 * time.Millisecond)
    runTicks(t, cellB, 1)

    pdata := cellBGameWorld.PlayerDB.GetOrCreate("alice")
    if got := pdata.GetCurrency(/*currencyIDFromDropTable*/); got <= 0 {
        t.Fatal("KillCredit did not deliver currency to killer's authoritative cell — REGRESSION")
    }
}
```

Match drop-table currency IDs to whatever your test NPC kind drops. If the existing drop table doesn't have a currency-only kind, add a minimal test fixture (a `TypeTestNPC` kind in `entity_kinds.go` with a single currency drop).

- [ ] **Step 3: Run + commit**

```bash
go test ./internal/game/ -run TestCrossCell_KillCredit -v
git add internal/game/cross_cell_kill_credit_test.go
git commit -m "test(game): cross-cell KillCredit delivers currency to killer (Plan D regression fix proof)"
```

---

## Phase 5: Universe-side cleanup

### Task 5.1: Delete pkg/universe/side_effect.go + re-exports + GameWorld field

**Files:**

- Delete: `pkg/universe/side_effect.go`
- Modify: `pkg/mmokit/mmokit.go` (delete `SideEffect*` re-exports)
- Modify: `internal/game/world.go` (delete `SideEffects` and `sideEffectRegistry` fields + the field initializer in `NewGameWorld`)

- [ ] **Step 1: Verify no remaining producers**

```bash
grep -rn "SideEffect\|sideEffectRegistry" pkg/ internal/ examples/
```

Should hit only:
- `pkg/universe/side_effect.go` (the file we're about to delete)
- `pkg/mmokit/mmokit.go` (the re-exports we're about to delete)
- `internal/game/world.go` (the field declarations + comment block we're about to delete)
- `internal/game/factory.go` was cleaned in Task 4.1; verify it's clean now

- [ ] **Step 2: Delete the universe file**

```bash
git rm pkg/universe/side_effect.go
```

- [ ] **Step 3: Delete the mmokit re-exports**

In `pkg/mmokit/mmokit.go`, search for and delete every `SideEffect*` re-export — `SideEffect`, `SideEffectType`, `SideEffectCollector`, `SideEffectRegistry`, `MarshalSideEffects`, `UnmarshalSideEffects`. Use:

```bash
grep -n "SideEffect" pkg/mmokit/mmokit.go
```

- [ ] **Step 4: Delete the GameWorld fields**

In `internal/game/world.go`, find and delete the two fields and their (Plan-D-2.5'd) doc comments:

```go
SideEffects *mmokit.SideEffectCollector
sideEffectRegistry *mmokit.SideEffectRegistry
```

In `NewGameWorld` (in `game.go` or `world.go`, depending on file layout), delete the initializer:

```go
SideEffects: &mmokit.SideEffectCollector{},
```

- [ ] **Step 5: Verify build**

```bash
go vet ./...
go test ./pkg/... ./internal/...
```

Expected: clean. If anything still references the deleted types, search and remove (or fix the test).

- [ ] **Step 6: Commit**

```bash
git add pkg/mmokit/mmokit.go internal/game/world.go internal/game/game.go
git rm pkg/universe/side_effect.go
git commit -m "refactor(universe,mmokit): delete SideEffect* surface (no remaining producers)"
```

---

## Phase 6: File reshuffle

### Task 6.1: Delete combat_helpers.go (move ApplyDamage to verb_damage.go)

**Files:**

- Modify: `internal/game/verb_damage.go` (gains `ApplyDamage`)
- Delete: `internal/game/combat_helpers.go`

- [ ] **Step 1: Inspect what's left in combat_helpers.go**

```bash
cat internal/game/combat_helpers.go
```

After Phases 3-4, only `ApplyDamage` should remain. Verify.

- [ ] **Step 2: Move ApplyDamage to verb_damage.go**

Append `ApplyDamage` (and any private helpers it uses) to `internal/game/verb_damage.go`. The file already imports `gamecomp` and `mmokit`. May need to add `component` (for `StatusFortified` constant) and `ecs` if they aren't already present.

- [ ] **Step 3: Delete combat_helpers.go**

```bash
git rm internal/game/combat_helpers.go
```

- [ ] **Step 4: Verify**

```bash
go vet ./...
go test ./internal/...
```

If imports broke, fix them. Run grep for any straggler:

```bash
grep -rn "combat_helpers" internal/ examples/
```

Should be zero hits.

- [ ] **Step 5: Commit**

```bash
git add internal/game/verb_damage.go
git rm internal/game/combat_helpers.go
git commit -m "refactor(game): move ApplyDamage into verb_damage.go (delete combat_helpers.go)"
```

---

### Task 6.2: Rename lifecycle.go → hooks.go; pull Hooks/Init/Shutdown/postTick from game.go

**Files:**

- Rename: `internal/game/lifecycle.go` → `internal/game/hooks.go`
- Modify: `internal/game/game.go` (export Hooks/Init/Shutdown/postTick)
- Modify: `internal/game/hooks.go` (receive them)

- [ ] **Step 1: Inspect game.go for the methods to move**

```bash
grep -n "^func .*GameWorld.*Hooks\|^func .*GameWorld.*Init\|^func .*GameWorld.*Shutdown\|^func .*GameWorld.*postTick" internal/game/game.go
```

Expected: `Hooks()`, `Init()`, `Shutdown()`, `postTick()` all defined in `game.go`.

- [ ] **Step 2: Rename the file**

```bash
git mv internal/game/lifecycle.go internal/game/hooks.go
```

- [ ] **Step 3: Move the methods**

Cut `Hooks()`, `Init()`, `Shutdown()`, `postTick()` from `game.go`. Paste into `hooks.go` (alongside `processDockCompletions`, `processRespawns`, `processUndocks`, `postFlush`, `clearTickState`, `GetNetID`, `hasStation`).

Match imports — `hooks.go` may need imports from the moved methods.

- [ ] **Step 4: Verify build**

```bash
go vet ./...
go test ./internal/...
```

- [ ] **Step 5: Commit**

```bash
git add internal/game/game.go internal/game/hooks.go
git commit -m "refactor(game): rename lifecycle.go → hooks.go; consolidate hook bodies"
```

---

### Task 6.3: Rename world.go → gameworld.go

**Files:**

- Rename: `internal/game/world.go` → `internal/game/gameworld.go`

- [ ] **Step 1: Rename**

```bash
git mv internal/game/world.go internal/game/gameworld.go
```

- [ ] **Step 2: Verify**

```bash
go vet ./...
go test ./internal/...
```

Should be a no-op since it's just a rename.

- [ ] **Step 3: Commit**

```bash
git add internal/game/gameworld.go
git commit -m "refactor(game): rename world.go → gameworld.go (matches the type it defines)"
```

---

## Phase 7: ECS-access full sweep

### Task 7.1: newTestEntity helper + scope inventory

**Files:**

- Modify: `internal/game/testutil_test.go` (add `newTestEntity`)
- Inspect: scope of remaining `gw.eng.ECS.X` / `gw.C.X` / `gw.NetIDToEntity` sites

- [ ] **Step 1: Inventory the sweep scope**

```bash
grep -rn "gw\.eng\.ECS\." internal/game/ examples/4node-basic/ | wc -l
grep -rn "gw\.C\.[A-Z][a-zA-Z]*\.\(Get\|Has\|HasAll\|Add\|Remove\)" internal/game/ examples/4node-basic/ | wc -l
grep -rn "gw\.NetIDToEntity\[" internal/game/ examples/4node-basic/ | wc -l
```

Record per-file counts:

```bash
grep -rln "gw\.eng\.ECS\.\|gw\.C\.\|gw\.NetIDToEntity" internal/game/ examples/4node-basic/ | sort | xargs -I{} sh -c 'echo "$(grep -c "gw\.eng\.ECS\.\|gw\.C\.\|gw\.NetIDToEntity" {}) {}"' | sort -rn
```

This gives a per-file count, hot-files first. Use it to plan commits.

- [ ] **Step 2: Add newTestEntity helper**

In `internal/game/testutil_test.go`, add (or update) a small helper that constructs entities via mmokit for tests:

```go
// newTestEntity is the post-sweep replacement for tests that previously did
// gw.C.X.Add(entity, &val). Spawns an entity registered with the given kind
// on gw.Stage and returns its mmokit.Entity handle. Caller adds extra
// components via mmokit.Set.
func newTestEntity(t *testing.T, gw *GameWorld, kind gamecomp.EntityType, x, y float32) mmokit.Entity {
    t.Helper()
    netID := gw.AllocNetID() // or whatever the test infrastructure uses
    e := mmokit.Spawn(gw.Stage, mmokit.KindID(kind), mmokit.Pos{X: x, Y: y})
    // Verify the spawn registered NetworkID; if not, fall back to manual ECS:
    return e
}
```

NOTE: `mmokit.Spawn` requires the kind to have been registered via `mmokit.RegisterKind`. If the test infrastructure doesn't pre-register, this helper won't work — fall back to the existing `newTestShip`-style helper that uses raw ECS, but route component additions through `mmokit.Set`. The signature of this helper is a judgment call; pick whichever shape makes the post-sweep tests cleanest. You may also choose to keep `newTestShip` as-is (it'll be swept like everything else) and not add `newTestEntity` at all.

- [ ] **Step 3: Verify**

```bash
go vet ./internal/...
go test ./internal/game/...
```

- [ ] **Step 4: Commit**

```bash
git add internal/game/testutil_test.go
git commit -m "test(game): newTestEntity helper for post-sweep test entity construction"
```

---

### Task 7.2: Sweep verb_*.go and entity_*.go

**Files:**

- Modify: `internal/game/verb_damage.go`, `verb_mining.go`, `verb_status.go`, `verb_death.go`
- Modify: `internal/game/entity_*.go` (every file)

- [ ] **Step 1: Sweep verb_*.go**

For each file, replace per the table:

| Pattern | Replacement |
|---|---|
| `gw.eng.ECS.Alive(e)` | `e.Alive()` (where `e` is `mmokit.Entity`) |
| `gw.eng.ECS.RemoveEntity(e)` | `mmokit.Despawn(e)` |
| `gw.C.X.Get(e)` | `mmokit.Get[X](e)` |
| `gw.C.X.HasAll(e)` | `mmokit.Has[X](e)` |
| `gw.C.X.Add(e, &v)` | `mmokit.Set(e, v)` |
| `gw.NetIDToEntity[id]` | `mmokit.EntityByNetID(gw.Stage, id)` |
| `gw.C.NetworkID.Get(e).ID` | `e.NetID()` |

Where a function takes `entity ecs.Entity`, change the parameter to `entity mmokit.Entity`. The caller side updates accordingly.

For raw ECS handle-yielding code (e.g. iterator hooks from `pkg/spatial`), wrap with `mmokit.EntityFromECS(gw.Stage, handle)` to get the typed Entity.

`ApplyDamage` is the trickiest one — its callers pass `ecs.Entity` today. After sweep, the signature becomes `ApplyDamage(target mmokit.Entity, damage float32, attackerNetID uint32) float32`. Update all callers.

- [ ] **Step 2: Sweep entity_*.go**

Same patterns. The `Spawn*` functions today often do raw ECS construction; convert their internal mappers but keep their public signatures stable (returning `ecs.Entity` is fine if the existing callers expect that — change opportunistically when the call chain is cleaner with `mmokit.Entity`).

- [ ] **Step 3: Verify after each file**

```bash
go vet ./internal/...
go test ./internal/game/...
```

If a function's signature change cascades to callers in other files, follow the chain — but keep the commit narrow (one file's sweep + its immediate callers).

- [ ] **Step 4: Commit per-file or per-group**

Suggested grouping (one commit per group):

```bash
git add internal/game/verb_*.go
git commit -m "refactor(game): ECS-access sweep — verb_*.go (mmokit.Get/Has/Set/Despawn)"

git add internal/game/entity_*.go
git commit -m "refactor(game): ECS-access sweep — entity_*.go"
```

---

### Task 7.3: Sweep system_*.go (split as needed)

**Files:**

- Modify: every `internal/game/system_*.go`

- [ ] **Step 1: Run inventory for systems**

```bash
ls internal/game/system_*.go
grep -c "gw\.eng\.ECS\.\|gw\.C\.\|gw\.NetIDToEntity" internal/game/system_*.go | sort -t: -k2 -rn
```

Hot files first. `system_network.go`, `system_spatial.go`, `system_targetlock.go` likely top the list.

- [ ] **Step 2: Sweep file-by-file**

Same replacement table as Task 7.2. Watch for:

- **Query loops**: many systems use `mmokit.Query[T]` already. If not, the sweep is your chance to convert them. Don't over-convert — only touch what's necessary for the access-pattern sweep.
- **Spatial iterator hooks**: where `system_spatial.go` calls callbacks with `ecs.Entity`, the callback signatures stay typed to `ecs.Entity` and the system internals wrap with `EntityFromECS` at the boundary.
- **`gw.C.NetworkID.Get(e).ID`** → `e.NetID()` saves a noticeable amount of boilerplate.
- Don't break tests — run `go test ./internal/game/...` after each system file.

- [ ] **Step 3: Commit per-file or per-cluster**

```bash
git add internal/game/system_network.go
git commit -m "refactor(game): ECS-access sweep — system_network.go"

git add internal/game/system_spatial.go internal/game/system_targetlock.go
git commit -m "refactor(game): ECS-access sweep — system_spatial.go + system_targetlock.go"

# Continue for each system_*.go
```

5-8 commits depending on how files split.

---

### Task 7.4: Sweep gameworld.go, hooks.go, game.go, factory.go, testutil_test.go

**Files:**

- Modify: `internal/game/gameworld.go`, `hooks.go`, `game.go`, `factory.go`, `testutil_test.go`

- [ ] **Step 1: Sweep helpers + containers**

Same replacement table. By this point most callers already use `mmokit.Entity`; helpers should follow.

- [ ] **Step 2: Test infra**

`testutil_test.go` and the verb-test files have the most `gw.C.X.Add(...)` patterns. Convert to `mmokit.Set(e, v)`. If a test relies on the raw `gw.C.X.Add(entity, &val)` shape because `mmokit.Set` doesn't quite match (e.g. the entity wasn't spawned via mmokit.RegisterKind), use a small `newTestEntity` helper that takes care of both spawning and component installation.

- [ ] **Step 3: Verify + commit**

```bash
go vet ./...
go test ./pkg/... ./internal/...
git add internal/game/gameworld.go internal/game/hooks.go internal/game/game.go internal/game/factory.go internal/game/testutil_test.go
git commit -m "refactor(game): ECS-access sweep — helpers + containers + tests"
```

---

### Task 7.5: Sweep examples/4node-basic

**Files:**

- Modify: `examples/4node-basic/*.go`

- [ ] **Step 1: Inventory**

```bash
grep -rn "gw\.C\.\|gw\.eng\.ECS\.\|gw\.NetIDToEntity" examples/4node-basic/
```

If the example shares idioms with internal/game, the same patterns apply. If it has its own `World` type with different field names, adapt the patterns.

- [ ] **Step 2: Sweep + verify**

```bash
go build ./examples/4node-basic/...
```

(Or `go vet`. Don't write the binary into the repo root — `go vet` is sufficient.)

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/
git commit -m "refactor(example): ECS-access sweep — 4node-basic"
```

---

### Task 7.6: Delete gw.C, NewComponents, gw.NetIDToEntity + SpatialSystem hook

**Files:**

- Modify: `internal/game/gameworld.go` (delete `C *Components` field)
- Modify: `internal/game/components.go` (delete `Components` struct + `NewComponents`)
- Modify: `internal/game/factory.go` (delete the SpatialSystem `OnEntity` hook that populates `NetIDToEntity`)

- [ ] **Step 1: Verify the sweep is complete**

```bash
grep -rn "gw\.C\." internal/game/ examples/4node-basic/
grep -rn "gw\.NetIDToEntity" internal/game/ examples/4node-basic/
grep -rn "gw\.eng\.ECS\." internal/game/ examples/4node-basic/
```

All three should return ZERO hits in non-test code, and ideally zero hits in test code too. If any survive, sweep them now.

- [ ] **Step 2: Delete the field**

In `internal/game/gameworld.go`, delete the `C *Components` field from the GameWorld struct. In `NewGameWorld` (in `game.go` or wherever it lives), delete the `gw.C = NewComponents(ecsWorld)` line.

- [ ] **Step 3: Delete Components struct + NewComponents**

In `internal/game/components.go`:

```bash
git rm internal/game/components.go
```

(If `components.go` contains anything OTHER than the `Components` struct + `NewComponents`, move that elsewhere first.)

- [ ] **Step 4: Delete NetIDToEntity field + the SpatialSystem hook**

In `internal/game/gameworld.go`, delete the `NetIDToEntity map[uint32]ecs.Entity` field. In `NewGameWorld`, delete the `gw.NetIDToEntity = make(map[uint32]ecs.Entity)` line.

In `internal/game/factory.go`, delete the SpatialSystem hook that populates `NetIDToEntity`:

```go
// Before (factory.go around line 60-67):
coord.AddSystem(mmokit.NewSpatialSystemWith(func(gw *GameWorld) mmokit.SpatialHooks {
    return mmokit.SpatialHooks{
        PreTick: func() { clear(gw.NetIDToEntity) },
        OnEntity: func(entity ecs.Entity, _ mmokit.SpatialEntry) {
            gw.NetIDToEntity[gw.C.NetworkID.Get(entity).ID] = entity
        },
    }
}))
```

Replace with:

```go
coord.AddSystem(mmokit.NewSpatialSystem())
```

(The Stage's NetID index is the source of truth; `mmokit.EntityByNetID` consults it directly.)

- [ ] **Step 5: Verify**

```bash
go vet ./...
go test ./pkg/... ./internal/...
```

If anything fails, fix it before committing. Common pitfalls:

- A test still uses `gw.C.X.Add(...)` — convert to `mmokit.Set`.
- A function signature still takes `ecs.Entity` from a caller that now passes `mmokit.Entity` — convert at the boundary with `e.Handle()` or change the signature.

- [ ] **Step 6: Commit**

```bash
git add internal/game/gameworld.go internal/game/factory.go
git rm internal/game/components.go
git commit -m "refactor(game): delete gw.C / NewComponents / gw.NetIDToEntity + SpatialSystem hook"
```

---

## Phase 8: Closeout

### Task 8.1: Full verification + spec update + final report

- [ ] **Step 1: Run the full suite**

```bash
go vet ./...
go test ./pkg/... ./internal/...
```

All must be green.

- [ ] **Step 2: Smoke-build the example**

```bash
mkdir -p /tmp/mmo-build
go build -o /tmp/mmo-build/4node-basic ./examples/4node-basic/
rm -rf /tmp/mmo-build
```

Should compile cleanly.

- [ ] **Step 3: Update the spec migration plan**

In `docs/superpowers/specs/2026-05-03-entity-message-passing-design.md`:

§10 step 4 is currently `**Migrate ECS access.** Replace gw.eng.ECS.Alive, gw.C.X.Get, gw.NetIDToEntity mechanically across systems.` Mark as done:

```diff
-4. **Migrate ECS access.** Replace `gw.eng.ECS.Alive`, `gw.C.X.Get`, `gw.NetIDToEntity` mechanically across systems.
+4. **[done — 2026-05-05, Plan E]** **Migrate ECS access.** All `gw.eng.ECS.X`, `gw.C.X.Get`, `gw.NetIDToEntity` sites swept; `gw.C` and `gw.NetIDToEntity` deleted. Function signatures across `internal/game/` propagated from `ecs.Entity` to `mmokit.Entity`.
```

§10 step 3 — append a Plan-E entry noting Currency (and the regression fix):

```diff
-StatusEffect: **[done — 2026-05-04, Plan D]**. ... **Remaining:** target lock, dock requests, currency transfers — one plan per verb (E onward).
+StatusEffect: **[done — 2026-05-04, Plan D]**. Currency transfer (kill rewards): **[done — 2026-05-05, Plan E]**. KillCredit typed message replaces the SideEffect-emit-into-ActionResult-drain path; the Plan-D-flagged regression is closed. **Remaining:** target lock, dock requests — one plan per verb (F onward).
```

§10 step 5 — extend the existing Plan D entry to include the now-fully-deleted SideEffect surface:

```diff
-`SideEffectCollector` / `SideEffectRegistry` types remain wired but currently undrained — typed-message replacement for cross-cell currency rewards pending in the currency-transfer migration plan.
+`SideEffectCollector` / `SideEffectRegistry` types and `pkg/universe/side_effect.go` deleted by Plan E (2026-05-05) along with the cross-cell currency-reward typed-message replacement.
```

§5 (composition example) — update the prose at the bottom (currently "The full path — input through visuals through death through reward — is **eight Sends and four Handlers**...") to note the example is realized in `internal/game/verb_death.go`.

```bash
git add docs/superpowers/specs/2026-05-03-entity-message-passing-design.md
git commit -m "docs(spec): mark Plan E (death/currency composition + ECS sweep) landed"
```

- [ ] **Step 4: Final report**

Summarize:

- Phase 1: Health gains LastDamagedByNetID + DeathFired (transfer-codec serialized).
- Phase 2: Killed + KillCredit typed messages + handlers; same-cell unit tests; cross-cell smoke test.
- Phase 3: Death observer migration; ApplyDamage no longer dispatches death; MarkPlayerDeath / MarkNPCDeath / processDeaths deleted.
- Phase 4: Currency cutover; rewardCurrency / RewardCurrencyToLocal / SideEffectCurrency / marshallers / side_effects.go deleted; cross-cell regression-fix proof test landed.
- Phase 5: Universe-side cleanup; pkg/universe/side_effect.go deleted; mmokit re-exports gone; gw.SideEffects / sideEffectRegistry fields gone.
- Phase 6: File reshuffle; combat_helpers.go deleted; lifecycle.go → hooks.go (with Hooks/Init/Shutdown/postTick consolidated); world.go → gameworld.go.
- Phase 7: ECS-access full sweep; gw.C, NewComponents, gw.NetIDToEntity all deleted; signatures across internal/game propagated to mmokit.Entity.
- Lines added vs deleted (`git diff --stat main..HEAD | tail -3`).
- Remaining migration work: TargetLock + Dock (Plan F), AoI auto-broadcast (Plan G), Input handling (Plan H).

---

## Out of scope / not in this plan

- **TargetLock + Dock-request migrations.** Plan F.
- **AoI auto-broadcast** for typed messages (spec §4.5). Plan G. The `Killed` handler still manually enqueues `GSE_PLAYER_DIED`; `KillCredit` still manually enqueues `GSE_CURRENCY_UPDATE`. The ergonomic upgrade lands when AoI auto-broadcast does.
- **Input-handler migration** (`OnInput*` → typed `Handle` with from-client-trust marker). Plan H.
- **Splitting up `NewGameWorld`**, system-family reorg, entity-family reorg.
- **Touching `pkg/universe`** beyond the `side_effect.go` deletion + `mmokit` re-export cleanup.

Each follow-up plan is independently revertible and the codebase stays green between plans.

---

## Quick orientation for a fresh agent

If you're picking this plan up cold, here's the state of the world:

- **Branch:** `feat/mmokit-entity-message-api` (continue on this branch — single ongoing dev branch per the user's solo-developer convention).
- **Latest commit:** `fb3263b` (spec doc for this plan).
- **What's already done:**
  - mmokit foundation: `Entity`, `Get/Has/Set`, `Spawn/Despawn`, `Nearby/NearbyWith`, `Send`/`Handle[M]`, `OnTickEach[B]`, `RawWorld`.
  - Process-level wrappers: `OnStageInit`, `HandleAll[M]`, `OnWorldTickAll`, `OnTickAll[T]`, `OnTickEachAll[B]`.
  - Verb migrations: Damage, Mining, StatusEffect — typed message + `gw.Verb(...)` helper. The patterns to mirror are in `verb_damage.go`, `verb_mining.go`, `verb_status.go`.
  - Plan D removed the entire legacy CrossCellAction game surface.
- **What you'll need to read first:**
  - `internal/game/verb_damage.go` — pattern to mirror for `verb_death.go`.
  - `internal/game/verb_status.go` — pattern to mirror for `Killed` (target-side handler).
  - `internal/game/lifecycle.go::processDeaths` — the imperative path being replaced.
  - `internal/game/world.go::MarkPlayerDeath` and `internal/game/combat_helpers.go::MarkNPCDeath` — logic being absorbed into `Killed` handler.
  - `internal/game/combat_helpers.go::rewardCurrency` and `RewardCurrencyToLocal` — both replaced by `KillCredit` handler.
  - `pkg/universe/reflect_marshal.go` — to understand the constraint that drove `LastDamagedByNetID uint32` (instead of `mmokit.Entity`) on the Health component.

The plan is concrete; mirror the patterns and don't over-think it. If anything is genuinely ambiguous after exploration, make the reasonable choice and report DONE_WITH_CONCERNS.
