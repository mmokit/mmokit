# Typed SystemBase[W] + Auto-Bound Queries Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the `gw *World` stash field, the `WorldOf[*World](s)` call, and the `s` parameter from query setup — by making `SystemBase` generic on the world type and auto-binding `Query[T]` fields during the system lifecycle.

**Architecture:** Phase 1 introduces `Query.With(opts...)` (no `s` parameter) alongside the existing `Query.Init(sys, opts...)`. Phase 2 migrates every callsite. Phase 3 makes `SystemBase` generic with a typed `World()` accessor and adds `BindQueries`/`BuildQueries` lifecycle methods that the coordinator drives between `SetDeps` and `Init` (and after `Init` for build). Phase 4 cleans up: `gw` fields, `gwFromSystem` helper, no-op `With()` calls, and BotSystem's incorrect `IncludeAll()`.

**Tech Stack:** Go 1.25, Ark ECS v0.7.1, reflect, unsafe.Pointer (already used by `pkg/query/query.go` for bundle field offsets).

---

## Spec Reference

[docs/superpowers/specs/2026-04-26-typed-systembase-design.md](../specs/2026-04-26-typed-systembase-design.md)

## File Inventory

**Framework code (touched in Phase 1 + 3):**

- `pkg/query/query.go` — add `With(opts...)` method, internal `built` flag, internal `build(ecs)` method.
- `pkg/engine/system.go` — make `SystemBase` generic, add `World()`, `BindQueries(outer)`, `BuildQueries()` methods.
- `pkg/universe/coordinator.go:1369-1378` — wire new lifecycle steps after `SetDeps` and after `Init`.
- `pkg/mmokit/mmokit.go` — add `WireSystem` test helper, update `SystemBase` re-export.

**Migration targets (Phase 2 + 3 + 4):**

Files using `Query.Init(sys, ...)` (Phase 2):

- `pkg/system/click_to_move.go`, `direction_move.go`, `lifetime.go`, `physics.go`, `spatial_system.go`
- `pkg/universe/boundary_system.go`
- `internal/game/system_ability.go`, `system_collision.go`, `system_docking.go`, `system_economy.go`, `system_equipment.go`, `system_mining.go`, `system_network.go`, `system_shieldregen.go`, `system_ship_dynamics.go`, `system_statuseffect.go`, `system_targetlock.go`, `system_wander.go`
- `examples/4node-basic/system_bots.go`, `system_debug_info.go`
- `examples/simple/main.go`
- `pkg/system/click_to_move_test.go`, `direction_move_test.go` (test files using `sys.SetDeps` + `q.Init`)

Files embedding `SystemBase` (atomic generic-rename in Phase 3):

- All of the above + `pkg/mmokit/mmokit.go` (`defaultNetworkSystem`, `networkSystem[W]`, ~3 internal types) + `pkg/mmokit/topology.go` (`topologyBroadcaster`).

Files with `gw *World` stash + `gwFromSystem` calls (Phase 4):

- All ~10 `internal/game/system_*.go` files
- `examples/4node-basic/system_bots.go`
- `pkg/mmokit/topology.go`
- `internal/game/system_util.go` (delete entirely — only contains `gwFromSystem`)

## Phase Overview

| Phase | What | Risk | Tasks |
|---|---|---|---|
| 1 | Add `Query.With` alongside `Query.Init` | Low (additive) | 1-2 |
| 2 | Migrate `Query.Init` callers to `With` | Low (mechanical) | 3-7 |
| 3 | Generic `SystemBase[W]` + auto-bind queries (atomic embed sweep) | Medium (atomic, but mechanical) | 8-9 |
| 4 | Eliminate `gw` stash + cleanup | Low (mechanical) | 10-13 |
| 5 | Verification | Low | 14 |

---

## Task 1: Add `Query.With(opts...)` with internal build state

**Files:**

- Modify: `pkg/query/query.go`
- Test: `pkg/query/query_with_test.go` (new)

This task adds `With(opts...)` as a parallel API to the existing `Init(sys, opts...)`. `Init` continues to work exactly as today; `With` is the new no-`sys` shape. We add a `built bool` flag on `Query[T]` that both `Init` and the future framework auto-bind set to `true`, and `With` panics if called after `built` is set.

- [ ] **Step 1: Write the failing test** for `With` happy path (single option)

Create `pkg/query/query_with_test.go`:

```go
package query

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmokit/pkg/component"
)

type withTestSystem struct {
	w *ecs.World
	q Query[struct {
		Pos *component.Position
	}]
}

func (s *withTestSystem) ECSWorld() *ecs.World { return s.w }

func TestQuery_With_AccumulatesOptions(t *testing.T) {
	w := ecs.NewWorld()
	s := &withTestSystem{w: w}

	// Configure: include-all + exclude Velocity.
	s.q.With(IncludeAll())
	s.q.With(Without[component.Velocity]())

	// Caller drives the build manually for now (Phase 3 wires this through SystemBase).
	s.q.build(w)

	// No exclusion of Ghost/Replica because IncludeAll cleared them.
	// Should still range without panic.
	count := 0
	for range s.q {
		count++
	}
	if count != 0 {
		t.Fatalf("expected 0 entities, got %d", count)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/query/ -run TestQuery_With_AccumulatesOptions -v`
Expected: FAIL with "s.q.With undefined" (compile error).

- [ ] **Step 3: Implement `With` + internal `build` + `built` flag**

In `pkg/query/query.go`, change the `Query[T]` shape from a function type to a struct that wraps the rangefunc. Today it's:

```go
type Query[T any] func(yield func(ecs.Entity, *T) bool)
```

Replace with:

```go
type Query[T any] struct {
	iter  func(yield func(ecs.Entity, *T) bool)
	opts  []QueryOption
	built bool
}
```

Add a range-over-func method so `for _, b := range q` keeps working:

```go
// Iter returns the rangefunc form. Used implicitly by `for _, b := range q`.
func (q *Query[T]) Iter(yield func(ecs.Entity, *T) bool) {
	if q.iter == nil {
		panic("query.Query: ranged before build (call With during system Init, or use NewQuery for ad-hoc)")
	}
	q.iter(yield)
}
```

In Go 1.25, `for k, v := range q` calls `q.Iter(yield)` when `Iter` is a `func(yield func(K,V) bool)` method. **Verify this works** — if not, fall back to a `Range(yield)` named method and update callsites. (Implementation note: today `Query[T]` is itself the func, so `for _, b := range q` ranges over the func directly. Switching to a struct means we need a range method or callers go via `q.Iter`.)

Add `With`:

