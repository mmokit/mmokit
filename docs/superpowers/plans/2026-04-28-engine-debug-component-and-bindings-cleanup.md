# Engine Debug Component + Bindings Cleanup — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the per-kind `EngineBindingsConfig{IncludeMeshState: true, ...}` flag with an engine-provided `mmokit.DebugInfo` component (Presence + OwnerHost + AoIRadius), move the universally-set quant scales onto `Config`, and dissolve `EngineBindingsConfig` entirely.

**Architecture:** Three logical layers, each commit-green:
1. **Additive** (Tasks 1–4): new `DebugInfo` component + writer system + new `Config` fields, all alongside the existing API.
2. **Cutover** (Task 5): single atomic commit that drops `EngineBindingsConfig`, the `MeshState` synthetic binding, the `RegisterKind` bindings argument, and the `EntityKindDef.EngineBindings` field — updating every call site in lockstep.
3. **Cleanup** (Tasks 6–9): delete 4node-basic's hand-rolled `DebugInfo`/`DebugInfoSystem`, regenerate SDK, update web client consumers, smoke-verify.

**Tech Stack:** Go 1.23+, ark ECS v0.7.1, reflection-based bundle wire format (existing), TypeScript SDK auto-gen, vitest for web.

**Spec:** [docs/superpowers/specs/2026-04-28-engine-debug-component-and-bindings-cleanup-design.md](../specs/2026-04-28-engine-debug-component-and-bindings-cleanup-design.md)

---

## File Structure

**Created:**
- (none — `DebugInfo` lives in existing `pkg/component/core.go`, writer in new `pkg/system/debug_info_writer.go`)
- `pkg/system/debug_info_writer.go` — the writer system + tests
- `pkg/system/debug_info_writer_test.go`

**Modified:**
- `pkg/component/core.go` — add `DebugInfo` struct
- `pkg/universe/coordinator.go` — add `Config.VelQuantScale` / `Config.SizeQuantScale` fields + defaults; add `HostIndex(hostID string) uint8` helper; auto-register `DebugInfoWriter` in `Build()`
- `pkg/system/auto_replicator.go` — delete `meshStateBinding`, delete `MeshState()` constructor, delete `parseCellIndex()`, shrink `EngineBindingsConfig` → delete entirely, change `EngineBindings()` signature to read scales from `*Config`
- `pkg/mmokit/mmokit.go` — re-export `mmokit.DebugInfo`; delete `EngineBindingsConfig` re-export; delete `EngineBindings()` re-export; update `BuildReplicators` to read scales from `coord.Cfg()`
- `pkg/mmokit/kindreg.go` — drop `bindings EngineBindingsConfig` parameter from `RegisterKind`
- `pkg/universe/entity_kind.go` — delete `EngineBindings *system.EngineBindingsConfig` field on `EntityKindDef`
- `pkg/mmokit/kindreg_test.go` — drop `EngineBindingsConfig{}` from every `RegisterKind` call
- `pkg/mmokit/spawn_init_test.go` — same
- `examples/4node-basic/main.go` — drop `playerBindings` local var + bindings args
- `examples/4node-basic/components.go` — replace game `DebugInfo` with `*mmokit.DebugInfo`; delete game `DebugInfo` struct
- `examples/4node-basic/system_debug_info.go` — **delete file**
- `internal/game/entity_kinds.go` — drop bindings args from all 7 `RegisterKind` calls; add `*mmokit.DebugInfo` to `ShipBundle` and `NPCBundle`
- `examples/4node-basic/web/src/network.ts` — rename `meshState` → `presence`, `ownerNode` → `ownerHost`
- `web-pixi/src/**` — same SDK field rename in any consumer

**Auto-regenerated (no hand-edits):**
- `examples/4node-basic/web/sdk/entities.ts`
- `web-pixi/sdk/entities.ts`

---

## Task 1: Add `DebugInfo` component

