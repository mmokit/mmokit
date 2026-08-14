# StatusEffect Migration + Cross-Cell Action Legacy Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate the last remaining game-defined cross-cell action — `StatusEffect` (DoT) — onto the `mmokit.Send`/`Handle` API. With StatusEffect migrated, every legacy game `ActionType` is gone, allowing a clean removal of the entire `CrossCellAction` game surface: delete `internal/game/action_codec.go`, delete `HandleCrossCellAction` / `HandleActionResult` from both `*GameWorld` and the `pkg/universe.GameWorld` interface, delete the fallback branches in `pkg/universe/cell.go`. Cross-cell actions become engine-internal only (the `ActionTypedMessage` opcode used by `mmokit.Send`). Also lands the tick-callback integration test deferred from the foundation code review.

**Architecture:** Mirrors the Damage/Mining migration pattern from Plan C exactly. Define typed `Status` message in `internal/game/verb_status.go`, register `statusHandler` via `mmokit.HandleAll`, expose `gw.ApplyStatus(target, source, effectType, duration, value, slot, abilityType)` helper. Migrate the `AbilityTypeIonBurn` callsite in `system_ability.go`. Then sweep the legacy surface:

- `internal/game/action_codec.go` becomes empty after deleting `StatusEffectAction` + marshallers + `ActionStatusEffect` const → delete the file.
- `internal/game/game.go` `case ActionStatusEffect:` blocks in both `HandleCrossCellAction` and `HandleActionResult` are gone → both methods become empty → delete them.
- `pkg/universe/world.go` `GameWorld` interface drops `HandleCrossCellAction(*CrossCellAction) *ActionResult` and `HandleActionResult(*ActionResult)` requirements.
- `pkg/universe/cell.go` `MsgCrossCellAction` / `MsgActionResult` arms drop the `c.World.HandleCrossCellAction` / `c.World.HandleActionResult` fallback calls — only `Stage.HandleEngineAction` remains.
- `pkg/universe/stage.go` lines 722-723 default no-op implementations of those methods on `*Stage` are no longer required → delete.

The result: `CrossCellAction`, `ActionResult`, and `ActionType` continue to exist *internally* in `pkg/universe` as the carrier for `mmokit.Send`'s `ActionTypedMessage` wire frames, but no game code touches them. The cross-cell action surface, as a game-visible concept, ceases to exist.

**Tech Stack:** Same as foundation. Builds on `mmokit.HandleAll` and the typed-message dispatcher (Plans A+B+C).

**Spec:** `docs/superpowers/specs/2026-05-03-entity-message-passing-design.md` (read for context if needed; the plan is self-contained otherwise)

**Predecessor plans:**
- `docs/superpowers/plans/2026-05-04-mmokit-entity-message-api.md` (foundation, merged)
- `docs/superpowers/plans/2026-05-04-mmokit-damage-mining-migration.md` (Damage + Mining + Process-level wrappers, merged on the same branch)

**Branch:** `feat/mmokit-entity-message-api` (this plan continues on the same branch — single ongoing development branch per the solo-developer convention).

---

## Project memory to apply throughout

- `feedback_no_unnecessary_type_args` — drop generic params Go can infer (e.g., `mmokit.HandleAll(p, fn)` not `mmokit.HandleAll[StatusMsg](p, fn)`)
- `feedback_no_backward_compat` — change consistently, no shim layers, no aliases for old names
- `feedback_mmokit_facade_only` — game code uses mmokit, never `pkg/` subpaths
- `feedback_logging` — log significant state changes (the existing `damageHandler` and `mineExtractHandler` patterns show what to do)
- IDE diagnostics may be stale — trust `go vet` + `go test` output, not the diagnostic panel

---

## File Structure

**New files (`internal/game/`):**
- `verb_status.go` — `Status` typed message, `statusHandler`, `RegisterStatusVerb(p)`, `(*GameWorld).ApplyStatus(...)`
- `verb_status_test.go` — same-cell `gw.ApplyStatus` flow test

**New files (`pkg/universe/` or `pkg/mmokit/`, depending on placement):**
- `tick_integration_test.go` — integration test that exercises `mmokit.OnWorldTickAll` through a real Process tick loop, verifying `mergedHooks.PreFlush` wiring is intact (foundation review deferred item).

