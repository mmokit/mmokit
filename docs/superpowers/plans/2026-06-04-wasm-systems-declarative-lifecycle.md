# WASM Systems: Declarative Registration + Cluster Lifecycle — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make hot-swappable WASM systems first-class: register them declaratively at startup (like `AddSystem`), and load/unload/swap them by name across all cells on all nodes by default (optionally targeting a `--node`/`--cell`).

**Architecture:** Builds on the Phase 0 wasm stack. Adds (1) a per-Process **wasm registry** (`name → {path, typed builder}`) populated by a new `mmokit.AddWasmSystem[T]` that also registers the system via the existing `systemDefs` path (so it boots into every cell on every node); (2) framework cmdsys verbs `wasm.list/load/unload/swap` routed `RouteAllHosts`, operating by name with `--node`/`--cell` filters; (3) loop primitives `SystemByName` + in-place `ReplaceSystemLive` so a swap keeps the system's tick-order slot; (4) a `Close()` on the wasm adapter so unload/swap truly drops the old wazero instance.

**Tech Stack:** Existing `pkg/wasmhost`/`pkg/wasmsys`/`pkg/mmokit`, `pkg/cmdsys` (RouteAllHosts), `pkg/engine` loop.

**Design decisions (approved):** auto-derived names from filename with an explicit-name override; targeting = all-by-default + `--node` + `--cell`.

**Prior spec:** [docs/superpowers/specs/2026-06-04-hot-swappable-wasm-systems-design.md](../specs/2026-06-04-hot-swappable-wasm-systems-design.md) (this is Phase-1 lifecycle/ergonomics work).

---

## File Structure
- `pkg/engine/loop.go` (modify) — add `SystemByName`, `ReplaceSystemLive`.
- `pkg/engine/loop_live_test.go` (modify) — tests for the two new methods.
- `pkg/mmokit/wasm_system.go` (modify) — wasm adapter gains a `name` field + `Close() error`; expose a `Closer`-style path.
- `pkg/mmokit/wasm_manager.go` (new) — per-Process registry, `AddWasmSystem[T]`/`AddWasmSystemNamed[T]`, `deriveWasmName`, the cmdsys verb registration + handlers.
- `pkg/mmokit/wasm_manager_test.go` (new) — registry/startup-load + verb behavior tests.
- `pkg/mmokit/mmokit.go` (modify) — call `registerWasmVerbs(proc)` during Process construction so the verbs always exist.
- `examples/4node-basic/main.go` (modify) — register shield+pulse via `AddWasmSystem`; drop the bespoke console group.
- `examples/4node-basic/wasm_console.go` (delete) — replaced by framework verbs.

---

## Task 1: Loop primitives — SystemByName + in-place ReplaceSystemLive

**Files:** Modify `pkg/engine/loop.go`; modify `pkg/engine/loop_live_test.go`.

- [ ] **Step 1: Write failing tests** — append to `pkg/engine/loop_live_test.go`:
```go
func TestGameLoop_SystemByName(t *testing.T) {
	eng := newLoopTestEngine()
	gl := NewGameLoop(eng, nil, nil, Hooks{})
	n := 0
	want := &liveCountSys{n: &n}
	gl.AddSystemLive("counter", want)

	got, ok := gl.SystemByName("counter")
	if !ok || got != want {
		t.Fatalf("SystemByName(counter) = %v,%v want %v,true", got, ok, want)
	}
	if _, ok := gl.SystemByName("missing"); ok {
		t.Fatal("SystemByName(missing) returned ok=true")
	}
}

func TestGameLoop_ReplaceSystemLive_PreservesOrder(t *testing.T) {
	eng := newLoopTestEngine()
	gl := NewGameLoop(eng, nil, nil, Hooks{})
	a, b, c := &liveCountSys{n: new(int)}, &liveCountSys{n: new(int)}, &liveCountSys{n: new(int)}
	gl.AddSystemLive("a", a)
	gl.AddSystemLive("b", b)
	gl.AddSystemLive("c", c)

	repl := &liveCountSys{n: new(int)}
	if !gl.ReplaceSystemLive("b", repl) {
		t.Fatal("ReplaceSystemLive(b) returned false")
	}
	// b's slot (index 1) now holds repl; order a, repl, c preserved.
	if gl.systems[0] != a || gl.systems[1] != repl || gl.systems[2] != c {
		t.Fatalf("order not preserved: %v", gl.systems)
	}
	if gl.systemNames[1] != "b" {
		t.Fatalf("name at slot 1 = %q want b", gl.systemNames[1])
	}
	if gl.ReplaceSystemLive("missing", repl) {
		t.Fatal("ReplaceSystemLive(missing) returned true")
	}
}
```

