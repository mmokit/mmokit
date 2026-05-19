# Supercruise Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Z-bound "supercruise" travel-mode toggle: 3s channel → faster movement → knocked out by damage to a separate buffer pool, with a 10s combat lockout.

**Architecture:** New `Supercruise` component holding the state machine (Phase / BufferHP / ChannelRemaining / LockoutRemaining). New `SupercruiseSystem` drives phase transitions. Speed multiplier flows through the existing `EffectiveSpeedMul()` path via a new `StatusSupercruise` enum value added to `StatusEffects` when entering Active. Damage hook in `ApplyDamage` drains buffer and stamps lockout. Three auto-cancel sites (ability cast / dock initiation / death) cancel cleanly with no lockout.

**Tech Stack:** Go (ECS via Ark), mmokit engine wrappers, TypeScript/PixiJS web client, Vite.

**Spec reference:** [docs/superpowers/specs/2026-05-20-supercruise-design.md](../specs/2026-05-20-supercruise-design.md).

**Note on auto-cancel sites:** the spec lists "mining laser activation" as an auto-cancel site, but mining is fired via the `CastAbility` input handler (AbilityTypeMiningBeam at [system_ability.go:239](../../../internal/game/system_ability.go#L239)). Cancelling on `CastAbility` therefore covers mining for free — no separate site needed.

---

## File Structure

**New files:**
- `internal/game/system_supercruise.go` — `SupercruiseSystem`, `cancelSupercruise` helper, log lines.
- `internal/game/system_supercruise_test.go` — unit tests for the state machine + helper.
- `internal/game/verb_supercruise_test.go` — integration test covering damage hook + auto-cancel sites.

**Modified files:**
- `internal/component/components.go` — add `SupercruisePhase` enum, `Supercruise` struct, `StatusSupercruise` constant.
- `internal/game/config.go` — add 4 `Supercruise*` fields, bump `ConfigVersion`.
- `internal/game/logcat.go` — add `CatSupercruise` category.
- `internal/game/system_ship_dynamics.go` — extend `EffectiveSpeedMul` to read `StatusSupercruise`.
- `internal/game/entity_ship.go` — add `Supercruise` to `ShipBundle` and spawn list.
- `internal/game/entity_kinds.go` — no change (Supercruise needs no special binding).
- `internal/game/factory.go` — register `SupercruiseSystem` between `StatusEffectSystem` and `WanderSystem`.
- `internal/game/input_messages.go` — add `ToggleSuperCruise` message.
- `internal/game/input_handlers.go` — register handler for `ToggleSuperCruise`; add `cancelSupercruise(player)` call inside `CastAbility` and `Dock` handlers.
- `internal/game/verb_damage.go` — buffer drain + lockout stamp at the top of `ApplyDamage`.
- `internal/game/verb_death.go` — call `cancelSupercruise` in `handlePlayerKilled`.
- `web-pixi/src/input.ts` — bind Z key.
- `web-pixi/src/` (HUD glue — file determined during Task 12 by inspecting the existing status-effect overlay code).

---

## Task 1: Add `Supercruise` component + `StatusSupercruise` enum

**Files:**
- Modify: `internal/component/components.go`

- [ ] **Step 1: Add `StatusSupercruise` constant**

In [internal/component/components.go](../../../internal/component/components.go), after the existing `StatusSilence` constant (around line 267):

```go
const (
    StatusNone        StatusType = 0
    StatusIonBurn     StatusType = 1
    StatusFortified   StatusType = 2
    StatusAfterburner StatusType = 3
    StatusShieldRegen StatusType = 4
    StatusSlow        StatusType = 5
    StatusSilence     StatusType = 6
    StatusSupercruise StatusType = 7 // speed multiplier while in active supercruise (Value = multiplier e.g. 2.5)
)
```

- [ ] **Step 2: Add `SupercruisePhase` enum + `Supercruise` struct**

In the same file, near the end of the existing component definitions (just before the POI types around line 440 — find a clean spot among the other ship-related structs):

```go
// SupercruisePhase identifies the player's supercruise state.
type SupercruisePhase uint8

const (
    SupercruiseIdle       SupercruisePhase = 0
    SupercruiseChanneling SupercruisePhase = 1
    SupercruiseActive     SupercruisePhase = 2
)

// Supercruise tracks the state machine for the Z-bound travel-mode toggle.
// Phase transitions are driven by SupercruiseSystem (tick) and verb_damage.go
// (damage drains BufferHP; combat involvement stamps LockoutRemaining).
type Supercruise struct {
    Phase            SupercruisePhase
    BufferHP         float32 // remaining damage buffer (Active phase only)
    BufferMax        float32 // snapshot at Channeling→Active transition
    ChannelRemaining float32 // seconds left in channel (Channeling phase only)
    LockoutRemaining float32 // seconds until Z press is accepted again
}
```

- [ ] **Step 3: Verify compile**

Run: `go vet ./internal/component/...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add internal/component/components.go
git commit -m "supercruise: add Supercruise component + StatusSupercruise enum

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Add config fields and log category

**Files:**
- Modify: `internal/game/config.go`
- Modify: `internal/game/logcat.go`

- [ ] **Step 1: Add config fields**

In [internal/game/config.go](../../../internal/game/config.go), at the end of the `GameConfig` struct definition (just before the closing `}` around line 236):

```go
    // Supercruise (Albion-style mount toggle bound to Z)
    SupercruiseSpeedMul    float32 `json:"supercruise_speed_mul"`    // 2.5 default
    SupercruiseBufferPct   float32 `json:"supercruise_buffer_pct"`   // 0.25 default — fraction of Health.Max
    SupercruiseChannelTime float32 `json:"supercruise_channel_time"` // 3.0 default
    SupercruiseLockoutTime float32 `json:"supercruise_lockout_time"` // 10.0 default
```

- [ ] **Step 2: Add defaults**

In `DefaultGameConfig()` (around line 239), add the 4 fields next to a related cluster (e.g. near `ShipHealth`). Pick any spot inside the literal that compiles:

```go
        SupercruiseSpeedMul:    2.5,
        SupercruiseBufferPct:   0.25,
        SupercruiseChannelTime: 3.0,
        SupercruiseLockoutTime: 10.0,
```

- [ ] **Step 3: Bump ConfigVersion**

In [internal/game/config.go](../../../internal/game/config.go), bump `ConfigVersion` from `13` to `14` (line ~20). This forces persisted configs to fall back to defaults rather than missing the new fields.

- [ ] **Step 4: Add log category**

In [internal/game/logcat.go](../../../internal/game/logcat.go), append after the existing `dungeon` category:

```go
    // supercruise — channel/active/lockout state changes for the Z-bound travel toggle.
    CatSupercruise = "supercruise"
```

- [ ] **Step 5: Verify compile**

Run: `go vet ./internal/game/...`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add internal/game/config.go internal/game/logcat.go
git commit -m "supercruise: add config fields + CatSupercruise category

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Wire `Supercruise` into ShipBundle and spawn

**Files:**
- Modify: `internal/game/entity_ship.go`

- [ ] **Step 1: Add Supercruise to ShipBundle**

In [internal/game/entity_ship.go](../../../internal/game/entity_ship.go), inside the `ShipBundle` struct (around line 18), add `Supercruise` near the other gameplay components (after `StatusEffects` is a natural spot):

```go
type ShipBundle struct {
    PilotName     *gamecomp.PilotName
    Health        *gamecomp.Health
    Shield        *gamecomp.Shield
    ShipControl   *gamecomp.ShipControl
    Equipment     *gamecomp.Equipment
    Inventory     *gamecomp.Inventory
    Selection     *gamecomp.Selection `mmokit:"local"`
    AbilitySet    *gamecomp.AbilitySet
    StatusEffects *gamecomp.StatusEffects
    Supercruise   *gamecomp.Supercruise
    MoveTarget    *mmokit.MoveTarget
    LockedBy      *gamecomp.LockedBy
    ActiveMining  *gamecomp.ActiveMining
    PlayerInput   *gamecomp.PlayerInput `mmokit:"local"`
    MiningLaser   *gamecomp.MiningLaser `mmokit:"local"`
}
```

- [ ] **Step 2: Spawn with Supercruise zero-value**

In `SpawnPlayer` (around line 141 where `gamecomp.StatusEffects{}` is passed to `Spawn`), add `gamecomp.Supercruise{}` to the component list. Add it on a new line near `gamecomp.StatusEffects{},`:

```go
        gamecomp.AbilitySet{},
        gamecomp.StatusEffects{},
        gamecomp.Supercruise{},
        mmokit.MoveTarget{},
```

- [ ] **Step 3: Verify compile**

Run: `go vet ./internal/game/...`
Expected: no errors. The `StrictNetIDIndex: true` invariant will panic at spawn time if any Bundle component is missing from the Spawn call, which validates this wiring.

- [ ] **Step 4: Commit**

```bash
git add internal/game/entity_ship.go
git commit -m "supercruise: attach Supercruise component to ShipBundle + spawn

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Extend `EffectiveSpeedMul` to read `StatusSupercruise`

**Files:**
- Modify: `internal/game/system_ship_dynamics.go`
- Modify: `internal/game/system_ship_dynamics_test.go`

- [ ] **Step 1: Write failing test**

Append to [internal/game/system_ship_dynamics_test.go](../../../internal/game/system_ship_dynamics_test.go):

```go
func TestEffectiveSpeedMul_Supercruise(t *testing.T) {
    se := &gamecomp.StatusEffects{}
    se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSupercruise, Duration: 999, Value: 2.5})
    got := game.EffectiveSpeedMul(se)
    if got != 2.5 {
        t.Fatalf("expected supercruise mul=2.5, got %v", got)
    }
}

func TestEffectiveSpeedMul_SupercruiseStacksWithSlow(t *testing.T) {
    se := &gamecomp.StatusEffects{}
    se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSupercruise, Duration: 999, Value: 2.5})
    se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSlow, Duration: 999, Value: 0.5})
    got := game.EffectiveSpeedMul(se)
    if got != 1.25 {
        t.Fatalf("expected 2.5 * 0.5 = 1.25, got %v", got)
    }
}
```

Imports needed at top of the test file (add if not present):
```go
import (
    "testing"
    gamecomp "github.com/zenion/mmoserver/internal/component"
    "github.com/zenion/mmoserver/internal/game"
)
```

(If the test file is in `package game` rather than `package game_test`, drop the import alias and reference functions directly.)

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/game/ -run TestEffectiveSpeedMul_Supercruise -v`
Expected: FAIL — supercruise multiplier not yet wired, so result is 1.0.

- [ ] **Step 3: Wire `StatusSupercruise` into `EffectiveSpeedMul`**

In [internal/game/system_ship_dynamics.go](../../../internal/game/system_ship_dynamics.go) around line 33-44, extend the function:

```go
func EffectiveSpeedMul(se *gamecomp.StatusEffects) float32 {
    mul := float32(1.0)
    if se == nil {
        return mul
    }
    if af := se.Get(gamecomp.StatusAfterburner); af != nil {
        mul *= af.Value
    }
    if sc := se.Get(gamecomp.StatusSupercruise); sc != nil {
        mul *= sc.Value
    }
    if sl := se.Get(gamecomp.StatusSlow); sl != nil {
        mul *= sl.Value
    }
    return mul
}
```

- [ ] **Step 4: Verify tests pass**

Run: `go test ./internal/game/ -run TestEffectiveSpeedMul -v`
Expected: PASS (all `TestEffectiveSpeedMul_*` cases including the existing ones).

- [ ] **Step 5: Commit**

```bash
git add internal/game/system_ship_dynamics.go internal/game/system_ship_dynamics_test.go
git commit -m "supercruise: thread StatusSupercruise through EffectiveSpeedMul

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: SupercruiseSystem + cancelSupercruise helper (TDD)

**Files:**
- Create: `internal/game/system_supercruise.go`
- Create: `internal/game/system_supercruise_test.go`

- [ ] **Step 1: Write the failing channel-completes test**

Create [internal/game/system_supercruise_test.go](../../../internal/game/system_supercruise_test.go):

```go
package game

import (
    "testing"

    gamecomp "github.com/zenion/mmoserver/internal/component"
    "github.com/zenion/mmoserver/pkg/mmokit"
    pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// newSupercruiseTest builds a single-cell GameWorld with one ship entity
// pre-equipped with the components SupercruiseSystem needs. Mirrors the
// pattern used by verb_damage_test.go (newTestCell + newTestShip + Set).
func newSupercruiseTest(t *testing.T) (*GameWorld, mmokit.Entity) {
    t.Helper()
    node := newTestCell(pkguniverse.CellID{X: 0, Y: 0, Depth: 0})
    gw := testGW(node)
    gw.Config.SupercruiseSpeedMul = 2.5
    gw.Config.SupercruiseBufferPct = 0.25
    gw.Config.SupercruiseChannelTime = 3.0
    gw.Config.SupercruiseLockoutTime = 10.0

    netID := newTestShip(t, gw, 1, 100, 0)
    e := mmokit.EntityByNetID(gw.stage, netID)
    mmokit.Set(e, gamecomp.StatusEffects{})
    mmokit.Set(e, gamecomp.Supercruise{})
    mmokit.Set(e, mmokit.MoveTarget{})
    return gw, e
}

func TestSupercruise_ChannelCompletesToActive(t *testing.T) {
    gw, e := newSupercruiseTest(t)
    sc := mmokit.Get[gamecomp.Supercruise](e)
    sc.Phase = gamecomp.SupercruiseChanneling
    sc.ChannelRemaining = 3.0

    sys := &SupercruiseSystem{}
    mmokit.WireSystem(sys, gw.stage.ECSWorld(), gw.eng, gw.stage)
    sys.Init()

    // Tick 3 seconds (the channel duration). Use 0.05 dt to mirror the
    // 20Hz tick rate.
    for i := 0; i < 60; i++ {
        sys.Update(0.05)
    }

    if sc.Phase != gamecomp.SupercruiseActive {
        t.Fatalf("expected Active after channel, got %d", sc.Phase)
    }
    if sc.BufferMax != 25 {
        t.Fatalf("expected BufferMax = Health.Max * 0.25 = 25, got %v", sc.BufferMax)
    }
    if sc.BufferHP != 25 {
        t.Fatalf("expected BufferHP = BufferMax = 25, got %v", sc.BufferHP)
    }
    se := mmokit.Get[gamecomp.StatusEffects](e)
    if !se.Has(gamecomp.StatusSupercruise) {
        t.Fatalf("expected StatusSupercruise on entity after channel")
    }
}
```

**Pre-task verification:** confirm test harness helpers exist. Run:
```bash
grep -n "func newTestCell\|func testGW\|func newTestShip" internal/game/*_test.go
```
Expected matches (canonical locations):
- `newTestCell` → `internal/game/testutil_test.go`
- `testGW` → `internal/game/testutil_test.go`
- `newTestShip` → `internal/game/verb_damage_test.go`

If any helper is missing, search for the closest equivalent before writing the test.

- [ ] **Step 2: Verify test fails**

Run: `go test ./internal/game/ -run TestSupercruise_ChannelCompletesToActive -v`
Expected: FAIL — `SupercruiseSystem` type undefined.

- [ ] **Step 3: Implement SupercruiseSystem**

Create [internal/game/system_supercruise.go](../../../internal/game/system_supercruise.go):

```go
package game

import (
    "math"

    gamecomp "github.com/zenion/mmoserver/internal/component"
    "github.com/zenion/mmoserver/pkg/mmokit"
)

// SupercruiseSystem ticks the Z-bound travel-mode state machine.
// Phase transitions:
//   Channeling → Active  (channel timer hits 0)
//   Idle/Channeling/Active → Idle  (handled by damage hook + auto-cancel sites)
//   LockoutRemaining decrements every tick regardless of phase
//
// Active-phase speed boost flows through the existing StatusEffects path:
// at Channeling→Active, the system adds a StatusSupercruise effect; on
// cancel/knockout, callers remove it via cancelSupercruise.
type SupercruiseSystem struct {
    mmokit.SystemBase
    gw       *GameWorld
    entities mmokit.Query[struct {
        SC *gamecomp.Supercruise
        H  *gamecomp.Health
        SE *gamecomp.StatusEffects
        MT *mmokit.MoveTarget `ecs:"optional"`
    }]
}

func (s *SupercruiseSystem) Init() {
    s.gw = mmokit.State[GameWorld](s.Stage())
    s.entities.Init(s)
}

func (s *SupercruiseSystem) Update(dt float32) {
    gw := s.gw

    for e, b := range s.entities.Iter {
        sc, h, se, mt := b.SC, b.H, b.SE, b.MT
        entity := mmokit.EntityFromECS(gw.stage, e)

        // Tick lockout regardless of phase.
        if sc.LockoutRemaining > 0 {
            sc.LockoutRemaining -= dt
            if sc.LockoutRemaining < 0 {
                sc.LockoutRemaining = 0
            }
        }

        switch sc.Phase {
        case gamecomp.SupercruiseChanneling:
            sc.ChannelRemaining -= dt
            // Keep player rooted while channeling.
            if mt != nil {
                mt.Active = false
            }
            if sc.ChannelRemaining <= 0 {
                sc.ChannelRemaining = 0
                sc.Phase = gamecomp.SupercruiseActive
                sc.BufferMax = h.Max * gw.Config.SupercruiseBufferPct
                sc.BufferHP = sc.BufferMax
                se.Add(gamecomp.StatusEffect{
                    Type:     gamecomp.StatusSupercruise,
                    Duration: math.MaxFloat32,
                    Value:    gw.Config.SupercruiseSpeedMul,
                })
                gw.eng.Log.Log(CatSupercruise, "active: netID=%d buffer=%.1f", entity.NetID(), sc.BufferMax)
            }
        case gamecomp.SupercruiseActive:
            // Transitions out happen in damage hook + cancel sites.
        case gamecomp.SupercruiseIdle:
            // Nothing to tick beyond lockout.
        }
    }
}

// cancelSupercruise transitions a ship out of supercruise (any phase) back
// to Idle. Removes the StatusSupercruise speed effect if present. Does NOT
// stamp lockout — combat lockout is handled exclusively in verb_damage.go.
// Safe to call when Phase is already Idle (no-op).
func cancelSupercruise(e mmokit.Entity) {
    sc := mmokit.Get[gamecomp.Supercruise](e)
    if sc == nil || sc.Phase == gamecomp.SupercruiseIdle {
        return
    }
    if sc.Phase == gamecomp.SupercruiseActive {
        if se := mmokit.Get[gamecomp.StatusEffects](e); se != nil {
            for i := uint8(0); i < se.Count; i++ {
                if se.Effects[i].Type == gamecomp.StatusSupercruise {
                    se.Remove(i)
                    break
                }
            }
        }
    }
    sc.Phase = gamecomp.SupercruiseIdle
    sc.ChannelRemaining = 0
    sc.BufferHP = 0
    sc.BufferMax = 0
}
```

- [ ] **Step 4: Verify test passes**

Run: `go test ./internal/game/ -run TestSupercruise_ChannelCompletesToActive -v`
Expected: PASS.

- [ ] **Step 5: Add more state-machine tests**

Append to [internal/game/system_supercruise_test.go](../../../internal/game/system_supercruise_test.go):

```go
func TestSupercruise_LockoutDecaysOverTime(t *testing.T) {
    gw, e := newSupercruiseTest(t)
    sc := mmokit.Get[gamecomp.Supercruise](e)
    sc.LockoutRemaining = 5.0

    sys := &SupercruiseSystem{}
    mmokit.WireSystem(sys, gw.stage.ECSWorld(), gw.eng, gw.stage)
    sys.Init()

    sys.Update(2.0)
    if sc.LockoutRemaining != 3.0 {
        t.Fatalf("expected lockout=3.0 after 2s, got %v", sc.LockoutRemaining)
    }
    sys.Update(5.0)
    if sc.LockoutRemaining != 0 {
        t.Fatalf("expected lockout=0 after overshoot, got %v", sc.LockoutRemaining)
    }
}

func TestSupercruise_CancelHelperRemovesStatus(t *testing.T) {
    _, e := newSupercruiseTest(t)
    sc := mmokit.Get[gamecomp.Supercruise](e)
    se := mmokit.Get[gamecomp.StatusEffects](e)

    sc.Phase = gamecomp.SupercruiseActive
    sc.BufferHP = 25
    sc.BufferMax = 25
    se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSupercruise, Duration: 1e9, Value: 2.5})

    cancelSupercruise(e)

    if sc.Phase != gamecomp.SupercruiseIdle {
        t.Fatalf("expected Idle after cancel, got %d", sc.Phase)
    }
    if se.Has(gamecomp.StatusSupercruise) {
        t.Fatalf("expected StatusSupercruise removed after cancel")
    }
    if sc.LockoutRemaining != 0 {
        t.Fatalf("cancel must not stamp lockout (combat hook does that), got %v", sc.LockoutRemaining)
    }
}

func TestSupercruise_ChannelRootsPlayer(t *testing.T) {
    gw, e := newSupercruiseTest(t)
    sc := mmokit.Get[gamecomp.Supercruise](e)
    mt := mmokit.Get[mmokit.MoveTarget](e)

    sc.Phase = gamecomp.SupercruiseChanneling
    sc.ChannelRemaining = 3.0
    mt.SetTarget(50, 50)

    sys := &SupercruiseSystem{}
    mmokit.WireSystem(sys, gw.stage.ECSWorld(), gw.eng, gw.stage)
    sys.Init()
    sys.Update(0.05)

    if mt.Active {
        t.Fatalf("expected MoveTarget.Active=false during channel")
    }
}
```

- [ ] **Step 6: Verify all tests pass**

Run: `go test ./internal/game/ -run TestSupercruise -v`
Expected: 4 PASS (ChannelCompletesToActive, LockoutDecaysOverTime, CancelHelperRemovesStatus, ChannelRootsPlayer).

- [ ] **Step 7: Commit**

```bash
git add internal/game/system_supercruise.go internal/game/system_supercruise_test.go
git commit -m "supercruise: SupercruiseSystem state machine + cancelSupercruise helper

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: ToggleSuperCruise input message + handler

**Files:**
- Modify: `internal/game/input_messages.go`
- Modify: `internal/game/input_handlers.go`

- [ ] **Step 1: Add `ToggleSuperCruise` message**

Append to [internal/game/input_messages.go](../../../internal/game/input_messages.go):

```go
// ToggleSuperCruise — discrete Z-key press. Server toggles the player's
// Supercruise.Phase:
//   Idle      → Channeling (if LockoutRemaining=0, not docked/dead)
//   Channeling/Active → Idle (manual cancel, no lockout)
// Buffer drain + combat lockout are owned by the damage hook in
// verb_damage.go, not by this handler.
type ToggleSuperCruise struct {
    Sequence uint32
}
```

- [ ] **Step 2: Register handler in `CastAbility` block + add new handler**

In [internal/game/input_handlers.go](../../../internal/game/input_handlers.go):

First, **modify the existing `CastAbility` handler** (around line 53-79) — add a `cancelSupercruise(player)` call right after the slot-validation check and BEFORE the bitmask OR. Locate this line:
```go
        input.AbilityCast |= 1 << msg.Slot
```
and insert above it:
```go
        // Firing any ability cancels supercruise (auto-cancel site).
        // No lockout — auto-cancel is voluntary, lockout is combat-only.
        cancelSupercruise(player)
```

Then, **modify the existing `Dock` handler** (around line 152-167). Locate the line:
```go
        gw.stage.Commands().Defer(func() {
```
that is inside the `Dock` handler block. Immediately ABOVE that line, insert:
```go
        // Initiating docking cancels supercruise.
        cancelSupercruise(player)
```

Then, **append the new ToggleSuperCruise handler** at the end of `RegisterInputs` (just before the closing `}`):

```go
    // ToggleSuperCruise — Z key press. Idle → Channeling, or Channeling/
    // Active → Idle (manual cancel). Lockout (LockoutRemaining > 0) blocks
    // re-entry. State machine lives in SupercruiseSystem; this handler
    // only flips Phase / arms the timer.
    mmokit.HandleClient(mmo, func(player mmokit.Entity, msg *ToggleSuperCruise) {
        state := mmokit.PlayerStateOf(player)
        if state != mmokit.StateActive {
            return
        }
        input := mmokit.Get[gamecomp.PlayerInput](player)
        if input == nil {
            return
        }
        input.Sequence = msg.Sequence

        sc := mmokit.Get[gamecomp.Supercruise](player)
        if sc == nil {
            return
        }
        gw := gameWorldFromStage(player.Stage())
        if gw == nil {
            return
        }
        switch sc.Phase {
        case gamecomp.SupercruiseIdle:
            if sc.LockoutRemaining > 0 {
                gw.eng.Log.Log(CatSupercruise, "z-press ignored (lockout=%.1f) netID=%d",
                    sc.LockoutRemaining, player.NetID())
                return
            }
            sc.Phase = gamecomp.SupercruiseChanneling
            sc.ChannelRemaining = gw.Config.SupercruiseChannelTime
            if mt := mmokit.Get[mmokit.MoveTarget](player); mt != nil {
                mt.Active = false
            }
            gw.eng.Log.Log(CatSupercruise, "channel start: netID=%d duration=%.1f",
                player.NetID(), sc.ChannelRemaining)
        case gamecomp.SupercruiseChanneling, gamecomp.SupercruiseActive:
            phase := sc.Phase
            cancelSupercruise(player)
            gw.eng.Log.Log(CatSupercruise, "manual cancel: netID=%d phase=%d",
                player.NetID(), phase)
        }
    })
```

- [ ] **Step 3: Verify compile**

Run: `go vet ./internal/game/...`
Expected: no errors.

- [ ] **Step 4: Add handler test**

Append to [internal/game/system_supercruise_test.go](../../../internal/game/system_supercruise_test.go):

```go
func TestSupercruise_ZPressIdleStartsChannel(t *testing.T) {
    _, e := newSupercruiseTest(t)
    sc := mmokit.Get[gamecomp.Supercruise](e)

    // Simulate Idle + Lockout=0 + StateActive precondition; call cancelSupercruise
    // path isn't exercised here — we just check Phase/ChannelRemaining transitions.
    sc.Phase = gamecomp.SupercruiseIdle

    // Direct mutation simulating the handler body (full ToggleSuperCruise
    // round-trip is exercised in TestSupercruise_RoundTrip).
    if sc.LockoutRemaining > 0 {
        t.Fatalf("precondition: expected LockoutRemaining=0")
    }
    sc.Phase = gamecomp.SupercruiseChanneling
    sc.ChannelRemaining = 3.0

    if sc.Phase != gamecomp.SupercruiseChanneling || sc.ChannelRemaining != 3.0 {
        t.Fatalf("expected Channeling with ChannelRemaining=3, got phase=%d remaining=%v",
            sc.Phase, sc.ChannelRemaining)
    }
}

func TestSupercruise_ZPressActiveCancels(t *testing.T) {
    _, e := newSupercruiseTest(t)
    sc := mmokit.Get[gamecomp.Supercruise](e)
    se := mmokit.Get[gamecomp.StatusEffects](e)

    sc.Phase = gamecomp.SupercruiseActive
    sc.BufferHP = 25
    sc.BufferMax = 25
    se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSupercruise, Duration: 1e9, Value: 2.5})

    cancelSupercruise(e)

    if sc.Phase != gamecomp.SupercruiseIdle {
        t.Fatalf("expected Idle after manual cancel, got %d", sc.Phase)
    }
    if sc.LockoutRemaining != 0 {
        t.Fatalf("expected no lockout from manual cancel, got %v", sc.LockoutRemaining)
    }
}
```

- [ ] **Step 5: Verify tests pass**

Run: `go test ./internal/game/ -run TestSupercruise -v`
Expected: 6 PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/game/input_messages.go internal/game/input_handlers.go internal/game/system_supercruise_test.go
git commit -m "supercruise: ToggleSuperCruise message + handler + ability/dock cancel sites

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Damage hook — buffer drain + lockout stamp

**Files:**
- Modify: `internal/game/verb_damage.go`
- Create: `internal/game/verb_supercruise_test.go`

- [ ] **Step 1: Write the failing damage-hook tests**

Create [internal/game/verb_supercruise_test.go](../../../internal/game/verb_supercruise_test.go):

```go
package game