**Modified files (`internal/game/`):**
- `system_ability.go` — replace the `AbilityTypeIonBurn` case (currently branches on `s.isReplica`) with a call to `gw.ApplyStatus(...)`. Delete `sendCrossNodeStatusEffect` helper.
- `factory.go` — add `RegisterStatusVerb(coord)` next to `RegisterDamageVerb(coord)` / `RegisterMiningVerb(coord)` in `GameSetup`.
- `game.go` — delete the `case ActionStatusEffect:` blocks from both `HandleCrossCellAction` (around line 281) and `HandleActionResult` (around line 350). Both methods become empty → delete the methods themselves.
- `cross_cell_combat_test.go` — delete `TestHandleCrossCellAction_StatusEffect_EnqueuesAnimation` and `TestStatusEffectAction_Roundtrip` (both exercise the deleted surface). Lock-visibility regression tests stay.

**Modified files (`pkg/universe/`):**
- `world.go` — remove `HandleCrossCellAction(*CrossCellAction) *ActionResult` and `HandleActionResult(*ActionResult)` from the `GameWorld` interface.
- `cell.go` — in `processMessage` switch, the `MsgCrossCellAction` arm drops the `c.World.HandleCrossCellAction(msg.Action)` fallback (only `c.Stage.HandleEngineAction(msg.Action)` remains; if it returns false, log and drop). The `MsgActionResult` arm drops `c.World.HandleActionResult(msg.ActionResult)` entirely; ActionResult traffic from the legacy round-trip path no longer exists.
- `stage.go` — remove lines 722-723 (the default no-op `Stage.HandleCrossCellAction` and `Stage.HandleActionResult` shims).

**Deleted files:**
- `internal/game/action_codec.go` — entire file. After Damage and Mining migrations, only `StatusEffectAction` remained; this plan removes that too.

---

## Phase 1: StatusEffect verb migration

### Task 1.1: Define the `Status` typed message

**File:** Create `internal/game/verb_status.go`

- [ ] **Step 1: Write the message**

```go
// internal/game/verb_status.go
package game

import (
    gamecomp "github.com/zenion/mmokit/internal/component"
    "github.com/zenion/mmokit/pkg/mmokit"
)

// Status is a typed cross-cell-aware status-effect application message.
// Applied to a target via mmokit.Send; the registered handler runs on
// whichever cell owns the target, adds the effect to the target's
// StatusEffects component, and enqueues the cast animation event for
// viewers near the target.
//
// This is the typed replacement for the legacy ActionStatusEffect /
// StatusEffectAction codec.
type Status struct {
    EffectType  gamecomp.StatusType
    Duration    float32
    Value       float32
    Slot        uint8         // ability slot (for visual)
    AbilityType uint8         // ability type enum (for visual)
    Source      mmokit.Entity // attacker — used for kill-attribution if a DoT kills
}
```

- [ ] **Step 2: Verify**

```bash
go vet ./internal/game/...
```

Should be clean.

---

### Task 1.2: Implement the handler + register it

**File:** Modify `internal/game/verb_status.go`

- [ ] **Step 1: Look at the Damage handler for the pattern**

```bash
cat internal/game/verb_damage.go
```

Mirror its structure: handler that runs on dest cell, mutates ECS state, enqueues `AbilityCastResultMsg` for viewers near the target.

- [ ] **Step 2: Implement the handler**

Append to `verb_status.go`:

```go
import (
    gamepb "github.com/zenion/mmokit/gen/go/gamepb"
)

// statusHandler applies the status effect to the target's StatusEffects
// component. Runs on the authoritative cell. Also enqueues the dest-cell
// AbilityCastResultMsg so viewers near the target see the cast animation.
func statusHandler(target mmokit.Entity, msg *Status) {
    se := mmokit.Get[gamecomp.StatusEffects](target)
    if se == nil {
        return
    }

    // Resolve the source's ECS handle for the StatusEffect.Source field
    // (used for kill-attribution if a DoT kills the target). The source
    // may be a replica on this cell — that's fine; the handle is still a
    // valid local reference for as long as the replica exists.
    se.Add(gamecomp.StatusEffect{
        Type:     msg.EffectType,
        Duration: msg.Duration,
        Value:    msg.Value,
        Source:   msg.Source.Handle(),
    })

    gw := gameWorldOfEntity(target)
    if gw == nil {
        return
    }
    gw.eng.Log.Log(CatCombatAbility, "status applied: source=%d -> target=%d type=%d dur=%.1f val=%.1f",
        msg.Source.NetID(), target.NetID(), msg.EffectType, msg.Duration, msg.Value)

    // Dest-cell animation enqueue. NetworkSystem.afterSend filters by
    // visibility (caster or target visible to the viewer).
    mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{
        Slot:        uint32(msg.Slot),
        Success:     true,
        TargetId:    target.NetID(),
        CasterId:    msg.Source.NetID(),
        AbilityType: uint32(msg.AbilityType),
    })
}
```

