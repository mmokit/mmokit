# mmokit Damage + Mining Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate the cross-cell Damage and Mining-extract paths off the legacy `CrossCellAction` codec onto the new typed `mmokit.Send`/`Handle` API. Lands the supporting `HandleAll` / `OnTickEachAll` Process-level wrappers so handlers auto-replay onto stages created by future cell splits.

**Architecture:** Add a Process-level stage-init registry (analogous to existing `RegisterKindSpec`). Re-expose `Handle` / `OnTickEach` etc. via Process-level wrappers that register against the registry. Define game-side typed messages (`Damage`, `MineExtract`) in `internal/game/`; register their handlers from `GameSetup` via the new wrappers; replace the `if isReplica { sendCrossNodeX } else { ApplyX }` branches in `internal/game/system_ability.go` with `target.Send(&Damage{...})`. AoI-broadcast for client visuals is deferred — the migrated handlers continue to enqueue `gamepb.AbilityCastResultMsg` via the existing per-cell `pendingAbilityEvents` path on both source and dest cells, with the source-side enqueue moved to a small game-helper wrapper.

**Tech Stack:** Same as foundation. Builds on `pkg/mmokit/` Entity / Get/Set / Spawn / Send / Handle from the foundation branch (now merged on main).

**Spec:** [docs/superpowers/specs/2026-05-03-entity-message-passing-design.md](../specs/2026-05-03-entity-message-passing-design.md)

**Predecessor plan:** [docs/superpowers/plans/2026-05-04-mmokit-entity-message-api.md](2026-05-04-mmokit-entity-message-api.md)

---

## File Structure

**New files (`pkg/mmokit/`):**
- `messaging_all.go` — `HandleAll[M]` Process-level wrapper
- `tick_all.go` — `OnWorldTickAll`, `OnTickAll[T]`, `OnTickEachAll[B]` Process-level wrappers
- `messaging_all_test.go` — Process-level handler auto-replay test (must verify post-split registration)
- `tick_all_test.go` — Process-level tick auto-replay test

**New files (`internal/game/`):**
- `verb_damage.go` — `Damage` message type, handler registration, `gw.Damage(...)` helper
- `verb_mining.go` — `MineExtract` message type, handler registration, `gw.MineExtract(...)` helper
- `verb_damage_test.go` — same-cell + cross-cell damage flow
- `verb_mining_test.go` — same-cell + cross-cell mining flow

**Modified files (`pkg/universe/`):**
- `coordinator.go` — add `OnStageInit(fn func(*Stage))` accessor + replay during cell creation (initial + split). Implementation mirrors `RegisterKindSpec` / `RealizeKindSpecs`.

**Modified files (`internal/game/`):**
- `system_ability.go` — replace damage / mining-extract branches with `gw.Damage(...)` / `gw.MineExtract(...)`. Delete `sendCrossNodeDamage`, `sendCrossNodeDamageWithBonus`, `sendCrossNodeMining` helpers.
- `factory.go` (or `GameSetup`) — register the new verb handlers via `mmokit.HandleAll`.
- `action_codec.go` — delete `DamageAction`, `DamageResult`, `MarshalDamageAction`, `UnmarshalDamageAction`, `MarshalDamageResult`, `UnmarshalDamageResult`, `MiningAction`, `MiningResult`, `MarshalMiningAction`, `UnmarshalMiningAction`, `MarshalMiningResult`, `UnmarshalMiningResult`, plus the `ActionDamage` / `ActionMining` opcode constants. Keep the `StatusEffectAction` codec — that verb migrates in a later plan.
- `game.go` — delete the `ActionDamage` and `ActionMining` cases from `HandleCrossCellAction` and `HandleActionResult`.

**Test cleanup:**
- `cross_cell_combat_test.go` — the regression tests we wrote during the May 3 bug fix exercise the legacy `HandleCrossCellAction` path. Two tests (`TestHandleCrossCellAction_Damage_EnqueuesAnimation`, `TestStatusEffectAction_Roundtrip`) become redundant once Damage migrates. Delete the Damage one and rewrite the lock-visibility one against the new path; keep the StatusEffect tests until the StatusEffect verb migrates in Plan E.

---

## Phase 1: Process-level wrappers

### Task 1.1: `Process.OnStageInit` registry

**Files:**
- Modify: `pkg/universe/coordinator.go`
- Test: `pkg/universe/coordinator_test.go` (new test, append if file exists)

- [ ] **Step 1: Locate the existing `RegisterKindSpec` machinery**

```bash
grep -n "kindSpecs\|RegisterKindSpec\|RealizeKindSpecs" pkg/universe/coordinator.go
```

You'll find:
- A `kindSpecs []kindSpec` field on `Process`
- `RegisterKindSpec(realize func(*Stage))` adds to it
- `RealizeKindSpecs(stage *Stage)` invokes every registered realizer against the given stage
- The realize loop is invoked at `coordinator.go:2029` for newly-built/split cells

The new `OnStageInit` follows the exact same pattern but for non-kind-related per-stage setup (handler registration, tick callbacks, etc.).

- [ ] **Step 2: Write the failing test**

Append to `pkg/universe/coordinator_test.go`, or create it:

```go
package universe_test

import (
    "testing"
    pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

func TestOnStageInit_FiresOnEveryStage(t *testing.T) {
    p := pkguniverse.New(pkguniverse.Config{
        CellsX:   2,
        CellsY:   1,
        TickRate: 20,
        Headless: true,
    })

    var seen []*pkguniverse.Stage
    p.OnStageInit(func(s *pkguniverse.Stage) {
        seen = append(seen, s)
    })

    p.Build()

    if len(seen) != 2 {
        t.Fatalf("OnStageInit fired %d times, want 2 (one per cell)", len(seen))
    }
}
```

- [ ] **Step 3: Run test (fails — `OnStageInit` undefined)**

```bash
go test ./pkg/universe/ -run TestOnStageInit -v
```

- [ ] **Step 4: Implement `OnStageInit` on Process**

In `pkg/universe/coordinator.go`, near `RegisterKindSpec`:

```go
// OnStageInit registers fn to fire once per Stage created by this Process —
// initial cells from Build() and stages created later by cell splits.
// Use for per-stage setup like handler registration (mmokit.Handle) and tick
// callbacks (mmokit.OnWorldTick) that must be present on every cell.
//
// Mirrors RegisterKindSpec's auto-replay pattern but for non-kind setup.
// Safe to call multiple times — each fn fires for every stage; safe before
// or after Build() (any future stage created by a split fires registered
// fns at creation time).
func (c *Process) OnStageInit(fn func(*Stage)) {
    c.stageInitHooks = append(c.stageInitHooks, fn)
    // For stages already created (caller registered late), fire fn now.
    for _, cell := range c.allCells() {
        fn(cell.Stage)
    }
}

// runStageInitHooks invokes every registered OnStageInit hook against stage.
// Called from the cell-creation paths (initial Build + split executor).
func (c *Process) runStageInitHooks(stage *Stage) {
    for _, fn := range c.stageInitHooks {
        fn(stage)
    }
}
```