```go
// With configures the query with the given options. Options accumulate across
// calls. Must be called during the system's Init() — calling With after the
// framework's build phase panics.
func (q *Query[T]) With(opts ...QueryOption) *Query[T] {
	if q.built {
		panic("query.Query: With called after build (only call inside system Init)")
	}
	q.opts = append(q.opts, opts...)
	return q
}
```

Add internal `build(ecs)`:

```go
// build materializes the underlying ECS filter from the accumulated options.
// Called by SystemBase[W].BuildQueries() after the system's Init() returns.
// Package-private — use With() to configure, then let the framework build.
func (q *Query[T]) build(w *ecs.World) {
	if q.built {
		panic("query.Query: build called twice")
	}
	q.iter = build[T](q.opts, w)
	q.built = true
}
```

Refactor the existing `build[T]` helper to take `opts` and `*ecs.World` directly (removing the `sys` plumbing). The body stays the same — just replace `sys.ECSWorld()` with the passed `w`:

```go
// build is the shared constructor used by NewQuery and Query.build.
func build[T any](opts []QueryOption, w *ecs.World) func(yield func(ecs.Entity, *T) bool) {
	fields := buildFields[T](w)
	filter := buildFilter(w, fields, opts)
	var bundle T
	base := unsafe.Pointer(&bundle)
	return func(yield func(ecs.Entity, *T) bool) {
		uq := filter.Query()
		for uq.Next() {
			for i := range fields {
				fm := &fields[i]
				fieldPtr := (*unsafe.Pointer)(unsafe.Add(base, fm.offset))
				if fm.optional && !uq.Has(fm.compID) {
					*fieldPtr = nil
				} else {
					*fieldPtr = uq.Get(fm.compID)
				}
			}
			if !yield(uq.Entity(), &bundle) {
				uq.Close()
				return
			}
		}
	}
}
```