- [ ] **Step 2: Run → FAIL** (`go test ./pkg/engine/ -run 'TestGameLoop_SystemByName|TestGameLoop_ReplaceSystemLive' -v`): undefined methods.

- [ ] **Step 3: Implement** in `pkg/engine/loop.go`:
```go
// SystemByName returns the first system registered under name. MUST be called
// on the loop goroutine.
func (gl *GameLoop) SystemByName(name string) (System, bool) {
	for i, n := range gl.systemNames {
		if n == name {
			return gl.systems[i], true
		}
	}
	return nil, false
}

// ReplaceSystemLive swaps the system registered under name in place, preserving
// its tick-order slot and timing/profiler indices (name and count unchanged).
// Returns false if no such system exists. MUST be called on the loop goroutine.
func (gl *GameLoop) ReplaceSystemLive(name string, s System) bool {
	for i, n := range gl.systemNames {
		if n == name {
			gl.systems[i] = s
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run → PASS.** Then `go test ./pkg/engine/ ./pkg/universe/ -count=1` (no regressions).

- [ ] **Step 5: Commit** — `git add pkg/engine/loop.go pkg/engine/loop_live_test.go && git commit -m "feat(engine): GameLoop.SystemByName + in-place ReplaceSystemLive"`

---

## Task 2: WASM adapter — stable name + Close() (true unload)

**Files:** Modify `pkg/mmokit/wasm_system.go`; add a test to `pkg/mmokit/wasm_swap_test.go`.

Background: `wasmSystem[T]` holds `mod *wasmhost.Module`. Unload/swap must `Close()` the displaced module to free the wazero instance (true unload). The adapter also needs a stable logical name so the loop can find it by name.

- [ ] **Step 1: Add a Close test** to `pkg/mmokit/wasm_swap_test.go`:
```go
func TestWasmSystem_ImplementsCloser(t *testing.T) {
	wasmPath := buildWasmModule(t, "../../examples/4node-basic/wasmmods/shieldregen")
	sys := mmokit.NewWasmSystem[gamecomp.Shield](wasmPath).Factory()
	c, ok := sys.(interface{ Close() error })
	if !ok {
		t.Fatal("wasm system does not implement Close() error")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
```

- [ ] **Step 2: Run → FAIL** (no Close method).

- [ ] **Step 3: Implement** in `pkg/mmokit/wasm_system.go` — add a `Close` method on `*wasmSystem[T]`:
```go
// Close releases the underlying wasm module instance (true unload). Call after
// the system has been removed/replaced on the loop.
func (s *wasmSystem[T]) Close() error {
	return s.mod.Close(context.Background())
}
```
(No name field is needed on the adapter itself — the loop tracks the name via AddSystemLive/ReplaceSystemLive. Keep the adapter minimal.)

- [ ] **Step 4: Run → PASS.** `go test ./pkg/mmokit/ -run TestWasmSystem -count=1`. `go vet ./pkg/mmokit/...` clean.

- [ ] **Step 5: Commit** — `git add pkg/mmokit/wasm_system.go pkg/mmokit/wasm_swap_test.go && git commit -m "feat(mmokit): wasm adapter Close() for true unload"`

---

## Task 3: Per-Process registry + AddWasmSystem[T] / AddWasmSystemNamed[T]

**Files:** Create `pkg/mmokit/wasm_manager.go`; create `pkg/mmokit/wasm_manager_test.go`.

This adds the declarative startup API and the registry the verbs consult. Mirror the `adminBusMap` per-`*universe.Process` pattern (see `pkg/mmokit/admin.go`).

- [ ] **Step 1: Write the registry + API** in `pkg/mmokit/wasm_manager.go`:
```go
package mmokit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// wasmRegEntry binds a registered wasm system's artifact path to a type-erased,
// panic-guarded builder (T captured at AddWasmSystem time).
type wasmRegEntry struct {
	path  string
	build func(path string) (System, error) // returns a fresh per-cell instance
}

type wasmRegistry struct {
	mu      sync.Mutex
	entries map[string]wasmRegEntry
}

var (
	wasmRegMu  sync.Mutex
	wasmRegMap = map[*pkguniverse.Process]*wasmRegistry{}
)

func wasmRegistryFor(p *pkguniverse.Process) *wasmRegistry {
	wasmRegMu.Lock()
	defer wasmRegMu.Unlock()
	r, ok := wasmRegMap[p]
	if !ok {
		r = &wasmRegistry{entries: map[string]wasmRegEntry{}}
		wasmRegMap[p] = r
	}
	return r
}

// deriveWasmName turns a module path into a logical name: the base filename
// without its extension. "dist/wasmmods/pulse.wasm" -> "pulse".
func deriveWasmName(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// AddWasmSystem registers a hot-swappable wasm system over POD component T,
// naming it after the module file (e.g. "dist/wasmmods/pulse.wasm" -> "pulse").
// It is added to the normal system set (so it boots into every cell on every
// node) AND recorded in the per-process registry for runtime load/swap/unload
// by name. T must match the component the module declares in its Query().
func AddWasmSystem[T any](p *pkguniverse.Process, path string) {
	AddWasmSystemNamed[T](p, deriveWasmName(path), path)
}

// AddWasmSystemNamed is AddWasmSystem with an explicit logical name, decoupled
// from the filename.
func AddWasmSystemNamed[T any](p *pkguniverse.Process, name, path string) {
	build := func(pth string) (System, error) {
		return buildWasmSystemInstance[T](pth)
	}
	reg := wasmRegistryFor(p)
	reg.mu.Lock()
	reg.entries[name] = wasmRegEntry{path: path, build: build}
	reg.mu.Unlock()

	// Boot into every cell via the existing systemDefs path, named so the loop
	// and the runtime verbs can find it by the same logical name.
	p.AddSystem(NewWasmSystem[T](path).Named(name))
}

// buildWasmSystemInstance creates one fresh per-cell wasm instance, converting
// NewWasmSystem/Factory panics (bad path/ABI) into errors so a runtime load
// can't crash a tick.
func buildWasmSystemInstance[T any](path string) (sys System, err error) {
	if _, statErr := os.Stat(path); statErr != nil {
		return nil, fmt.Errorf("wasm module not found at %s (run `just wasm-build` first): %w", path, statErr)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("load wasm module %s: %v", path, r)
		}
	}()
	return NewWasmSystem[T](path).Factory(), nil
}

// snapshotIfSwappable / closeIfCloser are small helpers used by the verbs.
func snapshotIfSwappable(s System) []byte {
	if sw, ok := s.(SwappableSystem); ok {
		b, _ := sw.Snapshot()
		return b
	}
	return nil
}
func restoreIfSwappable(s System, state []byte) error {
	if sw, ok := s.(SwappableSystem); ok && len(state) > 0 {
		return sw.Restore(state)
	}
	return nil
}
func closeIfCloser(s System) {
	if c, ok := s.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

// Ensure NewWasmSystem's SystemDef can be named (it returns engine.SystemDef
// which has .Named). Verified at call sites above.
var _ = context.Background
```
(Remove the unused `context` import + the trailing `var _` once the verbs in Task 4 use `context`; they will. If `go vet` complains about unused imports at this step, drop them and re-add in Task 4.)

- [ ] **Step 2: Write the startup-load test** in `pkg/mmokit/wasm_manager_test.go`:
```go
package mmokit_test

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func TestDeriveWasmName(t *testing.T) {
	// deriveWasmName is unexported; test it via a thin exported probe if needed,
	// or assert the behavior through AddWasmSystem naming in the verb test.
	// Here we just assert the registry path end-to-end in the verb test (Task 4).
	_ = gamecomp.ShieldTypeID
}
```
> Note: `deriveWasmName` is unexported. Rather than export it, the meaningful coverage is the end-to-end "registered system boots into cells and is swappable by its derived name", which Task 4's verb test exercises against a real Process. Keep this file minimal here; Task 4 adds the substantive tests. (If you prefer, add a tiny same-package `pkg/mmokit/wasm_manager_internal_test.go` with `package mmokit` that unit-tests `deriveWasmName` directly — do that, it's cleaner:)
```go
// pkg/mmokit/wasm_manager_internal_test.go
package mmokit

import "testing"

func TestDeriveWasmName(t *testing.T) {
	cases := map[string]string{
		"dist/wasmmods/pulse.wasm": "pulse",
		"shieldregen.wasm":         "shieldregen",
		"/a/b/c.wasm":              "c",
	}
	for in, want := range cases {
		if got := deriveWasmName(in); got != want {
			t.Fatalf("deriveWasmName(%q)=%q want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 3: Run → PASS** (`go test ./pkg/mmokit/ -run 'TestDeriveWasmName' -v`). `go vet ./pkg/mmokit/...` clean (fix unused imports if flagged).

- [ ] **Step 4: Commit** — `git add pkg/mmokit/wasm_manager.go pkg/mmokit/wasm_manager_internal_test.go pkg/mmokit/wasm_manager_test.go && git commit -m "feat(mmokit): per-process wasm registry + AddWasmSystem[T] declarative API"`

---

## Task 4: Framework cmdsys verbs — wasm.list/load/unload/swap (RouteAllHosts, --node/--cell)

**Files:** Modify `pkg/mmokit/wasm_manager.go` (add `registerWasmVerbs`); add verb tests to `pkg/mmokit/wasm_manager_test.go`.

Mirror `pkg/universe/builtins_cell.go` for the `cmdsys.Command` shape and `RouteAllHosts`. The handler runs on each host; it iterates `proc.Cells`, applies the per-cell op on that cell's loop goroutine via `mmokit.CmdOnLoop`, and honors `--node`/`--cell` filters.

**Verb semantics (all by name; default all cells/all nodes):**
- `wasm.list` → rows of `{cell, name, loaded(bool), ticks}` for every registered system × every local cell.
- `wasm.load <name> [--node] [--cell]` → for each target cell, if not already present, build + `WireSystem` + `AddSystemLive(name, inst)`.
- `wasm.unload <name> [--node] [--cell]` → for each target cell, `SystemByName(name)`; if present, snapshot (for the reported tick count), `RemoveSystemLive(name)`, `Close()` the old instance.
- `wasm.swap <name> [--node] [--cell]` → for each target cell, build a NEW instance from the registry path OFF-loop (panic-safe); then on-loop: snapshot old → restore into new → `WireSystem` → `ReplaceSystemLive(name, new)` → `Close()` old. If not currently present, fall back to load.

**Targeting:** route `RouteAllHosts`. Each host: if `--node` is set and != this host's id, return an empty result (skip). Within a host, filter `proc.Cells` by `--cell` (canonicalize via `mmokit.ParseCellID`) when set.

- [ ] **Step 1: Implement `registerWasmVerbs(proc *pkguniverse.Process) error`** in `pkg/mmokit/wasm_manager.go`. Concrete handler skeleton for `wasm.swap` (the others follow the same shape):
```go
// arg/result types
type wasmNameArgs struct {
	Name string `cmd:"help=registered wasm system name"`
	Node string `cmd:"optional,help=limit to one host id"`
	Cell string `cmd:"optional,help=limit to one cell id"`
}
type wasmOpRow struct {
	Cell   string
	Name   string
	Status string
	Ticks  uint64
}
type wasmOpResult struct {
	Rows []wasmOpRow `cmd:"table"`
}

func registerWasmVerbs(proc *pkguniverse.Process) error {
	reg := proc.CmdRegistry()
	// helper: resolve target cells on THIS host honoring --node/--cell.
	targetCells := func(node, cell string) []*Cell {
		if node != "" && !proc.IsThisHost(node) { // see note: find the real accessor
			return nil
		}
		var out []*Cell
		for _, c := range proc.Cells {
			if cell != "" {
				if canon, err := ParseCellID(cell); err != nil || string(c.MeshID) != string(canon.MeshID()) {
					continue
				}
			}
			out = append(out, c)
		}
		return out
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "wasm.swap",
		Capability:  "wasm.swap",
		Description: "rebuild a registered wasm system from disk and hot-swap it in place (state preserved)",
		Examples:    []string{"wasm swap pulse", "wasm swap pulse --node host-b", "wasm swap pulse --cell 0_0"},
		Route:       cmdsys.RouteAllHosts,
		Args:        wasmNameArgs{},
		Result:      wasmOpResult{},
		Handler: func(ctx context.Context, env *cmdsys.CommandEnv, raw any) (any, error) {
			args := raw.(wasmNameArgs)
			entry, ok := registryEntry(proc, args.Name)
			if !ok {
				return nil, fmt.Errorf("unknown wasm system %q (registered: %s)", args.Name, registeredNames(proc))
			}
			var rows []wasmOpRow
			for _, cell := range targetCells(args.Node, args.Cell) {
				// Build the replacement OFF-loop (panic-safe).
				newSys, err := entry.build(entry.path)
				if err != nil {
					return nil, err
				}
				row, err := mmokit_swapOnCell(ctx, cell, args.Name, newSys)
				if err != nil {
					closeIfCloser(newSys)
					return nil, err
				}
				rows = append(rows, row)
			}
			return wasmOpResult{Rows: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("wasm.swap: %w", err)
	}
	// ... wasm.load, wasm.unload, wasm.list registered similarly ...
	return nil
}
```
And the per-cell swap helper (loop-goroutine work):
```go
func mmokit_swapOnCell(ctx context.Context, cell *Cell, name string, newSys System) (wasmOpRow, error) {
	return CmdOnLoop(ctx, cell.Engine, func() (wasmOpRow, error) {
		key := string(cell.MeshID)
		old, ok := cell.Loop.SystemByName(name)
		if !ok {
			// not loaded here: install fresh (load semantics)
			WireSystem(newSys, cell.Stage.ECSWorld(), cell.Engine, cell.Stage)
			cell.Loop.AddSystemLive(name, newSys)
			return wasmOpRow{Cell: key, Name: name, Status: "loaded"}, nil
		}
		state := snapshotIfSwappable(old)
		if err := restoreIfSwappable(newSys, state); err != nil {
			return wasmOpRow{}, fmt.Errorf("restore: %w", err)
		}
		WireSystem(newSys, cell.Stage.ECSWorld(), cell.Engine, cell.Stage)
		cell.Loop.ReplaceSystemLive(name, newSys)
		closeIfCloser(old)
		return wasmOpRow{Cell: key, Name: name, Status: "swapped", Ticks: decodeTicks8(state)}, nil
	})
}
```
Add the small helpers `registryEntry(proc, name) (wasmRegEntry, bool)`, `registeredNames(proc) string`, and `decodeTicks8([]byte) uint64` (8-LE; 0 if not 8 bytes). Implement `wasm.load` (skip cells already present; AddSystemLive) and `wasm.unload` (snapshot+RemoveSystemLive+Close; skip cells without it) and `wasm.list` (read-only: for each cell × registered name, report loaded = SystemByName(name) present, ticks via snapshot).

> **IMPORTANT — find the real host-identity accessor.** `proc.IsThisHost(node)` is a placeholder. Grep for how the coordinator exposes the local host id (e.g. `rg -n "HostID|LocalHost|func .*Process.* string" pkg/universe/*.go` and how `host list` marks local hosts). Use the actual accessor to compare against `--node`. If no clean per-host id exists for the all-in-one process, treat `--node` as matching when the id equals the process's host id and document that distributed `--node` targeting relies on it.

> **IMPORTANT — imports.** This file now needs `context` and `github.com/zenion/mmoserver/pkg/cmdsys`. `Cell`, `CmdOnLoop`, `WireSystem`, `ParseCellID`, `NewWasmSystem`, `System`, `SwappableSystem` are all in package `mmokit`. Confirm `cmdsys.CommandEnv` is the right env type by checking `builtins_cell.go` (it may be `*cmdsys.CommandEnv` or the mmokit alias `*CommandEnv`). Match the existing example exactly.

- [ ] **Step 2: Write verb tests** in `pkg/mmokit/wasm_manager_test.go` — build a single-process Process with 1–4 cells, `AddWasmSystem[gamecomp.Shield](proc, shieldPath)` BEFORE `Build()`, build, then invoke the verbs through `proc.CmdRegistry()`/dispatcher and assert: (a) after build, every cell has the system loaded (boots everywhere); (b) `wasm.unload shieldregen` removes it from all cells (`SystemByName` false); (c) `wasm.load shieldregen` re-adds to all; (d) `wasm.swap shieldregen` preserves the tick counter (snapshot before/after). Use the existing test harness for standing up a small Process (grep `pkg/mmokit/*_test.go` for how Processes are built in tests, e.g. `tick_all_test.go`, `state_test.go`). If invoking via the dispatcher is heavy, call the handler closures’ underlying per-cell helpers directly against each `cell` after `proc.Build()` — but prefer going through `Dispatcher.Invoke` so routing is exercised.

- [ ] **Step 3: Run → PASS.** `go test ./pkg/mmokit/ -run 'TestWasm' -count=1 -v`. `go vet ./pkg/mmokit/...` clean.

- [ ] **Step 4: Commit** — `git add pkg/mmokit/wasm_manager.go pkg/mmokit/wasm_manager_test.go && git commit -m "feat(mmokit): wasm.list/load/unload/swap cmdsys verbs (RouteAllHosts, --node/--cell)"`

---

## Task 5: Auto-register verbs + migrate the example

**Files:** Modify `pkg/mmokit/mmokit.go` (register verbs during Process construction); modify `examples/4node-basic/main.go`; delete `examples/4node-basic/wasm_console.go`.

- [ ] **Step 1: Auto-register the verbs.** Find where `mmokit.New` finishes building the `*universe.Process` (or where other builtin verbs are wired) and call `registerWasmVerbs(proc)` exactly once per Process so `wasm list` etc. are always available. Grep `pkg/mmokit/mmokit.go` for `func New(` and the Process-construction tail; add the call there. If registration must happen after `CmdRegistry()` exists, place it accordingly. Verify it runs once (no double-register error — `reg.Register` likely errors on duplicate verbs; guard if New can be called such that this runs twice).

- [ ] **Step 2: Migrate the example.** In `examples/4node-basic/main.go`, REMOVE the `registerWasmCommands(...)` call from the `OnConsoleReady` closure, and ADD (near the other `AddSystem` calls, after the modules are expected to be built):
```go
mmokit.AddWasmSystem[gamecomp.Shield](process, "dist/wasmmods/shieldregen.wasm")
mmokit.AddWasmSystem[mmokit.Collider](process, "dist/wasmmods/pulse.wasm")
```
Add the `gamecomp "github.com/zenion/mmoserver/internal/component"` import if not already present. These boot both systems into every cell at startup; they’re swappable at runtime as `shield` and `pulse`.

> Decide: booting BOTH at startup means players' circles immediately pulse on launch. If that's undesirable as the default demo, register only `shield` at startup (invisible) and leave `pulse` registered-but-not-booted by using a registry-only variant. SIMPLEST for this task: register both via `AddWasmSystem` (both boot). If you want pulse registered-but-not-auto-loaded, add a `RegisterWasmSystem[T]` (registry only, no AddSystem) variant and use it for pulse — but only do this if trivially clean; otherwise boot both and note it.

- [ ] **Step 3: Delete the bespoke command file** — `git rm examples/4node-basic/wasm_console.go`. Confirm nothing else references its symbols (`rg -n "registerWasmCommands|wasmModules|buildWasmInstance" examples/4node-basic/`).

- [ ] **Step 4: Build + verify.** `just wasm-build`; `cd examples/4node-basic && go vet ./... ; cd -`; `go build -o /dev/null ./examples/4node-basic/`; `go test ./pkg/mmokit/ -count=1`. All exit 0.

- [ ] **Step 5: Commit** — `git add -A examples/4node-basic pkg/mmokit/mmokit.go && git commit -m "feat(examples): register shield+pulse via AddWasmSystem; framework wasm verbs replace bespoke group"`

---

## Task 6: Docs + manual smoke

**Files:** Modify the spec; inline smoke notes in the commit body.

- [ ] **Step 1: Update the spec** — in `docs/superpowers/specs/2026-06-04-hot-swappable-wasm-systems-design.md`, add a short "Declarative registration + cluster lifecycle (delivered)" subsection: the `AddWasmSystem[T]`/`AddWasmSystemNamed[T]` API, the `wasm list/load/unload/swap <name> [--node] [--cell]` verbs (RouteAllHosts, all-by-default), in-place `ReplaceSystemLive`, and `Close()` true-unload. Note distributed `--node` targeting is exercised manually via `just distributed`.

- [ ] **Step 2: Manual smoke (inline in commit body; do NOT create a *_SMOKE.md, do NOT leave a server running):**
```
just wasm-build && just dev
# console:
wasm list                 # shield+pulse shown loaded on every cell
wasm unload pulse         # circles stop pulsing on all cells
wasm load pulse           # pulsing resumes everywhere
# edit pulse tunables, just wasm-build (other terminal), then:
wasm swap pulse           # new pulse params take effect on all cells, phase preserved
wasm swap pulse --cell 0_0  # only cell 0_0 changes
```

- [ ] **Step 3: Commit** — `git add docs/ && git commit -m "docs(wasm): declarative AddWasmSystem + cluster lifecycle verbs"`

---

## Self-Review
- **Feature 1 (all cells/all nodes default + optional target):** Task 4 verbs route `RouteAllHosts` + iterate `proc.Cells`, with `--node`/`--cell` filters. ✓
- **Feature 2 (unload requires the system):** `wasm.unload <name>` — name required. ✓
- **Feature 3 (startup load via AddSystem-style API):** Task 3 `AddWasmSystem[T]` → `systemDefs` → boots into every cell. ✓
- **Order preservation on swap:** Task 1 `ReplaceSystemLive` (in-place). ✓
- **True unload:** Task 2 `Close()` on displaced instances. ✓
- **Placeholders:** the `proc.IsThisHost`/`CommandEnv` type and the verb-registration hook are explicitly flagged for the implementer to resolve against real accessors (grep instructions given), not left vague in code.
- **Open risk:** verb test harness — standing up a multi-cell Process in a unit test and invoking via the dispatcher. Task 4 Step 2 instructs grepping existing `_test.go` Process builders and allows a direct-per-cell fallback if the dispatcher path is heavy.
