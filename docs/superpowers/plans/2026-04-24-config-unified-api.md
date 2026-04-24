# Config-Unified API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move every one-time registration onto `mmokit.Config`. Delete the corresponding `Process` setter methods. `AddSystem` stays on Process.

**Architecture:** Additive Config fields land first (with `Build()` validation + tests). Then production callers migrate. Then tests. Then Process methods get deleted in one final commit. Each phase is shippable on its own and `go vet ./...` stays clean throughout.

**Tech Stack:** Go 1.23+ generics, existing `pkg/universe` / `pkg/mmokit` packages.

**Spec:** [docs/superpowers/specs/2026-04-24-config-unified-api-design.md](../specs/2026-04-24-config-unified-api-design.md)

---

## Task 1: Add Config fields + Build() validation

**Files:**
- Modify: `pkg/universe/coordinator.go` (Config struct + Build)
- Test: `pkg/universe/coordinator_test.go`

Additive change. The new Config fields (`World`, `OnInit`, `PlayerRouter`, `Console`, `OnConsoleReady`) coexist with the existing Process setter methods until Task 4 deletes the setters. `Build()` checks Config fields first; if set, they take precedence. If both Config and Process-setter slots are populated for the same role, panic with a clear message (catches mid-migration mistakes).

- [ ] **Step 1: Write failing tests at `pkg/universe/coordinator_test.go` (append)**

```go
func TestConfigWorldAndOnInitMutuallyExclusive(t *testing.T) {
    defer func() {
        if r := recover(); r == nil {
            t.Fatal("expected panic when both Config.World and Config.OnInit are set")
        }
    }()
    cfg := Config{
        Mode:    "all",
        CellsX:  1,
        CellsY:  1,
        World:   func(b *WorldBase) GameWorld { return b },
        OnInit:  func(b *WorldBase) {},
    }
    p := New(cfg)
    p.Build()
}

func TestConfigWorldDefaultsToBareWorldBase(t *testing.T) {
    // No World, no OnInit — Build creates a bare *WorldBase per cell.
    cfg := Config{
        Mode:    "all",
        CellsX:  1,
        CellsY:  1,
        Headless: true,
    }
    p := New(cfg)
    p.Build()
    if len(p.Cells) != 1 {
        t.Fatalf("expected 1 cell, got %d", len(p.Cells))
    }
    cell := p.Cells["cell_0_0"]
    if cell == nil {
        t.Fatal("cell_0_0 not found")
    }
    if cell.Base == nil {
        t.Error("cell.Base is nil; expected default *WorldBase")
    }
    if cell.World != GameWorld(cell.Base) {
        t.Errorf("expected cell.World to be the bare *WorldBase; got %T", cell.World)
    }
}

func TestConfigOnInitRunsOnceAfterConstruction(t *testing.T) {
    var calls int
    var seen *WorldBase
    cfg := Config{
        Mode:    "all",
        CellsX:  1,
        CellsY:  1,
        Headless: true,
        OnInit: func(b *WorldBase) {
            calls++
            seen = b
        },
    }
    p := New(cfg)
    p.Build()
    if calls != 1 {
        t.Errorf("OnInit called %d times, want 1", calls)
    }
    if seen != p.Cells["cell_0_0"].Base {
        t.Error("OnInit did not receive the cell's *WorldBase")
    }
}
```

- [ ] **Step 2: Run tests, verify fail**

```bash
go test ./pkg/universe/ -run 'TestConfigWorld|TestConfigOnInit' -v
```

Expected: undefined `Config.World`, `Config.OnInit` fields.

- [ ] **Step 3: Add Config fields**

In `pkg/universe/coordinator.go`, find the `Config` struct. Add these fields near other registration-style fields (e.g. after `OpRouter` or `Protocol`):

```go
// World, when set, is the per-cell GameWorld factory. Replaces
// Process.SetWorld. Mutually exclusive with OnInit — Build panics if
// both are set. If both are nil, the engine creates a bare *WorldBase
// per cell (the trivial factory).
World func(base *WorldBase) GameWorld

// OnInit, when set, runs once per cell after the engine constructs a
// bare *WorldBase. Use for the simple case where you don't need a
// custom GameWorld type but still need to spawn entities or register
// replicators. Mutually exclusive with World — Build panics if both
// are set. Replaces Process.OnInit.
OnInit func(base *WorldBase)

// PlayerRouter resolves a username to its target cell ID at login.
// Replaces Process.SetPlayerRouter. Optional — when nil, the gateway's
// default routing applies.
PlayerRouter PlayerRouter

// Console configures the interactive admin console (optional). Replaces
// Process.SetConsole.
Console ConsoleOpts

// OnConsoleReady fires once the console is constructed. Receives the
// owning *Process so admin commands can wire registries without
// closure-capturing a pre-existing variable. Replaces
// Process.OnConsoleReady (which took only the Console).
OnConsoleReady func(p *Process, c *engine.Console)
```

