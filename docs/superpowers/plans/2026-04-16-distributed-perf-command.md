# Distributed Perf Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the `perf` command work in distributed (multi-host, multi-process) mode — on each host it aggregates across that host's local cells (with per-cell drill-down); on the coordinator it aggregates per-host (with drill-down to a specific host or cell). All data collection runs on the target host.

**Architecture:** Introduce a structured `PerfCellSnapshot` wire type plus two worker verbs — `perf.snapshot` and `perf.reset` — both routed with `RouteAllHosts`. The top-level `perf` verb becomes a presentation frontend (`RouteLocal`) that dispatches to the workers via `Dispatcher.InvokeInternal` (using `RouteSpecificHost` when `--host` is supplied, and post-filtering rows by `CellID` when `--cell` is supplied). This mirrors the existing `cell.list --live` → `cell.snapshot` pattern in [pkg/universe/builtins_cell.go:154-239](pkg/universe/builtins_cell.go#L154-L239), which is already proven in production.

**Tech Stack:** Go, `pkg/cmdsys` (command/dispatcher/routing), `pkg/universe` (Coordinator/Cell), `pkg/engine` (TickProfile/Engine), `pkg/metrics` (CellMetrics/LoadSnapshot).

---

## File Structure

**Modify:**
- `pkg/universe/builtins_perf.go` — replace the single local `perf` handler with three verbs: `perf.snapshot` (worker, RouteAllHosts), `perf.reset` (worker, RouteAllHosts), `perf` (frontend, RouteLocal). Move the `load` command into its own file as it is unrelated.
- `pkg/universe/coordinator.go` (lines 1285-1325 in `startConsole`) — stop gating `registerPerfBuiltins` on `defaultEng != nil`; it now registers regardless and takes `*Coordinator` instead of `*engine.Engine`.
- `pkg/engine/console.go` (lines 505-542) — extract the text-rendering portion of `FormatPerfOutput` so it can accept a `PerfCellSnapshot` (structured) rather than reading directly from an `*Engine`.

**Create:**
- `pkg/universe/perf_snapshot.go` — defines `PerfCellSnapshot`, `SystemTiming`, `TickTimingStats`, plus a `buildPerfCellSnapshot(cell *Cell, hostID string)` helper. Kept separate from `builtins_perf.go` so the wire type lives next to the data collection helper, and the command registrations stay readable.
- `pkg/universe/builtins_load.go` — moved `load` command (extracted from builtins_perf.go).
- `pkg/universe/builtins_perf_test.go` — unit tests for the new wire type + handlers.
- `pkg/universe/s7_perf_test.go` — end-to-end test exercising coordinator → remote host `perf.snapshot` over MeshControl.

Each worker is short (≤60 lines of handler body). The frontend command also stays short (≤80 lines) by delegating rendering to the extracted formatter.

---

## Design Notes

**CLI syntax (coordinator and host alike):**

```
perf                              # aggregate — every cell on every host
perf --host <hostID>              # drill into one host's cells
perf --host <hostID> --cell <id>  # one specific cell on one host
perf --cell <id>                  # one cell anywhere (coord resolves its host)
perf reset                        # same scoping flags apply; fans out perf.reset
```

`--host` / `--cell` combine naturally: when both are given, the route is `RouteSpecificHost` (targeted at the named host) and the handler filters locally by `CellID`. When only `--cell` is given, the route is `RouteSpecificCell`, which the mesh resolver converts to the owning host. With no flags, the route is `RouteAllHosts`.

**Why routed verbs (`perf.snapshot`) instead of a single `perf` verb with `RouteAllHosts`:** a frontend/worker split keeps rendering logic on the caller — the host only needs to be able to produce structured data. It also matches `cell.list --live` + `cell.snapshot`, and it lets the frontend compose (fan out + post-filter by CellID, or target a single host) without every worker handler re-implementing routing logic.

**Fallback behavior (preserves single-process dev):** `meshRouteResolver.Resolve` already falls through to `RouteLocal` when `LiveHostIDs()` is empty ([pkg/universe/cmdsys_resolver.go:40-48](pkg/universe/cmdsys_resolver.go#L40-L48)). So in `--mode=all` with no `TestHosts`, the worker runs in-process — no special case in our code.

**Pure-coordinator mode:** today `registerPerfBuiltins` is skipped when the coord has no local cells. After this plan, the handler always registers; in pure-coordinator mode, `perf.snapshot` fans out across registered remote hosts and the frontend formats the result. `FormatPerfOutput` drops its dependence on `*Engine` (see Task 2).

**Host-local console:** pure `--mode=host` processes run the same `Coordinator.startConsole` path — their console gets `perf` registered and can dispatch locally without a coordinator round trip (the host's own resolver falls back to RouteLocal). This is a free win from not gating registration.

---

## Task 1: Define structured PerfCellSnapshot wire type

**Files:**
- Create: `pkg/universe/perf_snapshot.go`
- Test: `pkg/universe/perf_snapshot_test.go`

- [ ] **Step 1: Write failing test for buildPerfCellSnapshot**

Create `pkg/universe/perf_snapshot_test.go`:

```go
package universe

import (
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/engine"
)

func TestBuildPerfCellSnapshotPopulatesAllFields(t *testing.T) {
	// Construct a cell with a TickProfile that has one recorded sample.
	eng := &engine.Engine{
		Config: engine.Config{TickRate: 20},
		Perf:   engine.NewTickProfile([]string{"SystemA", "SystemB"}),
	}
	eng.Perf.Record(
		[]time.Duration{5 * time.Millisecond, 3 * time.Millisecond},
		8*time.Millisecond,
	)
	cell := &Cell{
		ID:     "0_0",
		Engine: eng,
	}

	snap := buildPerfCellSnapshot(cell, "host-a")

	if snap.HostID != "host-a" {
		t.Errorf("HostID = %q, want host-a", snap.HostID)
	}
	if snap.CellID != "0_0" {
		t.Errorf("CellID = %q, want 0_0", snap.CellID)
	}
	if snap.TickHz != 20 {
		t.Errorf("TickHz = %d, want 20", snap.TickHz)
	}
	if snap.Tick.SampleCount != 1 {
		t.Errorf("SampleCount = %d, want 1", snap.Tick.SampleCount)
	}
	if snap.Tick.Avg != 8*time.Millisecond {
		t.Errorf("Tick.Avg = %v, want 8ms", snap.Tick.Avg)
	}
	if len(snap.Systems) != 2 {
		t.Fatalf("Systems len = %d, want 2", len(snap.Systems))
	}
	if snap.Systems[0].Name != "SystemA" || snap.Systems[0].Avg != 5*time.Millisecond {
		t.Errorf("Systems[0] = %+v", snap.Systems[0])
	}
}

func TestBuildPerfCellSnapshotNilMetricsTolerated(t *testing.T) {
	// Cell without Metrics should not panic; Entities/Network/Load zeroed.
	eng := &engine.Engine{
		Config: engine.Config{TickRate: 20},
		Perf:   engine.NewTickProfile(nil),
	}
	cell := &Cell{ID: "0_0", Engine: eng, Metrics: nil}

	snap := buildPerfCellSnapshot(cell, "host-a")

	if snap.Entities.Real != 0 || snap.Network.Connections != 0 {
		t.Errorf("expected zero values, got %+v / %+v", snap.Entities, snap.Network)
	}
}
```

- [ ] **Step 2: Run test, verify failure**

Run: `go test ./pkg/universe/ -run TestBuildPerfCellSnapshot -count=1`
Expected: FAIL with `undefined: buildPerfCellSnapshot` and `undefined: PerfCellSnapshot`.

- [ ] **Step 3: Create `pkg/universe/perf_snapshot.go`**

```go
package universe

import (
	"time"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/metrics"
)

// PerfCellSnapshot is the wire format returned by perf.snapshot for a single
// cell. Every field is JSON-serializable so the dispatcher can ship it across
// MeshControl when the worker runs on a remote host.
type PerfCellSnapshot struct {
	HostID   string
	CellID   string
	TickHz   int
	BudgetMS int
	Tick     TickTimingStats
	Systems  []SystemTiming
	Entities metrics.EntitySnapshot
	Network  metrics.NetworkSnapshot
	Load     float64
	// OverbudgetPct is fraction of ticks that exceeded the budget (0..1).
	OverbudgetPct float64
	// EffectiveHz is the measured sustainable tick rate.
	EffectiveHz float64
}

// TickTimingStats is a JSON-friendly copy of engine.TimingStats for the
// whole-tick bucket, with SampleCount so the caller can detect empty
// profiles (newly reset).
type TickTimingStats struct {
	SampleCount int
	Latest      time.Duration
	Avg         time.Duration
	P50         time.Duration
	P95         time.Duration
	P99         time.Duration
	Max         time.Duration
}

// SystemTiming is a JSON-friendly per-system timing pair.
type SystemTiming struct {
	Name string
	Avg  time.Duration
	P95  time.Duration
}

// buildPerfCellSnapshot reads live state from a cell and returns a PerfCellSnapshot.
// Must run on the cell's game-loop goroutine (caller's responsibility — use
// engine.RunOnLoop). Tolerates nil Metrics; all read access to Engine.Perf is
// required, so Engine and Engine.Perf must be non-nil.
func buildPerfCellSnapshot(cell *Cell, hostID string) PerfCellSnapshot {
	eng := cell.Engine
	stats := eng.Perf.Stats()

	out := PerfCellSnapshot{
		HostID:   hostID,
		CellID:   cell.ID,
		TickHz:   eng.Config.TickRate,
		BudgetMS: 1000 / eng.Config.TickRate,
		Tick: TickTimingStats{
			SampleCount: stats.SampleCount,
			Latest:      stats.Total.Latest,
			Avg:         stats.Total.Avg,
			P50:         stats.Total.P50,
			P95:         stats.Total.P95,
			P99:         stats.Total.P99,
			Max:         stats.Total.Max,
		},
	}
	out.Systems = make([]SystemTiming, len(stats.Systems))
	for i, s := range stats.Systems {
		out.Systems[i] = SystemTiming{
			Name: stats.SystemNames[i],
			Avg:  s.Avg,
			P95:  s.P95,
		}
	}
	if cell.Metrics != nil {
		snap := cell.Metrics.Snapshot()
		out.Entities = snap.Entities
		out.Network = snap.Network
		out.Load = snap.CompositeLoad
		out.OverbudgetPct = snap.Tick.OverbudgetPct
		out.EffectiveHz = snap.Tick.EffectiveHz
	}
	return out
}

// Compile-time check that engine package is imported (used by the test too).
var _ = engine.NewTickProfile
```

- [ ] **Step 4: Run test, verify pass**

Run: `go test ./pkg/universe/ -run TestBuildPerfCellSnapshot -count=1`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/perf_snapshot.go pkg/universe/perf_snapshot_test.go
git commit -m "$(cat <<'EOF'
feat(perf): add PerfCellSnapshot wire type

First step toward distributed perf — defines the JSON-friendly
structured type that workers will return from perf.snapshot.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Decouple FormatPerfOutput from *Engine

**Files:**
- Modify: `pkg/engine/console.go:505-542` — rename `FormatPerfOutput` → keep signature as a thin shim; extract the real logic into a new `FormatPerfSnapshotText` that takes a shared structured input.
- Test: `pkg/engine/console_test.go` (new or extend)

We cannot introduce a dependency from `pkg/engine` on `pkg/universe` (layering rule — `pkg/engine` is lower). So the formatter takes a minimal struct defined in `pkg/engine`, and `pkg/universe.PerfCellSnapshot` provides an adapter method.

- [ ] **Step 1: Write failing test**

Create or extend `pkg/engine/console_test.go`:

```go
package engine

import (
	"strings"
	"testing"
	"time"
)

func TestFormatPerfSnapshotTextContainsKeyLabels(t *testing.T) {
	snap := PerfSnapshotText{
		TickHz:   20,
		BudgetMS: 50,
		Tick: TimingStats{
			Avg: 12 * time.Millisecond,
			P50: 11 * time.Millisecond,
			P95: 15 * time.Millisecond,
			P99: 18 * time.Millisecond,
			Max: 23 * time.Millisecond,
		},
		SystemNames:   []string{"Phys"},
		SystemTimings: []TimingStats{{Avg: 3 * time.Millisecond, P95: 4 * time.Millisecond}},
		EntitiesReal:  100,
		Connections:   5,
	}

	out := FormatPerfSnapshotText(snap)

	for _, want := range []string{"Tick (20Hz, budget 50ms):", "Phys", "100 real", "5 conns"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- full output ---\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run test, verify failure**

Run: `go test ./pkg/engine/ -run TestFormatPerfSnapshotText -count=1`
Expected: FAIL — `undefined: PerfSnapshotText` / `undefined: FormatPerfSnapshotText`.

- [ ] **Step 3: Add `PerfSnapshotText` + `FormatPerfSnapshotText` to `pkg/engine/console.go`**

Add above the existing `FormatPerfOutput` (replace lines 504-542 with):

```go
// PerfSnapshotText is the minimal input needed to render the human-readable
// perf block. It is populated by the universe layer from a PerfCellSnapshot;
// engine/console.go does no structural ECS or cell lookups itself.
type PerfSnapshotText struct {
	TickHz        int
	BudgetMS      int
	Tick          TimingStats
	SystemNames   []string
	SystemTimings []TimingStats
	EntitiesReal     uint64
	EntitiesReplica  uint64
	EntitiesGhost    uint64
	EntitiesConnected uint64
	Connections      uint64
	BytesSent        uint64
	BytesRecv        uint64
	CompositeLoad    float64
	OverbudgetPct    float64
	EffectiveHz      float64
}

// FormatPerfSnapshotText renders a PerfSnapshotText as the indented console block.
// Pure function — safe to call from any goroutine.
func FormatPerfSnapshotText(s PerfSnapshotText) string {
	var b strings.Builder
	budgetMs := s.BudgetMS
	if budgetMs == 0 && s.TickHz > 0 {
		budgetMs = 1000 / s.TickHz
	}
	fmt.Fprintf(&b, "  Tick (%dHz, budget %dms):\n", s.TickHz, budgetMs)
	fmt.Fprintf(&b, "    avg %s  p50 %s  p95 %s  p99 %s  max %s\n",
		fmtDur(s.Tick.Avg), fmtDur(s.Tick.P50), fmtDur(s.Tick.P95), fmtDur(s.Tick.P99), fmtDur(s.Tick.Max))

	if len(s.SystemTimings) > 0 {
		fmt.Fprintf(&b, "  Systems:\n")
		for i, sys := range s.SystemTimings {
			name := ""
			if i < len(s.SystemNames) {
				name = s.SystemNames[i]
			}
			fmt.Fprintf(&b, "    %-20s avg %s  p95 %s\n", name, fmtDur(sys.Avg), fmtDur(sys.P95))
		}
	}

	total := s.EntitiesReal + s.EntitiesReplica + s.EntitiesGhost
	fmt.Fprintf(&b, "  Entities: %d real, %d replica, %d ghost (%d total), %d connected\n",
		s.EntitiesReal, s.EntitiesReplica, s.EntitiesGhost, total, s.EntitiesConnected)
	fmt.Fprintf(&b, "  Network: %d conns, sent %s, recv %s\n",
		s.Connections, fmtBytes(s.BytesSent), fmtBytes(s.BytesRecv))
	fmt.Fprintf(&b, "  Load: %.2f", s.CompositeLoad)
	if s.OverbudgetPct > 0 {
		fmt.Fprintf(&b, "  overbudget: %.1f%%", s.OverbudgetPct*100)
	}
	if s.EffectiveHz > 0 {
		fmt.Fprintf(&b, "  capacity: %.0fHz", s.EffectiveHz)
	}
	fmt.Fprintln(&b)

	return b.String()
}

// FormatPerfOutput is the legacy single-engine formatter. Kept as a thin
// adapter so existing callers continue to work; delegates to
// FormatPerfSnapshotText. Must be called from the game loop goroutine.
func FormatPerfOutput(eng *Engine) string {
	stats := eng.Perf.Stats()
	text := PerfSnapshotText{
		TickHz:        eng.Config.TickRate,
		Tick:          stats.Total,
		SystemNames:   stats.SystemNames,
		SystemTimings: stats.Systems,
	}
	if eng.Metrics != nil {
		snap := eng.Metrics.Snapshot()
		text.EntitiesReal = snap.Entities.Real
		text.EntitiesReplica = snap.Entities.Replica
		text.EntitiesGhost = snap.Entities.Ghost
		text.EntitiesConnected = snap.Entities.Connected
		text.Connections = snap.Network.Connections
		text.BytesSent = snap.Network.BytesSent
		text.BytesRecv = snap.Network.BytesRecv
		text.CompositeLoad = snap.CompositeLoad
		text.OverbudgetPct = snap.Tick.OverbudgetPct
		text.EffectiveHz = snap.Tick.EffectiveHz
	}
	return FormatPerfSnapshotText(text)
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `go test ./pkg/engine/ -run TestFormatPerfSnapshotText -count=1`
Expected: PASS.

Also run: `go vet ./...` and `go test ./pkg/engine/... -count=1`
Expected: no regressions.

- [ ] **Step 5: Commit**

```bash
git add pkg/engine/console.go pkg/engine/console_test.go
git commit -m "$(cat <<'EOF'
refactor(engine): extract FormatPerfSnapshotText from FormatPerfOutput

Lets the universe layer render perf blocks from PerfCellSnapshot data
that arrives over the wire (not from a local *Engine). Legacy
FormatPerfOutput becomes a thin adapter.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Add PerfCellSnapshot → PerfSnapshotText adapter

**Files:**
- Modify: `pkg/universe/perf_snapshot.go` — add method `(s PerfCellSnapshot) toText() engine.PerfSnapshotText`.
- Test: `pkg/universe/perf_snapshot_test.go`

- [ ] **Step 1: Write failing test**

Append to `pkg/universe/perf_snapshot_test.go`:

```go
func TestPerfCellSnapshotToText(t *testing.T) {
	snap := PerfCellSnapshot{
		TickHz:   20,
		BudgetMS: 50,
		Tick: TickTimingStats{
			SampleCount: 1,
			Avg:         10 * time.Millisecond,
			P95:         15 * time.Millisecond,
		},
		Systems: []SystemTiming{
			{Name: "Phys", Avg: 3 * time.Millisecond, P95: 4 * time.Millisecond},
		},
	}
	snap.Entities.Real = 42

	text := snap.toText()

	if text.TickHz != 20 || text.BudgetMS != 50 {
		t.Errorf("tick mapping wrong: %+v", text)
	}
	if len(text.SystemNames) != 1 || text.SystemNames[0] != "Phys" {
		t.Errorf("systems not copied: %+v", text.SystemNames)
	}
	if text.EntitiesReal != 42 {
		t.Errorf("entities not copied: %d", text.EntitiesReal)
	}
}
```

- [ ] **Step 2: Run test, verify failure**

Run: `go test ./pkg/universe/ -run TestPerfCellSnapshotToText -count=1`
Expected: FAIL — `snap.toText undefined`.

- [ ] **Step 3: Implement adapter in `pkg/universe/perf_snapshot.go`**

Append to the file:

```go
// toText converts a PerfCellSnapshot (wire format) into the minimal text-render
// input that engine.FormatPerfSnapshotText consumes.
func (s PerfCellSnapshot) toText() engine.PerfSnapshotText {
	text := engine.PerfSnapshotText{
		TickHz:   s.TickHz,
		BudgetMS: s.BudgetMS,
		Tick: engine.TimingStats{
			Latest: s.Tick.Latest,
			Avg:    s.Tick.Avg,
			P50:    s.Tick.P50,
			P95:    s.Tick.P95,
			P99:    s.Tick.P99,
			Max:    s.Tick.Max,
		},
		EntitiesReal:      s.Entities.Real,
		EntitiesReplica:   s.Entities.Replica,
		EntitiesGhost:     s.Entities.Ghost,
		EntitiesConnected: s.Entities.Connected,
		Connections:       s.Network.Connections,
		BytesSent:         s.Network.BytesSent,
		BytesRecv:         s.Network.BytesRecv,
		CompositeLoad:     s.Load,
		OverbudgetPct:     s.OverbudgetPct,
		EffectiveHz:       s.EffectiveHz,
	}
	text.SystemNames = make([]string, len(s.Systems))
	text.SystemTimings = make([]engine.TimingStats, len(s.Systems))
	for i, st := range s.Systems {
		text.SystemNames[i] = st.Name
		text.SystemTimings[i] = engine.TimingStats{Avg: st.Avg, P95: st.P95}
	}
	return text
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `go test ./pkg/universe/ -run TestPerfCellSnapshotToText -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/perf_snapshot.go pkg/universe/perf_snapshot_test.go
git commit -m "$(cat <<'EOF'
feat(perf): add PerfCellSnapshot.toText adapter to engine renderer

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Register perf.snapshot worker verb (RouteAllHosts)

**Files:**
- Modify: `pkg/universe/builtins_perf.go` — add new registration; leave the old `perf` registration in place for now (will be rewritten in Task 6).
- Test: `pkg/universe/builtins_perf_test.go`

- [ ] **Step 1: Write failing test for perf.snapshot handler**

Create `pkg/universe/builtins_perf_test.go`:

```go
package universe

import (
	"context"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/engine"
)

// A minimal in-memory coordinator wrapper for handler-level tests.
// Uses the same construction pattern as the existing cell test doubles.
func newTestCoordWithCell(t *testing.T, cellID, hostID string) *Coordinator {
	t.Helper()
	c := &Coordinator{
		Cells: map[CellID]*Cell{},
		Hosts: map[string]*Host{},
	}
	id, err := ParseCellID(cellID)
	if err != nil {
		t.Fatalf("ParseCellID: %v", err)
	}
	eng := &engine.Engine{
		Config: engine.Config{TickRate: 20},
		Perf:   engine.NewTickProfile([]string{"S1"}),
	}
	eng.Perf.Record([]time.Duration{3 * time.Millisecond}, 7*time.Millisecond)
	cell := &Cell{ID: cellID, Engine: eng}
	c.Cells[id] = cell

	host := &Host{Cells: []*Cell{cell}}
	c.Hosts[hostID] = host
	return c
}

func TestPerfSnapshotHandlerReturnsOneRowPerCell(t *testing.T) {
	coord := newTestCoordWithCell(t, "0_0", "host-a")
	reg := cmdsys.NewRegistry()
	if err := registerPerfSnapshotWorker(reg, coord); err != nil {
		t.Fatalf("register: %v", err)
	}
	cmd, ok := reg.Lookup("perf.snapshot")
	if !ok {
		t.Fatal("perf.snapshot not registered")
	}
	if cmd.Route != cmdsys.RouteAllHosts {
		t.Errorf("Route = %v, want RouteAllHosts", cmd.Route)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := cmd.Handler(ctx, &cmdsys.Env{}, perfSnapshotArgs{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out, ok := res.(perfSnapshotResult)
	if !ok {
		t.Fatalf("result type = %T, want perfSnapshotResult", res)
	}
	if len(out.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(out.Rows))
	}
	if out.Rows[0].CellID != "0_0" || out.Rows[0].HostID != "host-a" {
		t.Errorf("row = %+v", out.Rows[0])
	}
	if out.Rows[0].Tick.SampleCount != 1 {
		t.Errorf("SampleCount = %d, want 1", out.Rows[0].Tick.SampleCount)
	}
}
```

NOTE: The test expects a helper `registerPerfSnapshotWorker(reg, coord) error`. If `Host.Cells` doesn't exist as a `[]*Cell`, use whatever the existing field is (verify in `pkg/universe/host.go`) — the test's job is to construct a Coordinator where the handler can find the owning host for a cell. Adjust the helper accordingly before submitting.

- [ ] **Step 2: Run test, verify failure**

Run: `go test ./pkg/universe/ -run TestPerfSnapshotHandler -count=1`
Expected: FAIL — `registerPerfSnapshotWorker undefined` / `perfSnapshotArgs undefined`.

- [ ] **Step 3: Implement `registerPerfSnapshotWorker` in `pkg/universe/builtins_perf.go`**

Add new types and registration (keep existing `registerPerfBuiltins` untouched for now):

```go
// ── perf.snapshot (internal fan-out verb) ────────────────────────────────────

type perfSnapshotArgs struct {
	// Optional filter — only return this cell's snapshot. Empty = all local cells.
	CellID string `cmd:"optional,help=restrict to this cell ID,complete=cells"`
}

type perfSnapshotResult struct {
	Rows []PerfCellSnapshot
}

// registerPerfSnapshotWorker registers perf.snapshot with RouteAllHosts.
// Each host's dispatcher runs the handler locally and returns its cells' data.
// Identical in spirit to cell.snapshot (see builtins_cell.go).
func registerPerfSnapshotWorker(reg *cmdsys.Registry, coord *Coordinator) error {
	return reg.Register(cmdsys.Command{
		Verb:        "perf.snapshot",
		Capability:  "perf",
		Description: "realtime per-cell perf data from this host (internal; fans out via `perf`)",
		Route:       cmdsys.RouteAllHosts,
		Args:        perfSnapshotArgs{},
		Result:      perfSnapshotResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(perfSnapshotArgs)
			coord.mu.RLock()
			cells := make([]*Cell, 0, len(coord.Cells))
			cellHost := map[*Cell]string{}
			for _, cell := range coord.Cells {
				if args.CellID != "" && cell.ID != args.CellID {
					continue
				}
				cells = append(cells, cell)
			}
			for id, h := range coord.Hosts {
				for _, hc := range h.Cells {
					cellHost[hc] = id
				}
			}
			coord.mu.RUnlock()

			rows := make([]PerfCellSnapshot, 0, len(cells))
			for _, cell := range cells {
				if cell.Engine == nil || cell.Engine.Perf == nil {
					continue
				}
				var snap PerfCellSnapshot
				err := cell.Engine.RunOnLoop(ctx, func() error {
					snap = buildPerfCellSnapshot(cell, cellHost[cell])
					return nil
				})
				if err != nil {
					return nil, err
				}
				rows = append(rows, snap)
			}
			return perfSnapshotResult{Rows: rows}, nil
		},
	})
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `go test ./pkg/universe/ -run TestPerfSnapshotHandler -count=1`
Expected: PASS. If `Host.Cells` is not a `[]*Cell` field, adjust both the test's `newTestCoordWithCell` and the handler's host-lookup loop to match the actual Host shape (see [pkg/universe/host.go](pkg/universe/host.go)).

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_perf.go pkg/universe/builtins_perf_test.go
git commit -m "$(cat <<'EOF'
feat(perf): add perf.snapshot worker verb (RouteAllHosts)

Each host runs the handler locally and returns structured PerfCellSnapshot
rows for its cells. Will be driven by the top-level `perf` frontend.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Register perf.reset worker verb (RouteAllHosts)

**Files:**
- Modify: `pkg/universe/builtins_perf.go` — add `registerPerfResetWorker`.
- Test: `pkg/universe/builtins_perf_test.go`

- [ ] **Step 1: Write failing test**

Append to `pkg/universe/builtins_perf_test.go`:

```go
func TestPerfResetHandlerClearsTickProfile(t *testing.T) {
	coord := newTestCoordWithCell(t, "0_0", "host-a")
	// Precondition: one sample exists.
	for _, cell := range coord.Cells {
		if cell.Engine.Perf.Stats().SampleCount != 1 {
			t.Fatalf("precondition failed: SampleCount != 1")
		}
	}

	reg := cmdsys.NewRegistry()
	if err := registerPerfResetWorker(reg, coord); err != nil {
		t.Fatalf("register: %v", err)
	}
	cmd, _ := reg.Lookup("perf.reset")
	if cmd.Route != cmdsys.RouteAllHosts {
		t.Errorf("Route = %v, want RouteAllHosts", cmd.Route)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := cmd.Handler(ctx, &cmdsys.Env{}, perfResetArgs{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out, ok := res.(perfResetResult)
	if !ok {
		t.Fatalf("result type = %T", res)
	}
	if out.CellsReset != 1 {
		t.Errorf("CellsReset = %d, want 1", out.CellsReset)
	}
	for _, cell := range coord.Cells {
		if cell.Engine.Perf.Stats().SampleCount != 0 {
			t.Errorf("postcondition failed: SampleCount = %d, want 0",
				cell.Engine.Perf.Stats().SampleCount)
		}
	}
}
```

- [ ] **Step 2: Run test, verify failure**

Run: `go test ./pkg/universe/ -run TestPerfResetHandler -count=1`
Expected: FAIL — undefined types.

- [ ] **Step 3: Implement `registerPerfResetWorker`**

Append to `pkg/universe/builtins_perf.go`:

```go
// ── perf.reset (internal fan-out verb) ───────────────────────────────────────

type perfResetArgs struct {
	// Optional filter — only reset this cell. Empty = all local cells.
	CellID string `cmd:"optional,help=restrict to this cell ID,complete=cells"`
}

type perfResetResult struct {
	CellsReset int
}

func registerPerfResetWorker(reg *cmdsys.Registry, coord *Coordinator) error {
	return reg.Register(cmdsys.Command{
		Verb:        "perf.reset",
		Capability:  "perf",
		Description: "reset perf counters on this host's cells (internal; fans out via `perf reset`)",
		Route:       cmdsys.RouteAllHosts,
		Args:        perfResetArgs{},
		Result:      perfResetResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(perfResetArgs)
			coord.mu.RLock()
			cells := make([]*Cell, 0, len(coord.Cells))
			for _, cell := range coord.Cells {
				if args.CellID != "" && cell.ID != args.CellID {
					continue
				}
				cells = append(cells, cell)
			}
			coord.mu.RUnlock()

			count := 0
			for _, cell := range cells {
				if cell.Engine == nil || cell.Engine.Perf == nil {
					continue
				}
				err := cell.Engine.RunOnLoop(ctx, func() error {
					cell.Engine.Perf.Reset()
					return nil
				})
				if err != nil {
					return nil, err
				}
				count++
			}
			return perfResetResult{CellsReset: count}, nil
		},
	})
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `go test ./pkg/universe/ -run TestPerfResetHandler -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_perf.go pkg/universe/builtins_perf_test.go
git commit -m "$(cat <<'EOF'
feat(perf): add perf.reset worker verb (RouteAllHosts)

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Replace the top-level perf handler with the distributed frontend

**Files:**
- Modify: `pkg/universe/builtins_perf.go` — rewrite `registerPerfBuiltins` to register the new frontend `perf` verb and call the two worker registrations. Keep the `load` command untouched (it moves in Task 8).
- Modify: `pkg/universe/coordinator.go` (line ~1290) — call `registerPerfBuiltins(coord.registry, coord.dispatcher, coord)` without the `defaultEng != nil` gate.
- Test: `pkg/universe/builtins_perf_test.go`

### Behavior of the new `perf` frontend (RouteLocal)

```
Args struct:
  Sub    string (positional, optional)  -- "reset" or ""
  HostID string (flag --host, optional, completes from hosts)
  CellID string (flag --cell, optional, completes from cells)

Dispatch rules:
  • Sub == "reset" → invoke perf.reset via InvokeInternal
                     (same --host / --cell filters apply).
  • Sub == ""      → invoke perf.snapshot via InvokeInternal.
  • When HostID != "" and CellID != "" → synthesize inner args with both fields;
    dispatcher resolves RouteAllHosts, but because we pre-filter the inner
    targets to exactly the named host, only that host runs.  This is simpler
    and more uniform than swapping routes at the frontend.
  • When HostID != "" and CellID == "" → target one host, no CellID filter.
  • When HostID == "" and CellID != "" → fan out to all hosts; the CellID
    filter narrows each host's result to zero-or-one rows.
  • When HostID == "" and CellID == "" → pure fan-out.

Rendering:
  • For `perf` (no reset): aggregate mode (multi-row) renders a summary line
    per cell + a "TOTAL: N cells, M entities" footer.  Single-row mode
    renders the full per-cell block via PerfCellSnapshot.toText() →
    engine.FormatPerfSnapshotText.
  • For `perf reset`: prints "  perf counters reset: <N> cells across <H> hosts".
  • Errors from individual targets are summarized ("host-x: <err>") and do
    not fail the overall command unless all targets errored.
```

**How to pin a single host for RouteAllHosts:** the dispatcher ranges over `targets`. The simplest way to target exactly one host is to use `RouteSpecificHost` — but the current meshRouteResolver requires the command's `Route` to be `RouteSpecificHost` for that path, and our worker is `RouteAllHosts`. Rather than duplicate the worker, the frontend will call `dispatcher.InvokeInternal` with the standard args and then filter the `PerTarget` slice after the call, dropping rows from non-matching hosts. This is cheap (N host payloads are small) and keeps the worker generic.

- [ ] **Step 1: Write failing test (host + cell filter semantics)**

Append to `pkg/universe/builtins_perf_test.go`:

```go
import (
	"strings"
)

func TestPerfFrontendFiltersByHostAndCell(t *testing.T) {
	// Two hosts, three cells total.
	coord := newTestCoordWithCell(t, "0_0", "host-a")
	addCellToCoord(t, coord, "0_1", "host-a")
	addCellToCoord(t, coord, "1_0", "host-b")

	reg := cmdsys.NewRegistry()
	if err := registerPerfSnapshotWorker(reg, coord); err != nil {
		t.Fatalf("register snapshot: %v", err)
	}
	if err := registerPerfResetWorker(reg, coord); err != nil {
		t.Fatalf("register reset: %v", err)
	}
	disp := newTestDispatcher(t, reg, coord) // see helper below
	if err := registerPerfFrontend(reg, disp, coord); err != nil {
		t.Fatalf("register frontend: %v", err)
	}
	cmd, _ := reg.Lookup("perf")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := cmd.Handler(ctx, &cmdsys.Env{}, perfArgs{HostID: "host-a"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := res.(perfResult).Output
	if !strings.Contains(out, "0_0") || !strings.Contains(out, "0_1") {
		t.Errorf("output should include host-a cells, got:\n%s", out)
	}
	if strings.Contains(out, "1_0") {
		t.Errorf("output should not include 1_0 (on host-b), got:\n%s", out)
	}
}
```

`addCellToCoord` and `newTestDispatcher` are helpers that must live at the bottom of the test file. Sample implementations:

```go
func addCellToCoord(t *testing.T, coord *Coordinator, cellID, hostID string) {
	t.Helper()
	id, err := ParseCellID(cellID)
	if err != nil { t.Fatalf("ParseCellID: %v", err) }
	eng := &engine.Engine{
		Config: engine.Config{TickRate: 20},
		Perf:   engine.NewTickProfile([]string{"S1"}),
	}
	eng.Perf.Record([]time.Duration{2*time.Millisecond}, 5*time.Millisecond)
	cell := &Cell{ID: cellID, Engine: eng}
	coord.Cells[id] = cell
	host, ok := coord.Hosts[hostID]
	if !ok {
		host = &Host{}
		coord.Hosts[hostID] = host
	}
	host.Cells = append(host.Cells, cell)
}

// newTestDispatcher wires a Registry and in-process resolver + transport.
// Use the same helpers tests in pkg/cmdsys/dispatcher_test.go use; if none
// exists, construct with:
//   disp := cmdsys.NewDispatcher(reg, newMeshRouteResolver(coord),
//                                newLoopbackTransport(reg), /* grants */ nil, logger.Default())
// The loopback transport is cmdsys.NewLoopbackTransport or equivalent —
// check pkg/cmdsys/ for existing helpers; if none, use a local stub that
// calls reg.Lookup + handler.
func newTestDispatcher(t *testing.T, reg *cmdsys.Registry, coord *Coordinator) *cmdsys.Dispatcher {
	t.Helper()
	// Single-process test: RouteAllHosts falls back to local when
	// LiveHostIDs() is empty (coord.hostRegistry is nil here).
	return cmdsys.NewDispatcher(reg, newMeshRouteResolver(coord), nil, nil, nil)
}
```

The test may need adjustment to match whatever NewDispatcher signature exists at the time of implementation — see [pkg/cmdsys/dispatcher.go](pkg/cmdsys/dispatcher.go). If the real Dispatcher requires a non-nil transport, use the existing test helper (search for existing usages of `NewDispatcher` in test files: `grep -n 'cmdsys.NewDispatcher' $(go list -f '{{.Dir}}/*_test.go' ./...)`).

- [ ] **Step 2: Run test, verify failure**

Run: `go test ./pkg/universe/ -run TestPerfFrontend -count=1`
Expected: FAIL — `perfArgs` / `registerPerfFrontend` undefined.

- [ ] **Step 3: Rewrite `registerPerfBuiltins` in `pkg/universe/builtins_perf.go`**

Replace the current `registerPerfBuiltins` (lines 28-95) with:

```go
// ── perf (frontend) ──────────────────────────────────────────────────────────

type perfArgs struct {
	Sub    string `cmd:"optional,help=subcommand: reset"`
	HostID string `cmd:"optional,name=host,help=target host ID,complete=hosts"`
	CellID string `cmd:"optional,name=cell,help=target cell ID,complete=cells"`
}

type perfResult struct {
	Output string
}

// registerPerfFrontend registers the user-facing `perf` verb.  It dispatches
// through InvokeInternal to perf.snapshot (or perf.reset), post-filters by
// HostID, and renders the aggregated rows as text.
func registerPerfFrontend(reg *cmdsys.Registry, disp *cmdsys.Dispatcher, coord *Coordinator) error {
	return reg.Register(cmdsys.Command{
		Verb:        "perf",
		Capability:  "perf",
		Description: "show tick timing, entities, network, load (fans out to hosts)",
		Route:       cmdsys.RouteLocal,
		Args:        perfArgs{},
		Result:      perfResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(perfArgs)

			if args.Sub == "reset" {
				innerArgs := perfResetArgs{CellID: args.CellID}
				inner, err := disp.InvokeInternal(ctx, env, "perf.reset", innerArgs)
				if err != nil {
					return nil, fmt.Errorf("perf reset: %w", err)
				}
				total, hosts := 0, 0
				var errs []string
				for _, tr := range inner.PerTarget {
					if args.HostID != "" && tr.TargetID != "local" && tr.TargetID != args.HostID {
						continue
					}
					if !tr.OK {
						errs = append(errs, fmt.Sprintf("%s: %s", tr.TargetID, tr.Error))
						continue
					}
					r, _ := tr.Result.(perfResetResult)
					total += r.CellsReset
					hosts++
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "  perf counters reset: %d cells across %d host(s)\n", total, hosts)
				for _, e := range errs {
					fmt.Fprintf(&sb, "  error: %s\n", e)
				}
				return perfResult{Output: sb.String()}, nil
			}

			innerArgs := perfSnapshotArgs{CellID: args.CellID}
			inner, err := disp.InvokeInternal(ctx, env, "perf.snapshot", innerArgs)
			if err != nil {
				return nil, fmt.Errorf("perf: %w", err)
			}

			var rows []PerfCellSnapshot
			var errs []string
			for _, tr := range inner.PerTarget {
				if args.HostID != "" && tr.TargetID != "local" && tr.TargetID != args.HostID {
					continue
				}
				if !tr.OK {
					errs = append(errs, fmt.Sprintf("%s: %s", tr.TargetID, tr.Error))
					continue
				}
				r, ok := tr.Result.(perfSnapshotResult)
				if !ok {
					continue
				}
				for _, row := range r.Rows {
					if row.HostID == "" {
						row.HostID = tr.TargetID
					}
					rows = append(rows, row)
				}
			}

			return perfResult{Output: renderPerfRows(rows, errs)}, nil
		},
	})
}

// renderPerfRows picks detail vs. aggregate formatting based on row count.
// Detail mode (single row): full per-system + entities/network block.
// Aggregate mode: one summary line per row + a total footer.
func renderPerfRows(rows []PerfCellSnapshot, errs []string) string {
	var sb strings.Builder
	if len(rows) == 0 {
		sb.WriteString("  no cells reporting\n")
	} else if len(rows) == 1 {
		r := rows[0]
		fmt.Fprintf(&sb, "  Host: %s  Cell: %s\n", r.HostID, r.CellID)
		sb.WriteString(engine.FormatPerfSnapshotText(r.toText()))
	} else {
		fmt.Fprintf(&sb, "  %-14s %-8s %7s %7s %9s %5s\n",
			"HOST", "CELL", "avg", "p95", "entities", "load")
		var totalEntities uint64
		for _, r := range rows {
			fmt.Fprintf(&sb, "  %-14s %-8s %7s %7s %9d %5.2f\n",
				r.HostID, r.CellID,
				fmtDurShort(r.Tick.Avg), fmtDurShort(r.Tick.P95),
				r.Entities.Real, r.Load)
			totalEntities += r.Entities.Real
		}
		fmt.Fprintf(&sb, "  TOTAL: %d cells, %d entities\n", len(rows), totalEntities)
	}
	for _, e := range errs {
		fmt.Fprintf(&sb, "  error: %s\n", e)
	}
	return sb.String()
}

// fmtDurShort renders a duration as `12.3ms` (always ms, 1 decimal) for tables.
func fmtDurShort(d time.Duration) string {
	return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
}

// registerPerfBuiltins registers perf, perf.snapshot, perf.reset.
// Always registers — even in pure-coordinator mode or when the coord owns no
// local cells. RouteAllHosts fans out; if the resolver returns no remote
// hosts it falls back to local execution.
func registerPerfBuiltins(reg *cmdsys.Registry, disp *cmdsys.Dispatcher, coord *Coordinator) error {
	if err := registerPerfSnapshotWorker(reg, coord); err != nil {
		return fmt.Errorf("registerPerfBuiltins snapshot: %w", err)
	}
	if err := registerPerfResetWorker(reg, coord); err != nil {
		return fmt.Errorf("registerPerfBuiltins reset: %w", err)
	}
	if err := registerPerfFrontend(reg, disp, coord); err != nil {
		return fmt.Errorf("registerPerfBuiltins frontend: %w", err)
	}
	return nil
}
```

Remove the unused `time` import if the load command (which uses it) has been extracted to another file, but since Task 8 extracts `load` later, keep the imports stable: add `strings` if not present; keep `engine` and `time`.

- [ ] **Step 4: Run test, verify pass**

Run: `go test ./pkg/universe/ -run TestPerfFrontend -count=1`
Expected: PASS. If the test fails because `cmdsys.NewDispatcher` signature doesn't match the stub, adjust the helper — see `go doc pkg/cmdsys.NewDispatcher`.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_perf.go pkg/universe/builtins_perf_test.go
git commit -m "$(cat <<'EOF'
feat(perf): rewrite `perf` as a distributed frontend verb

Dispatches to perf.snapshot / perf.reset via InvokeInternal, post-filters
by --host, and renders aggregate or detail view.  Supports --cell for
drill-down.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Update coordinator wiring to always register perf

**Files:**
- Modify: `pkg/universe/coordinator.go` — find `startConsole` (near line 1290) and remove the `defaultEng != nil` gate around `registerPerfBuiltins`. Change the call signature to take the coord + dispatcher instead of an engine.

- [ ] **Step 1: Read the current wiring**

Run: `grep -n 'registerPerfBuiltins\|defaultEng' pkg/universe/coordinator.go`
Expected: one call site inside `startConsole`.

- [ ] **Step 2: Write failing smoke test**

Append to `pkg/universe/builtins_perf_test.go`:

```go
// In pure-coordinator mode (no local cells), `perf` must still be
// registered — even if no remote hosts are connected yet.
func TestPerfFrontendRegistersWhenCoordHasNoCells(t *testing.T) {
	coord := &Coordinator{
		Cells: map[CellID]*Cell{},
		Hosts: map[string]*Host{},
	}
	reg := cmdsys.NewRegistry()
	disp := newTestDispatcher(t, reg, coord)

	if err := registerPerfBuiltins(reg, disp, coord); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := reg.Lookup("perf"); !ok {
		t.Error("perf verb missing")
	}
	if _, ok := reg.Lookup("perf.snapshot"); !ok {
		t.Error("perf.snapshot verb missing")
	}
}
```

Run: `go test ./pkg/universe/ -run TestPerfFrontendRegistersWhenCoordHasNoCells -count=1`
Expected: PASS (this test validates the new `registerPerfBuiltins` signature from Task 6 — it's mainly a regression guard for Task 7's wiring).

- [ ] **Step 3: Update `startConsole` in `pkg/universe/coordinator.go`**

Find the block (roughly lines 1285-1325):

```go
var defaultEng *engine.Engine
for _, node := range c.Cells {
    defaultEng = node.Engine
    break
}
...
if defaultEng != nil {
    if err := registerPerfBuiltins(c.registry, c.console, defaultEng); err != nil {
        log.Printf("coordinator: registerPerfBuiltins: %v", err)
    }
}
```

Replace with:

```go
// perf verbs always register — worker handlers fan out to hosts that do
// have cells; the frontend tolerates zero responding hosts cleanly.
if err := registerPerfBuiltins(c.registry, c.dispatcher, c); err != nil {
    log.Printf("coordinator: registerPerfBuiltins: %v", err)
}
```

The `defaultEng` local variable may be used by other legacy registrations (e.g. `registerLoadBuiltins` if such exists). Keep the variable if it's referenced elsewhere in the same function, just remove the `if defaultEng != nil { registerPerfBuiltins... }` gate. Verify with `grep -n defaultEng pkg/universe/coordinator.go` before deleting.

- [ ] **Step 4: Run full build + tests**

Run: `just build && go test ./pkg/universe/... -count=1 -short`
Expected: binary builds cleanly; all universe tests pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "$(cat <<'EOF'
refactor(universe): always register perf verbs in startConsole

Removes the defaultEng != nil gate — perf now works in pure-coordinator
mode (dispatches to remote hosts) and in mixed modes alike.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Extract `load` command to its own file

**Files:**
- Create: `pkg/universe/builtins_load.go` (new file with `load` command).
- Modify: `pkg/universe/builtins_perf.go` — remove `load` registration.
- Modify: `pkg/universe/coordinator.go` — register load independently (still gated on `defaultEng != nil` for now; can be revisited later).

The old `registerPerfBuiltins` registered both `perf` and `load`. They are unrelated: `load` prints a single-line composite load for the local engine. Extracting it keeps the perf file focused.

- [ ] **Step 1: Create `pkg/universe/builtins_load.go`**

```go
package universe

import (
	"context"
	"fmt"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/engine"
)

type loadArgs struct{}

type loadResult struct {
	Output string
}

func registerLoadBuiltins(reg *cmdsys.Registry, defaultEng *engine.Engine) error {
	return reg.Register(cmdsys.Command{
		Verb:        "load",
		Capability:  "load",
		Description: "show composite load score (local engine)",
		Route:       cmdsys.RouteLocal,
		Args:        loadArgs{},
		Result:      loadResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			var output string
			err := defaultEng.RunOnLoop(ctx, func() error {
				if defaultEng.Metrics == nil {
					output = "  metrics not wired\n"
					return nil
				}
				snap := defaultEng.Metrics.Snapshot()
				tickBudget := time.Duration(1000/defaultEng.Config.TickRate) * time.Millisecond
				output = fmt.Sprintf("  load: %.2f (tick=%.1f%% entity=%.1f%%)\n",
					snap.CompositeLoad,
					float64(snap.Tick.AvgDuration)/float64(tickBudget)*100,
					float64(snap.Entities.Real)/1000.0*100,
				)
				return nil
			})
			if err != nil {
				return nil, err
			}
			return loadResult{Output: output}, nil
		},
	})
}
```

- [ ] **Step 2: Remove `load` from `pkg/universe/builtins_perf.go`**

Delete the existing `loadArgs` / `loadResult` types and the `reg.Register(cmdsys.Command{Verb: "load", ...})` block.

- [ ] **Step 3: Update `startConsole` in `pkg/universe/coordinator.go`**

Where you previously had a `defaultEng != nil` block (now gutted for perf), add:

```go
if defaultEng != nil {
    if err := registerLoadBuiltins(c.registry, defaultEng); err != nil {
        log.Printf("coordinator: registerLoadBuiltins: %v", err)
    }
}
```

Note: `load` remains a local-engine-only command for now. Making it distributed would be a follow-up (it's barely used and largely duplicates what `perf` shows).

- [ ] **Step 4: Run build + tests**

Run: `just build && go test ./pkg/universe/... -count=1 -short`
Expected: everything passes; `load` still works (unchanged behavior).

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_load.go pkg/universe/builtins_perf.go pkg/universe/coordinator.go
git commit -m "$(cat <<'EOF'
refactor(universe): split `load` command into its own builtins file

Keeps perf focused on distributed tick profiling.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: End-to-end distributed test (coordinator → remote host)

**Files:**
- Create: `pkg/universe/s7_perf_test.go` — spin up coord + two in-process hosts via `TestHosts`, record a few ticks, invoke `perf` at the coordinator, assert each host's cells appear in the output.

The `s7_*_test.go` family in `pkg/universe/` already uses `TestHosts` to spin up multi-host setups over gRPC loopback. Model this test on [pkg/universe/s7_split_test.go](pkg/universe/s7_split_test.go) — specifically the test harness setup.

- [ ] **Step 1: Draft the test scaffolding**

Create `pkg/universe/s7_perf_test.go`:

```go
package universe

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// TestPerfDistributedFanOut verifies that `perf` at the coordinator returns
// snapshots from every registered remote host.
func TestPerfDistributedFanOut(t *testing.T) {
	// Use the same harness as s7_split_test.go — see its setup* helpers.
	// Construct: 1 coord + 2 in-process hosts via Config.TestHosts; 4 cells
	// distributed across them.
	h := newS7Harness(t, s7HarnessOpts{TestHosts: 2, CellsX: 2, CellsY: 2})
	defer h.Shutdown()

	// Let the game loop accumulate a few samples.
	time.Sleep(250 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	caller := cmdsys.Caller{ID: "test", Source: "test",
		Grants: []cmdsys.Grant{{Capability: cmdsys.CapabilityAll}}}
	res, err := h.coord.Dispatcher().Invoke(ctx, caller, "perf", perfArgs{})
	if err != nil {
		t.Fatalf("perf: %v", err)
	}
	if len(res.PerTarget) != 1 || !res.PerTarget[0].OK {
		t.Fatalf("perf frontend failed: %+v", res.PerTarget)
	}
	out := res.PerTarget[0].Result.(perfResult).Output

	// Both hosts should have contributed rows.
	for _, host := range h.HostIDs() {
		if !strings.Contains(out, host) {
			t.Errorf("output missing host %q\n--- output ---\n%s", host, out)
		}
	}

	// Now drill into one specific host.
	target := h.HostIDs()[0]
	res2, err := h.coord.Dispatcher().Invoke(ctx, caller, "perf",
		perfArgs{HostID: target})
	if err != nil {
		t.Fatalf("perf --host %s: %v", target, err)
	}
	out2 := res2.PerTarget[0].Result.(perfResult).Output
	if strings.Contains(out2, h.HostIDs()[1]) {
		t.Errorf("output included non-target host %q\n%s", h.HostIDs()[1], out2)
	}
}
```

The helpers `newS7Harness`, `h.HostIDs()`, `h.coord.Dispatcher()` may need slight adjustment or creation — **check [pkg/universe/s7_split_test.go](pkg/universe/s7_split_test.go) for the actual harness name and methods**. If none of those exist verbatim, use whatever setup the existing S7 tests use. Grant constants: use whatever grant-all constant the test files already import (search for `cmdsys.CapabilityAll` or a local test-caller helper).

- [ ] **Step 2: Read the existing S7 harness to align names**

```bash
grep -n 'func newS7\|func setup\|harness\|TestHosts' pkg/universe/s7_*_test.go | head -50
```

Update the test to use the actual harness.

- [ ] **Step 3: Run test, watch it fail or pass**

Run: `go test ./pkg/universe/ -run TestPerfDistributedFanOut -count=1 -v`
Expected: PASS if the harness matches (see Step 2). If it fails due to timing (no samples recorded yet), increase the `time.Sleep` — a handful of ticks is plenty.

- [ ] **Step 4: Iterate until green**

If the test fails, read the error and either fix the harness usage or fix a real bug in the worker/frontend. Do not commit until green.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/s7_perf_test.go
git commit -m "$(cat <<'EOF'
test(perf): end-to-end distributed perf via TestHosts

Exercises coord → remote host MeshControl dispatch of perf.snapshot,
confirms both --host drilldown and default fan-out.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Manual verification in 4node-basic

**Files:**
- No code changes; manual smoke test.

- [ ] **Step 1: Start 4node-basic with TestHosts**

```bash
cd examples/4node-basic && just build
./bin/4node-basic --mode=all   # default — uses TestHosts internally if set in world.go
```

- [ ] **Step 2: Exercise `perf` commands in the console**

From the interactive prompt:
```
perf
perf --host <one of the host IDs printed by `host list`>
perf --cell 0_0
perf --host <id> --cell 0_0
perf reset
perf     # should show SampleCount coming back up over the next few seconds
```

- [ ] **Step 3: Start split-process mode (coordinator + remote host + gateway)**

Three terminals:
```bash
# T1
./bin/4node-basic --mode=coordinator --control-listen=:9100
# T2
./bin/4node-basic --mode=host --coordinator-addr=localhost:9100 --host-id=remoteA
# T3
./bin/4node-basic --mode=gateway --coordinator-addr=localhost:9100
```

At the coordinator console, run `perf`. Expected: rows for `remoteA` appear. Run `perf --host remoteA` to drill in. SSH into the `--mode=host` process's console (if it has one) and run `perf` locally — verify it shows only that host's cells.

- [ ] **Step 4: Commit a CHANGELOG note (optional)**

Skip if the repo has no CHANGELOG. If the manual test surfaces issues, open tasks for each.

---

## Self-Review

Before handing off, I reread the plan:

1. **Spec coverage check:**
   - ✓ `perf` on host shows aggregate → default (no args) fans out; on a pure-host process the fan-out resolver falls back to local so it shows just this host's cells.
   - ✓ `perf --cell <id>` on host shows specific cell → CellID filter applied in worker.
   - ✓ `perf` on coordinator shows per-host data → fan-out returns rows tagged by HostID, frontend renders them.
   - ✓ `perf --host <id>` → post-filtered to that host's rows.
   - ✓ `perf --host <id> --cell <id>` → host filter + cell filter combined.
   - ✓ "all this should just run on the targeted host when running from coordinator" → data collection runs in each host's `buildPerfCellSnapshot` on its game-loop goroutine; the coordinator only aggregates/renders.
   - ✓ `perf reset` distributed → `perf.reset` worker resets each host's local cells.

2. **Placeholder scan:**
   - Task 4/Step 1 calls out the `Host.Cells` field by name and tells the implementer to verify and adjust in `pkg/universe/host.go`. This is a concrete instruction, not a placeholder.
   - Task 6/Step 1 explicitly says to adjust `newTestDispatcher` to match whatever `cmdsys.NewDispatcher` signature exists — this is unavoidable without reading dispatcher_test.go at plan-write time, but the plan points directly at the file and command needed.
   - Task 9 references the S7 harness by convention and tells the implementer to grep-to-confirm. Acceptable given the harness already exists.
   - No "TODO"/"fix later"/"add error handling" placeholders.

3. **Type consistency:**
   - `PerfCellSnapshot` ↔ `buildPerfCellSnapshot` ↔ `perfSnapshotResult.Rows []PerfCellSnapshot` ↔ `.toText()` — all match.
   - `perfSnapshotArgs{CellID}` ↔ `perfResetArgs{CellID}` ↔ `perfArgs{Sub, HostID, CellID}` — consistent field names.
   - `registerPerfBuiltins(reg, disp, coord)` — same signature in Tasks 6, 7, and the wiring call site.
   - `engine.PerfSnapshotText` — field names line up between Tasks 2 and 3.
   - `engine.TimingStats` is the existing type; reused verbatim in `engine.PerfSnapshotText` and in `PerfCellSnapshot.toText()`.

Plan complete and saved to `docs/superpowers/plans/2026-04-16-distributed-perf-command.md`.

Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