`gameWorldOfEntity` is already defined in `verb_damage.go`; reuse it.

- [ ] **Step 3: Add `RegisterStatusVerb` and the `gw.ApplyStatus` helper**

Append:

```go
// RegisterStatusVerb wires statusHandler onto every Stage owned by p.
// Call once at startup (typically from GameSetup).
func RegisterStatusVerb(p *mmokit.Process) {
    mmokit.HandleAll(p, statusHandler)
}

// ApplyStatus is the game-side helper for applying a status effect to
// another entity. Routes via target.Send — same-cell or cross-cell
// transparent. Source-cell animation enqueue happens here for non-local
// targets (so the caster's client sees the cast fire on the same tick).
//
// Identical pattern to gw.Damage / gw.MineExtract.
func (gw *GameWorld) ApplyStatus(caster, target mmokit.Entity, effectType gamecomp.StatusType, duration, value float32, slot, abilityType uint8) {
    if !target.Alive() {
        return
    }

    if !target.Local() {
        mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{
            Slot:        uint32(slot),
            Success:     true,
            TargetId:    target.NetID(),
            CasterId:    caster.NetID(),
            AbilityType: uint32(abilityType),
        })
    }

    target.Send(&Status{
        EffectType:  effectType,
        Duration:    duration,
        Value:       value,
        Slot:        slot,
        AbilityType: abilityType,
        Source:      caster,
    })
}
```

- [ ] **Step 4: Wire registration into GameSetup**

```bash
grep -n "RegisterDamageVerb\|RegisterMiningVerb" internal/game/factory.go
```

Add a sibling call:

```go
RegisterDamageVerb(coord)
RegisterMiningVerb(coord)
RegisterStatusVerb(coord)
```

- [ ] **Step 5: Verify**

```bash
go vet ./...
go test ./internal/game/...
```

Existing tests should remain green — handler is registered but no callsite uses `gw.ApplyStatus` yet.

- [ ] **Step 6: Commit**

```bash
git add internal/game/verb_status.go internal/game/factory.go
git commit -m "feat(game): RegisterStatusVerb + gw.ApplyStatus helper (handler not yet called)"
```

---

### Task 1.3: Migrate the `AbilityTypeIonBurn` callsite

**File:** Modify `internal/game/system_ability.go`

- [ ] **Step 1: Locate the callsite**

```bash
grep -n "AbilityTypeIonBurn\|sendCrossNodeStatusEffect" internal/game/system_ability.go
```

The block lives around line 190 inside the `executeAbility` switch.

- [ ] **Step 2: Replace the case body**

The current block (around `system_ability.go:189-209`):

```go
// --- DoT debuff ---
case item.AbilityTypeIonBurn:
    if gw.eng.ECS.Alive(lock.TargetEntity) {
        if s.isReplica(lock.TargetEntity) {
            s.sendCrossNodeStatusEffect(action.casterNetID, lock.TargetEntity,
                uint8(gamecomp.StatusIonBurn), action.slot, uint8(params.Type),
                params.DotDuration, params.DotDPS)
            sentCrossNode = true
        } else if gw.C.StatusEffects.HasAll(lock.TargetEntity) {
            se := gw.C.StatusEffects.Get(lock.TargetEntity)
            se.Add(gamecomp.StatusEffect{
                Type:     gamecomp.StatusIonBurn,
                Duration: params.DotDuration,
                Value:    params.DotDPS,
                Source:   entity,
            })
        }
        targetNetID = lock.TargetNetID
        gw.eng.Log.Log(CatCombatAbility, "ability %s: %d -> %d (%.1f dps for %.1fs)",
            params.Name, action.casterNetID, lock.TargetNetID, params.DotDPS, params.DotDuration)
    }
```

Replace with:

```go
// --- DoT debuff ---
case item.AbilityTypeIonBurn:
    target := mmokit.EntityByNetID(gw.Stage, lock.TargetNetID)
    if target.Alive() {
        caster := mmokit.EntityByNetID(gw.Stage, action.casterNetID)
        gw.ApplyStatus(caster, target, gamecomp.StatusIonBurn,
            params.DotDuration, params.DotDPS, action.slot, uint8(params.Type))
        targetNetID = lock.TargetNetID
        sentCrossNode = true // gw.ApplyStatus owns animation enqueue; suppress legacy post-block
        gw.eng.Log.Log(CatCombatAbility, "ability %s: %d -> %d (%.1f dps for %.1fs)",
            params.Name, action.casterNetID, lock.TargetNetID, params.DotDPS, params.DotDuration)
    }
```

