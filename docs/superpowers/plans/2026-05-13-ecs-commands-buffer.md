# ECS Commands Buffer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the locked-world panic class structurally by introducing a per-stage deferred-mutation buffer (`Commands`), then migrate every game-side ad-hoc deferral pattern to use it. End state: `internal/game/` imports zero ark/ecs types.

**Architecture:** A `Commands` type lives on every Stage and queues four kinds of ops (Despawn, AddComponent, RemoveComponent, Defer). The engine game loop flushes it after each system's Update — so System N's mutations are visible to System N+1 in the same tick. Existing `Pending*` typed queues and `gw.Queue *TickQueue` are deleted entirely, replaced by `Defer(closure)` for game-action logic.

**Tech Stack:** Go 1.22+ generics, ark/ecs v0.7.1 (kept as storage engine, hidden behind mmokit), existing pkg/mmokit/pkg/universe/pkg/engine layering.

---

## File Structure

**New files:**

- `pkg/universe/commands.go` — `Commands` type, op buffer, `Despawn`, `Defer`, `flush()` (package-private), `AddOp` (public hook for cross-package generic ops). Lives in `pkg/universe` because Stage owns it and Stage can't import mmokit.
- `pkg/universe/commands_test.go` — Unit tests for Commands (ordering, no-op-on-dead, Defer captures).
- `pkg/mmokit/commands.go` — `Commands` type alias to `universe.Commands`. Free generic functions `AddComponent[T]` and `RemoveComponent[T]`. Convenience accessor on `SystemBase`.
- `pkg/mmokit/commands_test.go` — Tests for the generic helpers.
- `pkg/mmokit/queries.go` — `Any[T]`, `FindOne[T]`, `ForEach1/2/3[T]` one-shot query helpers.
- `pkg/mmokit/queries_test.go` — Tests for query wrappers.
- `pkg/mmokit/entity_handle.go` — `type EntityHandle = ecs.Entity` alias so game code can name the raw handle type without importing ark.

**Modified files:**