**Files:**
- Modify: `pkg/component/core.go`
- Test: `pkg/component/core_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/component/core_test.go` (create the file if a test pattern doesn't already exist — check first; use the existing patterns there):

```go
func TestDebugInfo_Fields(t *testing.T) {
	d := DebugInfo{Presence: 1, OwnerHost: 7, AoIRadius: 800}
	if d.Presence != 1 {
		t.Errorf("Presence: got %d, want 1", d.Presence)
	}
	if d.OwnerHost != 7 {
		t.Errorf("OwnerHost: got %d, want 7", d.OwnerHost)
	}
	if d.AoIRadius != 800 {
		t.Errorf("AoIRadius: got %v, want 800", d.AoIRadius)
	}
}

func TestDebugInfo_NetTags(t *testing.T) {
	t.Helper()
	tp := reflect.TypeOf(DebugInfo{})
	cases := []struct {
		field, want string
	}{
		{"Presence", "u8"},
		{"OwnerHost", "u8"},
		{"AoIRadius", "f32"},
	}
	for _, c := range cases {
		f, ok := tp.FieldByName(c.field)
		if !ok {
			t.Fatalf("DebugInfo missing field %s", c.field)
		}
		if got := f.Tag.Get("net"); got != c.want {
			t.Errorf("DebugInfo.%s net tag: got %q, want %q", c.field, got, c.want)
		}
	}
}
```

If `core_test.go` doesn't exist or doesn't import `reflect`/`testing`, add both at the top.

- [ ] **Step 2: Run the test, expect FAIL**

```bash
go test -run "TestDebugInfo" ./pkg/component/...
```

Expected: build error or `undefined: DebugInfo`.

- [ ] **Step 3: Add the component**

Append to `pkg/component/core.go` (after the existing component types like `Replica`, `Ghost`, etc.):

```go
// DebugInfo holds per-entity engine-debug state replicated to clients.
// Engine-owned and engine-written: a builtin writer system populates
// these fields each tick on every entity whose kind bundle declares
// *DebugInfo. Game code should never write to this component.
//
//   - Presence: enginepb.EntityMeshState (LOCAL/REPLICA/GHOST). Derived
//     from Ghost/Replica markers on the entity at write time.
//   - OwnerHost: 0-based index into the cluster's ordered host list.
//     Stable for the lifetime of a host's membership; reused only after
//     a host leaves and a new one joins.
//   - AoIRadius: viewer's effective AoI radius. Today this mirrors
//     Process.Config.AoIRadius. Per-entity overrides may land later.
type DebugInfo struct {
	Presence  uint8   `net:"u8"`
	OwnerHost uint8   `net:"u8"`
	AoIRadius float32 `net:"f32"`
}
```

- [ ] **Step 4: Run the test, expect PASS**

```bash
go test -run "TestDebugInfo" ./pkg/component/...
```

Expected: `ok`.

- [ ] **Step 5: Verify the rest of the package still builds**

```bash
go vet ./pkg/component/...
```

Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add pkg/component/core.go pkg/component/core_test.go
git commit -m "$(cat <<'EOF'
feat(component): add engine-provided DebugInfo component

Adds Presence/OwnerHost/AoIRadius fields with net tags so bundles that
declare *DebugInfo flow through the standard reflection-based wire
format. Writer system that populates the fields lands in a follow-up
commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add `Config.VelQuantScale` / `Config.SizeQuantScale`

**Files:**
- Modify: `pkg/universe/coordinator.go` — `Config` struct + `New()` defaults
- Test: `pkg/universe/coordinator_config_test.go` (create if missing)

- [ ] **Step 1: Write the failing test**

Create `pkg/universe/coordinator_config_test.go` (or append to whichever existing config test file exists in `pkg/universe/`):

```go
package universe

import "testing"

func TestConfig_VelQuantScaleDefault(t *testing.T) {
	c := Config{CellsX: 1, CellsY: 1}
	p, err := newProcessForTest(t, c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Shutdown()

	if got := p.Cfg().VelQuantScale; got != 2000 {
		t.Errorf("VelQuantScale default: got %v, want 2000", got)
	}
	if got := p.Cfg().SizeQuantScale; got != 500 {
		t.Errorf("SizeQuantScale default: got %v, want 500", got)
	}
}

func TestConfig_VelQuantScaleOverride(t *testing.T) {
	c := Config{CellsX: 1, CellsY: 1, VelQuantScale: 999, SizeQuantScale: 333}
	p, err := newProcessForTest(t, c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Shutdown()

	if got := p.Cfg().VelQuantScale; got != 999 {
		t.Errorf("VelQuantScale override: got %v, want 999", got)
	}
	if got := p.Cfg().SizeQuantScale; got != 333 {
		t.Errorf("SizeQuantScale override: got %v, want 333", got)
	}
}
```

If `Process` doesn't already have a public `Cfg()` accessor, add one — see Step 3. If `newProcessForTest` doesn't exist, the trivial harness is:

```go
func newProcessForTest(t *testing.T, c Config) (*Process, error) {
	t.Helper()
	c.Headless = true
	return New(c)
}
```

(use whatever pattern the existing tests in this package use — `coordinator_test.go`, `process_test.go`, etc. Match imports and helpers).

- [ ] **Step 2: Run the test, expect FAIL**

```bash
go test -run "TestConfig_VelQuantScale" ./pkg/universe/...
```

Expected: build error (`Config has no field VelQuantScale`).

- [ ] **Step 3: Add fields + defaults + accessor**

In `pkg/universe/coordinator.go`, find the `Config` struct (around line 41) and add after the `AoIRadius` field:

```go
	// VelQuantScale is the velocity-quantization multiplier used by the
	// standard engine bindings (int16 = vel * VelQuantScale). Higher
	// values give more precision but lower max speed (32767 / scale).
	// Default 2000 (max ~16 u/s, precision 0.0005).
	VelQuantScale  float32

	// SizeQuantScale is the radius-quantization multiplier used by the
	// standard engine bindings (int16 = radius * SizeQuantScale).
	// Default 500 (max ~65 units, precision 0.002).
	SizeQuantScale float32
```

In `New()` (defaults section near line 525), append:

```go
	if cfg.VelQuantScale == 0 {
		cfg.VelQuantScale = 2000
	}
	if cfg.SizeQuantScale == 0 {
		cfg.SizeQuantScale = 500
	}
```

If `Process` doesn't already have `Cfg() Config`, add it near the other accessors (e.g. next to `InvariantMode()` around line 935):

```go
// Cfg returns a copy of the Process's effective configuration (with
// defaults applied). Read-only — modifying the returned value has no
// effect on the running Process.
func (c *Process) Cfg() Config { return c.cfg }
```

- [ ] **Step 4: Run the test, expect PASS**

```bash
go test -run "TestConfig_VelQuantScale" ./pkg/universe/...
```

Expected: `ok`.

- [ ] **Step 5: Verify the broader package still builds**

```bash
go vet ./pkg/universe/...
```

Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/coordinator_config_test.go
git commit -m "$(cat <<'EOF'
feat(universe): add VelQuantScale/SizeQuantScale to Config

Process-wide defaults for the standard engine bindings, sourced from the
universal values every game in the codebase was setting per-kind today
(2000 / 500). Lays the groundwork for collapsing EngineBindingsConfig.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Add `Process.HostIndex(hostID)` helper

**Files:**
- Modify: `pkg/universe/coordinator.go` — `HostIndex` method
- Test: `pkg/universe/coordinator_test.go` (or whichever existing host-related test file exists)

The `DebugInfo.OwnerHost` field is a `uint8` index into the cluster's ordered host list. We need a stable mapping from host ID → index. Stable across reads; index slots are reused only after a host leaves and a new one joins.

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/coordinator_test.go` (or wherever `Process` host tests live):

```go
func TestProcess_HostIndex_Deterministic(t *testing.T) {
	p, _ := New(Config{CellsX: 1, CellsY: 1, Headless: true})
	defer p.Shutdown()

	// Manually register two hosts (use whatever test helper your
	// existing host-related tests use; if none, expose a test seam).
	registerTestHost(t, p, "host-a")
	registerTestHost(t, p, "host-b")

	a1 := p.HostIndex("host-a")
	a2 := p.HostIndex("host-a")
	if a1 != a2 {
		t.Errorf("HostIndex(host-a) not deterministic: %d vs %d", a1, a2)
	}
	if p.HostIndex("host-b") == a1 {
		t.Errorf("HostIndex collision between distinct hosts")
	}
	// Unknown host returns 0 (the zero value); not an error.
	if got := p.HostIndex("does-not-exist"); got != 0 {
		t.Errorf("unknown host: got %d, want 0", got)
	}
}
```

Substitute `registerTestHost` for whatever the existing helper is — search `pkg/universe/*_test.go` for `Hosts[`, `RegisterHost`, or similar setup. If there's no clean seam, the simplest registration is to assign directly into `p.Hosts` under the `runMu` (or the relevant lock) — match the pattern in existing tests.

- [ ] **Step 2: Run the test, expect FAIL**

```bash
go test -run "TestProcess_HostIndex" ./pkg/universe/...
```

Expected: `Process has no method HostIndex`.

- [ ] **Step 3: Implement `HostIndex`**

Add to `pkg/universe/coordinator.go` next to `HostForCellID` (around line 905):

```go
// HostIndex returns a stable 0-based index for the given host ID.
// Indices are assigned in host-registration order and persist for the
// lifetime of the Process; a slot is recycled only when the host leaves
// (allowing the next joiner to take its slot). Returns 0 if hostID is
// unknown — callers that need to distinguish "host 0" from "unknown"
// should check membership separately.
//
// Used by the DebugInfo writer system to populate OwnerHost as a
// compact uint8 for the client debug overlay.
func (c *Process) HostIndex(hostID string) uint8 {
	c.hostIndexMu.Lock()
	defer c.hostIndexMu.Unlock()
	if c.hostIndex == nil {
		c.hostIndex = make(map[string]uint8)
	}
	if idx, ok := c.hostIndex[hostID]; ok {
		return idx
	}
	if _, known := c.Hosts[hostID]; !known {
		return 0
	}
	idx := uint8(len(c.hostIndex))
	c.hostIndex[hostID] = idx
	return idx
}
```

Add the backing fields to the `Process` struct (near the `Hosts` field, ~line 560):

```go
	hostIndexMu sync.Mutex
	hostIndex   map[string]uint8
```

Index recycling on host leave: find the existing host-leave handler (search `delete(c.Hosts,` in `pkg/universe/`) and add a `delete(c.hostIndex, hostID)` next to the existing `delete(c.Hosts, hostID)` call (under whatever lock the Hosts map is already held by; if hostIndexMu is a separate lock, take it briefly).

If you can't find a clean leave point in this task's scope, leave the recycle TODO out — the test in Step 1 doesn't exercise leave, and the writer system tolerates stable index growth fine. But scan for `delete(c.Hosts` and at minimum confirm there's only one or two call sites; record them for follow-up.

- [ ] **Step 4: Run the test, expect PASS**

```bash
go test -run "TestProcess_HostIndex" ./pkg/universe/...
```

Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/coordinator_test.go
git commit -m "$(cat <<'EOF'
feat(universe): add Process.HostIndex(hostID) for stable uint8 host slot

Returns a deterministic 0-based index into the cluster's host roster,
assigned on first lookup and persisting for the host's lifetime.
Replaces the dynamic-cell-fragile cellY*gridWidth+cellX index that the
to-be-removed MeshState binding used. Consumed by the DebugInfo writer
to populate OwnerHost.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Implement `DebugInfoWriter` system

**Files:**
- Create: `pkg/system/debug_info_writer.go`
- Test: `pkg/system/debug_info_writer_test.go`

The writer is a per-cell system that runs each tick. It walks every entity that has `*component.DebugInfo` and writes:

- `Presence` from `Ghost`/`Replica`/neither (matches today's `MeshState` enum mapping).
- `OwnerHost` from `Process.HostIndex(host-for-this-entity's-cell)`.
- `AoIRadius` from `Process.Cfg().AoIRadius`.

The system needs a handle to the `*Process` (for cluster-wide host lookup) and the `*ecs.World` (for component access). The standard pattern in this codebase is `mmokit.SystemBase` with a `WorldOfCell` cast, but since this lives inside `pkg/system/` it can't import `pkg/mmokit`. Use the lower-level seams: `engine.System` and the cell handle exposed through `engine.System.Init` (search `pkg/system/spatial.go` or `pkg/system/network.go` for the established pattern; copy that).

If those systems pull from `engine.Engine` and `*ecs.World` only and reach the Process via a side-channel (e.g. the world's stored coord pointer), use the same channel. Don't introduce a new seam unless none exists.

- [ ] **Step 1: Write the failing test**

Create `pkg/system/debug_info_writer_test.go`:

```go
package system

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	enginepb "github.com/zenion/mmokit/gen/go/enginepb"
	"github.com/zenion/mmokit/pkg/component"
)

// debugInfoWriterFixture wires up the smallest harness that can
// run DebugInfoWriter.Update once: a world, a fake host-resolver, a
// fake aoiResolver, and a couple of entities with various marker
// combinations.
type debugInfoWriterFixture struct {
	w           *ecs.World
	debugMap    *ecs.Map1[component.DebugInfo]
	cellMap     *ecs.Map1[component.CellCoord]
	ghostMap    *ecs.Map1[component.Ghost]
	replicaMap  *ecs.Map1[component.Replica]
	hostByCell  func(cellX, cellY int32) uint8
	aoiRadius   float32
}

func newDebugInfoWriterFixture() *debugInfoWriterFixture {
	w := ecs.NewWorld(1024)
	return &debugInfoWriterFixture{
		w:          w,
		debugMap:   ecs.NewMap1[component.DebugInfo](w),
		cellMap:    ecs.NewMap1[component.CellCoord](w),
		ghostMap:   ecs.NewMap1[component.Ghost](w),
		replicaMap: ecs.NewMap1[component.Replica](w),
		hostByCell: func(_, _ int32) uint8 { return 0 },
		aoiRadius:  500,
	}
}

func TestDebugInfoWriter_PresenceLocal(t *testing.T) {
	f := newDebugInfoWriterFixture()
	e := f.debugMap.NewEntity(&component.DebugInfo{})
	f.cellMap.Add(e, &component.CellCoord{CellX: 1, CellY: 2})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	got := f.debugMap.Get(e).Presence
	if got != uint8(enginepb.EntityMeshState_EMS_LOCAL) {
		t.Errorf("Presence: got %d, want EMS_LOCAL (%d)", got, enginepb.EntityMeshState_EMS_LOCAL)
	}
}

func TestDebugInfoWriter_PresenceGhost(t *testing.T) {
	f := newDebugInfoWriterFixture()
	e := f.debugMap.NewEntity(&component.DebugInfo{})
	f.cellMap.Add(e, &component.CellCoord{CellX: 1, CellY: 2})
	f.ghostMap.Add(e, &component.Ghost{})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	got := f.debugMap.Get(e).Presence
	if got != uint8(enginepb.EntityMeshState_EMS_GHOST) {
		t.Errorf("Presence: got %d, want EMS_GHOST", got)
	}
}

func TestDebugInfoWriter_PresenceReplica(t *testing.T) {
	f := newDebugInfoWriterFixture()
	e := f.debugMap.NewEntity(&component.DebugInfo{})
	f.cellMap.Add(e, &component.CellCoord{CellX: 1, CellY: 2})
	f.replicaMap.Add(e, &component.Replica{SourceCellID: "cell_3_4"})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	got := f.debugMap.Get(e).Presence
	if got != uint8(enginepb.EntityMeshState_EMS_REPLICA) {
		t.Errorf("Presence: got %d, want EMS_REPLICA", got)
	}
}

func TestDebugInfoWriter_AoIRadiusFromConfig(t *testing.T) {
	f := newDebugInfoWriterFixture()
	f.aoiRadius = 1234
	e := f.debugMap.NewEntity(&component.DebugInfo{})
	f.cellMap.Add(e, &component.CellCoord{})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	if got := f.debugMap.Get(e).AoIRadius; got != 1234 {
		t.Errorf("AoIRadius: got %v, want 1234", got)
	}
}

func TestDebugInfoWriter_OwnerHostFromResolver(t *testing.T) {
	f := newDebugInfoWriterFixture()
	f.hostByCell = func(x, y int32) uint8 {
		if x == 5 && y == 6 {
			return 7
		}
		return 0
	}
	e := f.debugMap.NewEntity(&component.DebugInfo{})
	f.cellMap.Add(e, &component.CellCoord{CellX: 5, CellY: 6})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	if got := f.debugMap.Get(e).OwnerHost; got != 7 {
		t.Errorf("OwnerHost: got %d, want 7", got)
	}
}

func TestDebugInfoWriter_NoComponentNoCrash(t *testing.T) {
	f := newDebugInfoWriterFixture()
	// Entity without DebugInfo — writer should skip silently.
	emptyMap := ecs.NewMap0(f.w)
	emptyMap.NewEntity()

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce() // must not panic
}
```

- [ ] **Step 2: Run the test, expect FAIL**

```bash
go test -run "TestDebugInfoWriter" ./pkg/system/...
```

Expected: build error (the writer + helper don't exist yet).

- [ ] **Step 3: Implement the writer**

The writer's source-of-truth for OwnerHost differs by presence:

- **LOCAL / GHOST**: the entity is owned by *this* stage's cell. OwnerHost = host owning this cell.
- **REPLICA**: the entity is a remote-cell mirror. OwnerHost = host owning `Replica.SourceCellID`.

This mirrors the deleted `meshStateBinding` semantics (see `pkg/system/auto_replicator.go:451-462` before deletion). Resolving by source-cell-ID instead of by `CellCoord` lookup also dodges the dynamic-cell-depth wrinkle: replicas carry an explicit string ID, so depth is encoded for free.

Create `pkg/system/debug_info_writer.go`:

```go
// Package system — DebugInfoWriter is a builtin engine system that
// populates the engine-owned DebugInfo component on every entity that
// has one. Runs each tick, before replication.
package system

import (
	"github.com/mlange-42/ark/ecs"
	enginepb "github.com/zenion/mmokit/gen/go/enginepb"
	"github.com/zenion/mmokit/pkg/component"
)

// DebugInfoWriter walks every entity with *component.DebugInfo each
// tick and writes Presence (LOCAL/REPLICA/GHOST), OwnerHost (uint8
// host index), and AoIRadius. Game code must not write to DebugInfo
// directly — the writer overwrites every tick.
type DebugInfoWriter struct {
	debugMap   *ecs.Map1[component.DebugInfo]
	ghostMap   *ecs.Map1[component.Ghost]
	replicaMap *ecs.Map1[component.Replica]

	// localHost is the host-index for the stage hosting this writer.
	// Captured at Init/SetDeps time; updated only on host migration
	// (rare; live update comes from the wiring layer if needed).
	localHost uint8

	// hostByCellID resolves a source-cell string ID (e.g. "cell_3_4")
	// to a uint8 host index. Used only for REPLICA entities.
	hostByCellID func(cellID string) uint8

	// aoiRadius reads the live AoI radius from Process.Cfg().
	aoiRadius func() float32
}

// NewDebugInfoWriter constructs a writer with closures for the bits
// that live outside the cell's ECS world. localHost is captured at
// construction; hostByCellID and aoiRadius are called per-tick.
func NewDebugInfoWriter(
	w *ecs.World,
	localHost uint8,
	hostByCellID func(cellID string) uint8,
	aoiRadius func() float32,
) *DebugInfoWriter {
	return &DebugInfoWriter{
		debugMap:     ecs.NewMap1[component.DebugInfo](w),
		ghostMap:     ecs.NewMap1[component.Ghost](w),
		replicaMap:   ecs.NewMap1[component.Replica](w),
		localHost:    localHost,
		hostByCellID: hostByCellID,
		aoiRadius:    aoiRadius,
	}
}

// SetLocalHost updates the writer's notion of which host owns the
// stage. Call from the wiring layer when a cell migrates to a new
// host (rare — most stages keep the same host for their lifetime).
func (w *DebugInfoWriter) SetLocalHost(idx uint8) { w.localHost = idx }

// Update runs the writer once. Plug into the engine's per-cell tick
// before the network/replication system.
func (w *DebugInfoWriter) Update(_ float32) {
	radius := w.aoiRadius()
	q := w.debugMap.Query()
	for q.Next() {
		e := q.Entity()
		di := q.Get()

		switch {
		case w.ghostMap.HasAll(e):
			di.Presence = uint8(enginepb.EntityMeshState_EMS_GHOST)
			di.OwnerHost = w.localHost
		case w.replicaMap.HasAll(e):
			di.Presence = uint8(enginepb.EntityMeshState_EMS_REPLICA)
			di.OwnerHost = w.hostByCellID(w.replicaMap.Get(e).SourceCellID)
		default:
			di.Presence = uint8(enginepb.EntityMeshState_EMS_LOCAL)
			di.OwnerHost = w.localHost
		}
		di.AoIRadius = radius
	}
}
```

Update the test fixture to match the new constructor signature — replace the `hostByCell func(int32, int32) uint8` field with `localHost uint8` and `hostByCellID func(string) uint8`. The Replica test (`TestDebugInfoWriter_PresenceReplica`) now also asserts that OwnerHost is resolved through the `hostByCellID` closure.

Adjusted test helper:

```go
type debugInfoWriterFixture struct {
	w            *ecs.World
	debugMap     *ecs.Map1[component.DebugInfo]
	ghostMap     *ecs.Map1[component.Ghost]
	replicaMap   *ecs.Map1[component.Replica]
	localHost    uint8
	hostByCellID func(cellID string) uint8
	aoiRadius    float32
}

func newDebugInfoWriterFixture() *debugInfoWriterFixture {
	w := ecs.NewWorld(1024)
	return &debugInfoWriterFixture{
		w:            w,
		debugMap:     ecs.NewMap1[component.DebugInfo](w),
		ghostMap:     ecs.NewMap1[component.Ghost](w),
		replicaMap:   ecs.NewMap1[component.Replica](w),
		localHost:    0,
		hostByCellID: func(string) uint8 { return 0 },
		aoiRadius:    500,
	}
}

func newDebugInfoWriterForTest(f *debugInfoWriterFixture) *debugInfoWriterUnderTest {
	w := NewDebugInfoWriter(
		f.w,
		f.localHost,
		func(id string) uint8 { return f.hostByCellID(id) },
		func() float32 { return f.aoiRadius },
	)
	return &debugInfoWriterUnderTest{DebugInfoWriter: w}
}
```

Adjust the `TestDebugInfoWriter_OwnerHostFromResolver` test:

```go
func TestDebugInfoWriter_OwnerHostFromResolver(t *testing.T) {
	f := newDebugInfoWriterFixture()
	f.hostByCellID = func(id string) uint8 {
		if id == "cell_3_4" {
			return 7
		}
		return 0
	}
	e := f.debugMap.NewEntity(&component.DebugInfo{})
	f.replicaMap.Add(e, &component.Replica{SourceCellID: "cell_3_4"})

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	if got := f.debugMap.Get(e).OwnerHost; got != 7 {
		t.Errorf("OwnerHost: got %d, want 7", got)
	}
}
```

Also add a Local-OwnerHost test:

```go
func TestDebugInfoWriter_OwnerHostLocal(t *testing.T) {
	f := newDebugInfoWriterFixture()
	f.localHost = 9
	e := f.debugMap.NewEntity(&component.DebugInfo{})
	// no Replica/Ghost — entity is LOCAL

	wr := newDebugInfoWriterForTest(f)
	wr.UpdateOnce()

	if got := f.debugMap.Get(e).OwnerHost; got != 9 {
		t.Errorf("OwnerHost: got %d, want 9 (localHost)", got)
	}
}
```

Drop the now-unused `cellMap` from the fixture struct + initialization.

Then add the test helper at the bottom of `pkg/system/debug_info_writer_test.go`:

```go
// newDebugInfoWriterForTest returns a writer whose closures read from
// the fixture so individual tests can tweak hostByCell / aoiRadius
// after construction.
type debugInfoWriterUnderTest struct{ *DebugInfoWriter }

func (t *debugInfoWriterUnderTest) UpdateOnce() { t.Update(0) }

func newDebugInfoWriterForTest(f *debugInfoWriterFixture) *debugInfoWriterUnderTest {
	w := NewDebugInfoWriter(
		f.w,
		func(x, y int32) uint8 { return f.hostByCell(x, y) },
		func() float32 { return f.aoiRadius },
	)
	return &debugInfoWriterUnderTest{DebugInfoWriter: w}
}
```

(Closures capture `f` so test mutations to `f.hostByCell` / `f.aoiRadius` after construction take effect on the next `UpdateOnce()`.)

- [ ] **Step 4: Run all writer tests, expect PASS**

```bash
go test -run "TestDebugInfoWriter" ./pkg/system/...
```

Expected: `ok` for all six tests.

- [ ] **Step 5: Verify no regressions in pkg/system**

```bash
go test ./pkg/system/...
```

Expected: existing tests pass. If any pre-existing flakes, note them and don't fix in this PR.

- [ ] **Step 6: Auto-register in `Process.Build()`**

`engine.SystemDef` has the shape:

```go
type SystemDef struct {
	Name    string
	Factory func() System
}
```

(See `pkg/engine/system.go:113`.) Per-cell instantiation in `pkg/universe/coordinator.go` around line 1674 calls `def.Factory()` to get a fresh `engine.System` per cell, then calls `SetDeps(w *ecs.World, eng *engine.Engine, gw any)` on it if the system implements that interface. That's the seam the writer plugs into.

Create a new file `pkg/universe/debug_info_writer_wiring.go`:

```go
package universe

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmokit/pkg/engine"
	"github.com/zenion/mmokit/pkg/system"
)

// debugInfoWriterShim wraps a system.DebugInfoWriter as an
// engine.System. The Factory closure captures the *Process; SetDeps
// receives the per-cell world + the typed game world (which is the
// *Stage). The shim resolves localHost / hostByCellID / aoiRadius
// from the Process at SetDeps time, then delegates Update() to the
// wrapped writer.
type debugInfoWriterShim struct {
	coord  *Process
	writer *system.DebugInfoWriter
}

// SetDeps is invoked by the per-cell setup loop in Build(). gw is the
// game world for this cell (a *Stage in mmokit-based games; some
// games embed Stage in a typed wrapper but always implement
// CellID() string and GetAoIRadius() float32 transitively).
func (s *debugInfoWriterShim) SetDeps(w *ecs.World, _ *engine.Engine, gw any) {
	stage, _ := gw.(*Stage)
	var localHost uint8
	if stage != nil {
		if host := s.coord.HostForCellID(stage.CellID()); host != "" {
			localHost = s.coord.HostIndex(host)
		}
	}
	hostByCellID := func(id string) uint8 {
		host := s.coord.HostForCellID(id)
		if host == "" {
			return 0
		}
		return s.coord.HostIndex(host)
	}
	aoiRadius := func() float32 {
		if stage != nil {
			return stage.GetAoIRadius()
		}
		return s.coord.cfg.AoIRadius
	}
	s.writer = system.NewDebugInfoWriter(w, localHost, hostByCellID, aoiRadius)
}

// Init is a no-op — all setup happened in SetDeps.
func (s *debugInfoWriterShim) Init() {}

// Update delegates to the underlying writer.
func (s *debugInfoWriterShim) Update(dt float32) {
	if s.writer != nil {
		s.writer.Update(dt)
	}
}

// debugInfoWriterSystemDef is the SystemDef the coordinator prepends
// to c.systemDefs in Build() so every cell receives the writer.
func debugInfoWriterSystemDef(c *Process) engine.SystemDef {
	return engine.SystemDef{
		Name: "DebugInfoWriter",
		Factory: func() engine.System {
			return &debugInfoWriterShim{coord: c}
		},
	}
}
```

(If the wrapping game world type doesn't satisfy `*Stage` directly — `gw` is `any` and may be a typed wrapper that embeds `*Stage` — fall back to a small interface in this same file:

```go
type stageProvider interface {
	CellID() string
	GetAoIRadius() float32
}

// In SetDeps:
if sp, ok := gw.(stageProvider); ok { ... }
```

Most callers pass `*Stage` directly, but this is the safer pattern.)

Then in `pkg/universe/coordinator.go`, find `Build()` (around line 996) and prepend the writer's SystemDef as the very first thing the function does, after early-returns and before any work that consumes `c.systemDefs`:

```go
func (c *Process) Build() {
	c.systemDefs = append(
		[]engine.SystemDef{debugInfoWriterSystemDef(c)},
		c.systemDefs...,
	)
	// ... existing body unchanged ...
}
```

If `Build()` is called more than once (idempotent guard somewhere?), check it doesn't double-prepend. Search the function for an `if c.built` or similar; place the prepend after that guard.

- [ ] **Step 7: Verify Build() compiles and existing tests pass**

```bash
go vet ./pkg/universe/...
go test ./pkg/universe/...
```

Expected: builds cleanly, existing tests pass.

- [ ] **Step 8: Commit**

```bash
git add pkg/system/debug_info_writer.go pkg/system/debug_info_writer_test.go \
        pkg/universe/debug_info_writer_wiring.go pkg/universe/coordinator.go
git commit -m "$(cat <<'EOF'
feat(system): builtin DebugInfoWriter system + auto-register in Build()

Walks every entity with *component.DebugInfo each tick and writes
Presence (LOCAL/REPLICA/GHOST), OwnerHost (uint8 cluster-host index),
and AoIRadius (from Config). No-op for entities without the component
— bundle membership is the sole opt-in for client wire exposure.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Atomic API break — drop `EngineBindingsConfig`, `MeshState` binding, `RegisterKind` bindings arg

This is the breaking commit. Every Go file that references `EngineBindingsConfig`, `IncludeMeshState`, the `MeshState()` constructor, or the four-argument form of `RegisterKind` must be updated in lockstep.

**Files (modify all in one commit):**
- `pkg/system/auto_replicator.go` — delete `meshStateBinding`, `MeshState()`, `parseCellIndex()`, `EngineBindingsConfig`; change `EngineBindings()` signature
- `pkg/mmokit/mmokit.go` — re-export `mmokit.DebugInfo`; delete `EngineBindingsConfig` re-export; delete `EngineBindings` re-export; update `BuildReplicators` to source quant scales from `coord.Cfg()`
- `pkg/mmokit/kindreg.go` — drop `bindings EngineBindingsConfig` parameter
- `pkg/universe/entity_kind.go` — delete `EngineBindings *system.EngineBindingsConfig` field
- `pkg/mmokit/kindreg_test.go` — drop `EngineBindingsConfig{}` from every call (8 sites)
- `pkg/mmokit/spawn_init_test.go` — drop `EngineBindingsConfig{}` from every call (4 sites)
- `examples/4node-basic/main.go` — drop `playerBindings` var + bindings args (2 sites)
- `internal/game/entity_kinds.go` — drop bindings args from all 5 sites
- Any other `*_test.go` in `pkg/` that still references `EngineBindingsConfig` or the old `RegisterKind` signature

- [ ] **Step 1: Inventory every call site**

```bash
grep -rn "EngineBindingsConfig\|IncludeMeshState\|MeshState(\|EntityKindDef.*EngineBindings\|RegisterKind\[" \
  --include="*.go" . | tee /tmp/api_break_sites.txt
```

Expected: a deterministic list of every site to update. Cross-check with the file list above; note any extras.

- [ ] **Step 2: Update `pkg/system/auto_replicator.go`**

Find and delete:

- `type EngineBindingsConfig struct { ... }` (around line 561)
- `type meshStateBinding struct { ... }` (around line 412)
- `func MeshState(...)` (around line 424)
- `func parseCellIndex(...)` (around line 493)
- The `if cfg.IncludeMeshState { ... }` branch in `EngineBindings()` (around line 617)

Change `EngineBindings()` signature from:

```go
func EngineBindings(w *ecs.World, cfg EngineBindingsConfig) ComponentBinding {
```

to:

```go
// EngineBindings returns a ComponentBinding bundling the standard
// engine-level replication fields: viewer-relative position, quantized
// velocity, quantized size. velScale and sizeScale come from the
// process Config.
func EngineBindings(w *ecs.World, velScale, sizeScale float32) ComponentBinding {
	if velScale == 0 {
		velScale = 100
	}
	if sizeScale == 0 {
		sizeScale = 100
	}
	posMap := ecs.NewMap1[component.Position](w)
	cellMap := ecs.NewMap1[component.CellCoord](w)
	velMap := ecs.NewMap1[component.Velocity](w)
	colliderMap := ecs.NewMap1[component.Collider](w)

	bindings := []ComponentBinding{
		ViewerRelativePos(posMap, cellMap),
		QVelocity(velMap, velScale),
		QSize(colliderMap, sizeScale),
	}
	return &bindingGroup{bindings: bindings}
}
```

(Drop `ghostMap`, `replicaMap`, `CellSizeFn`, `GridWidth`, `IncludeMeshState`. The fallback `velScale==0`/`sizeScale==0` defaults handle bare-minimum tests that pass zero; production calls always thread `coord.Cfg().VelQuantScale`.)

- [ ] **Step 3: Update `pkg/universe/entity_kind.go`**

Delete the `EngineBindings *system.EngineBindingsConfig` field from `EntityKindDef`:

```go
// Before
type EntityKindDef struct {
	Kind            uint8
	Name            string
	EngineBindings  *system.EngineBindingsConfig // <-- DELETE THIS LINE
	NetworkBindings []system.ComponentBinding
	// ... other fields ...
}
```

Remove the `system` import if it becomes unused (likely not — `NetworkBindings []system.ComponentBinding` keeps it).

- [ ] **Step 4: Update `pkg/mmokit/mmokit.go`**

Find and delete:

- `type EngineBindingsConfig = system.EngineBindingsConfig` (around line 813)
- `func EngineBindings(w *ecs.World, coord *universe.Process, cfg ...EngineBindingsConfig) ComponentBinding { ... }` (around line 821)

Add the new `DebugInfo` re-export near the existing component re-exports:

```go
// DebugInfo is the engine-provided per-entity debug component.
// Bundles that include *DebugInfo expose Presence/OwnerHost/AoIRadius
// to clients; bundles that omit it pay zero wire cost. The engine's
// builtin writer system (auto-registered on Build()) populates the
// fields each tick.
type DebugInfo = component.DebugInfo
```

Update `BuildReplicators()` (around line 1117). Replace the `if def.EngineBindings != nil { ... }` block with:

```go
		var velScale, sizeScale float32
		if coord != nil {
			velScale = coord.Cfg().VelQuantScale
			sizeScale = coord.Cfg().SizeQuantScale
		}
		bindings = append(bindings, system.EngineBindings(w, velScale, sizeScale))
```

Drop the comment block about `IncludeMeshState` semantics — it's obsolete.

- [ ] **Step 5: Update `pkg/mmokit/kindreg.go`**

Change the signature:

```go
// Before
func RegisterKind[T any](
	p *universe.Process,
	kind uint8,
	name string,
	bindings EngineBindingsConfig,
	args ...RegisterKindArg,
) {

// After
func RegisterKind[T any](
	p *universe.Process,
	kind uint8,
	name string,
	args ...RegisterKindArg,
) {
```

Inside the body, find `def := universe.EntityKindDef{Kind: kind, Name: name, EngineBindings: &bindings}` and replace with:

```go
def := universe.EntityKindDef{Kind: kind, Name: name}
```

The `bindings` local variable goes away.

Update the function-doc example block at the top to match the new signature.

- [ ] **Step 6: Update test call sites**

For each site found in Step 1's `/tmp/api_break_sites.txt`:

- In `pkg/mmokit/kindreg_test.go` (8 sites): drop the `EngineBindingsConfig{}` argument from each `RegisterKind` call.

  Example:
  ```go
  // Before
  RegisterKind[kindRegTestBundle](mmo, 100, "TestKind", EngineBindingsConfig{})

  // After
  RegisterKind[kindRegTestBundle](mmo, 100, "TestKind")
  ```

- In `pkg/mmokit/spawn_init_test.go` (4 sites): same treatment.

Drop any local `EngineBindingsConfig{}` literals that become orphaned.

- [ ] **Step 7: Update `examples/4node-basic/main.go`**

Replace lines 48-51 with:

```go
	mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player")
	mmokit.RegisterKind[BotComponents](mmo, KindBot, "Bot")
```

Drop the now-orphaned `playerBindings` local variable.

- [ ] **Step 8: Update `internal/game/entity_kinds.go`**

For each of the 5 `RegisterKind` calls, delete the `mmokit.EngineBindingsConfig{...}` argument:

```go
// Before (Ship, line 28-46)
mmokit.RegisterKind[ShipBundle](p, gamecomp.TypeShip, "Ship",
    mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500, IncludeMeshState: true},
    mmokit.WithExtraBindingFn(...),
    mmokit.WithField[gamecomp.Inventory](...),
    mmokit.WithField[gamecomp.StatusEffects](...),
)

// After
mmokit.RegisterKind[ShipBundle](p, gamecomp.TypeShip, "Ship",
    mmokit.WithExtraBindingFn(...),
    mmokit.WithField[gamecomp.Inventory](...),
    mmokit.WithField[gamecomp.StatusEffects](...),
)
```

Repeat for Asteroid, Station, NPC, LootCrate.

- [ ] **Step 9: Compile & test the world**

```bash
just build
go test ./...
```

Expected: clean build, all tests pass. If a test references the deleted symbols and was missed in Step 1's grep, fix it now (identical pattern: drop `EngineBindingsConfig{}` argument).

- [ ] **Step 10: Commit**

```bash
git add pkg/system/auto_replicator.go pkg/universe/entity_kind.go \
        pkg/mmokit/mmokit.go pkg/mmokit/kindreg.go \
        pkg/mmokit/kindreg_test.go pkg/mmokit/spawn_init_test.go \
        examples/4node-basic/main.go internal/game/entity_kinds.go
# add any extra _test.go files updated
git commit -m "$(cat <<'EOF'
refactor!: drop EngineBindingsConfig + MeshState binding; quant scales on Config

EngineBindingsConfig dissolves entirely — IncludeMeshState is replaced
by bundle membership of *mmokit.DebugInfo, GridWidth becomes
HostIndex(HostForCellID(cellID)), and VelQuantScale/SizeQuantScale move
to Config with defaults that match every existing call site.

Breaking: RegisterKind drops its bindings argument; EntityKindDef.EngineBindings
field deleted; system.MeshState() and parseCellIndex() deleted. All 7
in-repo callers updated; old wire byte for meshState/ownerNode replaced
by DebugInfo's three flat fields once games opt in.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Add `*mmokit.DebugInfo` to bundles that should expose it

Up to this point, no bundle declares `*DebugInfo`, so no entity actually serializes it. Wire that up now.

**Files:**
- Modify: `examples/4node-basic/components.go`
- Modify: `internal/game/entity_kinds.go` (or the bundle definitions in `internal/component/`)

- [ ] **Step 1: Update `examples/4node-basic/components.go`**

Replace the existing `Debug *DebugInfo` field in `PlayerComponents` with the engine-provided one:

```go
// Before
type PlayerComponents struct {
	Name       *PlayerName
	Debug      *DebugInfo
	MoveTarget *mmokit.MoveTarget
}

// After
type PlayerComponents struct {
	Name       *PlayerName
	DebugInfo  *mmokit.DebugInfo
	MoveTarget *mmokit.MoveTarget
}
```

Leave `BotComponents` unchanged — bots don't carry `DebugInfo` (matches the existing pattern).

The local `DebugInfo` struct is still in `components.go` at this point. Don't delete it yet — Task 7 does that as a cleaner narrative ("the hand-rolled component is now unused; remove it").

- [ ] **Step 2: Locate `internal/game/` bundle definitions**

The bundle structs (`ShipBundle`, `NPCBundle`, etc.) live in either `internal/game/entity_kinds.go` or `internal/component/`. Locate and add `*mmokit.DebugInfo`:

```bash
grep -n "type ShipBundle struct\|type NPCBundle struct\|type AsteroidBundle struct\|type StationBundle struct\|type LootCrateBundle struct" \
  internal/game/*.go internal/component/*.go
```

For each bundle, add the field. Conservative scope: add to **`ShipBundle` and `NPCBundle` only** (the moving entities; mining/combat is the use case). Stationary kinds (Asteroid, Station, LootCrate) can wait for a follow-up if anyone notices the missing overlay; today they just get the defaults baseline (no MeshState even today doesn't matter for static stuff — clients render them with no movement).

Decision rule: any bundle that previously had `IncludeMeshState: true` AND wants the debug overlay on the client gets `*mmokit.DebugInfo`. Looking at the spec, all 5 game kinds had `IncludeMeshState: true`, so add to all 5. Better to over-include than to silently drop overlay capability.

```go
// internal/component/ship_bundle.go (or wherever ShipBundle lives)
type ShipBundle struct {
	Pos        *component.Position
	// ... existing fields ...
	DebugInfo  *mmokit.DebugInfo  // <-- new
}
```

Repeat for `AsteroidBundle`, `StationBundle`, `NPCBundle`, `LootCrateBundle`.

If `mmokit` isn't already imported in the bundle file, add it.

- [ ] **Step 3: Build + run tests**

```bash
just build
go test ./...
```

Expected: clean build, tests pass. Wire format change is benign at this point — DebugInfo bytes are emitted but the web client still reads `meshState`/`ownerNode` (which now come from a different source — Task 8 fixes the client side).

Wait, that's a regression window: between Task 5 commit and Task 8 commit, the web client is reading fields that no longer exist on the wire. To keep this safe, run `just build` (which regenerates the SDK) and the build succeeds because the SDK now declares `presence`/`ownerHost`/`aoiRadius` on the entity TS interfaces. The web client TS code that references the old names will fail typecheck during the web build.

To dodge that intermediate breakage, fold Task 8's web-client field rename into this commit. Update Task 6 step list to include the SDK regen + web rename inline:

- [ ] **Step 4: Regenerate SDK + update web client field names**

```bash
just client-sdk examples/4node-basic
just space-sdk
```

These rewrite `examples/4node-basic/web/sdk/entities.ts` and `web-pixi/sdk/entities.ts` from the new schema.

Search for the old field names in non-generated code:

```bash
grep -rn "meshState\|ownerNode\|aoIRadius" \
  examples/4node-basic/web/src \
  web-pixi/src \
  --include="*.ts" --include="*.tsx"
```

For each hit, rename:
- `entity.meshState` → `entity.presence`
- `entity.ownerNode` → `entity.ownerHost`
- `entity.aoIRadius` → `entity.aoiRadius` (note casing: `aoiRadius`, since the codegen lowercases the first run of capitals; verify against the actual generated SDK)

Don't edit files inside `**/sdk/` — they regenerate from `just build`. Check via the sdkgen pattern in the repo: typically `sdk/` is generated from a `cmd/sdkgen/` run.

In `examples/4node-basic/web/src/network.ts:107-108`, the existing code reads:

```ts
ent.isReplica = raw.meshState === EntityMeshState.EMS_REPLICA;
ent.isGhost = raw.meshState === EntityMeshState.EMS_GHOST;
```

becomes:

```ts
ent.isReplica = raw.presence === EntityMeshState.EMS_REPLICA;
ent.isGhost = raw.presence === EntityMeshState.EMS_GHOST;
```

Repeat for any web-pixi consumers (use grep results from above).

- [ ] **Step 5: Run web tests**

```bash
cd examples/4node-basic/web && bun test
```

Expected: vitest passes. If the test snapshots include the old field names, regenerate snapshots: `bun test -u`.

- [ ] **Step 6: Smoke-run the dev server**

```bash
just dev
```

In another terminal, point a browser at `http://localhost:8080`, log in as a test user, observe the AoI overlay still draws (DebugInfoWriter writes AoIRadius from Config), and observe the R/G replica markers at cell borders (DebugInfoWriter writes Presence; web reads `presence`). Kill the dev server when satisfied.

- [ ] **Step 7: Commit**

```bash
git add examples/4node-basic/components.go examples/4node-basic/web/src \
        examples/4node-basic/web/sdk \
        web-pixi/src web-pixi/sdk \
        internal/component internal/game
git commit -m "$(cat <<'EOF'
feat(games): opt into mmokit.DebugInfo for client debug overlays

PlayerComponents (4node-basic) and the five internal/game bundles now
include *mmokit.DebugInfo, which the engine's writer system populates
each tick. Web clients read presence/ownerHost/aoiRadius via the
regenerated SDK; the old meshState/ownerNode fields are gone.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Delete 4node-basic's hand-rolled `DebugInfo` + `DebugInfoSystem`

The local component is now unreferenced. Sweep it.

**Files:**
- Modify: `examples/4node-basic/components.go` — delete the local `DebugInfo` struct
- Delete: `examples/4node-basic/system_debug_info.go`
- Modify: `examples/4node-basic/main.go` — drop `mmo.AddSystem(mmokit.NewSystem(&DebugInfoSystem{}))`
- Modify: `examples/4node-basic/mesh_e2e_test.go` (and any other test file) — drop references to `DebugInfoSystem`

- [ ] **Step 1: Verify the local component is truly unused**

```bash
grep -rn "DebugInfo\|DebugInfoSystem" examples/4node-basic --include="*.go"
```

Expected hits: only the local definition, the system file, and the test that adds the system. Anywhere else (e.g. spawn-time stamping) means there's still a consumer; investigate and migrate.

- [ ] **Step 2: Delete the file `system_debug_info.go`**

```bash
rm examples/4node-basic/system_debug_info.go
```

- [ ] **Step 3: Delete the `DebugInfo` struct in `components.go`**

```go
// Delete this block (around lines 10-13):
// DebugInfo holds per-entity game-specific debug state replicated to clients.
type DebugInfo struct {
	AoIRadius float32 `net:"f32"` // server's current AoI radius (for debug overlay)
}
```

- [ ] **Step 4: Drop the system registration in `main.go`**

Find and remove:

```go
mmo.AddSystem(mmokit.NewSystem(&DebugInfoSystem{}))
```

(line ~84 today)

- [ ] **Step 5: Remove from `mesh_e2e_test.go`**

```bash
grep -n "DebugInfoSystem" examples/4node-basic/mesh_e2e_test.go
```

For each hit (line 220 today):

```go
// Delete this line:
host.AddSystem(mmokit.NewSystem(&DebugInfoSystem{}))
```

- [ ] **Step 6: Build + test**

```bash
just build
go test ./examples/4node-basic/...
```

Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add examples/4node-basic
git commit -m "$(cat <<'EOF'
refactor(4node-basic): delete hand-rolled DebugInfo + DebugInfoSystem

Now that the engine provides mmokit.DebugInfo and an auto-registered
writer system, the example's local component and stamping system are
redundant. Removing them unblocks the example as a teaching reference
for the engine's debug-overlay pattern.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Verify `internal/game` still works end-to-end

The space-game has more replicated state and many more callers. Walk it.

- [ ] **Step 1: Build**

```bash
just build
```

Expected: clean. The five `RegisterKind` call sites in `internal/game/entity_kinds.go` were updated in Task 5; the bundles got `*mmokit.DebugInfo` in Task 6.

- [ ] **Step 2: Run unit tests**

```bash
go test ./internal/game/... ./internal/component/... ./internal/marketplace/...
```

Expected: pass. If any test directly constructs an `EntityKindDef` literal and uses the old `EngineBindings` field, fix it:

```go
// Before
def := universe.EntityKindDef{
	Kind: 1, Name: "Test",
	EngineBindings: &system.EngineBindingsConfig{IncludeMeshState: true},
}

// After
def := universe.EntityKindDef{Kind: 1, Name: "Test"}
```

- [ ] **Step 3: Run the integration suite**

```bash
just test-pg
```

Expected: pass. Persistence is unrelated to this refactor but exercising it gives confidence the broader build is healthy.

- [ ] **Step 4: Smoke the space-game web client**

In one terminal:

```bash
cd web-pixi && bun dev
```

In another:

```bash
just run
```

Log in via the web client, fly around, attack an NPC. Confirm the debug overlay (if exposed in the UI) draws AoI + replica markers.

- [ ] **Step 5: Commit if any fixes were needed**

```bash
git status
# only commit if there are changes
git add internal/
git commit -m "$(cat <<'EOF'
fix(internal): adjust internal/game tests for new RegisterKind signature

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

If `git status` is clean, skip the commit.

---

## Task 9: Final verification + grep sweep

- [ ] **Step 1: Repo-wide grep for orphaned references**

```bash
grep -rn "EngineBindingsConfig\|IncludeMeshState\|MeshState(\|EngineBindings(" \
  --include="*.go" --include="*.ts" --include="*.tsx" \
  .
```

Expected: no hits in source code (excluding `.git/` and `node_modules/`). If anything turns up, treat as a missed call site and patch it.

- [ ] **Step 2: Schema-export sanity check**

```bash
cd examples/4node-basic && go run . --dump-schema | jq '.entityKinds[] | {name, fields: [.fields[].name]}'
```

Expected: each `Player`/`Bot` entry's `fields` array contains `presence`, `ownerHost`, `aoiRadius` (Player only; Bot omits them since the bundle doesn't declare `*DebugInfo`). No `meshState` or `ownerNode` anywhere.