- [ ] **Step 3: Delete the `sendCrossNodeStatusEffect` helper**

In `system_ability.go`, find:

```go
func (s *AbilitySystem) sendCrossNodeStatusEffect(casterNetID uint32, target ecs.Entity, effectType, slot, abilityType uint8, duration, value float32) {
    ...
}
```

Delete it entirely.

- [ ] **Step 4: Verify build + run tests**

```bash
go vet ./internal/game/...
go test ./internal/game/...
```

Existing tests in `cross_cell_combat_test.go` (`TestHandleCrossCellAction_StatusEffect_EnqueuesAnimation`, `TestStatusEffectAction_Roundtrip`) will FAIL because they exercise the legacy path. Task 1.5 deletes them.

- [ ] **Step 5: Commit (with broken tests pending)**

```bash
git add internal/game/system_ability.go
git commit -m "feat(game): migrate AbilityTypeIonBurn to gw.ApplyStatus"
```

---

### Task 1.4: Same-cell test for `gw.ApplyStatus`

**File:** Create `internal/game/verb_status_test.go`

- [ ] **Step 1: Look at the Damage test for the pattern**

```bash
cat internal/game/verb_damage_test.go
```

Mirror it.

- [ ] **Step 2: Write the test**

```go
// internal/game/verb_status_test.go
package game

import (
    "testing"

    gamecomp "github.com/zenion/mmokit/internal/component"
    "github.com/zenion/mmokit/pkg/mmokit"
)

func TestApplyStatus_SameCell_AddsEffect(t *testing.T) {
    gw, _ := newTestGameWorld()
    gw.Stage.SetGameWorld(gw)
    mmokit.Handle(gw.Stage, statusHandler)

    target := newTestShip(t, gw, 100, 0)
    caster := newTestShip(t, gw, 100, 0)

    casterE := mmokit.EntityByNetID(gw.Stage, caster)
    targetE := mmokit.EntityByNetID(gw.Stage, target)

    gw.ApplyStatus(casterE, targetE, gamecomp.StatusIonBurn, 5.0, 3.0, 0, 1)

    se := mmokit.Get[gamecomp.StatusEffects](targetE)
    if se == nil {
        t.Fatal("StatusEffects component missing on target")
    }
    if !se.Has(gamecomp.StatusIonBurn) {
        t.Fatal("StatusIonBurn not present after ApplyStatus")
    }
    eff := se.Get(gamecomp.StatusIonBurn)
    if eff.Duration != 5.0 || eff.Value != 3.0 {
        t.Fatalf("effect duration=%v value=%v, want 5.0 / 3.0", eff.Duration, eff.Value)
    }
}
```

`newTestShip` is defined in `verb_damage_test.go` and lives in the same package; reuse it. If the helper doesn't already register a `StatusEffects` component, you may need to extend it OR add a small variant `newTestShipWithStatus`. Check what `newTestShip` does first.

NOTE on `se.Has` and `se.Get`: those methods exist on `gamecomp.StatusEffects` per the type definition in `internal/component/components.go`. Confirm with:

```bash
grep -n "func .*StatusEffects.*Has\|func .*StatusEffects.*Get" internal/component/components.go
```

If they don't exist, iterate manually.

- [ ] **Step 3: Run + commit**

```bash
go test ./internal/game/ -run TestApplyStatus -v
git add internal/game/verb_status_test.go
git commit -m "test(game): same-cell ApplyStatus path"
```

---

### Task 1.5: Remove the legacy StatusEffect tests

**File:** Modify `internal/game/cross_cell_combat_test.go`

- [ ] **Step 1: Read what's currently there**

```bash
cat internal/game/cross_cell_combat_test.go
```

Five tests should be present (per the state at the end of Plan C):
- `TestNetworkSystem_LockedByPopulated_FromReplicaLocker` — KEEP (lock visibility)
- `TestNetworkSystem_LockedByCleared_WhenLockerStops` — KEEP (lock visibility)
- `TestHandleCrossCellAction_StatusEffect_EnqueuesAnimation` — **DELETE** (exercises ActionStatusEffect)
- `TestStatusEffectAction_Roundtrip` — **DELETE** (exercises StatusEffectAction codec)
- `wireNetworkSystemForTest` helper — KEEP (used by the lock tests)

The replacement coverage for the StatusEffect tests is in `verb_status_test.go` (Task 1.4).

- [ ] **Step 2: Delete the two tests**

Find and delete:

```go
func TestHandleCrossCellAction_StatusEffect_EnqueuesAnimation(t *testing.T) {
    ...
}

func TestStatusEffectAction_Roundtrip(t *testing.T) {
    ...
}
```