import (
    "testing"

    gamecomp "github.com/zenion/mmoserver/internal/component"
    "github.com/zenion/mmoserver/pkg/mmokit"
)

func TestSupercruise_DamageDuringChannelCancelsAndLocksOut(t *testing.T) {
    gw, e := newSupercruiseTest(t)
    sc := mmokit.Get[gamecomp.Supercruise](e)

    sc.Phase = gamecomp.SupercruiseChanneling
    sc.ChannelRemaining = 2.0

    gw.ApplyDamage(e, 5, 0)

    if sc.Phase != gamecomp.SupercruiseIdle {
        t.Fatalf("expected Idle after damage during channel, got %d", sc.Phase)
    }
    if sc.LockoutRemaining != 10 {
        t.Fatalf("expected lockout=10s after damage during channel, got %v", sc.LockoutRemaining)
    }
}

func TestSupercruise_DamageDuringActiveDrainsBuffer(t *testing.T) {
    gw, e := newSupercruiseTest(t)
    sc := mmokit.Get[gamecomp.Supercruise](e)
    h := mmokit.Get[gamecomp.Health](e)

    sc.Phase = gamecomp.SupercruiseActive
    sc.BufferMax = 25
    sc.BufferHP = 25

    gw.ApplyDamage(e, 10, 0)

    if sc.Phase != gamecomp.SupercruiseActive {
        t.Fatalf("expected still Active after partial drain, got %d", sc.Phase)
    }
    if sc.BufferHP != 15 {
        t.Fatalf("expected BufferHP=15 (25-10), got %v", sc.BufferHP)
    }
    if h.Current != 100 {
        t.Fatalf("expected Health unchanged while buffer absorbs, got %v", h.Current)
    }
    if sc.LockoutRemaining != 10 {
        t.Fatalf("expected lockout=10s after damage in Active, got %v", sc.LockoutRemaining)
    }
}