- [ ] **Step 3: Vet + test the world**

```bash
go vet ./...
go test ./...
```

Expected: clean.

- [ ] **Step 4: Final commit if any sweep fixes were needed**

If Step 1 turned up missed sites:

```bash
git add ...
git commit -m "$(cat <<'EOF'
chore: sweep stragglers from EngineBindingsConfig removal

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review checklist

Run after the plan completes; each commit should be green and the spec fully implemented.

- [ ] `mmokit.DebugInfo` exists in `pkg/component/core.go`, re-exported as `mmokit.DebugInfo`.
- [ ] Builtin `DebugInfoWriter` is auto-registered on every cell via `Build()`.
- [ ] `Config.VelQuantScale` and `Config.SizeQuantScale` default to `2000`/`500`.
- [ ] `EngineBindingsConfig`, `MeshState()`, `parseCellIndex()` are gone.
- [ ] `EntityKindDef.EngineBindings` field is gone.
- [ ] `RegisterKind` has the new 3+variadic signature.
- [ ] 4node-basic's local `DebugInfo` and `DebugInfoSystem` are gone.
- [ ] Web clients read `presence`/`ownerHost`/`aoiRadius`; old names absent.
- [ ] No regressions: `go vet ./...` and `go test ./...` pass; web vitest passes.
- [ ] Smoke run: AoI overlay draws; replica/host markers render at cell borders.