- [ ] **Step 4: Wire Config fields into Build()**

Find the existing Build validation block (search `panic("mmokit: coordinator requires SetWorld or OnInit before Build")` — around line 794). Replace the existing pre-condition logic with one that prefers Config fields and validates the mutual exclusivity rule.

Change the existing block:

```go
if roles.Has(RoleHost) && c.worldFactory == nil && c.onInit == nil {
    panic("mmokit: coordinator requires SetWorld or OnInit before Build")
}
```

To:

```go
// Resolve world factory + init hook from Config first (preferred path),
// falling back to the legacy Process setter slots (worldFactory / onInit
// fields) until Task 4 of the config-unified-api plan deletes them.
if c.cfg.World != nil && c.cfg.OnInit != nil {
    panic("mmokit: Config.World and Config.OnInit are mutually exclusive — pick one")
}
if c.cfg.World != nil {
    if c.worldFactory != nil {
        panic("mmokit: both Config.World and Process.SetWorld() set — Config.World wins; remove the SetWorld call")
    }
    c.worldFactory = c.cfg.World
}
if c.cfg.OnInit != nil {
    if c.onInit != nil {
        panic("mmokit: both Config.OnInit and Process.OnInit() set — Config.OnInit wins; remove the OnInit call")
    }
    c.onInit = c.cfg.OnInit
}
// Default: bare *WorldBase factory when neither is set on Host roles.
if roles.Has(RoleHost) && c.worldFactory == nil && c.onInit == nil {
    c.worldFactory = func(base *WorldBase) GameWorld { return base }
}
```

- [ ] **Step 5: Wire Config.PlayerRouter, Config.Console, Config.OnConsoleReady**

Find the existing `playerRouter`, `consoleOpts`, and console-ready hook fields (search `c.playerRouter`, `c.consoleOpts`, `c.onConsoleReady`). Add Config-priority resolution near the top of `Build()`:

```go
// Config-supplied registrations override Process setter slots if both are
// set; legacy Process setters remain functional until Task 4 deletes them.
if c.cfg.PlayerRouter != nil {
    if c.playerRouter != nil {
        panic("mmokit: both Config.PlayerRouter and Process.SetPlayerRouter() set — Config.PlayerRouter wins; remove the setter call")
    }
    c.playerRouter = c.cfg.PlayerRouter
}
// Console field is a struct, not a pointer — check whether it's the zero
// value before treating it as set. The simplest test is whether any field
// is non-zero; ConsoleOpts is small enough that a direct compare suffices:
if c.cfg.Console != (ConsoleOpts{}) {
    if c.consoleOpts != (ConsoleOpts{}) {
        panic("mmokit: both Config.Console and Process.SetConsole() set — Config.Console wins; remove the setter call")
    }
    c.consoleOpts = c.cfg.Console
}
if c.cfg.OnConsoleReady != nil {
    // Note signature change: cfg.OnConsoleReady takes (p *Process, c *Console);
    // legacy onConsoleReady takes (c *Console). Wrap to bridge the legacy
    // slot until Task 4 deletes it.
    if c.onConsoleReady != nil {
        panic("mmokit: both Config.OnConsoleReady and Process.OnConsoleReady() set — Config.OnConsoleReady wins; remove the setter call")
    }
    c.onConsoleReady = func(con *engine.Console) {
        c.cfg.OnConsoleReady(c, con)
    }
}
```

If `ConsoleOpts` is large or contains fields that aren't directly comparable, use a sentinel: change `Config.Console` from `ConsoleOpts` to `*ConsoleOpts` and check for nil. Verify the type's comparability before committing to one approach.

- [ ] **Step 6: Run tests**

```bash
go test ./pkg/universe/ -run 'TestConfigWorld|TestConfigOnInit' -v
go vet ./...
```

Expected: 3 new tests pass; vet clean. Existing universe tests continue to pass (run a fast subset for confidence):