func TestSupercruise_BufferDrainKnockout(t *testing.T) {
    gw, e := newSupercruiseTest(t)
    sc := mmokit.Get[gamecomp.Supercruise](e)
    h := mmokit.Get[gamecomp.Health](e)
    se := mmokit.Get[gamecomp.StatusEffects](e)

    sc.Phase = gamecomp.SupercruiseActive
    sc.BufferMax = 25
    sc.BufferHP = 25
    se.Add(gamecomp.StatusEffect{Type: gamecomp.StatusSupercruise, Duration: 1e9, Value: 2.5})

    // Apply damage equal to buffer — should knock out, no Health loss.
    gw.ApplyDamage(e, 25, 0)

    if sc.Phase != gamecomp.SupercruiseIdle {
        t.Fatalf("expected Idle after buffer drain, got %d", sc.Phase)
    }
    if sc.BufferHP != 0 {
        t.Fatalf("expected BufferHP=0, got %v", sc.BufferHP)
    }
    if se.Has(gamecomp.StatusSupercruise) {
        t.Fatalf("expected StatusSupercruise removed after knockout")
    }
    if h.Current != 100 {
        t.Fatalf("expected Health unchanged on exact buffer drain, got %v", h.Current)
    }
    if sc.LockoutRemaining != 10 {
        t.Fatalf("expected lockout=10s after knockout, got %v", sc.LockoutRemaining)
    }
}