Update existing `Query.Init` to call the new internal helpers (preserves today's API):

```go
func (q *Query[T]) Init(sys interface{ ECSWorld() *ecs.World }, opts ...QueryOption) {
	if q.built {
		panic("query.Query: Init called twice")
	}
	q.opts = append(q.opts, opts...)
	q.iter = build[T](q.opts, sys.ECSWorld())
	q.built = true
}
```

Update `NewQuery` similarly:

```go
func NewQuery[T any](sys interface{ ECSWorld() *ecs.World }, opts ...QueryOption) Query[T] {
	q := Query[T]{}
	q.opts = opts
	q.iter = build[T](q.opts, sys.ECSWorld())
	q.built = true
	return q
}
```

- [ ] **Step 4: Run all `pkg/query` tests to verify nothing regressed**

Run: `go test ./pkg/query/ -v`
Expected: all existing tests still PASS, plus the new `TestQuery_With_AccumulatesOptions` PASSes.

If existing tests fail because `for _, b := range s.q { ... }` no longer works on a struct: switch the Query iteration call sites in tests to `s.q.Iter` or add a method named `All` that returns the rangefunc:

```go
// All returns the entries as a rangefunc. Use as `for e, b := range q.All() { ... }`.
func (q *Query[T]) All() func(yield func(ecs.Entity, *T) bool) {
	if q.iter == nil {
		panic("query.Query: ranged before build")
	}
	return q.iter
}
```

If `for _, b := range q` (no method call) is required to keep working without changing every caller, the cleanest path is to keep `Query[T]` as a function type and store the opts/built state in a sidecar. **Try the struct approach first** (it's simpler); fall back only if the migration to ranging via a method becomes too invasive.

- [ ] **Step 5: Add the `With-after-build` panic test**

Append to `pkg/query/query_with_test.go`:

```go
func TestQuery_With_AfterBuild_Panics(t *testing.T) {
	w := ecs.NewWorld()
	s := &withTestSystem{w: w}
	s.q.With(IncludeAll())
	s.q.build(w)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	s.q.With(Without[component.Velocity]())
}
```

- [ ] **Step 6: Run new test**

Run: `go test ./pkg/query/ -run TestQuery_With_AfterBuild -v`
Expected: PASS.

- [ ] **Step 7: Run full repo build to be sure nothing else broke**

Run: `go vet ./...`
Expected: no errors. If there are errors anywhere from the `Query[T]` struct/func change, decide whether to add the `Iter`/`All` method or revert to the func-type approach with sidecar state.

- [ ] **Step 8: Commit**

```bash
git add pkg/query/query.go pkg/query/query_with_test.go
git commit -m "$(cat <<'EOF'
feat(query): add Query.With(opts...) for sys-less query configuration

Adds a parallel API to Query.Init that drops the sys parameter — the
framework can call build(ecs) directly from a typed lifecycle hook
introduced in a later commit. Init continues to work as before; With
just accumulates options and the new internal build() materializes the
filter when invoked.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Generic `SystemBase[W]` skeleton with `World()` accessor

**Why no separate "migrate Query.Init → Query.With" task before this one:** Doing the Query migration before SystemBase is generic forces a transitional helper (something to call `build(ecs)` from inside each system's Init body without a coordinator hook to drive it). That's wasted churn. Instead, each system migrates Query.Init → Query.With **at the same time** as it migrates `s.gw = WorldOf[*W](s)` → `s.World()` (Tasks 5-8). Until then, `Query.Init(s, opts...)` keeps working unchanged — Task 4's `Built()` bridge prevents the framework's `BuildQueries` from double-building queries that were already built via the legacy path.

**Files:**

- Modify: `pkg/engine/system.go` (replace existing `SystemBase` struct with generic version)
- Modify: `pkg/mmokit/mmokit.go:114` (update re-export to be generic too)
- Test: `pkg/engine/system_test.go` (new)

This task introduces the generic shape but does NOT yet wire the auto-bind machinery. The goal is to land the type change and ripple it through every embed site in one atomic commit, while the behavior is identical to today (just typed `World()` accessor added).

**Atomic constraint:** Go disallows two types named `SystemBase` in the same package. Once `engine.SystemBase` becomes generic, every `mmokit.SystemBase` and `engine.SystemBase` embed line in the codebase becomes a compile error until the type parameter is supplied. This task therefore migrates every embed site in the same commit.

- [ ] **Step 1: Write the failing test** for typed `World()`

Create `pkg/engine/system_test.go`:

```go
package engine

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
)

type stubWorld struct{ tag string }

type testSystem struct {
	SystemBase[*stubWorld]
}

func TestSystemBase_TypedWorld(t *testing.T) {
	s := &testSystem{}
	w := &stubWorld{tag: "hello"}
	s.SetDeps(ecs.NewWorld(), nil, w)

	got := s.World()
	if got == nil || got.tag != "hello" {
		t.Fatalf("World() returned %+v, want stubWorld{tag:\"hello\"}", got)
	}
}

func TestSystemBase_TypeMismatch_Panics(t *testing.T) {
	type wrongWorld struct{}
	s := &testSystem{}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	s.SetDeps(ecs.NewWorld(), nil, &wrongWorld{})
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./pkg/engine/ -run TestSystemBase -v`
Expected: FAIL with compile errors (`SystemBase[*stubWorld]` not yet generic, `World()` method doesn't exist).

- [ ] **Step 3: Make `SystemBase` generic in `pkg/engine/system.go`**

Replace the existing `SystemBase` struct (currently lines 14-38) with:

```go
package engine

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"
)

// System is the interface all game systems implement.
// Embed SystemBase[W] for automatic dependency injection via SetDeps/Init.
type System interface {
	Update(dt float32)
}

// SystemBase provides dependency injection for systems, parameterized on the
// game world type. Embed it in your system struct to get ECSWorld(), Engine(),
// GameWorld(), and the typed World() accessor. The framework calls SetDeps()
// then Init() before the first Update().
//
// Game systems use SystemBase[*MyWorld] for typed access; engine-side systems
// that don't need world methods use SystemBase[any].
type SystemBase[W any] struct {
	ecsWorld *ecs.World
	eng      *Engine
	world    W
	// queries is populated in BindQueries (called by the framework after
	// SetDeps); BuildQueries iterates it after the user's Init() returns.
	queries []queryBuildable
}

// queryBuildable is implemented by *query.Query[T] (defined in pkg/query).
// SystemBase only sees this minimal contract — it doesn't import pkg/query
// (would be a cycle); discovery records pointers via reflection at SetDeps.
type queryBuildable interface {
	BuildFromECS(w *ecs.World)
}

// ECSWorld returns the ECS world for this cell.
func (b *SystemBase[W]) ECSWorld() *ecs.World { return b.ecsWorld }

// Engine returns the engine for this cell.
func (b *SystemBase[W]) Engine() *Engine { return b.eng }

// GameWorld returns the game world as `any`. Prefer the typed World()
// accessor — GameWorld is kept for callers that need the untyped form
// (e.g. the network system's reflection-based config path).
func (b *SystemBase[W]) GameWorld() any { return b.world }

// World returns the typed game world. The type parameter W is supplied at
// the embed site (e.g. `SystemBase[*MyWorld]`).
func (b *SystemBase[W]) World() W { return b.world }

// Init is called once after SetDeps + BindQueries. Override to call
// q.With(opts...) on any query that needs non-default options. After Init
// returns, the framework calls BuildQueries to materialize each query's
// underlying ECS filter.
func (b *SystemBase[W]) Init() {}

// SetDeps is called by the framework to inject dependencies. Panics if gw
// is not assignable to W — opensource callers hit this immediately instead
// of debugging a silent nil.
func (b *SystemBase[W]) SetDeps(w *ecs.World, eng *Engine, gw any) {
	b.ecsWorld = w
	b.eng = eng
	typed, ok := gw.(W)
	if !ok {
		var zero W
		panic(fmt.Sprintf("engine.SystemBase[%T]: GameWorld is %T, not assignable", zero, gw))
	}
	b.world = typed
}
```

**Note:** `BindQueries` and `BuildQueries` come in Task 3. This task only adds the typed-world plumbing.

- [ ] **Step 4: Run the new tests** — they should now compile but the rest of the repo won't

Run: `go vet ./pkg/engine/`
Expected: PASS for the engine package itself (all `SystemBase` uses inside this package are gone).

Run: `go vet ./...`
Expected: FAIL with errors at every site that embeds `engine.SystemBase` or `mmokit.SystemBase`. Note them — they'll be migrated in Step 5.

- [ ] **Step 5: Update `mmokit.SystemBase` re-export**

In `pkg/mmokit/mmokit.go:114`:

```go
// SystemBase is the generic base for all systems. Embed it with the game's
// typed world: `mmokit.SystemBase[*MyWorld]`. Engine-side systems that don't
// need world methods use `mmokit.SystemBase[any]`.
type SystemBase[W any] = engine.SystemBase[W]
```

- [ ] **Step 6: Atomic embed sweep — every `SystemBase`-embedding type gets a type parameter**

The choice of `W` per file:

- **`pkg/system/*.go`** (engine-side, no game-specific world): `engine.SystemBase[any]`. Affected: `click_to_move.go`, `direction_move.go`, `lifetime.go`, `physics.go`, `spatial_system.go`.
- **`pkg/universe/boundary_system.go`**: `engine.SystemBase[BoundaryWorld]` (it already uses `bw BoundaryWorld` field — typed embed makes the field redundant; clean up in Phase 4).
- **`pkg/mmokit/mmokit.go`** internal types (`defaultNetworkSystem`, `networkSystem[W]`, etc.): keep as `engine.SystemBase[any]` for now; they call `s.GameWorld().(...)` patterns that already handle typing per-call.
- **`pkg/mmokit/topology.go`** (`topologyBroadcaster`): `engine.SystemBase[topologyBroadcasterWorld]` (the interface is already defined in this file).
- **`internal/game/system_*.go`** (10+ files): `mmokit.SystemBase[*GameWorld]`.
- **`examples/4node-basic/system_*.go`** (`system_bots.go`, `system_debug_info.go`): `mmokit.SystemBase[*World]`.
- **`examples/simple/main.go`**: `mmokit.SystemBase[any]` (no game world type).

For each file, the edit is mechanical: change `mmokit.SystemBase` → `mmokit.SystemBase[*GameWorld]` (or whichever W applies). Use:

```bash
# From repo root, audit which files need editing:
grep -rn "engine\.SystemBase\b\|mmokit\.SystemBase\b" --include="*.go" .
```

The list should match the file inventory above.

For each file in the list, edit the embed line. **Don't** touch `Init()` / `Update()` / `gw` field / `gwFromSystem` calls in this task — those are Phase 4. The goal here is just: every embed line gets a type parameter, and the world (untyped via `GameWorld()`) is still accessed via the existing dance.

- [ ] **Step 7: Run `go vet ./...`**

Run: `go vet ./...`
Expected: PASS. If any embed site was missed, the error message names the file and line.

- [ ] **Step 8: Run all tests**

Run: `just build && go test ./...`
Expected: PASS. All systems still work the same — only the typed `World()` accessor is new (and not yet used by anything).

- [ ] **Step 9: Run the new SystemBase tests**

Run: `go test ./pkg/engine/ -run TestSystemBase -v`
Expected: PASS for both `TypedWorld` and `TypeMismatch_Panics`.

- [ ] **Step 10: Commit**

```bash
git add pkg/engine/system.go pkg/engine/system_test.go pkg/mmokit/mmokit.go \
        pkg/system/*.go pkg/universe/boundary_system.go pkg/mmokit/topology.go \
        internal/game/system_*.go examples/4node-basic/system_*.go examples/simple/main.go
git commit -m "$(cat <<'EOF'
refactor(engine): make SystemBase generic on world type

Adds a type parameter to engine.SystemBase (and the mmokit.SystemBase
alias) and a typed World() accessor. Every embed site is updated to
specify the world type. No behavior change yet — Init/Update bodies and
the gw stash field stay as-is. The auto-bind machinery and gw cleanup
land in subsequent commits.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Add `BindQueries` + `BuildQueries` lifecycle hooks

**Files:**

- Modify: `pkg/engine/system.go` (add methods on `SystemBase[W]`)
- Modify: `pkg/query/query.go` (add `BuildFromECS` to satisfy the `queryBuildable` interface — already declared in Task 2)
- Test: `pkg/engine/system_test.go` (extend)

This task adds the field-discovery + build machinery to `SystemBase[W]`. It doesn't yet wire the coordinator to call them — that's Task 4. After this task, `SystemBase[W]` has the new methods but the coordinator still uses the old SetDeps-then-Init flow.

- [ ] **Step 1: Write the failing test** for auto-bind

Append to `pkg/engine/system_test.go`:

```go
import (
	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/query"
)

type autoBindSystem struct {
	SystemBase[*stubWorld]
	pos query.Query[struct {
		Pos *component.Position
	}]
}

func TestSystemBase_AutoBindsQueries(t *testing.T) {
	s := &autoBindSystem{}
	w := ecs.NewWorld()
	s.SetDeps(w, nil, &stubWorld{})

	// Mimic framework lifecycle.
	s.BindQueries(s)
	s.Init()
	s.BuildQueries()

	// Range without panic; default exclusions apply (Ghost + Replica).
	count := 0
	for range s.pos.Iter {
		count++
	}
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/engine/ -run TestSystemBase_AutoBindsQueries -v`
Expected: FAIL with "BindQueries undefined" (compile error).

- [ ] **Step 3: Implement `BindQueries` + `BuildQueries` on `SystemBase[W]`**

Add to `pkg/engine/system.go`:

```go
import (
	"reflect"
	"unsafe"
)

// BindQueries discovers query.Query[T] fields on the outer system struct
// via reflection and records them for the build phase. Called by the
// framework after SetDeps. The outer parameter is the embedding system
// pointer (e.g. *BotSystem) — SystemBase needs the outer to reflect over
// fields beyond itself.
func (b *SystemBase[W]) BindQueries(outer any) {
	v := reflect.ValueOf(outer)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		panic("engine.SystemBase: BindQueries requires *Struct")
	}
	v = v.Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		ft := t.Field(i)
		// Look for fields whose type implements queryBuildable. The
		// pointer-receiver method set on *query.Query[T] satisfies it.
		fp := unsafe.Pointer(v.Field(i).UnsafeAddr())
		// Reconstruct an interface value pointing at the field via
		// reflection: take the address, then assert.
		field := reflect.NewAt(ft.Type, fp).Interface()
		if qb, ok := field.(queryBuildable); ok {
			b.queries = append(b.queries, qb)
		}
	}
}

// BuildQueries materializes each discovered query's ECS filter using the
// options the user accumulated during Init(). Called by the framework
// after Init() returns.
func (b *SystemBase[W]) BuildQueries() {
	for _, q := range b.queries {
		q.BuildFromECS(b.ecsWorld)
	}
}
```

Note: `unsafe.Pointer(v.Field(i).UnsafeAddr())` reaches unexported fields. `reflect.NewAt(ft.Type, fp)` creates a typed `*FieldType` interface. The `.(queryBuildable)` assertion succeeds when `FieldType`'s pointer receiver implements the interface.

- [ ] **Step 4: Add `BuildFromECS` to `query.Query[T]`**

In `pkg/query/query.go`, expose the previously-internal `build` as `BuildFromECS`:

```go
// BuildFromECS materializes the query's ECS filter from the accumulated
// options. Called by SystemBase[W].BuildQueries() — game code should not
// invoke this directly; use With(opts...) inside Init() and let the
// framework call BuildFromECS for you.
func (q *Query[T]) BuildFromECS(w *ecs.World) {
	if q.built {
		panic("query.Query: BuildFromECS called twice")
	}
	q.iter = build[T](q.opts, w)
	q.built = true
}
```

The internal `build(w)` method from Task 1 is no longer needed — delete it (callers move to `BuildFromECS`).

- [ ] **Step 5: Run the new test**

Run: `go test ./pkg/engine/ -run TestSystemBase_AutoBindsQueries -v`
Expected: PASS.

- [ ] **Step 6: Add the default-exclusions test**

Append to `pkg/engine/system_test.go`:

```go
type ghostExclusionSystem struct {
	SystemBase[*stubWorld]
	q query.Query[struct {
		Pos *component.Position
	}]
}

func TestSystemBase_DefaultExclusions(t *testing.T) {
	s := &ghostExclusionSystem{}
	w := ecs.NewWorld()
	s.SetDeps(w, nil, &stubWorld{})
	s.BindQueries(s)
	s.Init()
	s.BuildQueries()

	// Spawn one entity with a Ghost component — it should be excluded.
	mapper := ecs.NewMap2[component.Position, component.Ghost](w)
	mapper.NewEntity(&component.Position{}, &component.Ghost{})

	count := 0
	for range s.q.Iter {
		count++
	}
	if count != 0 {
		t.Fatalf("expected 0 (Ghost excluded by default), got %d", count)
	}
}
```

- [ ] **Step 7: Run the test**

Run: `go test ./pkg/engine/ -run TestSystemBase_DefaultExclusions -v`
Expected: PASS.

- [ ] **Step 8: Run full test suite to ensure nothing regressed**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add pkg/engine/system.go pkg/engine/system_test.go pkg/query/query.go
git commit -m "$(cat <<'EOF'
feat(engine): add SystemBase BindQueries/BuildQueries lifecycle

SystemBase[W] now reflects over its outer struct's fields at BindQueries
time, recording every Query[T] field. BuildQueries (called after Init)
materializes each query's filter using the options accumulated via With.
The coordinator wiring lands in the next commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Wire the coordinator to call BindQueries + BuildQueries

**Files:**

- Modify: `pkg/universe/coordinator.go:1366-1378` (system creation loop), `pkg/universe/coordinator.go:1212-1219` (`initSystems`)
- Modify: `pkg/mmokit/mmokit.go` (add `WireSystem` test helper)
- Test: `pkg/mmokit/wire_system_test.go` (new)

- [ ] **Step 1: Update the system creation loop**

In `pkg/universe/coordinator.go` around line 1366-1378, after `SetDeps`, call `BindQueries` if the system implements it:

```go
gameSystems := make([]engine.System, len(c.systemDefs))
systemNames := make([]string, len(c.systemDefs))
for i, def := range c.systemDefs {
	sys := def.Factory()

	type depsInjectable interface {
		SetDeps(w *ecs.World, eng *engine.Engine, gw any)
	}
	if di, ok := sys.(depsInjectable); ok {
		di.SetDeps(eng.ECS, eng, world)
	}

	type queryBinder interface {
		BindQueries(outer any)
	}
	if qb, ok := sys.(queryBinder); ok {
		qb.BindQueries(sys)
	}

	gameSystems[i] = sys
	systemNames[i] = def.Name
}
```

Also handle the `BoundarySystem` setup on line 1388-1392 (it does `bs.SetDeps(...)` directly; add `bs.BindQueries(bs)` right after).

- [ ] **Step 2: Update `initSystems` to call `BuildQueries` after `Init`**

In `pkg/universe/coordinator.go:1212-1219`:

```go
// initSystems calls Init() on each system that implements it, then
// triggers the query build phase for any system whose queries were
// auto-discovered via BindQueries.
func initSystems(systems []engine.System) {
	type initializable interface{ Init() }
	type queryBuilder interface{ BuildQueries() }
	for _, sys := range systems {
		if init, ok := sys.(initializable); ok {
			init.Init()
		}
		if qb, ok := sys.(queryBuilder); ok {
			qb.BuildQueries()
		}
	}
}
```

- [ ] **Step 3: Add `WireSystem` helper for test code**

In `pkg/mmokit/mmokit.go`, add (alongside the existing test/utility helpers):

```go
// WireSystem wires a system as the coordinator does — SetDeps, BindQueries,
// Init, BuildQueries — in one call. Use in tests where you want a fully-
// initialized system without spinning up a coordinator.
func WireSystem(sys engine.System, ecs *ecs.World, eng *engine.Engine, gw any) {
	type depsInjectable interface {
		SetDeps(w *ecs.World, eng *engine.Engine, gw any)
	}
	type queryBinder interface{ BindQueries(outer any) }
	type initializable interface{ Init() }
	type queryBuilder interface{ BuildQueries() }

	if di, ok := sys.(depsInjectable); ok {
		di.SetDeps(ecs, eng, gw)
	}
	if qb, ok := sys.(queryBinder); ok {
		qb.BindQueries(sys)
	}
	if i, ok := sys.(initializable); ok {
		i.Init()
	}
	if qb, ok := sys.(queryBuilder); ok {
		qb.BuildQueries()
	}
}
```

- [ ] **Step 4: Write a test for `WireSystem`**

Create `pkg/mmokit/wire_system_test.go`:

```go
package mmokit

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/engine"
	"github.com/zenion/mmokit/pkg/query"
)

type wireTestWorld struct{ tag string }

type wireTestSystem struct {
	engine.SystemBase[*wireTestWorld]
	q query.Query[struct {
		Pos *component.Position
	}]
}

func (s *wireTestSystem) Update(dt float32) {}

func TestWireSystem_FullLifecycle(t *testing.T) {
	w := ecs.NewWorld()
	s := &wireTestSystem{}
	WireSystem(s, w, nil, &wireTestWorld{tag: "ok"})

	if s.World().tag != "ok" {
		t.Fatalf("World() returned %+v", s.World())
	}

	count := 0
	for range s.q.Iter {
		count++
	}
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}
```

- [ ] **Step 5: Run the new test**

Run: `go test ./pkg/mmokit/ -run TestWireSystem -v`
Expected: PASS.

- [ ] **Step 6: Run full repo tests**

Run: `go test ./...`
Expected: PASS. Critical regression check: integration tests like `s7_*_test.go`, `s6_gateway_test.go`, and `pkg/system/*_test.go` should all still pass — the coordinator now calls `BindQueries`/`BuildQueries` for systems that have those methods (every system embedding `SystemBase[W]`), but the BodyClass-equivalent old systems migrated in Task 2 have empty query lists (no fields with `Query[T]` type yet — those still init via `q.Init(s, ...)` in their bodies).

The risk here: if Task 2 left `Init()` bodies that call `q.Init(s, ...)` AND the framework's `BuildQueries` ALSO sees those query fields and tries to build them again, we get a double-build panic. **Verify this doesn't happen**:

```bash
# Are any systems calling Init(s,...) on a Query that's also embedded directly on the system?
grep -rn "\.Init(s" --include="*.go" pkg/ internal/ examples/ | grep -v "_test.go"
```

If yes, the BindQueries path will discover those fields and try to build them in Step 6 — but `Init(s,...)` already set `built=true`, so `BuildFromECS` panics with "called twice". The fix: in `SystemBase[W].BuildQueries`, skip queries already built:

```go
func (b *SystemBase[W]) BuildQueries() {
	for _, q := range b.queries {
		// Skip queries already built via the legacy q.Init(s, ...) path.
		// This handles the migration window where some systems still use
		// Init(s,...) and others use With(...).
		if alreadyBuilt, ok := q.(interface{ Built() bool }); ok && alreadyBuilt.Built() {
			continue
		}
		q.BuildFromECS(b.ecsWorld)
	}
}
```

And add `Built()` to `query.Query`:

```go
func (q *Query[T]) Built() bool { return q.built }
```

This bridge is removed in Phase 4 (Task 11 — delete `Query.Init`). For now it makes the migration window safe.

- [ ] **Step 7: Re-run tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add pkg/universe/coordinator.go pkg/mmokit/mmokit.go pkg/mmokit/wire_system_test.go pkg/engine/system.go pkg/query/query.go
git commit -m "$(cat <<'EOF'
feat(universe): coordinator drives BindQueries + BuildQueries

After SetDeps, the coordinator calls BindQueries to let SystemBase[W]
discover Query[T] fields. After Init returns, BuildQueries materializes
each query's ECS filter. Adds mmokit.WireSystem for test code.
BuildQueries skips queries already built via the legacy Query.Init path,
keeping the migration window safe.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Migrate `pkg/system/*` from `Query.Init(s, opts...)` to `Query.With(opts...)` + remove redundant `Init()`

**Files:**

- Modify: `pkg/system/click_to_move.go`, `direction_move.go`, `lifetime.go`, `physics.go`, `spatial_system.go`
- Modify: `pkg/system/click_to_move_test.go`, `direction_move_test.go`

For each system:

1. Replace `s.entities.Init(s, opts...)` with `s.entities.With(opts...)` (or delete the call entirely if there are no opts — auto-bind handles defaults).
2. If the only thing `Init()` did was call `q.Init(s)` with no opts, delete the `Init()` method entirely.

- [ ] **Step 1: Migrate `pkg/system/physics.go`**

Currently (line 31-33):
```go
func (s *PhysicsSystem) Init() {
	s.entities.Init(s)
}
```

Delete the entire `Init()` method — auto-bind with default exclusions is what we want.

- [ ] **Step 2: Migrate `pkg/system/lifetime.go`**

Same pattern: delete the `Init()` method.

- [ ] **Step 3: Migrate `pkg/system/click_to_move.go`**

Same pattern: delete the `Init()` method.

- [ ] **Step 4: Migrate `pkg/system/direction_move.go`**

Same pattern: delete the `Init()` method.

- [ ] **Step 5: Migrate `pkg/system/spatial_system.go`**

Currently (line 39-41):
```go
func (s *SpatialSystem) Init() {
	s.entities.Init(s, query.IncludeAll())
}
```

Replace with:
```go
func (s *SpatialSystem) Init() {
	s.entities.With(query.IncludeAll())
}
```

(SpatialSystem deliberately uses `IncludeAll` — this is correct, see spec migration audit.)

- [ ] **Step 6: Update test files that call `sys.SetDeps + q.Init(s, ...)` directly**

`pkg/system/click_to_move_test.go` (and `direction_move_test.go`) currently set up systems by calling `sys.SetDeps(world, nil, nil)` then expect `q.Init(s)` to fire from the system's `Init()`. With `Init()` deleted, the queries no longer build — tests will panic with "ranged before build."

Replace each test setup with `mmokit.WireSystem(sys, world, nil, nil)`:

In `pkg/system/click_to_move_test.go`, change:

```go
sys := &ClickToMoveSystem{}
sys.SetDeps(world, nil, nil)
```

to:

```go
sys := &ClickToMoveSystem{}
mmokit.WireSystem(sys, world, nil, nil)
```

Add the `mmokit` import. **Watch for import cycle:** `pkg/system` already imports `pkg/engine`; `pkg/mmokit` imports both. If `pkg/system/*_test.go` importing `pkg/mmokit` creates a cycle (it shouldn't — tests live in the package with `_test.go` suffix; `pkg/mmokit` doesn't import `pkg/system`), proceed. If it does, define a local `wireSystem` helper in `pkg/system/test_util_test.go` that duplicates `WireSystem`'s logic.

Repeat for `direction_move_test.go`.

- [ ] **Step 7: Run pkg/system tests**

Run: `go test ./pkg/system/ -v`
Expected: PASS.

- [ ] **Step 8: Run full repo tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add pkg/system/
git commit -m "$(cat <<'EOF'
refactor(pkg/system): migrate Query.Init to With + drop default-only Init()

For PhysicsSystem, LifetimeSystem, ClickToMoveSystem, DirectionMoveSystem
the Init() method only called q.Init(s) with default exclusions —
deleted, auto-bind now handles it. SpatialSystem keeps Init() but uses
With(IncludeAll()) instead of the old Init(s, IncludeAll()) shape. Test
setup updated to use mmokit.WireSystem.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Migrate `pkg/universe/boundary_system.go` + `pkg/mmokit/topology.go`

**Files:**

- Modify: `pkg/universe/boundary_system.go`
- Modify: `pkg/mmokit/topology.go`

These two systems have non-default opts (`boundary` uses `IncludeAll` + `Without[Ghost]`; `topology` doesn't use queries at all but has the `gw` stash via `WorldOf`).

- [ ] **Step 1: Migrate `pkg/universe/boundary_system.go`**

Current `Init()` (line 47-50):
```go
func (s *BoundarySystem) Init() {
	s.entities.Init(s,
		query.IncludeAll(),
	)
}
```

Replace with:
```go
func (s *BoundarySystem) Init() {
	s.entities.With(query.IncludeAll())
}
```

- [ ] **Step 2: Migrate `pkg/mmokit/topology.go`**

Currently (line 85-88):
```go
func (s *topologyBroadcaster) Init() {
	s.gw = WorldOf[topologyBroadcasterWorld](s)
	s.sentHash = make(map[uint32]uint64)
}
```

The system embeds `engine.SystemBase[topologyBroadcasterWorld]` (set in Task 2), so `s.World()` returns `topologyBroadcasterWorld` directly. Replace:

```go
func (s *topologyBroadcaster) Init() {
	s.sentHash = make(map[uint32]uint64)
}
```

Delete the `gw topologyBroadcasterWorld` field at line 81. Replace every `s.gw.X()` reference in Update() (lines 91-onward) with `s.World().X()`.

- [ ] **Step 3: Run tests**

Run: `go test ./pkg/universe/ ./pkg/mmokit/ -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/boundary_system.go pkg/mmokit/topology.go
git commit -m "$(cat <<'EOF'
refactor: migrate BoundarySystem + topologyBroadcaster to With/World()

BoundarySystem swaps Query.Init(s, IncludeAll()) for With(IncludeAll()).
topologyBroadcaster drops its gw stash field and the WorldOf[W](s) call —
the typed World() accessor on SystemBase[topologyBroadcasterWorld] does
the same job for free.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Migrate `internal/game/system_*.go` — `gw` field elimination

**Files:**

- Modify: `internal/game/system_ability.go`, `system_collision.go`, `system_docking.go`, `system_economy.go`, `system_equipment.go`, `system_mining.go`, `system_network.go`, `system_shieldregen.go`, `system_ship_dynamics.go`, `system_statuseffect.go`, `system_targetlock.go`, `system_wander.go`
- Delete: `internal/game/system_util.go` (only contains `gwFromSystem`)

Each system has the same shape (Task 2 already migrated the embed line to `mmokit.SystemBase[*GameWorld]`):

```go
type FooSystem struct {
	mmokit.SystemBase[*GameWorld]   // already migrated in Task 2
	gw       *GameWorld              // delete
	entities mmokit.Query[Bundle]
}

func (s *FooSystem) Init() {
	s.gw = gwFromSystem(s.SystemBase)   // delete
	s.entities.Init(s, opts...)         // → s.entities.With(opts...) or delete entirely
}

func (s *FooSystem) Update(dt float32) {
	gw := s.gw                          // → gw := s.World()
	// ...
}
```

Per-system edits:

- [ ] **Step 1: `internal/game/system_shieldregen.go`**

```go
// Delete: gw *GameWorld field
// Init body: was `s.gw = gwFromSystem(s.SystemBase); s.entities.Init(s)` — delete entire Init() method
// Update: change `gw := s.gw` (or direct `s.gw.X()`) to use `s.World()`
```

Concretely: read the file, locate the `gw` field, delete it; locate Init(), inspect the body. If body is just `s.gw = gwFromSystem(s.SystemBase)` and `s.entities.Init(s)`, delete the Init method entirely (auto-bind handles the query). Replace `s.gw.X()` with `s.World().X()` in Update.

Run: `go vet ./internal/game/`
Expected: PASS.

- [ ] **Step 2: Repeat for `system_ability.go`, `system_collision.go`, `system_docking.go`, `system_economy.go`, `system_equipment.go`, `system_mining.go`, `system_ship_dynamics.go`, `system_statuseffect.go`, `system_targetlock.go`, `system_wander.go`**

Same mechanical edits. For each:

1. Delete `gw *GameWorld` field.
2. In Init: delete the `s.gw = gwFromSystem(...)` line.
3. In Init: change `s.entities.Init(s, opts...)` → `s.entities.With(opts...)`. If no opts, delete the line entirely.
4. If Init body is now empty, delete the method entirely.
5. Replace `s.gw.X()` everywhere (Update, helpers, etc.) with `s.World().X()`. Watch for cases like `gw := s.gw` at the top of Update — change to `gw := s.World()`.

Run: `go vet ./internal/game/`
Expected: PASS.

- [ ] **Step 3: Migrate `internal/game/system_network.go`**

This is the trickiest one — has TWO Query fields with `IncludeAll`:

```go
func (s *NetworkSystem) Init() {
	s.gw = gwFromSystem(s.SystemBase)
	s.locks.Init(s, mmokit.IncludeAll())
	s.lockVictims.Init(s, mmokit.IncludeAll())
}
```

Change to:

```go
func (s *NetworkSystem) Init() {
	s.locks.With(mmokit.IncludeAll())
	s.lockVictims.With(mmokit.IncludeAll())
}
```

Delete `gw *GameWorld` field. Replace `s.gw.X()` with `s.World().X()` in Update.

Run: `go vet ./internal/game/`
Expected: PASS.

- [ ] **Step 4: Delete `internal/game/system_util.go`**

The file contains only `gwFromSystem` (now unused after Steps 1-3):

Run: `git rm internal/game/system_util.go`

Then verify nothing else imports it: `grep -rn "gwFromSystem\|system_util" internal/ examples/ pkg/` should return zero matches.

- [ ] **Step 5: Run full game tests**

Run: `go test ./internal/game/...`
Expected: PASS.

- [ ] **Step 6: Run full repo tests + vet**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/game/
git commit -m "$(cat <<'EOF'
refactor(game): drop gw stash field; use SystemBase.World()

Every game system migrates from `gw *GameWorld` + `gwFromSystem(s.SystemBase)`
in Init() to `s.World()` calls in Update — the typed accessor on
SystemBase[*GameWorld] makes the stash redundant. NetworkSystem's
locks/lockVictims queries migrate from Init(s, IncludeAll()) to
With(IncludeAll()). Systems whose only Init body was the gw stash + a
default-options Query.Init now have no Init at all — auto-bind handles it.
gwFromSystem helper deleted.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Migrate `examples/4node-basic` + `examples/simple`

**Files:**

- Modify: `examples/4node-basic/system_bots.go`
- Modify: `examples/4node-basic/system_debug_info.go`
- Modify: `examples/simple/main.go`

- [ ] **Step 1: Migrate `examples/4node-basic/system_bots.go`**

Current shape:
```go
type BotSystem struct {
	mmokit.SystemBase[*World]   // already migrated in Task 2
	gw   *World                  // delete
	bots mmokit.Query[...]
}

func (s *BotSystem) Init() {
	s.gw = mmokit.WorldOf[*World](s)
	s.bots.Init(s, mmokit.IncludeAll())
}

func (s *BotSystem) Update(dt float32) {
	origin := s.gw.Cell()
	// ...
}
```

Target shape (per spec):
```go
type BotSystem struct {
	mmokit.SystemBase[*World]
	bots mmokit.Query[...]
}

// No Init() — IncludeAll() was wrong (bots are local-authority); defaults
// are correct. Auto-bind handles the rest.

func (s *BotSystem) Update(dt float32) {
	origin := s.World().Cell()
	// ...
}
```

Edits:
1. Delete `gw *World` field.
2. Delete the entire `Init()` method.
3. Replace `s.gw.X()` with `s.World().X()` in Update.

Run: `go vet ./examples/4node-basic/`
Expected: PASS.

- [ ] **Step 2: Migrate `examples/4node-basic/system_debug_info.go`**

Currently:
```go
func (s *DebugInfoSystem) Init() {
	s.entities.Init(s, mmokit.IncludeAll())
}
```

Change to:
```go
func (s *DebugInfoSystem) Init() {
	s.entities.With(mmokit.IncludeAll())
}
```

(The `IncludeAll()` itself is debatable but per spec migration audit, leave it — separate question.)

- [ ] **Step 3: Migrate `examples/simple/main.go`**

The system there uses `s.entities.Init(s, mmokit.IncludeAll())`. Change to `s.entities.With(mmokit.IncludeAll())`.

- [ ] **Step 4: Run example builds**

Run: `cd examples/4node-basic && go build ./...`
Expected: PASS.

Run: `cd examples/simple && go build ./...`
Expected: PASS.

- [ ] **Step 5: Run full test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add examples/
git commit -m "$(cat <<'EOF'
refactor(examples): migrate to SystemBase.World() and Query.With

BotSystem in 4node-basic loses its gw stash, the WorldOf[*World](s)
call, AND its Init method entirely — defaults (exclude Ghost+Replica)
are correct for locally-authoritative bots, so the spurious IncludeAll()
goes too. BotSystem is now the canonical "trivial system" example.
DebugInfoSystem and the simple example migrate Query.Init -> Query.With.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Migrate `pkg/mmokit/mmokit.go` internal types

**Files:**

- Modify: `pkg/mmokit/mmokit.go` (defaultNetworkSystem, networkSystem[W], possibly others around lines 1156-1340)

Internal mmokit systems still use `s.GameWorld().(...)` patterns. They embed `engine.SystemBase[any]` (set in Task 2). Since they don't have a single typed world (they probe interfaces), `World()` returning `any` doesn't help — leave their bodies unchanged. The only migration is for any `q.Init(s, ...)` calls on Query fields.

- [ ] **Step 1: Audit `pkg/mmokit/mmokit.go` for remaining Query.Init calls**

Run: `grep -n "\.Init(s\|\.Init(s," pkg/mmokit/mmokit.go`

For each match, change `q.Init(s, opts...)` to `q.With(opts...)` if the system embeds `SystemBase[W]`. If the call is on a Query that's NOT a struct field of a SystemBase-embedder (rare — would be ad-hoc construction in a function body), use `query.NewQuery[T](s, opts...)` instead.

- [ ] **Step 2: Audit `pkg/mmokit/topology.go` and other mmokit files**

Already done in Task 6. Re-verify with `grep -n "\.Init(s\|gwFromSystem\|WorldOf\[" pkg/mmokit/*.go`.

- [ ] **Step 3: Run tests**

Run: `go test ./pkg/mmokit/...`
Expected: PASS.

- [ ] **Step 4: Commit (if any changes were made)**

If audit found nothing: skip. Otherwise:

```bash
git add pkg/mmokit/
git commit -m "$(cat <<'EOF'
refactor(mmokit): finalize Query.Init -> With migration in internal types

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Delete `Query.Init` and the `Built()` bridge

**Files:**

- Modify: `pkg/query/query.go`
- Modify: `pkg/engine/system.go` (remove the `Built()` skip in `BuildQueries`)

After Tasks 5-9, no caller invokes `Query.Init(s, opts...)` anymore. Delete it and the `Built()` accessor that bridged the migration window.

- [ ] **Step 1: Verify no callers remain**

Run: `grep -rn "\.Init(s\|\.Init(s," --include="*.go" | grep -v "_test.go"`
Expected: zero matches (or only matches inside test files that haven't been updated — check those too).

If any non-test matches remain: don't delete `Init` yet. Migrate them first.

- [ ] **Step 2: Delete `Query.Init` from `pkg/query/query.go`**

Remove the entire `Init` method (lines 101-106 today) and the `Built()` accessor.

- [ ] **Step 3: Remove the bridge in `pkg/engine/system.go`**

In `BuildQueries`:

```go
func (b *SystemBase[W]) BuildQueries() {
	for _, q := range b.queries {
		q.BuildFromECS(b.ecsWorld)
	}
}
```

(The `if alreadyBuilt, ok := q.(interface{ Built() bool }); ok && ...` skip from Task 4 Step 6 is removed.)

- [ ] **Step 4: Run full repo build**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/query/query.go pkg/engine/system.go
git commit -m "$(cat <<'EOF'
chore(query): remove Query.Init now that all callers use With

After the migration sweep, no caller uses the legacy Query.Init(sys,
opts...) shape — the framework discovers and builds queries via
SystemBase[W].BindQueries/BuildQueries. Delete Init and the Built()
bridge that kept the migration window safe.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Final verification

- [ ] **Step 1: Run `go vet ./...`**

Expected: clean.

- [ ] **Step 2: Run `just build`**

Expected: builds `bin/server` (or whatever the example produces) without error.

- [ ] **Step 3: Run the full test suite**

Run: `go test ./...`
Expected: PASS, including `TestS6HandoffAcrossNodes`, `TestS7…`, `pkg/mmokit/…`, and any others.

- [ ] **Step 4: Run the bot smoke test**

```bash
cd examples/4node-basic
just dev
```

In the server console:

```
bot spawn 30 0_0
```

Watch for ~5 seconds. Bots should appear and wander randomly across cell 0_0. Then:

```
cell split 0_0
```

Bots should redistribute across the 4 child cells and continue wandering, crossing child cell boundaries cleanly. No panics, no lost bots.

```
cell merge 0_0
```

Bots reconverge on the merged cell. Still no panics.

- [ ] **Step 5: Run distributed smoke (optional but recommended)**

```bash
just distributed
```

Wait for the 4-process tmux to come up; then in the coordinator window, `bot spawn 30 0_0` and watch them wander. Verify cross-host migration via `cell migrate 0_1 host-2` (or similar). Expected: no panics, bots survive migration.

- [ ] **Step 6: Verify the spec's success criteria explicitly**

Open `examples/4node-basic/system_bots.go` and confirm it reads as:

```go
type BotSystem struct {
	mmokit.SystemBase[*World]
	bots mmokit.Query[struct {
		Behavior *BotBehavior
		MT       *mmokit.MoveTarget
		Pos      *mmokit.Position
	}]
}

func (s *BotSystem) Update(dt float32) {
	// ... uses s.World().Cell() ...
}
```

No `Init()`, no `gw` field, no `WorldOf[*World](s)` call, no `s.bots.With(IncludeAll())`. This is the canonical "trivial system" the spec promises.

- [ ] **Step 7: Confirm `internal/game/system_util.go` is gone**

Run: `ls internal/game/system_util.go 2>&1 | grep -q "No such file" && echo "deleted" || echo "still present!"`
Expected: `deleted`.

- [ ] **Step 8: Verify no stragglers**

```bash
grep -rn "WorldOf\[" --include="*.go" pkg/ internal/ examples/ | grep -v _test.go | grep -v "func WorldOf\|func WorldOfCell"
```

Expected: zero matches in production code (the helpers themselves still exist in `pkg/mmokit/mmokit.go` for cmdsys/console use).

```bash
grep -rn "gwFromSystem" --include="*.go"
```

Expected: zero matches.

```bash
grep -rn "gw \*\(GameWorld\|World\)" --include="*.go" pkg/ internal/ examples/ | grep -v _test.go
```

Expected: zero matches (the stash field is gone everywhere).

- [ ] **Step 9: No commit needed for verification**

If everything passes, the migration is complete. The branch is ready to merge to main per the user's solo-dev workflow.

---

## Self-Review Checklist (run BEFORE the executor starts)

- [x] Every Task references real file paths (verified by recent grep + read).
- [x] Every code block contains complete, runnable code.
- [x] Tests come before implementation for new framework code (Task 1, 3).
- [x] Migration tasks (5-9) are mechanical and ordered to keep the build green.
- [x] The `Built()` bridge in Task 4 is the explicit answer to the spec's Open Question about how to keep the migration window safe.
- [x] The atomic `SystemBase` rename (Task 2) is the only large-blast-radius commit; everything else is per-area.
- [x] `WorldOf[W]` and `WorldOfCell[W]` are explicitly preserved for non-system callers (per spec Non-Goals).
- [x] BotSystem's spurious `IncludeAll()` is dropped in Task 8 per spec Migration step 7.
- [x] `gwFromSystem` deletion is in Task 7.

## Open Items for the Executor

- **Query[T] struct vs. func type (Task 1, Step 3):** the plan calls for changing `Query[T]` from a function type to a struct, then exposing iteration via an `Iter` method or `All()` rangefunc. If the existing `for _, b := range s.q { ... }` syntax can't be preserved with a struct (Go 1.25 range-over-method semantics), fall back to keeping `Query[T]` as a function type and storing `opts`/`built` in a sidecar map keyed by the function pointer. The spec is the source of truth on the API; the implementation detail is yours to navigate.
- **`pkg/system/*_test.go` import cycle (Task 5, Step 6):** if `pkg/system/*_test.go` can't import `pkg/mmokit`, define a local `wireSystem` helper that duplicates the lifecycle. Both are acceptable.