```bash
go test ./pkg/universe/ -run 'TestConfig|TestProtocol|TestRouter|TestRoles' -count=1 -timeout=60s
```

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/coordinator_test.go
git commit -m "feat(universe): add Config.World/OnInit/PlayerRouter/Console/OnConsoleReady

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Migrate production callers

**Files:**
- Modify: `examples/4node-basic/main.go`
- Modify: `examples/slither/main.go`
- Modify: `examples/simple/main.go`
- Modify: `internal/game/factory.go`
- Modify: `cmd/server/main.go`

Move Process setter calls into the `Config{...}` literal in each main.go. After this task, no production code calls `SetWorld`, `OnInit`, `SetPlayerRouter`, `SetConsole`, or `OnConsoleReady` — all moved to Config fields.

`OnConsoleReady` callbacks have a signature change: from `func(c *engine.Console)` to `func(p *mmokit.Process, c *engine.Console)`. Closures that previously captured the surrounding `mmo`/`coord` variable now use the parameter directly.

- [ ] **Step 1: Migrate `examples/4node-basic/main.go`**

Find the existing `mmo.SetWorld(NewWorld)` and `mmo.OnConsoleReady(...)` calls. Move into the Config literal:

```go
mmo := mmokit.New(mmokit.Config{
    // ... existing fields ...
    World: NewWorld,
    OnConsoleReady: func(p *mmokit.Process, c *engine.Console) {
        if err := registerBotCommands(p, c.Registry()); err != nil {
            log.Printf("4node-basic: failed to register bot commands: %v", err)
        }
    },
})
// SetWorld and OnConsoleReady calls deleted
```

The closure now uses `p` (the Process arg) instead of capturing `mmo`. If the original callback had any other reference to `mmo`, swap to `p`.

- [ ] **Step 2: Migrate `examples/slither/main.go`**

Find `coord.SetWorld(...)` and `coord.OnConsoleReady(...)`. Move into the Config literal:

```go
cfg := mmokit.Config{
    // ... existing fields ...
    World: func(base *mmokit.WorldBase) mmokit.GameWorld {
        return NewSlitherWorld(base, slitherCfg)
    },
    OnConsoleReady: func(p *mmokit.Process, c *mmokit.Console) {
        // existing closure body, with `coord` references replaced by `p`
        var gw *SlitherWorld
        for _, node := range p.Cells {
            gw = node.World.(*SlitherWorld)
            break
        }
        if gw == nil {
            return
        }
        registry := buildEntityRegistry(gw)
        c.RegisterBuiltins(mmokit.BuiltinOpts{
            Engine:   gw.Engine(),
            Registry: registry,
            Entities: buildEntityOpts(gw, registry),
        })
    },
}
coord := mmokit.New(cfg)
// SetWorld and OnConsoleReady calls deleted
```

- [ ] **Step 3: Migrate `examples/simple/main.go`**

Find `mmo.OnInit(...)`. Move into the Config literal:

```go
mmo := mmokit.New(mmokit.Config{
    // ... existing fields ...
    OnInit: func(w *mmokit.WorldBase) {
        // existing body unchanged
    },
})
// OnInit call deleted
```

- [ ] **Step 4: Migrate `internal/game/factory.go`**