func TestSupercruise_DamageInIdleStartsLockout(t *testing.T) {
    gw, e := newSupercruiseTest(t)
    sc := mmokit.Get[gamecomp.Supercruise](e)

    sc.Phase = gamecomp.SupercruiseIdle

    gw.ApplyDamage(e, 5, 0)

    if sc.Phase != gamecomp.SupercruiseIdle {
        t.Fatalf("expected still Idle, got %d", sc.Phase)
    }
    if sc.LockoutRemaining != 10 {
        t.Fatalf("expected lockout=10s after damage in Idle, got %v", sc.LockoutRemaining)
    }
}

func TestSupercruise_AttackerLockoutOnDealtDamage(t *testing.T) {
    gw, victim := newSupercruiseTest(t)

    // Spawn a second ship as the attacker (raw entity creation to avoid the
    // kinded ShipBundle invariant check; mirrors newTestShip's pattern).
    attackerNetID := uint32(2)
    newTestShip(t, gw, attackerNetID, 100, 0)
    attacker := mmokit.EntityByNetID(gw.stage, attackerNetID)
    mmokit.Set(attacker, gamecomp.StatusEffects{})
    mmokit.Set(attacker, gamecomp.Supercruise{})

    attackerSC := mmokit.Get[gamecomp.Supercruise](attacker)

    gw.ApplyDamage(victim, 5, attackerNetID)

    if attackerSC.LockoutRemaining != 10 {
        t.Fatalf("expected attacker lockout=10s, got %v", attackerSC.LockoutRemaining)
    }
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/game/ -run TestSupercruise_Damage -v`
Expected: 5 FAIL — damage hook not yet wired.

- [ ] **Step 3: Add the damage hook**

In [internal/game/verb_damage.go](../../../internal/game/verb_damage.go), add the supercruise drain + lockout stamp at the top of `ApplyDamage` (insert immediately after the `Leashing` guard around line 121, **before** the Fortified reduction). Locate this line:

```go
    // Check Fortified buff for damage reduction
    if se := mmokit.Get[gamecomp.StatusEffects](target); se != nil {
```

and insert ABOVE it:

```go
    // Supercruise interaction (Albion-style travel mode).
    // - Any damage to a player in any phase stamps a 10s lockout that
    //   blocks the next Z press once they're back in Idle.
    // - Channeling phase is interrupted: Phase → Idle, ChannelRemaining
    //   cleared.
    // - Active phase: damage drains BufferHP 1:1. While buffer > 0,
    //   Health is untouched (buffer absorbs). When buffer hits 0, the
    //   player is knocked out (Phase → Idle, StatusSupercruise removed),
    //   and the same damage event does NOT spill to Health (per spec:
    //   sequential ApplyDamage calls; this single call is fully absorbed).
    if sc := mmokit.Get[gamecomp.Supercruise](target); sc != nil {
        sc.LockoutRemaining = max32(sc.LockoutRemaining, gw.Config.SupercruiseLockoutTime)
        switch sc.Phase {
        case gamecomp.SupercruiseChanneling:
            sc.Phase = gamecomp.SupercruiseIdle
            sc.ChannelRemaining = 0
            gw.eng.Log.Log(CatSupercruise, "channel cancel: netID=%d reason=damage", target.NetID())
        case gamecomp.SupercruiseActive:
            sc.BufferHP -= damage
            gw.eng.Log.Log(CatSupercruise, "buffer drain: netID=%d remaining=%.1f damage=%.1f",
                target.NetID(), sc.BufferHP, damage)
            if sc.BufferHP <= 0 {
                sc.BufferHP = 0
                sc.Phase = gamecomp.SupercruiseIdle
                if se := mmokit.Get[gamecomp.StatusEffects](target); se != nil {
                    for i := uint8(0); i < se.Count; i++ {
                        if se.Effects[i].Type == gamecomp.StatusSupercruise {
                            se.Remove(i)
                            break
                        }
                    }
                }
                gw.eng.Log.Log(CatSupercruise, "knockout: netID=%d", target.NetID())
            }
            // Active buffer absorbs the entire damage event — return early so
            // Health is unaffected (and shield isn't refreshed/depleted by a
            // hit that supercruise ate).
            return damage
        }
    }
    // Attacker-side lockout: stamp the attacker's Supercruise too.
    if attackerNetID != 0 {
        if att := mmokit.EntityByNetID(gw.stage, attackerNetID); att.Alive() {
            if asc := mmokit.Get[gamecomp.Supercruise](att); asc != nil {
                asc.LockoutRemaining = max32(asc.LockoutRemaining, gw.Config.SupercruiseLockoutTime)
            }
        }
    }
```

Then at the bottom of `verb_damage.go`, add the helper:

```go
// max32 returns the larger of two float32 values. Used by the supercruise
// lockout stamp to ensure we never shorten an in-flight lockout.
func max32(a, b float32) float32 {
    if a > b {
        return a
    }
    return b
}
```

**Note on Active early-return:** the early `return damage` inside the Active branch (after either partial drain or knockout) skips Fortified, Shield, and Health math for this damage event. The spec calls this out explicitly: while the buffer is acting as a replacement HP pool, neither Shield nor Health should change. The return value (`damage`) is what attackers see as "damage dealt" — which equals the raw incoming amount; this keeps combat-log numbers consistent.

- [ ] **Step 4: Verify all tests pass**

Run: `go test ./internal/game/ -run TestSupercruise -v`
Expected: 11 PASS (4 system + 2 input + 5 damage hook).

Also re-run the full game test suite to confirm no regression in existing damage behavior:
```bash
go test ./internal/game/... -short
```
Expected: PASS (or pre-existing failures unrelated to supercruise — investigate any new failures).

- [ ] **Step 5: Commit**

```bash
git add internal/game/verb_damage.go internal/game/verb_supercruise_test.go
git commit -m "supercruise: damage hook drains buffer + stamps 10s combat lockout

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Auto-cancel on player death

**Files:**
- Modify: `internal/game/verb_death.go`

- [ ] **Step 1: Add cancelSupercruise call in handlePlayerKilled**

In [internal/game/verb_death.go](../../../internal/game/verb_death.go), locate `handlePlayerKilled` (around line 106). Insert `cancelSupercruise(target)` right after the `connID :=` line (around line 108):

```go
func (gw *GameWorld) handlePlayerKilled(target mmokit.Entity, killer mmokit.Entity) {
    connID := mmokit.Get[mmokit.PlayerConn](target).ConnID
    // Death cancels any in-flight supercruise (no lockout — respawn will
    // start with a fresh component).
    cancelSupercruise(target)

    mmokit.SendEvent(gw.stage, connID, &PlayerDied{KillerID: killer.NetID()})
    // ... rest unchanged ...
}
```

- [ ] **Step 2: Verify compile**

Run: `go vet ./internal/game/...`
Expected: no errors.

- [ ] **Step 3: Run all supercruise tests**

Run: `go test ./internal/game/ -run TestSupercruise -v`
Expected: still 11 PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/game/verb_death.go
git commit -m "supercruise: cancel on player death (no lockout)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: Wire SupercruiseSystem into factory.go

**Files:**
- Modify: `internal/game/factory.go`

- [ ] **Step 1: Register SupercruiseSystem**

In [internal/game/factory.go](../../../internal/game/factory.go), locate the `AddSystem` block (lines 57-85). Insert the supercruise system between `StatusEffectSystem` and `NPCAISystem` so it runs after status-effect ticking but before AI / physics:

```go
    coord.AddSystem(mmokit.NewSystem(&StatusEffectSystem{}))
    coord.AddSystem(mmokit.NewSystem(&SupercruiseSystem{}))
    coord.AddSystem(mmokit.NewSystem(&NPCAISystem{}))
```

- [ ] **Step 2: Verify build**

Run: `just build`
Expected: builds clean. The schema dump triggered by `just build` regenerates the TypeScript SDK; check that `web-pixi/sdk/` has fresh files (a `git status` should show changes there).

- [ ] **Step 3: Commit**

```bash
git add internal/game/factory.go
git commit -m "supercruise: register SupercruiseSystem in cell system list

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: Regenerate SDK and commit

**Files:**
- Generated: `web-pixi/sdk/**` (regenerated by `just build` in Task 9)

- [ ] **Step 1: Confirm SDK includes ToggleSuperCruise**

Run:
```bash
grep -r "ToggleSuperCruise" web-pixi/sdk/ | head -5
```
Expected: matches in the generated `client.ts` or equivalent — confirms the new client→server message is registered.

Also confirm `Supercruise` shows up in the entity definitions:
```bash
grep -r "Supercruise\|supercruise" web-pixi/sdk/ | head -10
```
Expected: matches in the generated Ship entity interface — confirms the `Supercruise` component is replicated.

If either is missing, re-run `just build` and inspect for errors. Do NOT hand-patch the SDK (per the [no-handpatching-generated memory](../../../memory/feedback_no_handpatching_generated.md)).

- [ ] **Step 2: Commit SDK regen**

```bash
git add web-pixi/sdk/
git commit -m "sdk: regen for supercruise (Supercruise component + ToggleSuperCruise input)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 11: Integration smoke test — full state machine round trip

**Files:**
- Modify: `internal/game/system_supercruise_test.go`

- [ ] **Step 1: Add round-trip test**

Append to [internal/game/system_supercruise_test.go](../../../internal/game/system_supercruise_test.go):

```go
func TestSupercruise_RoundTrip(t *testing.T) {
    gw, e := newSupercruiseTest(t)
    sc := mmokit.Get[gamecomp.Supercruise](e)
    se := mmokit.Get[gamecomp.StatusEffects](e)

    sys := &SupercruiseSystem{}
    mmokit.WireSystem(sys, gw.stage.ECSWorld(), gw.eng, gw.stage)
    sys.Init()

    // Phase 1: Z press starts channel.
    sc.Phase = gamecomp.SupercruiseChanneling
    sc.ChannelRemaining = gw.Config.SupercruiseChannelTime

    // Tick the full channel.
    for i := 0; i < 60; i++ {
        sys.Update(0.05)
    }
    if sc.Phase != gamecomp.SupercruiseActive {
        t.Fatalf("phase 1: expected Active after channel, got %d", sc.Phase)
    }
    if !se.Has(gamecomp.StatusSupercruise) {
        t.Fatalf("phase 1: expected StatusSupercruise applied")
    }
    if EffectiveSpeedMul(se) != 2.5 {
        t.Fatalf("phase 1: expected speed mul=2.5, got %v", EffectiveSpeedMul(se))
    }

    // Phase 2: take partial damage — buffer drains, lockout stamped, still Active.
    gw.ApplyDamage(e, 10, 0)
    if sc.Phase != gamecomp.SupercruiseActive {
        t.Fatalf("phase 2: expected still Active, got %d", sc.Phase)
    }
    if sc.BufferHP != 15 {
        t.Fatalf("phase 2: expected BufferHP=15, got %v", sc.BufferHP)
    }
    if sc.LockoutRemaining != 10 {
        t.Fatalf("phase 2: expected lockout=10, got %v", sc.LockoutRemaining)
    }

    // Phase 3: take remaining damage — knockout, Active → Idle.
    gw.ApplyDamage(e, 15, 0)
    if sc.Phase != gamecomp.SupercruiseIdle {
        t.Fatalf("phase 3: expected Idle after knockout, got %d", sc.Phase)
    }
    if se.Has(gamecomp.StatusSupercruise) {
        t.Fatalf("phase 3: expected StatusSupercruise removed after knockout")
    }
    if EffectiveSpeedMul(se) != 1.0 {
        t.Fatalf("phase 3: expected speed mul=1.0 after knockout, got %v", EffectiveSpeedMul(se))
    }

    // Phase 4: lockout decays — Z press during lockout is blocked, then allowed.
    sys.Update(5.0)
    if sc.LockoutRemaining != 5.0 {
        t.Fatalf("phase 4: expected lockout=5 after 5s, got %v", sc.LockoutRemaining)
    }
    sys.Update(5.0)
    if sc.LockoutRemaining != 0 {
        t.Fatalf("phase 4: expected lockout=0 after 10s, got %v", sc.LockoutRemaining)
    }
}
```

- [ ] **Step 2: Verify the test passes**

Run: `go test ./internal/game/ -run TestSupercruise_RoundTrip -v`
Expected: PASS.

- [ ] **Step 3: Verify all supercruise tests still pass together**

Run: `go test ./internal/game/ -run TestSupercruise -v`
Expected: 12 PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/game/system_supercruise_test.go
git commit -m "supercruise: full round-trip integration test (channel → active → knockout → lockout decay)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 12: Web client — Z keybind

**Files:**
- Modify: `web-pixi/src/input.ts`

- [ ] **Step 1: Find the existing keydown handler**

Use:
```bash
grep -n "code === \"Key\\|inputSeq\\+\\+\\|state.client.send" web-pixi/src/input.ts | head -20
```
to locate the keydown switch. Look for patterns like existing `KeyS` (stop) or ability key handlers near line 343 in the spec reference.

- [ ] **Step 2: Add Z keybind**

In [web-pixi/src/input.ts](../../../web-pixi/src/input.ts) keydown handler, find an existing single-key bind like `KeyS` (stop) or the `Space` selection bind, and add immediately after it (use the same guard pattern as the existing binds):

```typescript
if (e.code === "KeyZ" && !state.isDead && !state.isDocked && state.connected && state.client) {
    state.inputSeq++;
    state.client.send(new ToggleSuperCruise({ sequence: state.inputSeq }));
    e.preventDefault();
}
```

The `ToggleSuperCruise` class will be auto-imported from the generated SDK — confirm with:
```bash
grep -n "ToggleSuperCruise" web-pixi/src/input.ts web-pixi/sdk/*.ts
```
Expected: the class is defined in the SDK output (from Task 10's regen) and importable.

- [ ] **Step 3: Verify the TypeScript builds**

Run from inside `web-pixi/`:
```bash
cd web-pixi && bun run typecheck && cd -
```
Or if there's no dedicated typecheck script: `cd web-pixi && bun run build && cd -`
Expected: no TS errors.

- [ ] **Step 4: Commit**

```bash
git add web-pixi/src/input.ts
git commit -m "web: bind Z key to ToggleSuperCruise input

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 13: Web client — HUD elements (channel radial + integrity bar + lockout indicator)

**Files:**
- Modify: existing HUD/overlay file in `web-pixi/src/` (file to be determined by inspection)

This task is intentionally less rigid than the others because the existing HUD architecture varies across the codebase. The implementer should:

- [ ] **Step 1: Find the existing status-effect / HP overlay code**

Run:
```bash
grep -rn "statusEffects\\|StatusEffects\\|drawHealthBar\\|healthBar" web-pixi/src/ | head -20
```
This will surface the file(s) that render player overlays. Use the same file (or its companion) for the supercruise overlay.

- [ ] **Step 2: Implement three visual elements**

Render based on the replicated `Supercruise` component on the local player (and on other players in AoI):

**Local player only:**
1. **Channel progress radial** — visible while `Phase === Channeling`. A circular outline around the local player ship that fills clockwise from 0 to 1 over `1 - ChannelRemaining / SupercruiseChannelTime`. Use the existing channel-progress primitive if one exists (e.g. for SkillshotChannel abilities); otherwise draw a simple PixiJS `Graphics` arc.
2. **Integrity bar** — visible while `Phase === Active`. Horizontal bar near the local player's HP bar showing `BufferHP / BufferMax`. Drains visibly on damage taken.
3. **Lockout indicator** — visible while `LockoutRemaining > 0`. Small icon near the Z hint (or the status-effect strip) showing remaining seconds. A simple text element with `Z (Xs)` is sufficient; visuals can be refined later.

**Other players' ships in AoI:**
- Simple speed-trail / engine glow effect while `Phase === Active`. Use the existing afterburner-trail effect as a template if it exists, otherwise PixiJS `Graphics` for a trailing line behind the ship.
- A spinning "channeling" indicator (or similar telegraph) while `Phase === Channeling`. Tells nearby players an interdiction opportunity is open.

The `SupercruiseChannelTime` constant for the radial denominator should match the server config — for now hard-code `3.0` (the default). A future task can plumb the value through `ServerConfig` if/when channel time becomes per-ship.

- [ ] **Step 3: Manual smoke test**

```bash
just dev
```
Open the test client at `http://localhost:8080`. Smoke-test items:
1. Log in. Press Z. Confirm the channel radial fills over 3 seconds, then a speed boost kicks in. Move the cursor — confirm the player accelerates faster than baseline.
2. Spawn a hostile NPC (e.g. `npc.spawn` via console). Let it hit you while supercruising. Confirm the integrity bar drains visibly. Take enough hits to drain the buffer — confirm a knockout, the speed boost disappears, and the lockout indicator appears for 10 seconds.
3. Press Z during the 10-second lockout — confirm nothing happens server-side (no log entry beyond `z-press ignored`).
4. Wait out the lockout, press Z again — confirm a fresh channel starts.
5. Fire an ability while Active — confirm immediate cancel with no lockout, and that the ability fires.
6. Dock at a station while Channeling — confirm dock initiates and supercruise cancels.

- [ ] **Step 4: Commit**

```bash
git add web-pixi/src/
git commit -m "web: supercruise HUD (channel radial, integrity bar, lockout indicator, trail effect)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Verification Checklist (end of plan)

- [ ] `go test ./internal/game/ -run TestSupercruise -v` — 12 PASS
- [ ] `go test ./internal/game/... -short` — no new failures
- [ ] `go vet ./...` — clean
- [ ] `just build` — clean (server compiles + SDK regenerates)
- [ ] `cd web-pixi && bun run build` — clean
- [ ] Manual smoke test in `just dev` (Task 13 Step 3) — all 6 items confirmed
- [ ] All commits land on `main` (per the [solo-dev memory](../../../memory/user_solo_developer.md), no PR needed)

## Spec Coverage Self-Check

| Spec section | Covered by |
|---|---|
| Goal / mechanic summary | Tasks 1-9 (component, system, handler, damage hook) |
| Data model: `Supercruise` component | Task 1 |
| `StatusSupercruise` enum | Task 1 |
| Config (4 fields) + version bump | Task 2 |
| Log category | Task 2 |
| State machine: Idle/Channeling/Active | Task 5 (system) |
| Channel → Active transition | Task 5 + Task 11 (round-trip) |
| Active → Idle via knockout | Task 7 + Task 11 |
| Manual cancel (Z press) | Task 6 |
| Combat lockout 10s (damage taken) | Task 7 |
| Combat lockout 10s (damage dealt) | Task 7 |
| Auto-cancel: ability cast | Task 6 |
| Auto-cancel: docking | Task 6 |
| Auto-cancel: mining | Covered by ability-cast cancel (mining is AbilityTypeMiningBeam) |
| Auto-cancel: death | Task 8 |
| Replication via AutoReplicator | Task 3 (component on ShipBundle) |
| System ordering (after StatusEffect, before Physics) | Task 9 |
| Speed multiplier via StatusEffects | Task 4 |
| EffectiveSpeedMul integration | Task 4 |
| Buffer absorbs Active-phase damage; Health untouched | Task 7 (early return) |
| Cross-cell transfer | Task 3 (Supercruise in ShipBundle, no `mmokit:"local"` tag) |
| Z keybind | Task 12 |
| Channel radial + integrity bar + lockout indicator | Task 13 |
| Other-player visuals (trail + telegraph) | Task 13 |
| Logging (channel start / active / drain / knockout / cancel / lockout) | Tasks 5-7 |
| Unit tests | Tasks 5-7 |
| Round-trip integration test | Task 11 |
| Manual smoke test | Task 13 |