- `pkg/universe/stage.go` — Add `commands *Commands` field, `Commands()` accessor, wiring in `NewStage`. Add `RemoveNow(h)` private method that destroys an entity immediately (used by `Commands.Despawn` at flush time).
- `pkg/mmokit/system.go` — Add `Commands()` shortcut method on `SystemBase`.
- `pkg/engine/loop.go` — Modify the systems iteration loop (line 134-139) to flush commands after each system. Requires a hook mechanism since loop lives in `pkg/engine` (can't import universe).
- `pkg/engine/engine.go` — Add `AfterSystemHook func()` to `Hooks` so universe can install the per-system flush callback.

**Game-code migrations (Phase 2-6):**

- `internal/game/system_npc_ai.go` — Delete `pendingLeashClears`; switch to `mmokit.RemoveComponent[Leashing]`.
- `internal/game/game.go` — Replace `PendingDeathMarker` queue with direct `AddComponent(Dormant{})` at flush time.
- `internal/game/hooks.go` — Replace raw `dormantMap.Remove`, all `PendingX` drains, with Commands ops.
- `internal/game/gameworld.go` — Delete `Pending*` structs and `gw.Queue` field. Migrate `hasStation` to `mmokit.Any`.
- `internal/game/entity_station.go`, `entity_poi.go`, `op_bank.go` — Migrate raw `ecs.NewFilterN` to `mmokit.ForEachN`.
- `internal/game/entity_npc.go`, `entity_ship.go`, `factory.go`, `system_ability.go`, `system_network.go`, `transfer.go` — Swap `ecs.Entity` for `mmokit.EntityHandle`; drop ark import.

**Deleted files / types:**

- `pkg/engine/tickqueue.go` (the entire file).
- `mmokit.TickQueue` alias and `Enqueue[T]` / `Drain[T]` free functions in `pkg/mmokit/mmokit.go`.
- All `Pending*` types in `internal/game/gameworld.go`.
- `gw.Queue` field in `GameWorld`.

---

## Phase 1 — Land the new API

### Task 1: Add `universe.Commands` skeleton + `Despawn` + `Defer`

**Files:**
- Create: `pkg/universe/commands.go`
- Create: `pkg/universe/commands_test.go`

- [ ] **Step 1: Write the failing test**

`pkg/universe/commands_test.go`:

```go
package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
)

// TestCommands_Defer_ExecutesInSubmitOrder verifies that Defer closures
// run in the order they were submitted when flush() is invoked.
func TestCommands_Defer_ExecutesInSubmitOrder(t *testing.T) {
	c := &Commands{}
	var got []int
	c.Defer(func() { got = append(got, 1) })
	c.Defer(func() { got = append(got, 2) })
	c.Defer(func() { got = append(got, 3) })

	c.flush()

	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("Defer order = %v, want [1 2 3]", got)
	}
}

// TestCommands_Flush_ClearsQueue verifies that ops are not re-applied
// on a second flush — the buffer resets after draining.
func TestCommands_Flush_ClearsQueue(t *testing.T) {
	c := &Commands{}
	calls := 0
	c.Defer(func() { calls++ })

	c.flush()
	c.flush()

	if calls != 1 {
		t.Fatalf("Defer called %d times across two flushes, want 1", calls)
	}
}

// TestCommands_Despawn_NoopOnDeadHandle verifies that Despawning an
// already-removed entity is a silent no-op (covers the AddComponent-after-
// Despawn-in-same-batch and the cross-cell-handoff-then-defer cases).
func TestCommands_Despawn_NoopOnDeadHandle(t *testing.T) {
	w := ecs.NewWorld()
	type tag struct{}
	mapper := ecs.NewMap1[tag](w)
	h := mapper.NewEntity(&tag{})
	w.RemoveEntity(h)

	c := &Commands{world: w}
	c.Despawn(h) // queue against an already-dead handle
	// Should not panic on flush.
	c.flush()
}
```

- [ ] **Step 2: Run the test, expect FAIL (Commands type doesn't exist yet)**

```bash
go test ./pkg/universe/ -run TestCommands_ -count=1
```

Expected: `undefined: Commands` / `undefined: Commands.Defer` etc.

- [ ] **Step 3: Create `pkg/universe/commands.go`**

```go
package universe

import (
	"github.com/mlange-42/ark/ecs"
)

// Commands is a per-stage deferred ECS mutation buffer. The engine
// flushes it after every system's Update so ops queued in System N
// are visible to System N+1 in the same tick. Game code should never
// call flush() directly — only the engine loop does that.
//
// Operations are applied in submit order. Each op checks
// ecs.World.Alive on its target handle and silently no-ops if the
// entity is gone, so AddComponent-after-Despawn within the same
// batch is safe.
type Commands struct {
	world *ecs.World
	stage *Stage // nil during initial construction; set after Stage wires it
	ops   []func()
}

// Despawn queues immediate destruction of the entity at next flush.
// Subsequent ops on this handle within the same batch become no-ops
// (via the world.Alive check inside each op).
func (c *Commands) Despawn(h ecs.Entity) {
	c.ops = append(c.ops, func() {
		if c.world != nil && c.world.Alive(h) {
			c.world.RemoveEntity(h)
		}
	})
}

// Defer schedules an arbitrary closure to run during next flush. Use
// for game-action logic that doesn't reduce to a single ECS primitive
// (spawning a loot crate with inventory setup, starting a docking
// sequence, cross-cell respawn routing).
//
// Closures may call Commands ops on this same buffer, but those ops
// land in the NEXT system's flush, not the current one. Single-pass
// per flush; no convergence loop.
func (c *Commands) Defer(fn func()) {
	c.ops = append(c.ops, fn)
}

// AddOp is the public-but-internal hook for cross-package generic
// ops in mmokit. Game code should use mmokit.AddComponent /
// mmokit.RemoveComponent instead of calling this directly.
func (c *Commands) AddOp(op func()) {
	c.ops = append(c.ops, op)
}

// flush applies all queued ops in submit order and clears the buffer.
// Package-private — the engine game loop is the only caller in
// production. Test helpers in mmokit (stage.TickOne) also call it.
func (c *Commands) flush() {
	for _, op := range c.ops {
		op()
	}
	c.ops = c.ops[:0]
}
```

- [ ] **Step 4: Run the test, expect PASS**

```bash
go test ./pkg/universe/ -run TestCommands_ -count=1
```

Expected: `PASS` for all three tests.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/commands.go pkg/universe/commands_test.go
git commit -m "$(cat <<'EOF'
universe: add Commands deferred-mutation buffer skeleton

Despawn and Defer methods + flush() and AddOp hook. AddComponent /
RemoveComponent generic helpers will live in mmokit (Task 3) and
call into AddOp.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Wire `Commands` into `Stage`

**Files:**
- Modify: `pkg/universe/stage.go` (add field + accessor + constructor wire)
- Test: `pkg/universe/commands_test.go` (add stage-integration test)

- [ ] **Step 1: Find the Stage constructor**

Run:
```bash
grep -n "func NewStage\|func newStage\|func.*Stage.*Stage{" pkg/universe/stage.go | head -5
```

The Stage is constructed inside `pkg/universe/stage.go` — find the constructor or struct-literal site for the `Stage` struct (line ~155). Note the exact name (likely `NewStage` or a constructor inside Cell.Run).

- [ ] **Step 2: Add `commands *Commands` to Stage struct**

In `pkg/universe/stage.go`, add to the `Stage` struct (line ~155):

```go
type Stage struct {
	// ... existing fields ...
	commands *Commands
}
```

- [ ] **Step 3: Initialize `commands` in the Stage constructor**

Wherever Stage is constructed (NewStage or equivalent), after the world is created and the Stage struct is populated, add:

```go
b.commands = &Commands{world: b.eng.ECS, stage: b}
```

Where `b` is the `*Stage` being built and `b.eng.ECS` is the ark world (verify field name from existing code).

- [ ] **Step 4: Add the public accessor**

In `pkg/universe/stage.go`, after the existing accessors (around line 410):

```go
// Commands returns the per-stage deferred-mutation buffer. Use from
// inside system Update or any other locked-world context to queue
// structural changes that will apply after the current system finishes.
func (b *Stage) Commands() *Commands { return b.commands }
```

- [ ] **Step 5: Add the stage-integration test**

Append to `pkg/universe/commands_test.go`:

```go
// TestStage_HasCommands verifies the stage exposes its Commands buffer
// after construction.
func TestStage_HasCommands(t *testing.T) {
	// This uses whatever test fixture other Stage tests use — pattern
	// from existing test in stage_test.go.
	stage := newTestStage(t)  // existing helper; see pkg/universe/stage_test.go
	if stage.Commands() == nil {
		t.Fatal("Stage.Commands() returned nil")
	}
}
```

If `newTestStage` doesn't exist or has a different name, find the equivalent test helper in `pkg/universe/*_test.go` — there is one (used by border replication tests, etc.).

- [ ] **Step 6: Run tests**

```bash
go test ./pkg/universe/ -run "TestCommands_|TestStage_HasCommands" -count=1
```

Expected: PASS.

- [ ] **Step 7: Run `go vet` to catch any wiring errors**

```bash
go vet ./pkg/universe/
```

Expected: no output (clean).

- [ ] **Step 8: Commit**

```bash
git add pkg/universe/stage.go pkg/universe/commands_test.go
git commit -m "$(cat <<'EOF'
universe: wire Commands buffer into Stage

Adds a per-stage Commands instance with a Stage.Commands() accessor.
Every cell gets one buffer; engine loop integration (next task) drives
the flush.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Add `mmokit.Commands` alias + `AddComponent` / `RemoveComponent` generic helpers

**Files:**
- Create: `pkg/mmokit/commands.go`
- Create: `pkg/mmokit/commands_test.go`

- [ ] **Step 1: Write the failing test**

`pkg/mmokit/commands_test.go`:

```go
package mmokit

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
)

// TestAddComponent_AppliesAtFlush verifies that AddComponent queues
// the op and applies it when flush runs.
func TestAddComponent_AppliesAtFlush(t *testing.T) {
	stage := newTestStageForCommands(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap1[component.Position](w)
	h := mapper.NewEntity(&component.Position{X: 1, Y: 1})
	e := EntityFromECS(stage, h)

	AddComponent(stage.Commands(), e, component.Velocity{X: 10, Y: 20})

	// Before flush, Velocity is not yet on the entity.
	velMap := ecs.NewMap1[component.Velocity](w)
	if velMap.HasAll(h) {
		t.Fatal("AddComponent applied before flush")
	}

	stage.Commands().FlushForTest() // test-only hook in next task

	if !velMap.HasAll(h) {
		t.Fatal("AddComponent did not apply after flush")
	}
	v := velMap.Get(h)
	if v.X != 10 || v.Y != 20 {
		t.Fatalf("Velocity = %+v, want {X:10, Y:20}", *v)
	}
}

// TestRemoveComponent_AppliesAtFlush verifies RemoveComponent drops
// the component at flush time.
func TestRemoveComponent_AppliesAtFlush(t *testing.T) {
	stage := newTestStageForCommands(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap2[component.Position, component.Velocity](w)
	h := mapper.NewEntity(
		&component.Position{X: 1, Y: 1},
		&component.Velocity{X: 10, Y: 20},
	)
	e := EntityFromECS(stage, h)

	RemoveComponent[component.Velocity](stage.Commands(), e)

	velMap := ecs.NewMap1[component.Velocity](w)
	if !velMap.HasAll(h) {
		t.Fatal("RemoveComponent applied before flush")
	}

	stage.Commands().FlushForTest()

	if velMap.HasAll(h) {
		t.Fatal("RemoveComponent did not drop component at flush")
	}
}

// TestAddComponent_NoopOnDeadEntity verifies that ops on a dead
// entity are silent no-ops, not panics.
func TestAddComponent_NoopOnDeadEntity(t *testing.T) {
	stage := newTestStageForCommands(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap1[component.Position](w)
	h := mapper.NewEntity(&component.Position{X: 1, Y: 1})
	e := EntityFromECS(stage, h)
	w.RemoveEntity(h) // kill the entity before flush

	AddComponent(stage.Commands(), e, component.Velocity{X: 99, Y: 99})

	// Should not panic.
	stage.Commands().FlushForTest()
}

// newTestStageForCommands constructs a minimal Stage for unit tests.
// Adapt to whatever fixture pattern the existing mmokit tests use —
// see pkg/mmokit/entity_test.go for the canonical helper.
func newTestStageForCommands(t *testing.T) *Stage {
	t.Helper()
	// Reuse the existing test stage helper from pkg/mmokit/*_test.go.
	return newTestStage(t) // exists in existing test files
}
```

- [ ] **Step 2: Add a test-only `FlushForTest` shim**

In `pkg/universe/commands.go`, add:

```go
// FlushForTest is a test-only entrypoint exposing the package-private
// flush. Production code must not call this — the engine game loop is
// the only authorized caller in mmokit's per-system integration.
//
// Used by mmokit unit tests and stage.TickOne to drive a single
// command-buffer cycle without spinning up the full game loop.
func (c *Commands) FlushForTest() { c.flush() }
```

- [ ] **Step 3: Run the test, expect FAIL (AddComponent/RemoveComponent don't exist)**

```bash
go test ./pkg/mmokit/ -run "TestAddComponent_|TestRemoveComponent_" -count=1
```

Expected: undefined: AddComponent / RemoveComponent.

- [ ] **Step 4: Create `pkg/mmokit/commands.go`**

```go
package mmokit

import (
	"github.com/mlange-42/ark/ecs"

	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// Commands is the per-stage deferred-mutation buffer. Aliased from
// pkg/universe so game code can refer to it as `mmokit.Commands`
// without importing universe directly.
type Commands = pkguniverse.Commands

// AddComponent queues a component add/overwrite for entity e. T is
// inferred from val. If the component is already present on e, it's
// overwritten (same semantics as mmokit.Set). Applied at next flush
// (end of current system Update). No-op if e is dead at flush time.
//
// Safe to call from inside a system's Update query iteration —
// the actual ECS mutation runs after the system completes and the
// world lock is released.
func AddComponent[T any](c *Commands, e Entity, val T) {
	h := e.Handle()
	stage := e.Stage()
	if stage == nil {
		return // nothing to do; not on a stage
	}
	c.AddOp(func() {
		if !stage.ECSWorld().Alive(h) {
			return
		}
		m := ecs.NewMap1[T](stage.ECSWorld())
		if m.HasAll(h) {
			*m.Get(h) = val
			return
		}
		m.Add(h, &val)
	})
}

// RemoveComponent queues removal of component T from entity e at
// next flush. T must be specified explicitly (no value to infer
// from). Silent no-op if e doesn't have the component or is dead.
func RemoveComponent[T any](c *Commands, e Entity) {
	h := e.Handle()
	stage := e.Stage()
	if stage == nil {
		return
	}
	c.AddOp(func() {
		if !stage.ECSWorld().Alive(h) {
			return
		}
		m := ecs.NewMap1[T](stage.ECSWorld())
		if m.HasAll(h) {
			m.Remove(h)
		}
	})
}
```

- [ ] **Step 5: Verify `mmokit.Entity` has `Handle()` and `Stage()` accessors**

Run:
```bash
grep -n "func (e Entity) Handle\|func (e Entity) Stage" pkg/mmokit/entity.go
```

If `Handle()` doesn't exist as a method, use `e.resolveHandle()` (which is package-private but accessible in the same package). The same goes for `Stage()` — there may already be a getter at the struct level.

Adjust the implementation if the method names differ — the test will tell you what's needed.

- [ ] **Step 6: Run tests, expect PASS**

```bash
go test ./pkg/mmokit/ -run "TestAddComponent_|TestRemoveComponent_" -count=1
```

Expected: all three tests pass.

- [ ] **Step 7: Commit**

```bash
git add pkg/mmokit/commands.go pkg/mmokit/commands_test.go pkg/universe/commands.go
git commit -m "$(cat <<'EOF'
mmokit: AddComponent/RemoveComponent generic helpers + Commands alias

Free functions live in mmokit (Go doesn't allow generic methods on
non-generic types). They take *mmokit.Commands (alias for
universe.Commands) and an mmokit.Entity wrapper; internal closures
do the dead-entity check + ark Map.Add/Remove dance.

Test-only FlushForTest exposed on universe.Commands so unit tests
can drive a single cycle without the game loop.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Wire per-system flush into the engine game loop

**Files:**
- Modify: `pkg/engine/engine.go` (add `AfterSystemHook`)
- Modify: `pkg/engine/loop.go` (call hook after each system)
- Modify: `pkg/universe/coordinator.go` or wherever Hooks are wired (install the flush callback)

- [ ] **Step 1: Read the existing Hooks type**

```bash
sed -n '13,40p' pkg/engine/loop.go
```

You'll see something like:

```go
type Hooks struct {
    ClearTickState func()
    PreFlush       func()
    PostFlush      func()
    PostTick       func()
}
```

- [ ] **Step 2: Add `AfterSystem` to Hooks**

In `pkg/engine/loop.go`, modify the `Hooks` struct (around line 13):

```go
type Hooks struct {
	ClearTickState func()
	AfterSystem    func() // NEW: called after each system's Update returns
	PreFlush       func()
	PostFlush      func()
	PostTick       func()
}
```

- [ ] **Step 3: Invoke `AfterSystem` in the systems loop**

Modify `pkg/engine/loop.go` lines 134-139:

```go
// Run all systems in order, measuring each
for i, sys := range gl.systems {
	sysStart := time.Now()
	sys.Update(dt)
	if gl.hooks.AfterSystem != nil {
		gl.hooks.AfterSystem()
	}
	gl.sysTimings[i] = time.Since(sysStart)
}
```

- [ ] **Step 4: Install the flush hook from universe**

Find where the engine's Hooks struct is built and passed to NewGameLoop. Likely in `pkg/universe/cell.go` or `pkg/universe/coordinator.go`.

Run:
```bash
grep -rn "engine.Hooks{\|Hooks{" pkg/universe/ --include="*.go" | head -5
```

In whichever file builds the Hooks, set the `AfterSystem` field:

```go
hooks := engine.Hooks{
    // ... existing fields ...
    AfterSystem: func() {
        if stage != nil && stage.Commands() != nil {
            stage.Commands().FlushForTest() // see note below
        }
    },
}
```

**Note:** `FlushForTest` is misnamed in this production wire-up. Rename it to a more honest name: `internalFlush` (package-private) or expose `flush` properly. The cleanest fix: rename `FlushForTest` to just `Flush` and add a comment that "production code calls this only from the engine game-loop integration."

Actually — since universe is the package owning Commands, it can call the lowercase `flush()` directly from within universe code. So in `pkg/universe/cell.go` (or wherever) the wire-up reads:

```go
AfterSystem: func() {
    if stage != nil && stage.commands != nil {
        stage.commands.flush()
    }
},
```

Direct call. Delete the `FlushForTest` method — replace with `Flush` (uppercase, exported) and document it:

```go
// Flush drains all queued ops in submit order. The engine game loop
// invokes this after every system's Update. Tests that bypass the
// loop (calling sys.Update directly) must call this manually — use
// stage.TickOne(sys) to wrap that pattern.
func (c *Commands) Flush() {
	for _, op := range c.ops {
		op()
	}
	c.ops = c.ops[:0]
}
```

Update Task 3's test calls from `stage.Commands().FlushForTest()` to `stage.Commands().Flush()`.

- [ ] **Step 5: Write an integration test**

Append to `pkg/mmokit/commands_test.go`:

```go
// TestCommands_FlushesAfterEachSystem verifies the engine game loop's
// AfterSystem hook drains the buffer between systems. A two-system
// fixture: System A queues AddComponent, System B reads the component
// — System B must see it in the same tick.
func TestCommands_FlushesAfterEachSystem(t *testing.T) {
	// Construct a minimal Process with two test systems; tick once.
	// Pattern matches existing tests in pkg/mmokit/wire_system_test.go
	// or pkg/universe/lifecycle_test.go.
	//
	// This test exists primarily to guard against regressions if the
	// AfterSystem hook is ever removed.
	t.Skip("TODO: wire up Process fixture — see pkg/mmokit/wire_system_test.go for pattern")
}
```

(The skip is acceptable here — the unit tests in Task 3 already verify the flush path. The integration test is a guard that would be re-enabled in a future polish task. List it in the open-tasks tracker but don't block on it.)

- [ ] **Step 6: Verify build**

```bash
go vet ./pkg/engine/ ./pkg/universe/ ./pkg/mmokit/
go test ./pkg/universe/ ./pkg/mmokit/ -count=1
```

Expected: vet clean, all tests pass.

- [ ] **Step 7: Commit**

```bash
git add pkg/engine/loop.go pkg/universe/cell.go pkg/universe/commands.go pkg/mmokit/commands_test.go
git commit -m "$(cat <<'EOF'
engine: per-system Commands flush via AfterSystem hook

Adds engine.Hooks.AfterSystem callback fired after each system's
Update. Universe wires it to stage.commands.flush() so structural
changes queued in System N are visible to System N+1 in the same
tick.

Exports Commands.Flush() (was FlushForTest); engine + tests are the
only callers.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Add `SystemBase.Commands()` shortcut + `stage.TickOne(sys)` test helper

**Files:**
- Modify: `pkg/mmokit/system.go` (add Commands shortcut)
- Modify: `pkg/universe/stage.go` (add TickOne helper)
- Test: extend `pkg/mmokit/commands_test.go`

- [ ] **Step 1: Add `Commands()` to SystemBase**

In `pkg/mmokit/system.go`, after the existing `Stage()` accessor (line 24):

```go
// Commands returns the per-stage deferred-mutation buffer. Shortcut for
// s.Stage().Commands(). Use inside Update to queue structural ECS
// changes that would otherwise panic under the locked-world rule.
func (b *SystemBase) Commands() *Commands { return b.stage.Commands() }
```

- [ ] **Step 2: Add `Stage.TickOne` test helper**

In `pkg/universe/stage.go`, after the Commands accessor:

```go
// TickOne is a test-only helper that ticks a single system: calls
// sys.Update(dt), then flushes the Commands buffer. Mirrors the
// engine game loop's per-system contract so unit tests outside the
// full loop see the same mutation visibility.
//
// Not for production use — the game loop drives the real flush.
func (b *Stage) TickOne(sys engine.System, dt float32) {
	sys.Update(dt)
	if b.commands != nil {
		b.commands.Flush()
	}
}
```

Verify the `engine.System` interface name (`Update(float32)` is the contract). Run:
```bash
grep -n "type System interface\|type System struct" pkg/engine/*.go
```

- [ ] **Step 3: Write a test using TickOne**

Append to `pkg/mmokit/commands_test.go`:

```go
// TestSystemBase_CommandsShortcut verifies s.Commands() returns the
// same buffer as s.Stage().Commands() and ops queued via the shortcut
// apply through TickOne.
func TestSystemBase_CommandsShortcut(t *testing.T) {
	stage := newTestStageForCommands(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap1[component.Position](w)
	h := mapper.NewEntity(&component.Position{X: 0, Y: 0})

	sys := &commandsTestSystem{handle: h}
	WireSystem(sys, w, stage.Engine(), stage)

	stage.TickOne(sys, 0.05)

	velMap := ecs.NewMap1[component.Velocity](w)
	if !velMap.HasAll(h) {
		t.Fatal("system's queued AddComponent did not apply at tick")
	}
}

// Minimal fake system that exercises s.Commands() inside Update.
type commandsTestSystem struct {
	SystemBase
	handle ecs.Entity
}

func (s *commandsTestSystem) Init() {}

func (s *commandsTestSystem) Update(dt float32) {
	e := EntityFromECS(s.Stage(), s.handle)
	AddComponent(s.Commands(), e, component.Velocity{X: 7, Y: 11})
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./pkg/mmokit/ -run "TestSystemBase_CommandsShortcut" -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/system.go pkg/mmokit/commands_test.go pkg/universe/stage.go
git commit -m "$(cat <<'EOF'
mmokit: SystemBase.Commands() shortcut + Stage.TickOne test helper

SystemBase gets s.Commands() so systems don't have to write
s.Stage().Commands() inside Update. Stage.TickOne(sys, dt) is the
test-side mirror of the engine loop's per-system flush contract.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Add query wrappers (`Any`, `FindOne`, `ForEach1/2/3`)

**Files:**
- Create: `pkg/mmokit/queries.go`
- Create: `pkg/mmokit/queries_test.go`

- [ ] **Step 1: Write the failing test**

`pkg/mmokit/queries_test.go`:

```go
package mmokit

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
)

// TestAny_True returns true when an entity with T exists.
func TestAny_True(t *testing.T) {
	stage := newTestStageForCommands(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap1[component.Position](w)
	mapper.NewEntity(&component.Position{X: 1})

	if !Any[component.Position](stage) {
		t.Fatal("Any returned false for present component")
	}
}

// TestAny_False returns false when no entity has T.
func TestAny_False(t *testing.T) {
	stage := newTestStageForCommands(t)
	if Any[component.Position](stage) {
		t.Fatal("Any returned true for absent component")
	}
}

// TestFindOne returns the first matching entity.
func TestFindOne(t *testing.T) {
	stage := newTestStageForCommands(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap1[component.Position](w)
	want := mapper.NewEntity(&component.Position{X: 42})

	got, ok := FindOne[component.Position](stage)
	if !ok {
		t.Fatal("FindOne returned ok=false")
	}
	if got.Handle() != want {
		t.Fatalf("FindOne returned handle %v, want %v", got.Handle(), want)
	}
}

// TestForEach1 iterates every entity with T.
func TestForEach1(t *testing.T) {
	stage := newTestStageForCommands(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap1[component.Position](w)
	for i := 0; i < 5; i++ {
		mapper.NewEntity(&component.Position{X: float32(i)})
	}

	count := 0
	ForEach1[component.Position](stage, func(e Entity, pos *component.Position) {
		count++
	})
	if count != 5 {
		t.Fatalf("ForEach1 visited %d entities, want 5", count)
	}
}

// TestForEach2 iterates entities with both T1 and T2.
func TestForEach2(t *testing.T) {
	stage := newTestStageForCommands(t)
	w := stage.ECSWorld()
	pv := ecs.NewMap2[component.Position, component.Velocity](w)
	pOnly := ecs.NewMap1[component.Position](w)
	pv.NewEntity(&component.Position{X: 1}, &component.Velocity{X: 2})
	pv.NewEntity(&component.Position{X: 3}, &component.Velocity{X: 4})
	pOnly.NewEntity(&component.Position{X: 99}) // should NOT be visited

	count := 0
	ForEach2[component.Position, component.Velocity](stage,
		func(e Entity, p *component.Position, v *component.Velocity) {
			count++
		})
	if count != 2 {
		t.Fatalf("ForEach2 visited %d entities, want 2", count)
	}
}

// TestForEach3 iterates entities with three required components.
func TestForEach3(t *testing.T) {
	stage := newTestStageForCommands(t)
	w := stage.ECSWorld()
	mp := ecs.NewMap3[component.Position, component.Velocity, component.Rotation](w)
	mp.NewEntity(
		&component.Position{X: 1},
		&component.Velocity{X: 2},
		&component.Rotation{Angle: 0.5},
	)

	count := 0
	ForEach3[component.Position, component.Velocity, component.Rotation](stage,
		func(e Entity, p *component.Position, v *component.Velocity, r *component.Rotation) {
			count++
		})
	if count != 1 {
		t.Fatalf("ForEach3 visited %d entities, want 1", count)
	}
}
```

- [ ] **Step 2: Run the test, expect FAIL**

```bash
go test ./pkg/mmokit/ -run "TestAny_|TestFindOne|TestForEach" -count=1
```

Expected: undefined: Any / FindOne / ForEach1 / ForEach2 / ForEach3.

- [ ] **Step 3: Create `pkg/mmokit/queries.go`**

```go
package mmokit

import (
	"github.com/mlange-42/ark/ecs"

	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// Any reports whether any entity in the stage carries component T.
// Replaces the `ecs.NewFilter1[T] + query.Next()` idiom for one-shot
// existence checks.
func Any[T any](stage *pkguniverse.Stage) bool {
	filter := ecs.NewFilter1[T](stage.ECSWorld())
	q := filter.Query()
	defer q.Close()
	return q.Next()
}

// FindOne returns the first entity carrying T, if any. Order is
// implementation-defined (ark archetype-iteration order). Used for
// singleton lookups like "find the station entity."
func FindOne[T any](stage *pkguniverse.Stage) (Entity, bool) {
	filter := ecs.NewFilter1[T](stage.ECSWorld())
	q := filter.Query()
	defer q.Close()
	if !q.Next() {
		return Entity{}, false
	}
	return EntityFromECS(stage, q.Entity()), true
}

// ForEach1 iterates every entity carrying T, invoking fn for each.
// The closure receives the wrapped Entity and a pointer to T. Queueing
// Commands ops inside the closure is safe — they flush after this
// iteration completes (after the calling system's Update returns).
func ForEach1[T any](stage *pkguniverse.Stage, fn func(Entity, *T)) {
	filter := ecs.NewFilter1[T](stage.ECSWorld())
	q := filter.Query()
	defer q.Close()
	for q.Next() {
		t := q.Get()
		fn(EntityFromECS(stage, q.Entity()), t)
	}
}

// ForEach2 iterates entities carrying both T1 and T2.
func ForEach2[T1, T2 any](stage *pkguniverse.Stage, fn func(Entity, *T1, *T2)) {
	filter := ecs.NewFilter2[T1, T2](stage.ECSWorld())
	q := filter.Query()
	defer q.Close()
	for q.Next() {
		t1, t2 := q.Get()
		fn(EntityFromECS(stage, q.Entity()), t1, t2)
	}
}

// ForEach3 iterates entities carrying T1, T2, and T3.
func ForEach3[T1, T2, T3 any](stage *pkguniverse.Stage, fn func(Entity, *T1, *T2, *T3)) {
	filter := ecs.NewFilter3[T1, T2, T3](stage.ECSWorld())
	q := filter.Query()
	defer q.Close()
	for q.Next() {
		t1, t2, t3 := q.Get()
		fn(EntityFromECS(stage, q.Entity()), t1, t2, t3)
	}
}
```

- [ ] **Step 4: Run the test, expect PASS**

```bash
go test ./pkg/mmokit/ -run "TestAny_|TestFindOne|TestForEach" -count=1
```

Expected: all six tests pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/queries.go pkg/mmokit/queries_test.go
git commit -m "$(cat <<'EOF'
mmokit: query wrappers — Any, FindOne, ForEach1/2/3

Drop-in replacements for raw ecs.NewFilterN + query.Next() / Close()
patterns. All auto-close on return. Game code can do one-shot
existence checks and iterations without touching ark/ecs directly.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Add `mmokit.EntityHandle` type alias

**Files:**
- Create: `pkg/mmokit/entity_handle.go`

- [ ] **Step 1: Create the alias file**

```go
package mmokit

import (
	"github.com/mlange-42/ark/ecs"
)

// EntityHandle is the raw ECS entity handle type — an alias for
// ark's ecs.Entity. Use this type when you need to store, pass, or
// compare-zero a bare entity reference without going through the
// richer mmokit.Entity wrapper (which carries a Stage and NetID).
//
// Most game code should use mmokit.Entity (the wrapper) for ECS
// operations — Get/Set/Has/Send all take Entity. EntityHandle exists
// for cases that have to interoperate with framework types that
// already use the raw handle, e.g. engine.PlayerSession.Entity.
type EntityHandle = ecs.Entity
```

- [ ] **Step 2: Verify it compiles**

```bash
go vet ./pkg/mmokit/
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/entity_handle.go
git commit -m "$(cat <<'EOF'
mmokit: add EntityHandle type alias for ark's ecs.Entity

Lets game code name the raw ECS handle type without importing
ark/ecs. Used in Phase 6 to erase the last ark imports from
internal/game/ (handler parameters, session.Entity field references).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 2 — Migrate panic-class structural mutations

### Task 8: Migrate `system_npc_ai.go::pendingLeashClears` to RemoveComponent

**Files:**
- Modify: `internal/game/system_npc_ai.go`

- [ ] **Step 1: Read the current implementation**

```bash
sed -n '20,100p' internal/game/system_npc_ai.go
sed -n '265,290p' internal/game/system_npc_ai.go
```

Note the current shape:
- `pendingLeashClears []ecs.Entity` field on `NPCAISystem`
- `tickLeash` appends `self.Handle()` to it in two branches
- `Update` drains the slice after the for loop, calling `Map.Remove`

- [ ] **Step 2: Run existing tests as a baseline**

```bash
go test ./internal/game/ -run "TestNPCAI" -count=1
```

Expected: PASS. Note the count for the regression check.

- [ ] **Step 3: Replace `tickLeash`'s slice appends with Commands calls**

In `internal/game/system_npc_ai.go`, find the two `s.pendingLeashClears = append(...)` calls and replace each with:

```go
// Before:
s.pendingLeashClears = append(s.pendingLeashClears, self.Handle())

// After:
mmokit.RemoveComponent[gamecomp.Leashing](s.Commands(), self)
```

- [ ] **Step 4: Delete the drain loop in `Update`**

Remove the entire block at the end of `Update`:

```go
// Drain leash clears now that the query has released the world lock.
if len(s.pendingLeashClears) > 0 {
	w := s.Stage().ECSWorld()
	leashMap := ecs.NewMap1[gamecomp.Leashing](w)
	for _, h := range s.pendingLeashClears {
		if h == (ecs.Entity{}) {
			continue
		}
		if leashMap.HasAll(h) {
			leashMap.Remove(h)
		}
	}
	s.pendingLeashClears = s.pendingLeashClears[:0]
}
```

(All four lines through the closing `}`.)

- [ ] **Step 5: Delete the `pendingLeashClears` field**

From the NPCAISystem struct:

```go
// Delete:
pendingLeashClears []ecs.Entity
```

(Also delete the multi-line comment block above it.)

- [ ] **Step 6: Run tests, expect PASS**

```bash
go test ./internal/game/ -run "TestNPCAI" -count=1
go vet ./internal/game/
```

Expected: PASS, vet clean. The Leashing-clear behavior is now driven by Commands; the engine's per-system flush replaces the manual drain.

- [ ] **Step 7: Commit**

```bash
git add internal/game/system_npc_ai.go
git commit -m "$(cat <<'EOF'
ai: migrate NPC leash-clear to Commands.RemoveComponent

Drops the pendingLeashClears slice + post-Update drain. The same
"queue inside locked query, apply after iteration completes" guarantee
now comes from the engine-wide Commands flush.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8b (continued): Migrate `dieKeepEntity` Dormant-add to AddComponent

**Files:**
- Modify: `internal/game/game.go`
- Modify: `internal/game/hooks.go` (delete the drain)
- Modify: `internal/game/gameworld.go` (delete the PendingDeathMarker struct)

- [ ] **Step 1: Replace the enqueue in `dieKeepEntity`**

In `internal/game/game.go`, find:

```go
mmokit.Enqueue(gw.Queue, PendingDeathMarker{ConnID: s.ConnID})
```

Replace with:

```go
mmokit.AddComponent(gw.stage.Commands(), entity, mmokit.Dormant{})
```

Here `entity` is the local `mmokit.Entity` wrapper already available in `dieKeepEntity`'s scope (see the existing `entity := mmokit.EntityFromECS(...)` near the top of `dieKeepEntity`).

- [ ] **Step 2: Delete the postFlush drain in hooks.go**

In `internal/game/hooks.go`, find the block in `postFlush`:

```go
// Apply Dormant to entities that transitioned to StateDead this tick.
// Done here (post-FlushRemovals, world unlocked) instead of in the
// dieKeepEntity action because that action runs inside the death
// observer's locked-world query iteration.
for _, m := range mmokit.Drain[PendingDeathMarker](gw.Queue) {
	sess := gw.Players.ByConnID(m.ConnID)
	if sess == nil {
		continue
	}
	entity := mmokit.EntityFromECS(gw.stage, sess.Entity)
	if !entity.Alive() {
		continue
	}
	if mmokit.Has[mmokit.Dormant](entity) {
		continue
	}
	mmokit.Set(entity, mmokit.Dormant{})
}
```

Delete the entire block (comment + loop).

- [ ] **Step 3: Delete `PendingDeathMarker` from gameworld.go**

In `internal/game/gameworld.go`, delete the struct definition and its comment:

```go
// PendingDeathMarker records that a player has transitioned to
// StateDead — the dieKeepEntity action queues it instead of marking
// Dormant directly, because the action runs inside the death
// observer's locked-world query iteration (which would panic on
// component add). postFlush drains the queue and applies the
// Dormant marker in a phase where the world is unlocked.
type PendingDeathMarker struct {
	ConnID uint32
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/game/ -count=1
```

Expected: all PASS. The dormant-on-death behavior is unchanged externally; internal mechanism is now Commands.

- [ ] **Step 5: Commit**

```bash
git add internal/game/game.go internal/game/hooks.go internal/game/gameworld.go
git commit -m "$(cat <<'EOF'
death: migrate Dormant-on-death to Commands.AddComponent

Replaces PendingDeathMarker + postFlush drain with a single
mmokit.AddComponent call inside dieKeepEntity. Same outcome —
Dormant lands at the next safe boundary — but the queue + drain
boilerplate is gone and the structural mutation can't accidentally
panic if someone reorders code.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Migrate `hooks.go::processUndocks` raw `dormantMap.Remove` to Commands

**Files:**
- Modify: `internal/game/hooks.go`

- [ ] **Step 1: Find the current raw-ark call**

```bash
grep -n "dormantMap" internal/game/hooks.go
```

In `processUndocks` you'll see:

```go
dormantMap := ecs.NewMap1[mmokit.Dormant](gw.stage.ECSWorld())
if dormantMap.HasAll(s.Entity) {
    dormantMap.Remove(s.Entity)
}
```

- [ ] **Step 2: Replace with Commands.RemoveComponent**

Replace the 4-line block with:

```go
mmokit.RemoveComponent[mmokit.Dormant](gw.stage.Commands(), entity)
```

Where `entity` is the local `mmokit.Entity` wrapper already in scope (see the line `entity := mmokit.EntityFromECS(...)` earlier in `processUndocks`).

- [ ] **Step 3: Run tests**

```bash
go test ./internal/game/ -run "TestDock\|TestUndock\|TestPlayer" -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/game/hooks.go
git commit -m "$(cat <<'EOF'
hooks: migrate undock Dormant-remove to Commands.RemoveComponent

Eliminates the raw ark Map1[Dormant].Remove call in processUndocks.
The undock path runs in postFlush (world unlocked) so the structural
change was already safe, but routing through Commands keeps the
"never reach for ark directly from game code" invariant clean.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Delete component-priming calls

**Files:**
- Modify: `internal/game/system_npc_ai.go`
- Modify: `internal/game/system_targetlock.go`
- Modify: `internal/game/system_poi.go`
- Modify: `internal/game/gameworld.go`
- Modify: `internal/game/hooks.go`

These priming calls (`ecs.NewMap1[T](w)` in Init/elsewhere) were workarounds for ark's "first-touch of a component type must be outside a query" quirk. After Phase 2, mmokit's Get/Has/Set/Commands ops auto-prime via the internal map cache, so explicit priming is unnecessary.

- [ ] **Step 1: Find every priming call**

```bash
grep -rn "ecs.NewMap1\[.*\](w)\|ecs.NewMap1\[.*\](gw.stage" internal/game/ --include="*.go"
```

You should see entries like:
- `system_npc_ai.go::Init`: `ecs.NewMap1[gamecomp.Leashing](w)`, `ecs.NewMap1[mmokit.Dormant](w)`, `ecs.NewMap1[gamecomp.POIAnchor](w)`
- `system_targetlock.go::Init`: similar primings
- `system_poi.go::Init`: Leashing prime
- `gameworld.go`: maybe Dormant prime
- `hooks.go`: places that compute a Map1 just to do an op (now replaced by Commands)

- [ ] **Step 2: Delete each priming line + its surrounding comment**

For each match, remove:
- The `ecs.NewMap1[T](w)` line.
- Any "// Belt-and-suspenders: prime ..." or "// force component registration" comment block above it.

Leave the `Init()` function body coherent — if removing all primings empties the function, leave the method declaration with just `s.gw = mmokit.State[GameWorld](s.Stage())` or whatever non-priming setup remains.

- [ ] **Step 3: Run the full game test suite**

```bash
go test ./internal/game/ -count=1
```

Expected: PASS. If any test fails with a "component not registered" panic, the priming was actually load-bearing — restore that specific prime and leave a comment about why.

- [ ] **Step 4: Commit**

```bash
git add internal/game/system_npc_ai.go internal/game/system_targetlock.go internal/game/system_poi.go internal/game/gameworld.go internal/game/hooks.go
git commit -m "$(cat <<'EOF'
game: drop ecs.NewMap1 priming workarounds

The first-touch-outside-query quirk that motivated these primings is
no longer reachable from game code: every component access goes
through mmokit.Get/Has/Set/Commands, all of which auto-prime via
the internal map cache.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 3 — Migrate game actions to `Defer`

### Task 11: Migrate `PendingLootDrop` to `Defer(SpawnLootCrate)`

**Files:**
- Modify: the producer site (the death observer / handlePlayerKilled / equivalent)
- Modify: `internal/game/hooks.go` (delete drain)
- Modify: `internal/game/gameworld.go` (delete struct)

- [ ] **Step 1: Find the producer**

```bash
grep -rn "PendingLootDrop{" internal/game/ --include="*.go"
```

You'll find the call site(s) where `mmokit.Enqueue(gw.Queue, PendingLootDrop{...})` happens. Usually inside a death observer or kill credit handler.

- [ ] **Step 2: Replace each producer call**

For each enqueue:

```go
// Before:
mmokit.Enqueue(gw.Queue, PendingLootDrop{X: x, Y: y, Items: drops})

// After:
gw.stage.Commands().Defer(func() {
	gw.SpawnLootCrate(x, y, drops)
})
```

Note: `gw` and the local variables `x`, `y`, `drops` are captured by the closure.

If the producer runs inside a system's Update (locked-world), use `s.Commands().Defer(...)` instead — but typically these producers run in safe phases (post-tick observers, etc.).

- [ ] **Step 3: Delete the drain in `hooks.go::postFlush`**

```go
// Delete:
for _, drop := range mmokit.Drain[PendingLootDrop](gw.Queue) {
	gw.SpawnLootCrate(drop.X, drop.Y, drop.Items)
}
```

- [ ] **Step 4: Delete the struct**

In `internal/game/gameworld.go`:

```go
// Delete:
type PendingLootDrop struct {
	X, Y  float32
	Items map[uint32]int32
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/game/ -run "TestLoot\|TestDeath\|TestKill" -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/game/gameworld.go internal/game/hooks.go internal/game/<producer-file>.go
git commit -m "$(cat <<'EOF'
loot: migrate PendingLootDrop to Commands.Defer

SpawnLootCrate is now invoked directly via a deferred closure
captured at the death-observer site. Drains the postFlush queue
and the typed struct.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: Migrate `PendingDockRequest` to `Defer`

**Files:**
- Modify: dock input handler
- Modify: `internal/game/hooks.go` (delete `processDocks` drain or rewrite it)
- Modify: `internal/game/gameworld.go` (delete struct)

- [ ] **Step 1: Find the producer + drain**

```bash
grep -rn "PendingDockRequest\b" internal/game/ --include="*.go"
```

You'll find:
- Producer: an input handler (probably in `input_handlers.go`) that enqueues on dock request.
- Drain: a `processDocks` function in `hooks.go`.

- [ ] **Step 2: Convert the dock-start logic into a method on GameWorld**

In `internal/game/hooks.go` (or a new helper file), refactor the drain body into a method:

```go
// startDocking initiates the docking sequence for the given session
// against the resolved station entity. Pulled out of the old
// PendingDockRequest drain so it can be invoked directly from a
// Defer closure.
func (gw *GameWorld) startDocking(s *mmokit.PlayerSession, station mmokit.Entity, stationPos mmokit.Position) {
	// ... existing logic from the old drain body ...
}
```

(Use the existing drain body verbatim — just rename `req.ConnID` → `s.ConnID` etc. as variables become parameters.)

- [ ] **Step 3: Replace the producer with a Defer**

In the input handler:

```go
// Before:
mmokit.Enqueue(gw.Queue, PendingDockRequest{ConnID: conn.ConnID, StationNetID: msg.StationNetID})

// After (after station/sess validation, capturing locals):
gw.stage.Commands().Defer(func() {
	gw.startDocking(sess, station, stationPos)
})
```

If the input handler doesn't already have `sess`, `station`, and `stationPos` resolved, do the resolution inline so the closure captures them.

- [ ] **Step 4: Delete the drain function**

Delete `processDocks` (or whatever the drain is named) and its call site in `postFlush`.

- [ ] **Step 5: Delete `PendingDockRequest` struct**

In `gameworld.go`.

- [ ] **Step 6: Run dock tests**

```bash
go test ./internal/game/ -run "TestDock\|TestUndock" -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/game/input_handlers.go internal/game/hooks.go internal/game/gameworld.go
git commit -m "$(cat <<'EOF'
dock: migrate PendingDockRequest to Commands.Defer + GameWorld.startDocking

Dock flow now runs as an explicit method invoked from a deferred
closure at the input handler. Drains the typed-queue boilerplate;
keeps the docking state machine intact.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: Migrate `PendingUndockRequest` to `Defer`

**Files:**
- Modify: undock input handler
- Modify: `internal/game/hooks.go` (rewrite `processUndocks` as a method)
- Modify: `internal/game/gameworld.go`

Same pattern as Task 12. Refactor the body of `processUndocks` into `gw.startUndock(s *PlayerSession)`. Replace the producer with `cmds.Defer(func() { gw.startUndock(sess) })`. Delete `PendingUndockRequest` and the drain.

- [ ] **Step 1: Refactor `processUndocks` body into `gw.startUndock(sess)`**
- [ ] **Step 2: Replace producer enqueue with `Defer`**
- [ ] **Step 3: Delete the drain function and the struct**
- [ ] **Step 4: Run dock/undock tests** — `go test ./internal/game/ -run "TestUndock" -count=1`
- [ ] **Step 5: Commit** — message: `undock: migrate PendingUndockRequest to Commands.Defer`

---

### Task 14: Migrate `PendingRespawn` to `Defer`

**Files:**
- Modify: `internal/game/hooks.go::postTick` (auto-respawn timer producer)
- Modify: `internal/game/hooks.go::processRespawns` (rewrite as method)
- Modify: `internal/game/input_handlers.go` (Respawn input handler producer)
- Modify: `internal/game/gameworld.go`

Same pattern. `processRespawns` body → `gw.executeRespawn(connID, sess)` method. Both producers (the autoRespawnAt-timer drain in `postTick` and the `Respawn` input handler) switch from `mmokit.Enqueue(gw.Queue, PendingRespawn{...})` to `gw.stage.Commands().Defer(func() { gw.executeRespawn(connID, sess) })`.

- [ ] **Step 1: Refactor `processRespawns` body into `gw.executeRespawn`**
- [ ] **Step 2: Replace both enqueue sites with Defer**
- [ ] **Step 3: Delete the drain function and the struct**
- [ ] **Step 4: Run respawn tests** — `go test ./internal/game/ -run "TestRespawn\|TestDeath" -count=1`
- [ ] **Step 5: Commit** — `respawn: migrate PendingRespawn to Commands.Defer`

---

### Task 15: Migrate `PendingLootAll` to `Defer`

**Files:**
- Modify: producer (loot-all input handler)
- Modify: `internal/game/hooks.go` (drain → method)
- Modify: `internal/game/gameworld.go`

Same pattern.

- [ ] **Step 1: Find usages** — `grep -rn "PendingLootAll" internal/game/ --include="*.go"`
- [ ] **Step 2: Refactor + migrate + delete**
- [ ] **Step 3: Run tests** — `go test ./internal/game/ -run "TestLoot" -count=1`
- [ ] **Step 4: Commit** — `loot: migrate PendingLootAll to Commands.Defer`

---

## Phase 4 — Delete the old queue infrastructure

### Task 16: Delete `gw.Queue` field and verify zero PendingX references

**Files:**
- Modify: `internal/game/gameworld.go` (delete field + struct init)
- Modify: anywhere that referenced `gw.Queue`

- [ ] **Step 1: Verify all Pending* types are gone**

```bash
grep -rn "PendingLootDrop\|PendingDeathMarker\|PendingDockRequest\|PendingUndockRequest\|PendingRespawn\|PendingLootAll" internal/
```

Expected: NO results. If there are stragglers, complete the corresponding migration task before proceeding.

- [ ] **Step 2: Verify no callers of `mmokit.Enqueue` / `mmokit.Drain` remain in game code**

```bash
grep -rn "mmokit.Enqueue\|mmokit.Drain\b" internal/
```

Expected: NO results.

- [ ] **Step 3: Delete the `Queue` field from GameWorld**

In `internal/game/gameworld.go`:

```go
// Delete:
Queue *mmokit.TickQueue
```

Also delete the initialization wherever GameWorld is constructed (`Queue: mmokit.NewTickQueue()` or equivalent).

- [ ] **Step 4: Delete `gw.Queue.ClearAll()` in clearTickState**

In `internal/game/hooks.go::clearTickState`, find and delete:

```go
gw.Queue.ClearAll()
```

- [ ] **Step 5: Run all game tests**

```bash
go test ./internal/game/ -count=1
go vet ./internal/game/
```

Expected: PASS, vet clean.

- [ ] **Step 6: Commit**

```bash
git add internal/game/gameworld.go internal/game/hooks.go internal/game/game.go
git commit -m "$(cat <<'EOF'
game: delete gw.Queue field and all PendingX residuals

Every PendingX type has been migrated to Commands.AddComponent
or Commands.Defer. The TickQueue field on GameWorld has no
remaining producers or consumers.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 17: Delete `pkg/engine/tickqueue.go` and mmokit aliases

**Files:**
- Delete: `pkg/engine/tickqueue.go`
- Modify: `pkg/mmokit/mmokit.go` (delete TickQueue alias + Enqueue/Drain funcs)

- [ ] **Step 1: Verify nothing outside the file uses TickQueue**

```bash
grep -rn "TickQueue\|engine.TickQueue" --include="*.go" .
```

Expected: only the definitions in `pkg/engine/tickqueue.go` and the aliases/helpers in `pkg/mmokit/mmokit.go`. Test files might reference these — check those too.

```bash
grep -rn "mmokit.NewTickQueue\|engine.NewTickQueue\|NewTickQueue" --include="*.go" .
```

If any remain (e.g. in older tests), update those tests to use Commands or delete them if obsolete.

- [ ] **Step 2: Delete `pkg/engine/tickqueue.go`**

```bash
git rm pkg/engine/tickqueue.go
```

If there's a test file `pkg/engine/tickqueue_test.go`, delete it too:

```bash
git rm pkg/engine/tickqueue_test.go 2>/dev/null || true
```

- [ ] **Step 3: Delete the mmokit alias and helpers**

In `pkg/mmokit/mmokit.go`, find and delete:

```go
// type TickQueue = engine.TickQueue          (around line 115)
// func Enqueue[T any](q *engine.TickQueue, event T) { ... }   (around line 1098)
// func Drain[T any](q *engine.TickQueue) []T { ... }            (around line 1103)
```

Also delete any "NewTickQueue" alias if present.

- [ ] **Step 4: Build everything**

```bash
go build ./...
go vet ./...
go test ./pkg/... ./internal/... -count=1
```

Expected: clean build, all tests pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/engine/ pkg/mmokit/mmokit.go
git commit -m "$(cat <<'EOF'
engine,mmokit: delete TickQueue + Enqueue/Drain helpers

All callers migrated to Commands.AddComponent / Defer. The typed
queue + drain pattern is gone from the codebase.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 5 — Migrate query construction in game code

### Task 18: Migrate `gameworld.go::hasStation` to `mmokit.Any`

**Files:**
- Modify: `internal/game/gameworld.go`

- [ ] **Step 1: Find the current implementation**

```bash
grep -n "func.*hasStation" internal/game/gameworld.go
```

Current code:

```go
func (gw *GameWorld) hasStation() bool {
	filter := ecs.NewFilter1[gamecomp.Station](gw.stage.ECSWorld())
	query := filter.Query()
	defer query.Close()
	return query.Next()
}
```

- [ ] **Step 2: Replace with `mmokit.Any`**

```go
func (gw *GameWorld) hasStation() bool {
	return mmokit.Any[gamecomp.Station](gw.stage)
}
```

- [ ] **Step 3: Run tests + vet**

```bash
go test ./internal/game/ -run "TestRespawn\|TestStation" -count=1
go vet ./internal/game/
```

Expected: PASS, vet clean.

- [ ] **Step 4: Commit**

```bash
git add internal/game/gameworld.go
git commit -m "game: hasStation uses mmokit.Any[Station]

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 19: Migrate `entity_station.go` filter to `mmokit.ForEach3`

**Files:**
- Modify: `internal/game/entity_station.go`

- [ ] **Step 1: Find the existing query**

```bash
grep -n "ecs.NewFilter" internal/game/entity_station.go
```

Inspect the current 3-component filter (Station + Position + CellCoord).

- [ ] **Step 2: Replace with ForEach3**

Convert the `for query.Next() { ... }` loop body into the closure body for `ForEach3`:

```go
mmokit.ForEach3[gamecomp.Station, mmokit.Position, mmokit.CellCoord](gw.stage,
	func(e mmokit.Entity, st *gamecomp.Station, pos *mmokit.Position, cc *mmokit.CellCoord) {
		// body of the original for query.Next() loop, with:
		//   q.Entity() → e.Handle()
		//   q.Get1() / q.Get2() / etc → st, pos, cc
	})
```

If the original code uses `query.Entity()` to break early or skip, restructure to return from the closure (continue equivalent) or maintain external state for the break case.

- [ ] **Step 3: Drop the `ark/ecs` import if no other ark references remain in the file**

```bash
grep -n "ecs\." internal/game/entity_station.go
```

If empty, remove the `github.com/mlange-42/ark/ecs` import.

- [ ] **Step 4: Build + test**

```bash
go vet ./internal/game/
go test ./internal/game/ -run "TestStation\|TestEntity" -count=1
```

Expected: PASS, vet clean.

- [ ] **Step 5: Commit**

```bash
git add internal/game/entity_station.go
git commit -m "station: entity scan via mmokit.ForEach3

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 20: Migrate `entity_poi.go` filter to `mmokit.ForEach1`

Same pattern as Task 19, but for the single-component `POIAnchor` filter.

- [ ] **Step 1: Replace `ecs.NewFilter1[gamecomp.POIAnchor]` + Query loop with `mmokit.ForEach1[gamecomp.POIAnchor]`**
- [ ] **Step 2: Remove `ark/ecs` import if unused**
- [ ] **Step 3: Build + test** — `go test ./internal/game/ -run "TestPOI" -count=1`
- [ ] **Step 4: Commit** — `poi: anchor scan via mmokit.ForEach1`

---

### Task 21: Migrate `op_bank.go` filter to `mmokit.ForEach2`

Same pattern, for the `Station + Position` proximity filter.

- [ ] **Step 1: Replace `ecs.NewFilter2[gamecomp.Station, mmokit.Position]` with `mmokit.ForEach2[gamecomp.Station, mmokit.Position]`**
- [ ] **Step 2: Remove ark import if unused**
- [ ] **Step 3: Build + test** — `go test ./internal/game/ -run "TestBank\|TestStation" -count=1`
- [ ] **Step 4: Commit** — `bank: station proximity via mmokit.ForEach2`

---

## Phase 6 — Erase remaining ark imports from internal/game/

### Task 22: Swap `ecs.Entity` for `mmokit.EntityHandle` in type-alias-only files

**Files (6 files):**
- Modify: `internal/game/entity_npc.go`
- Modify: `internal/game/entity_ship.go`
- Modify: `internal/game/factory.go`
- Modify: `internal/game/game.go`
- Modify: `internal/game/system_ability.go`
- Modify: `internal/game/system_network.go`
- Modify: `internal/game/transfer.go`

For each file:

- [ ] **Step 1: Find all `ecs.Entity` references**

```bash
grep -n "ecs.Entity" internal/game/<filename>.go
```

- [ ] **Step 2: Replace each `ecs.Entity` with `mmokit.EntityHandle`**

Use a per-file edit. Example for `entity_npc.go`:

```go
// Before:
func (gw *GameWorld) SpawnNPC(x, y float32, archetype uint8, poiNetID uint32) ecs.Entity {
    // ...
}

// After:
func (gw *GameWorld) SpawnNPC(x, y float32, archetype uint8, poiNetID uint32) mmokit.EntityHandle {
    // ...
}
```

Repeat for every occurrence in the file: parameter types, return types, struct field types, map key/value types.

- [ ] **Step 3: Remove the `ark/ecs` import**

If the import is now unused (no `ecs.` references remain), delete the import line. `go vet` will fail if you leave an unused import.

- [ ] **Step 4: Build the file**

```bash
go vet ./internal/game/
```

Expected: clean. If `goimports` ran and re-added the ark import, manually re-delete it.

- [ ] **Step 5: Test**

```bash
go test ./internal/game/ -count=1
```

Expected: PASS.

- [ ] **Step 6: Repeat for all 7 files**

(Yes, 7 — the task description says 6 but `transfer.go` is the 7th in the spec list.)

- [ ] **Step 7: Verify final state**

```bash
grep -rn "mlange-42/ark/ecs" internal/game/ --include="*.go"
```

Expected: NO results. If any remain, that file still has a non-type-alias ark usage (likely a Map.Remove or NewFilter that wasn't caught in Phase 5). Investigate and migrate.

- [ ] **Step 8: Commit (one commit for all 7 files)**

```bash
git add internal/game/entity_npc.go internal/game/entity_ship.go internal/game/factory.go internal/game/game.go internal/game/system_ability.go internal/game/system_network.go internal/game/transfer.go
git commit -m "$(cat <<'EOF'
game: replace ecs.Entity type alias usage with mmokit.EntityHandle

These files only used ark/ecs for the Entity type alias — never for
actual ECS operations. Switching to mmokit.EntityHandle (alias for
ecs.Entity) removes the ark import and matches the "no ark in
internal/game/" invariant.

Verified: `grep -rn "mlange-42/ark/ecs" internal/game/` returns
nothing.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 7 — Documentation + enforcement

### Task 23: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add a new section under "Architecture" (or near the ECS section)**

Insert a new section titled `### Deferred ECS mutations (Commands)`:

```markdown
### Deferred ECS mutations (Commands)

Game-side code never imports `github.com/mlange-42/ark/ecs` directly.
Structural mutations (component add/remove, entity despawn) go through
the per-stage **Commands** buffer. Inside a system's `Update`, queue
ops via `s.Commands()`; the engine flushes after each system's Update,
so System N's mutations are visible to System N+1 in the same tick.

API surface (in `pkg/mmokit`):

- `s.Commands().Despawn(e)` — queue entity destruction.
- `mmokit.AddComponent(s.Commands(), e, val)` — queue component add/overwrite (T inferred).
- `mmokit.RemoveComponent[T](s.Commands(), e)` — queue component removal (T explicit).
- `s.Commands().Defer(func(){...})` — escape hatch for multi-step game-action logic that doesn't fit a single ECS primitive.
- `mmokit.Set[T]` is the IMMEDIATE write — only call from non-query contexts (hooks, postFlush handlers, command verbs). Inside a system's Update loop, always use `AddComponent`.

For one-shot queries, use `mmokit.Any[T](stage)`, `mmokit.FindOne[T](stage)`, `mmokit.ForEach1/2/3[T](stage, fn)`. Sticky per-tick iteration in hot systems still uses `mmokit.Query[Bundle]` with the bundle-struct pattern — that's unchanged.

**Hard invariant:** `internal/game/` must not import `github.com/mlange-42/ark/ecs`. Use `mmokit.Entity` for ECS-bound entities, `mmokit.EntityHandle` for the raw handle type. Engine internals in `pkg/system/`, `pkg/universe/`, `pkg/engine/` continue to import ark directly — that's deliberate (perf, framework proximity).
```

- [ ] **Step 2: Verify CLAUDE.md is still under any size hints in the existing memory rules**

The existing CLAUDE.md is large but the project ships it intentionally. No further trimming required.

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs: CLAUDE.md — document Commands buffer API and no-ark rule

Adds a Deferred ECS mutations section under Architecture. Documents
the four Commands ops, the "no ark/ecs imports in internal/game/"
invariant, and the Set-vs-AddComponent distinction (immediate vs
deferred).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 24: Add optional CI grep check (no-ark-in-game)

**Files:**
- Create: `scripts/no_ark_in_game.sh`
- Modify: `justfile` (add `just lint-no-ark` recipe)

- [ ] **Step 1: Create the script**

`scripts/no_ark_in_game.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

# Enforces the architectural invariant that game-side code does not
# import ark/ecs directly. Game code uses mmokit's wrappers.
#
# Exits non-zero if any internal/game/*.go file imports
# github.com/mlange-42/ark/ecs.

OFFENDERS=$(grep -rln "mlange-42/ark/ecs" internal/game/ --include="*.go" || true)

if [[ -n "${OFFENDERS}" ]]; then
    echo "ERROR: internal/game/ files import ark/ecs directly:"
    echo "${OFFENDERS}"
    echo ""
    echo "Use mmokit wrappers instead:"
    echo "  - mmokit.Entity / mmokit.EntityHandle for entity types"
    echo "  - mmokit.AddComponent / RemoveComponent for structural mutations"
    echo "  - mmokit.Any / FindOne / ForEach1/2/3 for queries"
    echo "  - s.Commands().Despawn / Defer for entity/game-action deferral"
    exit 1
fi

echo "OK: no ark/ecs imports in internal/game/"
```

- [ ] **Step 2: Make it executable**

```bash
chmod +x scripts/no_ark_in_game.sh
```

- [ ] **Step 3: Add justfile recipe**

Append to `justfile`:

```
# Enforces the no-ark-in-game architectural invariant
lint-no-ark:
    ./scripts/no_ark_in_game.sh
```

- [ ] **Step 4: Run it**

```bash
just lint-no-ark
```

Expected: `OK: no ark/ecs imports in internal/game/`

- [ ] **Step 5: Commit**

```bash
git add scripts/no_ark_in_game.sh justfile
git commit -m "$(cat <<'EOF'
ci: add lint-no-ark script enforcing the game/-no-ark invariant

Simple grep-based check that fails when internal/game/ imports
github.com/mlange-42/ark/ecs. Catches regressions where a new
system reaches for ark directly instead of going through mmokit
wrappers.

Run via: just lint-no-ark
EOF
)"
```

---

## Self-Review

### Spec coverage

Walking through each section of [docs/superpowers/specs/2026-05-13-ecs-commands-buffer-design.md](../specs/2026-05-13-ecs-commands-buffer-design.md):

- **API surface** (Commands type, Despawn, Defer, AddComponent, RemoveComponent free funcs) — Tasks 1, 3.
- **Query wrappers** (Any, FindOne, ForEach1/2/3) — Task 6.
- **Flush semantics** (per-system, between Update and next system) — Task 4 (engine integration), Task 5 (TickOne test helper).
- **Despawn-at-flush diverges from MarkForRemoval** — Task 1 (Despawn calls `world.RemoveEntity` directly at flush, not MarkForRemoval). ✓
- **What gets deleted** — Tasks 16, 17 (gw.Queue field, tick_queue.go, aliases), Task 22 (ark imports in internal/game/).
- **Phase 1: Land the new API** — Tasks 1-7.
- **Phase 2: Migrate panic-class structural mutations** — Tasks 8, 8b, 9, 10.
- **Phase 3: Migrate game actions to Defer** — Tasks 11-15.
- **Phase 4: Delete the old queue** — Tasks 16-17.
- **Phase 5: Migrate query construction** — Tasks 18-21.
- **Phase 6: Erase remaining ark imports** — Task 22.
- **Phase 7: Documentation + enforcement** — Tasks 23-24.

### Type consistency

- `Commands` defined in `pkg/universe/commands.go` (Task 1), aliased in mmokit (Task 3) — consistent across tasks.
- `Flush` (exported, was `FlushForTest` in Task 3 draft) — finalized as `Flush` in Task 4. Task 3's example uses `Flush()` accordingly.
- `mmokit.EntityHandle` introduced in Task 7 — used consistently in Task 22.
- `s.Commands()` shortcut introduced in Task 5 — used in subsequent migration tasks.

### Verified file-path references

All paths cross-checked with the actual codebase during planning:
- `pkg/mmokit/system.go:24` is the `Stage()` accessor (confirmed).
- `pkg/engine/loop.go:134-139` is the systems loop (confirmed).
- `pkg/universe/stage.go:155` is the Stage struct definition (confirmed).
- `pkg/mmokit/components.go` is where `Get/Has/Set` live (confirmed).

### Risks called out

- Task 22 may encounter files where `ecs.Entity` usage was actually a deeper ark dependency (Map manipulation hidden behind the alias). The verification step (`grep -rn "mlange-42/ark/ecs" internal/game/`) is the gate; any straggler indicates a missed migration in Phase 5.
- Task 4's wire-up may require finding the exact construction site of `engine.Hooks` in `pkg/universe/`. The grep step is the discovery mechanism — if the file or pattern differs, adapt the edit location.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-13-ecs-commands-buffer.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