Find `coord.SetWorld(func(base *mmokit.WorldBase) mmokit.GameWorld {...})`. This is a constructor helper called from `cmd/server/main.go` (search for the exported function name and how it's invoked). Two options:

**4a.** Change the helper to RETURN the world factory function instead of calling SetWorld on the coord. Then `cmd/server/main.go` assigns the returned factory to `coordCfg.World` before calling `mmokit.New`.

**4b.** Have the helper accept a `*Config` pointer and assign `cfg.World = ...` directly.

Option 4a is cleaner — the helper becomes a pure builder, no side effects.

```go
// Before:
func RegisterGameWorld(coord *mmokit.Process, ...) {
    coord.SetWorld(func(base *mmokit.WorldBase) mmokit.GameWorld {
        // ...
    })
}

// After:
func GameWorldFactory(...) func(base *mmokit.WorldBase) mmokit.GameWorld {
    return func(base *mmokit.WorldBase) mmokit.GameWorld {
        // ...
    }
}
```

Then in `cmd/server/main.go`:

```go
coordCfg.World = game.GameWorldFactory(deps...)
```

Adjust the surrounding name and signature to match what's already there.

- [ ] **Step 5: Migrate `cmd/server/main.go`**

Find `coordinator.OnConsoleReady(...)`. Move into the Config literal. The closure captures `coord` (or whatever the Process variable is named) — replace with the new `p` arg.

```go
// Before:
coordinator.OnConsoleReady(func(console *mmokit.Console) {
    // uses coordinator, gw, etc.
})

// After:
coordCfg.OnConsoleReady = func(p *mmokit.Process, console *mmokit.Console) {
    // replace `coordinator` with `p`; other captures remain unchanged
}
```

The `OnConsoleReady` closure in `cmd/server` may capture multiple things (gameWorld, marketSvc, etc.) — only the Process-typed capture changes; everything else is fine.

If `cmd/server/main.go` also called `coord.SetWorld(...)` indirectly through a helper, that's already addressed by Step 4.

- [ ] **Step 6: Verify**

```bash
go vet ./...
grep -rn "\.SetWorld\|\.OnInit(\|\.SetPlayerRouter\|\.SetConsole(\|\.OnConsoleReady(" --include="*.go" . | grep -v "_test.go"
```

The grep MUST return zero hits in production (`*.go` minus `*_test.go`) outside `pkg/universe/coordinator.go` itself (which still defines the methods until Task 4). Each of the 5 file migrations should be visible via:

```bash
git diff main..HEAD --stat -- '*main.go' 'internal/game/factory.go'
```

- [ ] **Step 7: Smoke test (manual; subagent skips)**

For human-driven runs:
```bash
just dev      # space game
cd examples/4node-basic && just dev
cd examples/slither && just dev
```

Each should boot cleanly. For subagent-driven execution, skip and note in the report.

- [ ] **Step 8: Commit**

```bash
git add examples/ internal/game/factory.go cmd/server/main.go
git commit -m "refactor(games): move SetWorld/OnInit/OnConsoleReady into Config

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Migrate test callers

**Files (all `_test.go`):**
- Modify: `pkg/universe/roles_test.go`
- Modify: `pkg/universe/universe_test.go`
- Modify: `pkg/universe/s4_5_cross_host_test.go`
- Modify: `pkg/universe/cell_transfer_executor_test.go`
- Modify: `pkg/universe/s4_control_plane_test.go`
- Modify: `pkg/universe/cluster_fixture_distributed_test.go`
- Modify: `pkg/universe/cluster_fixture_test.go`
- Modify: `pkg/universe/partition_test.go`
- Modify: `examples/4node-basic/mesh_e2e_test.go`

Same pattern as Task 2: move Process setter calls into the `Config{...}` literal each test constructs. Most tests build their `Config` with a pre-set `World`/`OnInit` for a stub `GameWorld`; the migration is mechanical.

- [ ] **Step 1: Enumerate exact references**

```bash
grep -rn "\.SetWorld\|\.OnInit(\|\.SetPlayerRouter\|\.SetConsole(\|\.OnConsoleReady(" --include="*_test.go" .
```

15 references total across 9 files. Open each, locate the Process variable being mutated and the surrounding Config literal (or `New(...)` call), and migrate.

For tests that don't have a Config literal (they call `New(Config{Mode: "all"})` inline), pull the Config out into a local variable so the new field can be set:

```go
// Before:
p := New(Config{Mode: "all"})
p.SetWorld(stubWorldFactory)

// After:
cfg := Config{Mode: "all", World: stubWorldFactory}
p := New(cfg)
```

- [ ] **Step 2: Apply the migration**

For each of the 9 files, do a focused edit replacing the setter call with a Config field assignment. Be careful to preserve other test logic — only the registration form changes.

- [ ] **Step 3: Verify**

```bash
go test ./pkg/universe/ -count=1 -timeout=10m
go test ./examples/4node-basic/ -count=1 -timeout=60s
grep -rn "\.SetWorld\|\.OnInit(\|\.SetPlayerRouter\|\.SetConsole(\|\.OnConsoleReady(" --include="*_test.go" .
```

The grep MUST return zero hits. Both test commands MUST pass.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/*_test.go examples/4node-basic/mesh_e2e_test.go
git commit -m "refactor(test): migrate Process setters to Config fields

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Delete the Process setter methods

**Files:**
- Modify: `pkg/universe/coordinator.go`

After Tasks 1-3, no caller invokes `SetWorld`, `OnInit`, `SetPlayerRouter`, `SetConsole`, or `OnConsoleReady`. Delete the methods + the now-redundant Config-vs-setter conflict checks added in Task 1 Step 4-5.

- [ ] **Step 1: Confirm zero callers**

```bash
grep -rn "\.SetWorld\|\.OnInit(\|\.SetPlayerRouter\|\.SetConsole(\|\.OnConsoleReady(" --include="*.go" . | grep -v "^./pkg/universe/coordinator.go:"
```

Expected: zero hits. If any remain, finish the migration before proceeding.

- [ ] **Step 2: Delete the methods**

In `pkg/universe/coordinator.go`, delete:

- `func (c *Process) SetWorld(factory func(base *WorldBase) GameWorld)`
- `func (c *Process) OnInit(fn func(w *WorldBase))`
- `func (c *Process) SetPlayerRouter(router PlayerRouter)` (if it exists; verify with grep)
- `func (c *Process) SetConsole(opts ConsoleOpts)`
- `func (c *Process) OnConsoleReady(fn func(c *engine.Console))`

Delete the corresponding private fields if they're no longer referenced anywhere except the Build() resolution block. Verify with grep:

```bash
grep -n "c\.worldFactory\|c\.onInit\|c\.playerRouter\|c\.consoleOpts\|c\.onConsoleReady" pkg/universe/*.go
```

Any references in `pkg/universe/*.go` that aren't from the deleted methods should still work (they refer to the same fields populated from Config in Build).

- [ ] **Step 3: Simplify the Build() resolution block**

The conflict-detection panics added in Task 1 Step 4-5 are now dead code (no setter can populate the legacy slots). Reduce the resolution to:

```go
// Resolve world factory + init hook from Config.
if c.cfg.World != nil && c.cfg.OnInit != nil {
    panic("mmokit: Config.World and Config.OnInit are mutually exclusive — pick one")
}
c.worldFactory = c.cfg.World
c.onInit = c.cfg.OnInit
if roles.Has(RoleHost) && c.worldFactory == nil && c.onInit == nil {
    c.worldFactory = func(base *WorldBase) GameWorld { return base }
}

c.playerRouter = c.cfg.PlayerRouter
c.consoleOpts = c.cfg.Console
if c.cfg.OnConsoleReady != nil {
    c.onConsoleReady = func(con *engine.Console) {
        c.cfg.OnConsoleReady(c, con)
    }
}
```

(If the private slots were also removed, eliminate them entirely and reference `c.cfg.*` directly throughout coordinator.go.)

- [ ] **Step 4: Verify**

```bash
go vet ./...
go test ./pkg/universe/ ./pkg/mmokit/ ./internal/... -count=1 -timeout=10m
```

Expected: vet clean, every test still passes.

End-to-end: run each example briefly (skip in subagent context).

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "feat(universe): delete Process setters; Config is sole registration surface

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Self-review

**Spec coverage:**
- ✅ All 5 Config fields added (Task 1)
- ✅ Three-tier World/OnInit resolution implemented (Task 1, validated by tests)
- ✅ All 5 production callers migrated (Task 2)
- ✅ All 9 test files migrated (Task 3)
- ✅ All 5 Process setter methods deleted (Task 4)
- ✅ `OnConsoleReady` signature change to `func(*Process, *Console)` reflected in every callsite (Task 2 + 3)
- ✅ No backward compat — single-commit deletion in Task 4 (matches project policy)

**Type consistency:**
- `Config.World` signature `func(base *WorldBase) GameWorld` matches existing `worldFactory` field type
- `Config.OnInit` signature `func(base *WorldBase)` matches existing `onInit` field type
- `Config.OnConsoleReady` signature `func(p *Process, c *engine.Console)` is the new shape; legacy `func(c *engine.Console)` is wrapped to the legacy slot in Task 1 Step 5 then dropped in Task 4

**Migration risk:**
- Task 2 migration of `cmd/server/main.go` is the trickiest — its OnConsoleReady closure may capture multiple game-world / marketplace references. The migration only changes the *Process* capture; other captures stay unchanged.
- The `internal/game/factory.go` refactor (Step 4 of Task 2) changes a helper's API — verify the new signature is consumed correctly in `cmd/server/main.go`.

---

**Plan complete.** Two execution options:

**1. Subagent-Driven (recommended)** — Fresh subagent per task, review between tasks. 4 tasks; should run quickly given each is a focused mechanical migration with clear scope.

**2. Inline Execution** — Execute tasks in this session.

Which approach?