Add `stageInitHooks []func(*Stage)` to the `Process` struct (find the field block near `kindSpecs`).

`allCells()` may not exist; if not, replace the catch-up loop with a simpler approach: iterate `c.cells` map. Find via grep — the field is likely `cells map[CellID]*Cell` or similar. If iteration semantics get fiddly, drop the catch-up loop entirely and require callers to register before `Build()`. Document that constraint in the docstring.

- [ ] **Step 5: Wire into the cell-build path**

Find where new Stages are created and `RealizeKindSpecs` is called. From the grep, the loop is at `coordinator.go:2029` and there's a similar call site in `cell_transfer_executor.go` (cell split path).

For each call site, add a sibling call:

```go
c.RealizeKindSpecs(node.Stage)
c.runStageInitHooks(node.Stage)  // NEW
```

There are likely 2-3 call sites (initial Build, split commit, possibly migrate commit). Find them all with:

```bash
grep -n "RealizeKindSpecs" pkg/universe/
```

- [ ] **Step 6: Run test, expect PASS**

```bash
go test ./pkg/universe/ -run TestOnStageInit -v
```

- [ ] **Step 7: Add a split-coverage test**

Append to `coordinator_test.go`:

```go
func TestOnStageInit_FiresOnSplitChildren(t *testing.T) {
    p := pkguniverse.New(pkguniverse.Config{
        CellsX:   1,
        CellsY:   1,
        TickRate: 20,
        Headless: true,
        DynamicPartitioning: pkguniverse.DefaultPartitionConfig(),
    })

    var firedFor []pkguniverse.CellID
    p.OnStageInit(func(s *pkguniverse.Stage) {
        firedFor = append(firedFor, s.CellID())
    })

    p.Build()
    if len(firedFor) != 1 {
        t.Fatalf("after Build: fired %d times, want 1", len(firedFor))
    }

    // Split the root cell.
    rootCell := pkguniverse.CellID{X: 0, Y: 0, Depth: 0}
    if err := p.SplitCell(rootCell, true /* bypass cooldown */); err != nil {
        t.Fatalf("SplitCell: %v", err)
    }

    // After split, the parent retires and 4 children appear.
    // OnStageInit should fire once per child.
    if got := len(firedFor); got != 5 {
        t.Fatalf("after split: fired %d times total, want 5 (1 initial + 4 children)", got)
    }
}
```

`pkguniverse.DefaultPartitionConfig()` and `Process.SplitCell` may have slightly different names — find via grep:

```bash
grep -rn "DefaultPartitionConfig\|SplitCell\b" pkg/universe/ | head -5
```

If the test setup is too involved, simplify or skip the split test — the basic OnStageInit_FiresOnEveryStage test is the load-bearing one. Note in the report which tests landed.

- [ ] **Step 8: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/coordinator_test.go
git commit -m "feat(universe): Process.OnStageInit auto-replay registry for per-stage setup"
```

---

### Task 1.2: `mmokit.HandleAll[M]` wrapper

**Files:**
- Create: `pkg/mmokit/messaging_all.go`
- Create: `pkg/mmokit/messaging_all_test.go`

- [ ] **Step 1: Write the test**

```go
// pkg/mmokit/messaging_all_test.go
package mmokit_test