If the file imports symbols (`gamepb`, `mmokit`) used only by the deleted tests, remove those imports too. Verify with `go vet` after.

- [ ] **Step 3: Run + commit**

```bash
go vet ./internal/game/...
go test ./internal/game/...
git add internal/game/cross_cell_combat_test.go
git commit -m "test(game): delete legacy StatusEffect cross-cell tests (path migrated to gw.ApplyStatus)"
```

All tests should be green now.

---

## Phase 2: Delete the legacy cross-cell action surface

### Task 2.1: Delete `internal/game/action_codec.go`

**File:** Delete `internal/game/action_codec.go`

- [ ] **Step 1: Confirm contents**

```bash
cat internal/game/action_codec.go
```

After Plan C, the file should contain only `ActionStatusEffect` const + `StatusEffectAction` struct + marshallers. No other types should be there.

- [ ] **Step 2: Delete the file**

```bash
rm internal/game/action_codec.go
```

- [ ] **Step 3: Verify build (will break — game.go still references the deleted symbols)**

```bash
go vet ./internal/game/...
```

Expected errors: `undefined: ActionStatusEffect`, `undefined: UnmarshalStatusEffectAction`, etc. Task 2.2 fixes these.

- [ ] **Step 4: Stage the deletion (don't commit yet — bundle with Task 2.2)**

```bash
git rm internal/game/action_codec.go
```

---

### Task 2.2: Delete the `ActionStatusEffect` cases from game.go

**File:** Modify `internal/game/game.go`

- [ ] **Step 1: Locate the cases**

```bash
grep -n "case ActionStatusEffect\|HandleCrossCellAction\|HandleActionResult" internal/game/game.go
```

Find:
- `case ActionStatusEffect:` in `HandleCrossCellAction` (around line 281)
- `case ActionStatusEffect:` in `HandleActionResult` (around line 350)

After Damage and Mining were migrated in Plan C, these are the *only* cases left in both switches.

- [ ] **Step 2: Delete the cases**

For `HandleCrossCellAction`:

```go
func (gw *GameWorld) HandleCrossCellAction(action *mmokit.CrossCellAction) *mmokit.ActionResult {
    var result *mmokit.ActionResult

    switch action.Type {
    case ActionStatusEffect:
        // ... entire case block ...

    default:
        gw.eng.Log.Log(CatCombatAbility, "cross-cell action: unknown type=%d from node=%s", action.Type, action.SourceCellID)
        return nil
    }

    // ... possibly trailing code (SideEffects.Drain) ...
    return result
}
```

After deleting `case ActionStatusEffect:`, the switch has only the `default:` case left, and the function body just returns nil. **Delete the whole function.**

For `HandleActionResult`:

```go
func (gw *GameWorld) HandleActionResult(result *mmokit.ActionResult) {
    if len(result.SideEffects) > 0 {
        // dispatch side effects
    }
    switch result.Type {
    case ActionStatusEffect:
        // ... case body ...
    }
}
```

After deleting, only the `SideEffects` dispatch + an empty switch remains. If the SideEffects dispatch is still meaningful for the engine path (it's not — `ActionTypedMessage` doesn't use SideEffects), **delete the whole function**.

- [ ] **Step 3: Verify build**

```bash
go vet ./internal/game/...
```

Build should be clean — but Task 2.3 / 2.4 still need to handle the interface side.

- [ ] **Step 4: Commit (with action_codec.go deletion bundled)**

```bash
git add internal/game/game.go
git commit -m "refactor(game): delete HandleCrossCellAction/HandleActionResult + action_codec.go (no game ActionTypes left)"
```

---

### Task 2.3: Remove the methods from the `pkg/universe.GameWorld` interface

**File:** Modify `pkg/universe/world.go`

- [ ] **Step 1: Confirm the interface**

```bash
cat pkg/universe/world.go
```

The interface currently contains:

```go
type GameWorld interface {
    ...
    HandleCrossCellAction(action *CrossCellAction) *ActionResult
    HandleActionResult(result *ActionResult)
    ...
}
```

- [ ] **Step 2: Delete the two methods from the interface**

Remove the two lines. Keep the rest of the interface intact.

- [ ] **Step 3: Delete the default no-op implementations on Stage**

```bash
grep -n "func (b \*Stage) HandleCrossCellAction\|func (b \*Stage) HandleActionResult" pkg/universe/stage.go
```

Find lines 722-723 (or wherever they ended up) — the default implementations:

```go
func (b *Stage) HandleCrossCellAction(*CrossCellAction) *ActionResult { return nil }
func (b *Stage) HandleActionResult(*ActionResult)                     {}
```

Delete both. They were only there because the GameWorld interface required them; with the interface change, they're vestigial.

- [ ] **Step 4: Verify build**

```bash
go vet ./...
```

Expected: build error in `pkg/universe/cell.go` because `c.World.HandleCrossCellAction(...)` still calls the now-removed method. Task 2.4 fixes this.

---

### Task 2.4: Remove the fallback paths from `pkg/universe/cell.go`

**File:** Modify `pkg/universe/cell.go`

- [ ] **Step 1: Locate the call sites**

```bash
grep -n "HandleCrossCellAction\|HandleActionResult\|HandleEngineAction" pkg/universe/cell.go
```

Find the `MsgCrossCellAction` arm (around line 367-385) and the `MsgActionResult` arm (around line 386-395).

- [ ] **Step 2: Update `MsgCrossCellAction` arm**

The current code:

```go
case MsgCrossCellAction:
    c.Log.Log(CatMeshAction, "[%s] cross-cell action from=%s type=%d targetNetID=%d", ...)
    if c.Stage.HandleEngineAction(msg.Action) {
        return  // engine consumed it
    }
    result := c.World.HandleCrossCellAction(msg.Action)  // ← DELETE this fallback path
    if result != nil {
        // ... ship result back via bridge ...
    }
```

Replace with:

```go
case MsgCrossCellAction:
    c.Log.Log(CatMeshAction, "[%s] cross-cell action from=%s type=%d targetNetID=%d", ...)
    if !c.Stage.HandleEngineAction(msg.Action) {
        c.Log.Log(CatMeshAction,
            "[%s] cross-cell action: unhandled action type=%d from=%s (no engine handler)",
            c.MeshID, msg.Action.Type, msg.FromCellID)
    }
    return
```

The result-shipping branch is unreachable now (no game-defined ActionTypes return results) — delete it. `Stage.HandleEngineAction` for `ActionTypedMessage` doesn't produce a result either; the typed-message reply path is via mmokit.Send-from-handler, not via the legacy result mechanism.

- [ ] **Step 3: Update `MsgActionResult` arm**

The current code:

```go
case MsgActionResult:
    c.Log.Log(CatMeshAction, "[%s] action result from=%s type=%d", ...)
    c.World.HandleActionResult(msg.ActionResult)
    return
```

The `MsgActionResult` envelope itself is no longer produced by anything — game-defined ActionTypes that returned results are all gone, and `ActionTypedMessage` doesn't use this path. Either:

- **(a)** Delete the entire `case MsgActionResult:` arm — it's unreachable.
- **(b)** Keep it as a defensive log + drop, in case something somewhere still emits it.

Pick (a) for cleanliness. If you're unsure whether anything still emits `MsgActionResult`, grep first:

```bash
grep -rn "MsgActionResult\|Type: MsgActionResult\|ActionResult{" pkg/universe/ internal/game/ | head
```

If the search shows no producer remains, delete the arm and the related types if they're now unused. **However**, `MsgActionResult` itself is just an enum value on `CellMessage`; deleting the message-type constant is more invasive than needed. Keep the constant + the arm but simplify the arm body to a log + drop:

```go
case MsgActionResult:
    c.Log.Log(CatMeshAction, "[%s] action result from=%s type=%d (legacy path — no handler, dropping)",
        c.MeshID, msg.FromCellID, msg.ActionResult.Type)
    return
```

This is tolerant; future-you can delete the arm + constant + types in a later cleanup if confirmed unused.

- [ ] **Step 4: Verify build**

```bash
go vet ./...
go test ./pkg/...
```

Should be clean. Existing universe tests continue to pass because they exercise `Stage.HandleEngineAction` for typed messages, not the legacy fallback.

- [ ] **Step 5: Commit (bundle interface + cell + stage changes)**

```bash
git add pkg/universe/world.go pkg/universe/cell.go pkg/universe/stage.go
git commit -m "refactor(universe): remove HandleCrossCellAction/HandleActionResult from GameWorld interface and cell receive path"
```

---

## Phase 3: Tick-callback integration test (foundation deferral)

### Task 3.1: Real-loop integration test for `mmokit.OnWorldTickAll`

**File:** Create `pkg/mmokit/tick_integration_test.go`

The foundation review noted: "the wiring through the coordinator (`coordinator.go:2198-2209`) is verified only by code inspection. A regression that drops the for-loop in mergedHooks would not break any test in this branch." This task adds that integration coverage.

- [ ] **Step 1: Investigate how to run a real Process tick**

```bash
grep -rn "Process.*Build\(\)\|TickRate.*20\|Headless.*true" pkg/universe/*_test.go pkg/mmokit/*_test.go | head -10
```

Look at how existing universe tests construct a real Process and drive ticks. Likely candidates:
- `pkg/universe/coordinator_test.go` (the test file we added in Plan C Phase 1)
- `pkg/universe/distributed_*_test.go`
- `pkg/universe/s7_*_test.go`

The pattern is: construct via `New(Config{Headless: true, ...})`, call `Build()`, then drive ticks somehow. Find the canonical "drive one tick" call — it may be `process.Tick()`, `process.RunOnce()`, `cell.Loop.RunOnce()`, or you may need to send a tick via the engine directly.

If no public single-tick driver exists, the cleanest option is to call `process.Start(ctx)` in a goroutine, sleep ~150ms (3+ ticks at 20Hz), then `process.Stop()` and assert. Less ideal because it's wallclock-dependent, but unambiguous.

- [ ] **Step 2: Write the test**

```go
// pkg/mmokit/tick_integration_test.go
//
// Integration test for the tick-callback wiring: registers a callback via
// mmokit.OnWorldTickAll on a real Process, runs the loop, asserts the
// callback fires. Covers the foundation review's open concern that the
// mergedHooks.PreFlush wiring in coordinator.go is currently verified
// only by code inspection.
package mmokit_test

import (
    "context"
    "sync/atomic"
    "testing"
    "time"

    "github.com/zenion/mmokit/pkg/mmokit"
    pkguniverse "github.com/zenion/mmokit/pkg/universe"
)

func TestOnWorldTickAll_FiresThroughRealLoop(t *testing.T) {
    p := pkguniverse.New(pkguniverse.Config{
        CellsX:   1,
        CellsY:   1,
        TickRate: 20,
        Headless: true,
    })

    var ticks atomic.Int32
    mmokit.OnWorldTickAll(p, func(stage *pkguniverse.Stage, dt float32) {
        ticks.Add(1)
    })

    p.Build()

    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan struct{})
    go func() {
        defer close(done)
        p.Start(ctx)
    }()

    // Run for ~5 ticks at 20Hz — gives 250ms of wallclock plus overhead.
    time.Sleep(300 * time.Millisecond)
    cancel()
    <-done

    got := ticks.Load()
    if got < 3 {
        t.Fatalf("OnWorldTickAll fired %d times in ~250ms; expected at least 3", got)
    }
}
```

NOTE: The `Process.Start(ctx)` API may not exist with this exact signature. Check via:

```bash
grep -n "func (c \*Process) Start\|func (c \*Process) Stop\|func (c \*Process) Run" pkg/universe/coordinator.go
```

Adapt the test to match the real lifecycle methods. If `Start` blocks until ctx cancels (which it likely does — that's the typical pattern), the goroutine + cancel pattern works.

If running a full Process is too involved, simplify to constructing a single Cell directly and ticking its game loop:

```bash
grep -n "func.*GameLoop.*Tick\|RunOnce\|func.*Loop.*Step" pkg/engine/loop.go pkg/universe/*.go
```

If there's a public `loop.Tick()` or equivalent, use it for synchronous drive (no goroutine needed). The synchronous version is preferable.

- [ ] **Step 3: Run + commit**

```bash
go test ./pkg/mmokit/ -run TestOnWorldTickAll_FiresThroughRealLoop -v
git add pkg/mmokit/tick_integration_test.go
git commit -m "test(mmokit): integration test for OnWorldTickAll firing through real coordinator loop"
```

---

## Phase 4: Closeout

### Task 4.1: Full verification + spec update + final report

- [ ] **Step 1: Run the full suite**

```bash
go vet ./...
go test ./pkg/... ./internal/...
just build
```

All must be green. (`just build` requires postgres running for the SDK regen step — if postgres is down, `go build ./...` is sufficient evidence.)

- [ ] **Step 2: Smoke-test the example**

```bash
go build ./examples/4node-basic/...
```

Should compile cleanly.

- [ ] **Step 3: Update the spec migration plan**

In `docs/superpowers/specs/2026-05-03-entity-message-passing-design.md` §10, update step 3 to reflect StatusEffect being done and step 5 to reflect that the legacy cross-cell-action surface is fully gone:

```diff
-3. **Migrate remaining verbs.** Mining: **[done — 2026-05-04, Plan C]**. ... **Remaining:** status effects (`ActionStatusEffect`), target lock, dock requests, currency transfers ...
+3. **[done for Damage/Mining/StatusEffect — 2026-05-04, Plans C+D]** **Migrate remaining verbs.** ... **Remaining:** target lock, dock requests, currency transfers (these don't currently use CrossCellAction; they're separate concerns).

-5. **Delete the old API surfaces.** ... Once StatusEffect (the last `ActionType` user) migrates, the legacy switch reduces to just the engine-level `ActionTypedMessage` handler — at which point most of `action_codec.go` can disappear.
+5. **[done — 2026-05-04, Plan D]** **Delete the old API surfaces.** `internal/game/action_codec.go` deleted. `HandleCrossCellAction` / `HandleActionResult` removed from both `*GameWorld` and the `pkg/universe.GameWorld` interface. Cross-cell action infrastructure remains engine-internal only (the `ActionTypedMessage` opcode used by `mmokit.Send`'s wire path).
```

```bash
git add docs/superpowers/specs/2026-05-03-entity-message-passing-design.md
git commit -m "docs(spec): mark StatusEffect migration + legacy surface removal landed (Plan D done)"
```

- [ ] **Step 4: Final report**

Summarize:
- Phase 1: StatusEffect verb migrated. `gw.ApplyStatus(...)` replaces `sendCrossNodeStatusEffect` in `system_ability.go`. New `verb_status.go` + test.
- Phase 2: Legacy cross-cell-action game surface removed entirely. `internal/game/action_codec.go` deleted. `HandleCrossCellAction` / `HandleActionResult` removed from GameWorld + interface + cell.go fallback path.
- Phase 3: Tick-callback integration test landed.
- Lines deleted vs added (`git diff --stat main..HEAD | tail -3`).
- Remaining migration work: TargetLock, Dock, Currency are NOT cross-cell-action migrations; they're separate (likely lighter) concerns. ECS-access mechanical sweep still pending. Input handling migration still pending.

---

## Out of scope / not in this plan

- **TargetLock, Dock-request, Currency-transfer migrations.** These don't currently use the legacy `CrossCellAction` infrastructure — they have their own (smaller) cross-cell concerns and warrant separate plans.
- **Mechanical ECS-access sweep.** Replacing `gw.eng.ECS.Alive(e)` with `e.Alive()`, `gw.C.X.Get(e)` with `mmokit.Get[X](e)`, etc., across all game systems. Pure mechanical replacement — own plan.
- **Input handling migration.** Converting `InputBindings` to use the typed `Send`/`Handle` mechanism with a from-client-trust tag. Own plan.
- **AoI auto-broadcast for typed messages.** Animation events still use the existing per-cell `AbilityCastResultMsg` enqueue path. Reflective auto-anchor broadcast (spec §4.5) is a separate plan.
- **Death/Loot composition.** Killed-event observation + loot crate spawn pattern from spec §5 is a separate plan.

Each follow-up plan is independently revertible and the codebase remains green between plans.

---

## Quick orientation for a fresh agent

If you're picking this plan up cold, here's the state of the world:

- **Branch:** `feat/mmokit-entity-message-api` (continue on this branch — single ongoing dev branch per the user's solo-developer convention).
- **Latest commit:** `90e2b7c` (spec update marking Plan C done).
- **What's already done:**
  - `mmokit` foundation: `Entity` value type, `Get/Has/Set`, `Spawn/Despawn`, `Nearby/NearbyWith`, `Send`/`Handle[M]` (cross-cell aware), `OnWorldTick`/`OnTick[T]`/`OnTickEach[B]`, `RawWorld` escape hatch.
  - Process-level wrappers: `Process.OnStageInit`, `mmokit.HandleAll[M]`, `OnWorldTickAll`, `OnTickAll[T]`, `OnTickEachAll[B]`.
  - Verb migrations: Damage and Mining done. `gw.Damage(...)`, `gw.MineExtract(...)` are the patterns to mirror for `gw.ApplyStatus(...)`.
- **What you'll need to read first:**
  - `internal/game/verb_damage.go` — pattern to mirror exactly.
  - `internal/game/verb_mining.go` — pattern to mirror exactly.
  - `internal/game/factory.go` — see how `RegisterDamageVerb(coord)` is wired in `GameSetup`; mirror it.
  - `internal/game/system_ability.go` — see how the migrated `case item.AbilityTypePulseLaser:` block reads now (it's clean — that's what `case item.AbilityTypeIonBurn:` will look like after this plan).
  - `pkg/universe/world.go` — small file, the `GameWorld` interface. Removing two methods from it is safe.

The plan is concrete; mirror the patterns and don't over-think it. If anything is genuinely ambiguous after exploration, make the reasonable choice and report DONE_WITH_CONCERNS.