import (
    "testing"
    "github.com/mmokit/mmokit/pkg/mmokit"
    pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

type allPing struct{ N int }

func TestHandleAll_RegistersOnAllStages(t *testing.T) {
    p := pkguniverse.New(pkguniverse.Config{
        CellsX:   2,
        CellsY:   1,
        TickRate: 20,
        Headless: true,
    })

    fired := map[pkguniverse.CellID]int{}
    mmokit.HandleAll(p, func(target mmokit.Entity, msg *allPing) {
        fired[target.Stage().CellID()]++  // Stage() accessor — see implementation note
    })

    p.Build()

    // Spawn an entity on each cell, send a ping, verify per-cell fire.
    cells := p.AllCells() // list cell handles — verify exact API name via grep
    for _, c := range cells {
        registerTestKind(t, c.Stage)
        e := mmokit.Spawn(c.Stage, testKindID, mmokit.Pos{})
        e.Send(&allPing{N: 1})
    }

    if len(cells) == 0 || len(fired) != len(cells) {
        t.Fatalf("HandleAll fired on %d cells, want %d", len(fired), len(cells))
    }
}
```

`Entity.Stage()` may not exist publicly. If it doesn't, use a different way to identify which stage the handler ran on — e.g., capture by closure with a per-stage counter, or expose `Entity.Stage()`. Implementation note: a `Stage()` accessor on Entity is reasonable to add (returns `*pkguniverse.Stage`); guard with a docstring that says "rarely needed in game code, used for diagnostics."

- [ ] **Step 2: Implement HandleAll**

```go
// pkg/mmokit/messaging_all.go
package mmokit

import (
    pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// HandleAll registers fn as the handler for messages of type M on every
// Stage owned by world — both stages that exist now and stages created
// later by dynamic partitioning (cell splits, migrations).
//
// This is the common case for game-defined message handlers; prefer it
// over the per-stage Handle unless you have a specific reason to register
// only on one stage.
func HandleAll[M any](world *pkguniverse.Process, fn func(target Entity, msg *M)) {
    world.OnStageInit(func(stage *pkguniverse.Stage) {
        Handle(stage, fn)
    })
}
```

(If you need to add `Entity.Stage()` for the test, do that in `pkg/mmokit/entity.go`:

```go
// Stage returns the Stage this Entity is bound to. Rare — used for
// diagnostics and for tests that need to identify the cell.
func (e Entity) Stage() *pkguniverse.Stage { return e.stage }
```

This is a small addition; commit it in the same task.)

- [ ] **Step 3: Run test, fix until green**

```bash
go test ./pkg/mmokit/ -run TestHandleAll -v
```

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/messaging_all.go pkg/mmokit/messaging_all_test.go pkg/mmokit/entity.go
git commit -m "feat(mmokit): HandleAll[M] auto-replays handler registration on every Stage"
```

---

### Task 1.3: `OnWorldTickAll`, `OnTickAll[T]`, `OnTickEachAll[B]`

**Files:**
- Create: `pkg/mmokit/tick_all.go`
- Create: `pkg/mmokit/tick_all_test.go`

- [ ] **Step 1: Write the test (one test exercises all three)**

```go
// pkg/mmokit/tick_all_test.go
package mmokit_test

import (
    "testing"
    "github.com/mmokit/mmokit/pkg/mmokit"
    pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

func TestOnWorldTickAll_FiresOnAllStages(t *testing.T) {
    p := pkguniverse.New(pkguniverse.Config{
        CellsX: 2, CellsY: 1, TickRate: 20, Headless: true,
    })

    fired := map[pkguniverse.CellID]int{}
    mmokit.OnWorldTickAll(p, func(stage *pkguniverse.Stage, dt float32) {
        fired[stage.CellID()]++
    })

    p.Build()

    // Drive 3 ticks per stage manually.
    for _, c := range p.AllCells() {
        runTicks(t, c.Stage, 3)
    }

    if len(fired) != 2 {
        t.Fatalf("WorldTick fired on %d cells, want 2", len(fired))
    }
    for cid, n := range fired {
        if n != 3 {
            t.Errorf("cell %s: %d ticks, want 3", cid, n)
        }
    }
}
```

- [ ] **Step 2: Implement**

```go
// pkg/mmokit/tick_all.go
package mmokit

import (
    pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// OnWorldTickAll registers fn to fire once per simulation tick on every
// Stage owned by world — both initial cells and cells created by dynamic
// partitioning. fn receives the stage so cross-stage state can be keyed by
// CellID.
func OnWorldTickAll(world *pkguniverse.Process, fn func(stage *pkguniverse.Stage, dt float32)) {
    world.OnStageInit(func(stage *pkguniverse.Stage) {
        OnWorldTick(stage, func(dt float32) { fn(stage, dt) })
    })
}

// OnTickAll registers fn to fire once per tick for every entity with
// component T on every Stage owned by world.
func OnTickAll[T any](world *pkguniverse.Process, fn func(e Entity, dt float32)) {
    world.OnStageInit(func(stage *pkguniverse.Stage) {
        OnTick[T](stage, fn)
    })
}

// OnTickEachAll registers fn to fire once per tick for every entity matching
// bundle B on every Stage owned by world.
func OnTickEachAll[B any](world *pkguniverse.Process, fn func(e Entity, b *B, dt float32)) {
    world.OnStageInit(func(stage *pkguniverse.Stage) {
        OnTickEach[B](stage, fn)
    })
}
```

- [ ] **Step 3: Run + commit**

```bash
go test ./pkg/mmokit/ -run TestOnWorldTickAll -v
git add pkg/mmokit/tick_all.go pkg/mmokit/tick_all_test.go
git commit -m "feat(mmokit): OnWorldTickAll / OnTickAll[T] / OnTickEachAll[B] Process-level wrappers"
```

---

### Task 1.4: Update `pkg/mmokit/doc.go` to recommend the All-suffix variants

- [ ] **Step 1: Edit `pkg/mmokit/doc.go`**

In the "Per-stage lifecycle" section, replace the `coord.OnInit(...)` example with:

```go
// Most game code calls the All-suffixed variants — Handle, OnTick, etc.
// each have an All variant that auto-replays onto every Stage:
//
//	mmokit.HandleAll(world, applyDamage)
//	mmokit.OnTickEachAll[ShieldRegen](world, regenShields)
//
// These cover the typical "register at process startup, run on every cell"
// case. The non-All variants register against a single Stage — use them
// only when you specifically want per-stage scoping.
```

- [ ] **Step 2: Commit**

```bash
git add pkg/mmokit/doc.go
git commit -m "docs(mmokit): recommend HandleAll / OnTickEachAll as the default registration form"
```

---

## Phase 2: Damage migration

### Task 2.1: Define the `Damage` typed message

**Files:**
- Create: `internal/game/verb_damage.go`

- [ ] **Step 1: Write the message type**

```go
// internal/game/verb_damage.go
package game

import (
    "github.com/mmokit/mmokit/pkg/mmokit"
)

// Damage is a typed cross-cell-aware message: deal damage to an entity.
// Routed via mmokit.Send; the registered handler runs on whichever cell
// owns the target, applies shield/health math, and fills the result fields
// (Dealt, Killed) before the call returns.
//
// This is a game-defined message — the framework provides the routing,
// the game owns the formula.
type Damage struct {
    // Request fields
    Amount      float32
    BonusDamage float32       // applied if target shield is depleted
    Slot        uint8         // ability slot (for visual)
    AbilityType uint8         // ability type enum (for visual)
    Source      mmokit.Entity // attacker (NetID-resolvable across cells)

    // Result fields — filled by handler before broadcast / reply
    Dealt  float32
    Killed bool // target.Health.Current dropped to 0 because of this hit
}
```

(No tests yet — this task is just the type declaration. Tasks 2.2-2.3 add the handler and game helper, then 2.4 migrates callsites.)

- [ ] **Step 2: Verify the file compiles**

```bash
go vet ./internal/game/...
```

(Should be clean — Damage doesn't reference anything else yet.)

---

### Task 2.2: Implement the Damage handler

**Files:**
- Modify: `internal/game/verb_damage.go`

- [ ] **Step 1: Implement**

Append to `verb_damage.go`:

```go
// damageHandler is the canonical damage formula. Runs on the authoritative
// cell for the target. Mutates Health (and Shield, if present), records the
// attacker for kill-attribution, and fills msg.Dealt and msg.Killed for the
// caller's reply path.
func damageHandler(target mmokit.Entity, msg *Damage) {
    h := mmokit.Get[gamecomp.Health](target)
    if h == nil || h.Current <= 0 {
        return // already dead — drop
    }

    final := msg.Amount
    if msg.BonusDamage > 0 {
        s := mmokit.Get[gamecomp.Shield](target)
        if s != nil && s.Current <= 0 {
            final += msg.BonusDamage
        }
    }

    // Existing ApplyDamage handles Dormant guards, shield absorption,
    // damage-tracker updates, etc. We delegate to it rather than duplicating.
    gw := gameWorldOfEntity(target)
    if gw == nil {
        return
    }
    msg.Dealt = gw.ApplyDamage(rawHandle(target), final, msg.Source.NetID())
    msg.Killed = h.Current <= 0
}

// gameWorldOfEntity returns the *GameWorld bound to the entity's stage.
// Reaches across the universe boundary via the Stage's GameWorld() accessor.
// If that doesn't exist, see implementation note at end of this task.
func gameWorldOfEntity(e mmokit.Entity) *GameWorld {
    stage := e.Stage()
    if stage == nil { return nil }
    if gw, ok := stage.GameWorld().(*GameWorld); ok {
        return gw
    }
    return nil
}

// rawHandle returns the local ECS entity handle. Escape hatch for code that
// must call into legacy ApplyDamage; expected to disappear when ApplyDamage
// is itself rewritten to take an Entity.
func rawHandle(e mmokit.Entity) ecs.Entity {
    // Use mmokit.Get[component.NetworkID] indirection? Cleanest is to add
    // an unexported accessor on Entity. For now, rely on the fact that the
    // entity is local on this cell (handler runs on dest = authoritative)
    // and look up via the stage's NetID index.
    //
    // Implementation: add a small Entity.Handle() *ecs.Entity accessor (or
    // similar) that returns the cached handle. Document as escape-hatch.
    return e.Handle()
}
```

NOTE: `Entity.Handle() ecs.Entity` and `Entity.Stage() *pkguniverse.Stage` need to be added in `pkg/mmokit/entity.go` (Stage() was added in Task 1.2; if not, add it). `Stage.GameWorld() any` should already exist — search via:

```bash
grep -n "GameWorld\(\)\|func.*GameWorld" pkg/universe/stage.go
```

If `Stage.GameWorld()` doesn't exist, add it as a small accessor returning the world value passed at NewStage time (verify the world-pointer is stored on Stage; if not, this lookup needs another path).

- [ ] **Step 2: Add the Entity escape-hatch accessors if needed**

```go
// pkg/mmokit/entity.go — append

// Handle returns the cached local ECS handle. Returns the zero ecs.Entity
// if the cache is unset; callers that need a guaranteed-live handle should
// check Alive() first. Escape hatch for code that must call legacy ECS
// APIs that take ecs.Entity directly.
func (e Entity) Handle() ecs.Entity {
    if e.cached != (ecs.Entity{}) {
        return e.cached
    }
    return e.resolveHandle()
}
```

- [ ] **Step 3: Verify it compiles**

```bash
go vet ./internal/game/... ./pkg/mmokit/...
```

- [ ] **Step 4: Commit**

```bash
git add internal/game/verb_damage.go pkg/mmokit/entity.go
git commit -m "feat(game): damageHandler + Entity.Handle / Entity.Stage escape hatches"
```

---

### Task 2.3: Register the handler + add the game helper

**Files:**
- Modify: `internal/game/verb_damage.go`
- Modify: `internal/game/factory.go` (registration site — find via grep `func GameSetup`)

- [ ] **Step 1: Add `RegisterDamageVerb` and the helper**

Append to `verb_damage.go`:

```go
// RegisterDamageVerb wires the damage handler onto every Stage owned by
// the given Process. Call once at startup (typically from GameSetup).
func RegisterDamageVerb(p *mmokit.Process) {
    mmokit.HandleAll(p, damageHandler)
}

// Damage is the game-side helper for damaging another entity. Handles the
// caller-side animation enqueue (so the caster's client sees the cast fire
// on the same tick as the input) and routes the damage application via
// target.Send — which handles cross-cell routing transparently.
//
// Same-cell: handler runs synchronously, msg.Dealt is populated by the time
// this returns. Cross-cell: handler runs on the target's cell next tick;
// the caster's local AbilityCastResultMsg fires immediately with Dealt=0.
func (gw *GameWorld) Damage(caster, target mmokit.Entity, amount, bonusDmg float32, slot, abilityType uint8) {
    if !target.Alive() {
        return
    }

    msg := Damage{
        Amount:      amount,
        BonusDamage: bonusDmg,
        Slot:        slot,
        AbilityType: abilityType,
        Source:      caster,
    }

    // Source-cell animation enqueue (immediate caster feedback).
    // Dealt is filled in on dest cell; the caster-side event uses the
    // request amount as a placeholder.
    mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{
        Slot:        uint32(slot),
        Success:     true,
        TargetId:    target.NetID(),
        DamageDealt: amount, // placeholder; corrected by Health replication
        CasterId:    caster.NetID(),
        AbilityType: uint32(abilityType),
    })

    target.Send(&msg)

    // After Send returns: if same-cell, msg.Dealt is now correct (handler
    // ran sync). If cross-cell, msg.Dealt is still 0 — the dest cell's
    // separate enqueue (in damageHandler, see Task 2.2 — actually we're
    // moving the dest enqueue here in Step 2 below) handles the dest
    // viewers.
}
```

- [ ] **Step 2: Move the dest-cell animation enqueue into the handler**

The cleanest design has the handler emit the dest-cell animation event so it carries the actual Dealt value. Update `damageHandler` in `verb_damage.go`:

```go
func damageHandler(target mmokit.Entity, msg *Damage) {
    h := mmokit.Get[gamecomp.Health](target)
    if h == nil || h.Current <= 0 {
        return
    }
    final := msg.Amount
    if msg.BonusDamage > 0 {
        if s := mmokit.Get[gamecomp.Shield](target); s != nil && s.Current <= 0 {
            final += msg.BonusDamage
        }
    }
    gw := gameWorldOfEntity(target)
    if gw == nil { return }
    msg.Dealt = gw.ApplyDamage(rawHandle(target), final, msg.Source.NetID())
    msg.Killed = h.Current <= 0

    // Dest-cell animation enqueue with actual Dealt damage.
    mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{
        Slot:        uint32(msg.Slot),
        Success:     true,
        TargetId:    target.NetID(),
        DamageDealt: msg.Dealt,
        CasterId:    msg.Source.NetID(),
        AbilityType: uint32(msg.AbilityType),
    })
}
```

For SAME-cell, `gw.Damage(...)` enqueues twice (once in the helper, once in the handler — they're on the same cell and both go to the same Queue). Detect this and skip the source-cell enqueue when local:

In the helper, replace the source-side enqueue with:

```go
// Source-cell enqueue ONLY when target is on a different cell. Same-cell
// dispatch will fire the handler synchronously below, which enqueues
// directly.
if !target.Local() {
    mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{ /* placeholder dealt = amount */ })
}
target.Send(&msg)
```

`Entity.Local()` was added in Phase 1 of the foundation (returns true iff the authoritative copy is on this cell). Use it.

- [ ] **Step 3: Wire registration into GameSetup**

```bash
grep -n "func GameSetup" internal/game/
```

Find `GameSetup(coord *mmokit.Process)` (or similar). Add a call:

```go
RegisterDamageVerb(coord)
```

Place it near other system / kind registrations.

- [ ] **Step 4: Verify it compiles**

```bash
go vet ./internal/game/...
```

(Tests will be broken because callsites still use the legacy paths; we fix that in Task 2.4.)

- [ ] **Step 5: Commit**

```bash
git add internal/game/verb_damage.go internal/game/factory.go
git commit -m "feat(game): RegisterDamageVerb + gw.Damage helper (handler not yet called)"
```

---

### Task 2.4: Migrate `system_ability.go` damage callsites

**Files:**
- Modify: `internal/game/system_ability.go`

- [ ] **Step 1: Locate the legacy callsites**

```bash
grep -n "sendCrossNodeDamage\|sendCrossNodeDamageWithBonus\|gw.ApplyDamage" internal/game/system_ability.go
```

The damage call paths are in the `executeAbility` switch:
- `case item.AbilityTypePulseLaser, item.AbilityTypePulseBarrage, item.AbilityTypeRailShot, item.AbilityTypeIonOverload, item.AbilityTypePlasmaBolt:` — base damage
- `case item.AbilityTypePiercingRound, item.AbilityTypePlasmaTorpedo:` — base + bonus damage if shields down

Both currently branch on `s.isReplica(lock.TargetEntity)`.

- [ ] **Step 2: Replace the first damage block**

Find the block beginning around `system_ability.go:166` (`case item.AbilityTypePulseLaser, ...`):

```go
case item.AbilityTypePulseLaser, item.AbilityTypePulseBarrage,
    item.AbilityTypeRailShot, item.AbilityTypeIonOverload, item.AbilityTypePlasmaBolt:
    if gw.eng.ECS.Alive(lock.TargetEntity) {
        if s.isReplica(lock.TargetEntity) {
            s.sendCrossNodeDamage(action.casterNetID, lock.TargetEntity, params.Damage, action.slot, uint8(params.Type))
            sentCrossNode = true
        } else {
            damageDealt = gw.ApplyDamage(lock.TargetEntity, params.Damage, action.casterNetID)
        }
        targetNetID = lock.TargetNetID
    }
```

Replace with:

```go
case item.AbilityTypePulseLaser, item.AbilityTypePulseBarrage,
    item.AbilityTypeRailShot, item.AbilityTypeIonOverload, item.AbilityTypePlasmaBolt:
    target := mmokit.EntityByNetID(gw.Stage, lock.TargetNetID)
    if target.Alive() {
        caster := mmokit.EntityByNetID(gw.Stage, action.casterNetID)
        gw.Damage(caster, target, params.Damage, 0, action.slot, uint8(params.Type))
        targetNetID = lock.TargetNetID
        // damageDealt is no longer threaded out — animation events fire
        // from inside gw.Damage (source side) and the handler (dest side).
        // sentCrossNode is also gone; gw.Damage owns the routing decision.
    }
```

- [ ] **Step 3: Replace the second damage block**

Around `system_ability.go:181-201` (the bonus-damage variant):

```go
case item.AbilityTypePiercingRound, item.AbilityTypePlasmaTorpedo:
    if gw.eng.ECS.Alive(lock.TargetEntity) {
        if s.isReplica(lock.TargetEntity) {
            s.sendCrossNodeDamageWithBonus(...)
            sentCrossNode = true
        } else {
            damage := params.Damage
            if gw.C.Shield.HasAll(lock.TargetEntity) {
                shield := gw.C.Shield.Get(lock.TargetEntity)
                if shield.Current <= 0 { damage += params.BonusDamage }
            }
            damageDealt = gw.ApplyDamage(lock.TargetEntity, damage, action.casterNetID)
        }
        targetNetID = lock.TargetNetID
    }
```

Replace with:

```go
case item.AbilityTypePiercingRound, item.AbilityTypePlasmaTorpedo:
    target := mmokit.EntityByNetID(gw.Stage, lock.TargetNetID)
    if target.Alive() {
        caster := mmokit.EntityByNetID(gw.Stage, action.casterNetID)
        gw.Damage(caster, target, params.Damage, params.BonusDamage, action.slot, uint8(params.Type))
        targetNetID = lock.TargetNetID
    }
```

The shield-vs-bonus check moved into `damageHandler` (Task 2.2).

- [ ] **Step 4: Update the post-switch animation enqueue**

Around `system_ability.go:356-365`:

```go
if fired && !sentCrossNode {
    mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{ ... })
}
```

For the migrated damage cases, the helper + handler already enqueue the animation events. The remaining cases that hit this block (mining toggle, status effects, etc.) still need it — but ONLY for those non-damage cases.

Track which cases still need it. For now, you can leave the block alone and ensure the migrated damage cases set `fired = false` to skip it — OR cleanly restructure. The simpler fix:

Set `sentCrossNode = true` after the migrated `gw.Damage` call so the post-switch block skips it:

```go
gw.Damage(caster, target, params.Damage, 0, action.slot, uint8(params.Type))
targetNetID = lock.TargetNetID
sentCrossNode = true  // gw.Damage owns animation enqueue; skip the legacy post-switch block
```

(Use the existing `sentCrossNode` flag to suppress the legacy enqueue. Comment the line.)

- [ ] **Step 5: Verify build + run all game tests**

```bash
go vet ./internal/game/...
go test ./internal/game/...
```

Tests in `cross_cell_combat_test.go` may FAIL — those tests exercise the legacy `HandleCrossCellAction` path and the old animation enqueue site. We address them in Task 2.6.

- [ ] **Step 6: Commit (with tests possibly broken — note that)**

```bash
git add internal/game/system_ability.go
git commit -m "feat(game): migrate damage callsites in system_ability to gw.Damage"
```

If the cross_cell_combat tests are red, that's expected — they're tested against the legacy path.

---

### Task 2.5: Delete legacy damage codec + dispatch

**Files:**
- Modify: `internal/game/system_ability.go` (delete sendCrossNodeDamage / sendCrossNodeDamageWithBonus)
- Modify: `internal/game/action_codec.go` (delete DamageAction, DamageResult, marshallers)
- Modify: `internal/game/game.go` (delete ActionDamage cases)

- [ ] **Step 1: Delete the legacy helper functions**

In `internal/game/system_ability.go`, delete:

```go
func (s *AbilitySystem) sendCrossNodeDamage(...)
func (s *AbilitySystem) sendCrossNodeDamageWithBonus(...)
```

- [ ] **Step 2: Delete the codec**

In `internal/game/action_codec.go`, delete:
- `const ActionDamage` (line 13)
- `type DamageAction struct {...}`
- `func MarshalDamageAction`
- `func UnmarshalDamageAction`
- `type DamageResult struct {...}`
- `func MarshalDamageResult`
- `func UnmarshalDamageResult`

Keep:
- `const ActionStatusEffect`, `const ActionMining` — those verbs migrate in Plan E and this plan respectively
- `StatusEffectAction` codec — still in use until Plan E

After Mining migration in Phase 3, also delete the Mining* types from `action_codec.go`. For now, leave them.

- [ ] **Step 3: Delete the ActionDamage dispatch cases**

In `internal/game/game.go`:

- Find `case ActionDamage:` in `HandleCrossCellAction` (around line 281). Delete the entire case block.
- Find `case ActionDamage:` in `HandleActionResult` (around line 452). Delete.

- [ ] **Step 4: Verify build**

```bash
go vet ./internal/game/...
```

If anything still references the deleted symbols, fix the callers (probably in tests).

- [ ] **Step 5: Run game tests**

```bash
go test ./internal/game/...
```

Some `cross_cell_combat_test.go` tests will fail because they call `MarshalDamageAction` / `HandleCrossCellAction` with `ActionDamage`. Those tests get rewritten in Task 2.6.

- [ ] **Step 6: Commit (with broken tests still pending Task 2.6)**

```bash
git add internal/game/system_ability.go internal/game/action_codec.go internal/game/game.go
git commit -m "refactor(game): delete legacy DamageAction codec + ActionDamage dispatch cases"
```

---

### Task 2.6: Update / rewrite `cross_cell_combat_test.go`

**Files:**
- Modify: `internal/game/cross_cell_combat_test.go`

- [ ] **Step 1: Read the existing tests**

```bash
cat internal/game/cross_cell_combat_test.go
```

Five tests live there from the May 3 bug fix:
1. `TestNetworkSystem_LockedByPopulated_FromReplicaLocker` — exercises the reverse-lock map. Stays as-is; not damage-related.
2. `TestNetworkSystem_LockedByCleared_WhenLockerStops` — same.
3. `TestHandleCrossCellAction_Damage_EnqueuesAnimation` — exercises the deleted ActionDamage path. **Delete or rewrite.**
4. `TestHandleCrossCellAction_StatusEffect_EnqueuesAnimation` — exercises ActionStatusEffect. **Keep as-is** (StatusEffect migrates in Plan E).
5. `TestStatusEffectAction_Roundtrip` — codec roundtrip for the still-existing StatusEffect codec. **Keep.**

- [ ] **Step 2: Delete `TestHandleCrossCellAction_Damage_EnqueuesAnimation`**

The test as written calls `gw.HandleCrossCellAction(action)` with `ActionDamage`, which no longer exists. The replacement coverage is:

- The new path is exercised by integration tests in Task 2.7 (cross-cell damage via `gw.Damage`).
- Delete this test cleanly.

- [ ] **Step 3: Verify the surviving tests still pass**

```bash
go test ./internal/game/ -run "TestNetworkSystem_LockedBy|TestHandleCrossCellAction_StatusEffect|TestStatusEffectAction" -v
```

All four should PASS.

- [ ] **Step 4: Run the full game test suite**

```bash
go test ./internal/game/...
```

Should be green now.

- [ ] **Step 5: Commit**

```bash
git add internal/game/cross_cell_combat_test.go
git commit -m "test(game): delete legacy damage cross-cell test (path migrated to gw.Damage)"
```

---

### Task 2.7: New integration test for cross-cell damage via `gw.Damage`

**Files:**
- Create: `internal/game/verb_damage_test.go`

- [ ] **Step 1: Write the test**

```go
// internal/game/verb_damage_test.go
package game

import (
    "testing"
    "time"

    gamecomp "github.com/mmokit/mmokit/internal/component"
    "github.com/mmokit/mmokit/pkg/mmokit"
)

func TestDamage_SameCell_AppliesViaSend(t *testing.T) {
    gw, _ := newTestGameWorld()

    // Spawn target with Health.
    target := newTestShipEntity(t, gw, 100, 50)  // helper: Health=100, Shield=50; see below
    caster := newTestShipEntity(t, gw, 100, 50)

    // Register the handler (would normally happen in GameSetup; for unit
    // tests we wire it manually via the per-stage Handle).
    mmokit.Handle(gw.Stage, damageHandler)

    targetE := mmokit.EntityByNetID(gw.Stage, target.NetID)
    casterE := mmokit.EntityByNetID(gw.Stage, caster.NetID)

    gw.Damage(casterE, targetE, 25, 0, 0, 1)

    // Same-cell: handler ran synchronously. Health should be 75.
    h := mmokit.Get[gamecomp.Health](targetE)
    if h == nil || h.Current != 75 {
        t.Fatalf("Health.Current = %v, want 75", h)
    }
}
```

The `newTestShipEntity` helper needs to be available — look in `transfer_test.go` or `testutil_test.go` for existing ship-creation helpers and reuse. If none fits cleanly, create a small one in `verb_damage_test.go` that adds Position, Health, Shield, NetworkID directly via the mappers.

- [ ] **Step 2: Add a cross-cell test (loopback bridge)**

```go
func TestDamage_CrossCell_RoutesAndApplies(t *testing.T) {
    cellA, cellB, drain := newTwoCellLoopback(t)  // helper from pkg/mmokit/testutil_test.go pattern
    
    // Register handler on both stages.
    mmokit.Handle(cellA.Stage, damageHandler)
    mmokit.Handle(cellB.Stage, damageHandler)

    // Spawn target on cell A (authoritative there).
    target := newTestShipEntityOn(t, cellA, 100, 50)
    pushBorderReplicaTo(t, cellA, cellB, target.NetID)

    caster := newTestShipEntityOn(t, cellB, 100, 50)
    casterE := mmokit.EntityByNetID(cellB.Stage, caster.NetID)
    targetE := mmokit.EntityByNetID(cellB.Stage, target.NetID)  // resolves to replica on B

    cellB.GameWorld.Damage(casterE, targetE, 25, 0, 0, 1)

    // Drain the bridge to deliver the cross-cell action.
    drain(100 * time.Millisecond)

    // Authoritative target on cellA: Health should be 75.
    targetOnA := mmokit.EntityByNetID(cellA.Stage, target.NetID)
    h := mmokit.Get[gamecomp.Health](targetOnA)
    if h == nil || h.Current != 75 {
        t.Fatalf("after cross-cell Damage: Health.Current = %v, want 75", h)
    }
}
```

NOTE: `internal/game/` doesn't currently have its own `newTwoCellLoopback`. The helpers exist in `pkg/mmokit/testutil_test.go`, but those are package-private to mmokit_test. You need to either:

- Add the loopback bridge helper to a game-internal testutil (copy from the mmokit version)
- Or skip the cross-cell test if too involved, and rely on the mmokit-level cross-cell test (`TestCrossCellSend_RoutesToAuthoritativeCell`) to prove the routing works in general — the game-level test focuses on the same-cell handler logic.

If you skip the cross-cell game test, document why in the test file's package comment.

- [ ] **Step 3: Run + commit**

```bash
go test ./internal/game/ -run TestDamage -v
git add internal/game/verb_damage_test.go
git commit -m "test(game): same-cell + cross-cell damage flow via gw.Damage"
```

---

## Phase 3: Mining migration

The Mining migration follows the same pattern as Damage. Tasks 3.1 through 3.5 mirror Tasks 2.1 through 2.5. Compress the steps where the pattern is identical; spell out only the parts that differ.

### Task 3.1: Define the `MineExtract` typed message

**Files:**
- Create: `internal/game/verb_mining.go`

- [ ] **Step 1: Write the message type**

```go
// internal/game/verb_mining.go
package game

import (
    "github.com/mmokit/mmokit/pkg/mmokit"
)

// MineExtract is a typed cross-cell-aware mining-extract message. Sent from
// the caster's cell to the asteroid's cell; handler subtracts from Minable,
// marks the asteroid for removal if depleted, and fills the result fields.
//
// Caster-side inventory add and ammunition tracking happen on the caster's
// cell BEFORE Send (optimistic, matches existing behavior). msg.Extracted
// is the actual amount the asteroid had — same as the request unless the
// asteroid is nearly depleted.
type MineExtract struct {
    Caster        mmokit.Entity
    Beam          uint8
    RequestedAmt  float32 // what caster asked for
    Extracted     float32 // result: actual amount removed from asteroid
    Depleted      bool    // result: asteroid hit zero and is being removed
}
```

- [ ] **Step 2: Verify**

```bash
go vet ./internal/game/...
```

---

### Task 3.2: Mining handler + game helper

- [ ] **Step 1: Implement**

Append to `verb_mining.go`:

```go
func mineExtractHandler(target mmokit.Entity, msg *MineExtract) {
    minable := mmokit.Get[gamecomp.Minable](target)
    if minable == nil || minable.Remaining <= 0 {
        return
    }

    extracted := msg.RequestedAmt
    if extracted > minable.Remaining {
        extracted = minable.Remaining
    }
    minable.Remaining -= extracted
    msg.Extracted = extracted
    msg.Depleted = minable.Remaining <= 0

    if msg.Depleted {
        gw := gameWorldOfEntity(target)
        if gw != nil {
            gw.MarkForRemoval(rawHandle(target))
        }
    }
}

// MineExtract is the game-side helper for mining-extract pulses. Mirrors
// gw.Damage's structure: optimistic caster-side inventory add, then Send to
// asteroid. The asteroid's cell handler authoritatively reduces Minable.
func (gw *GameWorld) MineExtract(caster, asteroid mmokit.Entity, beam uint8, requested float32, itemID uint32) {
    if !asteroid.Alive() {
        return
    }
    asteroid.Send(&MineExtract{
        Caster:       caster,
        Beam:         beam,
        RequestedAmt: requested,
    })
    // Note: caller (executeAbility) already did the optimistic inventory
    // add and the local-replica .Remaining decrement before reaching here.
    // Cleaning up that double-update is a follow-up.
}

// RegisterMiningVerb wires the handler onto every Stage owned by p.
func RegisterMiningVerb(p *mmokit.Process) {
    mmokit.HandleAll(p, mineExtractHandler)
}
```

- [ ] **Step 2: Wire into GameSetup**

```go
RegisterDamageVerb(coord)
RegisterMiningVerb(coord)
```

- [ ] **Step 3: Verify**

```bash
go vet ./internal/game/...
```

- [ ] **Step 4: Commit**

```bash
git add internal/game/verb_mining.go internal/game/factory.go
git commit -m "feat(game): RegisterMiningVerb + gw.MineExtract helper"
```

---

### Task 3.3: Migrate the ExtractPulse callsite

**Files:**
- Modify: `internal/game/system_ability.go`

- [ ] **Step 1: Locate the callsite**

```bash
grep -n "AbilityTypeExtractPulse\|sendCrossNodeMining" internal/game/system_ability.go
```

The block lives around `system_ability.go:286-353`. The legacy code:

```go
case item.AbilityTypeExtractPulse:
    // ... range checks, capacity checks, calculate extraction `whole`
    if s.isReplica(laser.Target) {
        // Cross-cell: send action to authoritative node
        s.sendCrossNodeMining(action.casterNetID, laser.Target, float32(added))
        // Update local replica for immediate visual feedback
        minable.Remaining -= float32(added)
        sentCrossNode = true
        // ...
    } else {
        minable.Remaining -= float32(added)
        // ...
        if minable.Remaining <= 0 {
            gw.MarkForRemoval(laser.Target)
        }
    }
```

- [ ] **Step 2: Replace the if/else with a single Send-based call**

```go
// Resolve the asteroid target as an mmokit.Entity (NetID-based, cell-aware).
target := mmokit.EntityByNetID(gw.Stage, gw.C.NetworkID.Get(laser.Target).ID)
caster := mmokit.EntityByNetID(gw.Stage, action.casterNetID)

// Optimistic local-replica decrement still happens for immediate visual
// feedback on the caster's cell (matches existing same-cell semantics for
// the caster's own perception). The authoritative decrement happens in the
// MineExtract handler on the asteroid's cell.
minable.Remaining -= float32(added)

gw.MineExtract(caster, target, uint8(beamIdx), float32(added), itemID)
sentCrossNode = true // gw.MineExtract owns the cross-cell hop; suppress legacy post-block

// For the same-cell case, mark for removal here if depleted. Cross-cell
// case: the handler on the asteroid's authoritative cell handles removal.
if target.Local() && minable.Remaining <= 0 {
    gw.MarkForRemoval(laser.Target)
}
```

- [ ] **Step 3: Verify build**

```bash
go vet ./internal/game/...
```

- [ ] **Step 4: Run game tests**

```bash
go test ./internal/game/...
```

Some `cross_cell_combat_test.go` tests may fail — see Task 3.5.

- [ ] **Step 5: Commit**

```bash
git add internal/game/system_ability.go
git commit -m "feat(game): migrate AbilityTypeExtractPulse to gw.MineExtract"
```

---

### Task 3.4: Delete legacy mining codec + dispatch

**Files:**
- Modify: `internal/game/system_ability.go` (delete sendCrossNodeMining)
- Modify: `internal/game/action_codec.go` (delete MiningAction, MiningResult, marshallers)
- Modify: `internal/game/game.go` (delete ActionMining cases)

- [ ] **Step 1: Delete sendCrossNodeMining**

In `system_ability.go`, find:

```go
func (s *AbilitySystem) sendCrossNodeMining(...)
```

Delete the entire function.

- [ ] **Step 2: Delete the codec**

In `action_codec.go`, delete:
- `const ActionMining`
- `type MiningAction struct{...}`
- `func MarshalMiningAction`
- `func UnmarshalMiningAction`
- `type MiningResult struct{...}`
- `func MarshalMiningResult`
- `func UnmarshalMiningResult`

- [ ] **Step 3: Delete the dispatch cases**

In `game.go`:
- Find `case ActionMining:` in `HandleCrossCellAction` (around line 381). Delete.
- Find `case ActionMining:` in `HandleActionResult` (around line 486). Delete.

- [ ] **Step 4: Verify**

```bash
go vet ./internal/game/...
go test ./internal/game/...
```

- [ ] **Step 5: Commit**

```bash
git add internal/game/system_ability.go internal/game/action_codec.go internal/game/game.go
git commit -m "refactor(game): delete legacy MiningAction codec + ActionMining dispatch cases"
```

---

### Task 3.5: Mining integration test

**Files:**
- Create: `internal/game/verb_mining_test.go`

- [ ] **Step 1: Same-cell test**

```go
package game

import (
    "testing"
    gamecomp "github.com/mmokit/mmokit/internal/component"
    "github.com/mmokit/mmokit/pkg/mmokit"
)

func TestMineExtract_SameCell_ReducesMinable(t *testing.T) {
    gw, _ := newTestGameWorld()
    mmokit.Handle(gw.Stage, mineExtractHandler)

    // Spawn an asteroid + a caster.
    asteroid := newTestAsteroid(t, gw, 100 /* Minable.Remaining */, 1 /* item ID */)
    caster := newTestShipEntity(t, gw, 100, 50)

    casterE := mmokit.EntityByNetID(gw.Stage, caster.NetID)
    asteroidE := mmokit.EntityByNetID(gw.Stage, asteroid.NetID)

    gw.MineExtract(casterE, asteroidE, 0 /* beam */, 25, 1)

    minable := mmokit.Get[gamecomp.Minable](asteroidE)
    if minable.Remaining != 75 {
        t.Fatalf("Minable.Remaining = %v, want 75", minable.Remaining)
    }
}

func TestMineExtract_DepletesAndMarksForRemoval(t *testing.T) {
    gw, _ := newTestGameWorld()
    mmokit.Handle(gw.Stage, mineExtractHandler)

    asteroid := newTestAsteroid(t, gw, 10, 1)
    caster := newTestShipEntity(t, gw, 100, 50)

    casterE := mmokit.EntityByNetID(gw.Stage, caster.NetID)
    asteroidE := mmokit.EntityByNetID(gw.Stage, asteroid.NetID)

    gw.MineExtract(casterE, asteroidE, 0, 25, 1)

    minable := mmokit.Get[gamecomp.Minable](asteroidE)
    if minable.Remaining != 0 {
        t.Fatalf("Minable.Remaining = %v, want 0 (depleted)", minable.Remaining)
    }
    // The asteroid's ECS handle should be in the removal queue. Verify via
    // gw's removal tracking — search for the existing test pattern.
}
```

`newTestAsteroid` helper: look for existing patterns in `transfer_test.go` (`TestFinishTransferSpawn_Asteroid`). Build a small helper at the top of `verb_mining_test.go` that creates an ECS entity with Position, NetworkID, Minable, EntityKind=Asteroid.

- [ ] **Step 2: Run + commit**

```bash
go test ./internal/game/ -run TestMineExtract -v
git add internal/game/verb_mining_test.go
git commit -m "test(game): same-cell MineExtract path"
```

---

## Phase 4: Closeout

### Task 4.1: Run the full module suite

- [ ] **Step 1: Verify everything**

```bash
go vet ./...
go test ./pkg/... ./internal/...
just build
```

All must be green.

- [ ] **Step 2: Smoke-test the 4node-basic example**

```bash
cd examples/4node-basic
go build ./...
```

Should compile cleanly.

- [ ] **Step 3: Update the spec migration plan**

In `docs/superpowers/specs/2026-05-03-entity-message-passing-design.md` §10:

- Mark step 2 (proof-migrate one verb) as `[done — 2026-05-04]` with reference to the Damage migration in this plan.
- Add to step 3: "Damage and Mining migrated in Plan C ([2026-05-04-mmokit-damage-mining-migration.md](../plans/2026-05-04-mmokit-damage-mining-migration.md))." Remaining: StatusEffect, TargetLock, Dock requests, Currency transfers.

```bash
git add docs/superpowers/specs/2026-05-03-entity-message-passing-design.md
git commit -m "docs(spec): mark Damage + Mining migrations landed (Plan C done)"
```

- [ ] **Step 4: Final report**

Summarize:
- Process-level wrappers: HandleAll / OnWorldTickAll / OnTickAll / OnTickEachAll
- Damage migration: done end-to-end. Legacy DamageAction codec + dispatch cases + sendCrossNode helpers all deleted.
- Mining migration: done. Legacy MiningAction codec + dispatch cases + sendCrossNodeMining deleted.
- Lines deleted vs added (`git diff --stat main..HEAD`).
- What's left for future plans (StatusEffect, TargetLock, Dock, etc.).

---

## Out of scope / not in this plan

- **AoI auto-broadcast.** Animations during Damage / Mining still go through the existing `AbilityCastResultMsg` enqueue path (manually called from `gw.Damage` for source-cell, and from `damageHandler` for dest-cell). The reflective auto-anchor broadcast described in spec §4.5 is a future plan.
- **ServerOnly marker enforcement.** Defer until AoI broadcast lands.
- **Death / KillCredit composition.** The spec's death-as-a-separate-event design (DeathSystem watches Health, emits Killed; Killed handler spawns loot, sends KillCredit) is its own plan.
- **StatusEffect, TargetLock, Dock-request, Currency-transfer migrations.** Each gets its own plan.
- **Replacing `gw.eng.ECS.Alive`, `gw.C.X.Get`, `gw.NetIDToEntity` mechanically across systems.** Mechanical sweep, separate plan.
- **Final delete of `action_codec.go`, `HandleCrossCellAction` switch.** Once all verbs migrated.
- **Input handling migration.** Plan H or later.

Each follow-up plan is independently revertible and the codebase remains green between plans.
