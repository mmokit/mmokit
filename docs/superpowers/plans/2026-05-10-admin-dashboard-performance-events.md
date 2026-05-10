# Admin Dashboard — Performance + Events Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the two diagnose-tab panels — `/performance` (live sparklines per cell + per-system tick profile drilldown) and `/events` (live commit-log tail with scenario/cell/step filters) — driven by existing SSE topics and a new wired-through `perf.snapshot` accessor.

**Architecture:** Backend unblocks `LocalClusterView.Perf()` by adding a `Process.PerfSnapshotForCell(ctx, cellID)` accessor that calls the existing `perf.snapshot` cmdsys verb (RouteAllHosts) internally and returns the per-cell `PerfCellSnapshot`. Frontend adds a per-cell metrics ring buffer (60-sample, ~1 min at 1Hz) populated from the `cells` SSE topic, a tiny zero-dep canvas `Sparkline.svelte` component, a `BarChart.svelte` for the per-system drilldown, and two routes (`/performance`, `/events`).

**Tech Stack:** Go (existing `pkg/admin` + `pkg/universe`), Svelte 5 runes, Tailwind v4, Vitest. No new deps.

**Spec:** [`docs/superpowers/specs/2026-05-10-admin-dashboard-design.md`](../specs/2026-05-10-admin-dashboard-design.md) §8 panels #16 (Live metric charts), #17 (Per-system tick profile), #20 (Commit-log tail).

**Prior plans:**
- [`2026-05-10-admin-dashboard-backend-foundation.md`](2026-05-10-admin-dashboard-backend-foundation.md) — `pkg/admin` skeleton + ClusterView + cmdsys wiring.
- [`2026-05-10-admin-dashboard-frontend-cluster.md`](2026-05-10-admin-dashboard-frontend-cluster.md) — Svelte SPA scaffold + Cluster page.
- [`2026-05-10-admin-dashboard-hosts-gateways-players.md`](2026-05-10-admin-dashboard-hosts-gateways-players.md) — Hosts/Gateways/Players panels.

---

## Quick orientation

What already exists, reusable as-is:

- `commitPublisher` (in `pkg/admin/publishers.go`) already publishes every commit-log entry on the `events` topic, with topology subset on `topology` and invariant violations on `alerts`.
- `/admin/api/events?n=N&cell=...&commit=...&since=DUR` is wired via `handleEvents` and returns `[]CommitEvent`.
- `cells` SSE topic already publishes `[]CellInfo` at 4Hz with full per-cell metrics (load, tickP99Us, tickP95Us, entities, bytes).
- `eventsStore` and `alertsStore` already exist in `web-admin/src/lib/stores.svelte.ts`.
- `cmdsys` verb `perf.snapshot` (`pkg/universe/builtins_perf.go`) — `RouteAllHosts`, returns `perfSnapshotResult{Rows []PerfCellSnapshot}`. Each `PerfCellSnapshot` carries `Tick TickTimingStats` (whole-tick p50/p95/p99/etc.) plus `Systems []SystemTiming{Name,Avg,P95}`.
- `Process.dispatcher` is the `*cmdsys.Dispatcher`. `disp.InvokeInternal(ctx, env, "perf.snapshot", args)` is the existing internal-call entry point.
- `pkg/admin/view.go` already declares `PerfSnapshot{CellID, SystemNames, Systems []TimingStats, Total TimingStats, SampleCount}` and `TimingStats{LatestUs, AvgUs, P50Us, P95Us, P99Us, MaxUs}` — the wire shape is settled.
- `pkg/admin/view_local.go::Perf(cellID)` currently returns `ErrUnavailable` — that's the unblock target.
- Route dispatch in `web-admin/src/app.svelte` falls through to a "not yet implemented" placeholder for `/performance` and `/events`.

What's deliberately out of scope:

- **Charting library.** All charts are zero-dep canvas — a minimal `Sparkline.svelte` (line) and `BarChart.svelte` (horizontal bars). No chart.js / uplot / d3.
- **Server-side metric history.** The frontend keeps a 60-sample ring per cell from the live SSE; old data on page reload starts fresh. A historical metrics endpoint is Phase 2+.
- **Per-system drilldown navigation.** v1 expands inline within `/performance`; no separate detail route.
- **Event filtering on the server.** The existing `/admin/api/events` already filters by cell/commit/since; the dashboard re-applies its scenario/step filter client-side over the live SSE stream.

Build/test commands stay the same:

- Backend: `go test ./pkg/admin/... ./pkg/universe/...`, `go vet ./...`
- Frontend: `cd web-admin && bun run test`, `bun run typecheck`, `bun run build`
- e2e: build the binary + 4node-basic, log in to `localhost:9101/admin/`, click `/performance` and `/events`.

---

## File structure

**Backend additions:**

```text
pkg/universe/
└── admin_accessors.go             # MODIFY — add PerfSnapshotForCell

pkg/admin/
├── view_local.go                  # MODIFY — Perf() calls PerfSnapshotForCell
└── view_local_test.go             # MODIFY — error-path test for Perf
```

**Frontend additions:**

```text
web-admin/src/
├── lib/
│   ├── stores.svelte.ts           # MODIFY — add metricsHistoryStore (per-cell ring buffer)
│   └── types.ts                   # MODIFY — add PerfSnapshot / TimingStats / MetricsSample types
├── components/
│   ├── Sparkline.svelte           # NEW — canvas line chart
│   ├── Sparkline.test.ts          # NEW
│   ├── BarChart.svelte            # NEW — canvas horizontal bar chart
│   └── BarChart.test.ts           # NEW
├── routes/
│   ├── performance.svelte         # NEW
│   └── events.svelte              # NEW
└── app.svelte                     # MODIFY — route /performance and /events
```

---

### Task 1: Backend — `Process.PerfSnapshotForCell`

**Files:**
- Modify: `pkg/universe/admin_accessors.go`

- [ ] **Step 1: Read the existing perf wiring**

```bash
grep -n "perfSnapshotArgs\|perfSnapshotResult\|PerfCellSnapshot\b" pkg/universe/builtins_perf.go pkg/universe/perf_snapshot.go | head -10
```

You should see:
- `perfSnapshotArgs{CellID string}` — the input (empty = all cells; setting CellID filters server-side)
- `perfSnapshotResult{Rows []PerfCellSnapshot}` — the output
- `PerfCellSnapshot{HostID, CellID, TickHz, BudgetMS, Tick TickTimingStats, Systems []SystemTiming, Entities, Network, Load, OverbudgetPct, EffectiveHz}`

The verb is `RouteAllHosts` — it fans out to every host and merges results. We just want the row matching `cellID`. There may be zero rows (cell not on any reachable host) or one (the host that owns it).

- [ ] **Step 2: Find an existing internal-invoke usage as a template**

```bash
grep -n "InvokeInternal\|\"perf.snapshot\"" pkg/universe/builtins_perf.go | head -10
```

Around `pkg/universe/builtins_perf.go:276` you'll see the `perf` frontend verb's handler call `disp.InvokeInternal(ctx, env, "perf.snapshot", innerArgs)`. The `env` parameter carries the Caller; for the admin dashboard we'll synthesize an internal Caller.

- [ ] **Step 3: Add the accessor**

Append to `pkg/universe/admin_accessors.go`:

```go
// PerfSnapshotForCell returns live tick profiling data for one cell. Routes
// through the existing perf.snapshot cmdsys verb (RouteAllHosts), which
// fans out to every host and returns the matching cell's snapshot —
// transparent over distributed mode.
//
// Returns (PerfCellSnapshot{}, false) when the cell is not currently owned
// by any reachable host. Errors from the dispatcher (e.g. context cancel)
// surface as the error return.
//
// Used by pkg/admin's LocalClusterView.Perf(). Caller must supply a context;
// 2-second timeout is recommended since the call fans out over MeshControl
// in distributed deployments.
func (c *Process) PerfSnapshotForCell(ctx context.Context, cellID string) (PerfCellSnapshot, bool, error) {
	if c.dispatcher == nil {
		return PerfCellSnapshot{}, false, errors.New("perf snapshot: no dispatcher")
	}
	env := &cmdsys.Env{
		Caller: cmdsys.Caller{
			Source:   cmdsys.SourceAdminHTTP,
			Identity: "admin",
			Grants:   []cmdsys.Grant{{Capability: "perf"}},
		},
	}
	raw, err := c.dispatcher.InvokeInternal(ctx, env, "perf.snapshot", perfSnapshotArgs{CellID: cellID})
	if err != nil {
		return PerfCellSnapshot{}, false, err
	}
	res, ok := raw.(perfSnapshotResult)
	if !ok {
		return PerfCellSnapshot{}, false, errors.New("perf snapshot: unexpected result type")
	}
	for _, row := range res.Rows {
		if row.CellID == cellID {
			return row, true, nil
		}
	}
	return PerfCellSnapshot{}, false, nil
}
```

If the `cmdsys.Caller` shape differs (some projects use `Username` not `Identity`, or carry source as a typed enum), grep for an existing internal-call site:

```bash
grep -n "cmdsys.Caller{" pkg/universe/*.go | head -5
```

and mirror the construction. If `cmdsys.SourceAdminHTTP` isn't the constant name, list available source enum values:

```bash
grep -n "type CallerSource\|Source[A-Z][a-zA-Z]*\s*CallerSource" pkg/cmdsys/*.go | head -5
```

Pick the source value that means "internal admin caller" (alternatives: `SourceTest`, `SourceMeshControl`, `SourceConsole`).

The new file imports needed: `context`, `errors`, and `github.com/zenion/mmoserver/pkg/cmdsys`. Add them to the existing import block.

- [ ] **Step 4: Verify compile**

```bash
cd .
go vet ./pkg/universe/...
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/admin_accessors.go
git commit -m "$(cat <<'EOF'
universe: PerfSnapshotForCell — typed perf accessor for pkg/admin

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Backend — wire `LocalClusterView.Perf` to the new accessor

**Files:**
- Modify: `pkg/admin/view_local.go`

- [ ] **Step 1: Read the existing stub**

```bash
grep -n "func.*Perf\b" pkg/admin/view_local.go
```

You'll see the current implementation returns `ErrUnavailable` unconditionally.

- [ ] **Step 2: Replace the stub**

Find the `Perf` method in `pkg/admin/view_local.go` and replace its body with:

```go
// Perf returns per-cell tick profiling data via the perf.snapshot cmdsys
// verb. Returns ErrCellNotFound when the cell isn't currently owned by any
// reachable host (e.g. mid-split, host crashed, unknown ID).
func (v *LocalClusterView) Perf(cellID string) (PerfSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	row, ok, err := v.p.PerfSnapshotForCell(ctx, cellID)
	if err != nil {
		return PerfSnapshot{}, err
	}
	if !ok {
		return PerfSnapshot{}, ErrCellNotFound
	}
	out := PerfSnapshot{
		CellID:      row.CellID,
		SampleCount: row.Tick.SampleCount,
		SystemNames: make([]string, len(row.Systems)),
		Systems:     make([]TimingStats, len(row.Systems)),
		Total: TimingStats{
			LatestUs: row.Tick.Latest.Microseconds(),
			AvgUs:    row.Tick.Avg.Microseconds(),
			P50Us:    row.Tick.P50.Microseconds(),
			P95Us:    row.Tick.P95.Microseconds(),
			P99Us:    row.Tick.P99.Microseconds(),
			MaxUs:    row.Tick.Max.Microseconds(),
		},
	}
	for i, s := range row.Systems {
		out.SystemNames[i] = s.Name
		out.Systems[i] = TimingStats{
			AvgUs: s.Avg.Microseconds(),
			P95Us: s.P95.Microseconds(),
		}
	}
	return out, nil
}
```

Add `"context"` and `"time"` to the import block of `view_local.go` if not already present.

- [ ] **Step 3: Verify the existing handler still works**

`pkg/admin/api_read.go::handlePerf` already maps `ErrCellNotFound` to 404 and `ErrUnavailable` to a best-effort 200. The new path returns `ErrCellNotFound` when the cell isn't owned, so 404 is correct. No change needed in the handler.

- [ ] **Step 4: Run vet + tests**

```bash
go vet ./pkg/admin/...
go test ./pkg/admin/...
```

Expected: clean / pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/admin/view_local.go
git commit -m "$(cat <<'EOF'
admin: LocalClusterView.Perf wired to perf.snapshot

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Backend — fixture test for `LocalClusterView.Perf`

**Files:**
- Modify: `pkg/admin/view_local_test.go`

- [ ] **Step 1: Add a test that exercises both paths**

Append to `pkg/admin/view_local_test.go`:

```go
func TestLocalClusterView_Perf_NotFound(t *testing.T) {
	t.Parallel()
	v := NewLocalClusterView(newTestProcessForView(t))
	_, err := v.Perf("cell_99_99")
	if err != ErrCellNotFound {
		t.Fatalf("expected ErrCellNotFound, got %v", err)
	}
}

func TestLocalClusterView_Perf_HappyPath(t *testing.T) {
	t.Parallel()
	v := NewLocalClusterView(newTestProcessForView(t))
	// The 2x2 fixture creates cells cell_0_0..cell_1_1; pick one and assert
	// we get a snapshot back (SampleCount may be 0 in a never-ticked fixture
	// but the call must not error).
	got, err := v.Perf("cell_0_0")
	if err != nil {
		t.Fatalf("Perf(cell_0_0): %v", err)
	}
	if got.CellID != "cell_0_0" {
		t.Fatalf("CellID = %q, want cell_0_0", got.CellID)
	}
}
```

If the fixture's cell IDs don't match `cell_0_0` exactly, adjust to one that exists. Find with:

```bash
grep -n "newTestProcessForView" pkg/admin/*_test.go
```

and read its body to see what cells it spawns.

- [ ] **Step 2: Run + commit**

```bash
go test ./pkg/admin/ -run "TestLocalClusterView_Perf" -v
```

Expected: 2 passing.

```bash
git add pkg/admin/view_local_test.go
git commit -m "$(cat <<'EOF'
admin: tests for LocalClusterView.Perf (not-found + happy path)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Frontend — types for perf + metrics history

**Files:**
- Modify: `web-admin/src/lib/types.ts`

- [ ] **Step 1: Read the existing file**

```bash
cat web-admin/src/lib/types.ts | head -80
```

Confirm `CellInfo`, `CommitEvent`, etc. are present.

- [ ] **Step 2: Append the new types**

Append to `web-admin/src/lib/types.ts`:

```ts
// PerfSnapshot mirrors pkg/admin/view.go::PerfSnapshot — the on-demand
// drilldown payload returned by GET /admin/api/perf/<cellID>.
export type PerfSnapshot = {
  cellId: string;
  systemNames: string[];
  systems: TimingStats[];
  total: TimingStats;
  sampleCount: number;
};

export type TimingStats = {
  latestUs: number;
  avgUs: number;
  p50Us: number;
  p95Us: number;
  p99Us: number;
  maxUs: number;
};

// MetricsSample is one tick's worth of dashboard-relevant numbers for one
// cell, captured from the `cells` SSE topic. The performance route keeps a
// rolling ring of these per cell to drive sparklines.
export type MetricsSample = {
  t: number;          // ms epoch when the sample was captured client-side
  load: number;
  tickP99Us: number;
  tickP95Us: number;
  entitiesReal: number;
  bytesSent: number;
  bytesRecv: number;
};
```

- [ ] **Step 3: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/lib/types.ts
git commit -m "$(cat <<'EOF'
web-admin: PerfSnapshot / TimingStats / MetricsSample types

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Frontend — metrics history ring buffer in stores

**Files:**
- Modify: `web-admin/src/lib/stores.svelte.ts`

- [ ] **Step 1: Append a per-cell ring buffer + push helper**

Open `web-admin/src/lib/stores.svelte.ts`. After the existing `Store<T>` class and the `pushAlert` helper, append:

```ts
import type { MetricsSample } from "./types";

// METRICS_HISTORY_LEN is the per-cell sample budget. At ~4Hz from the cells
// SSE topic, 60 samples covers ~15 seconds — enough for a useful sparkline
// without holding huge arrays. A page reload starts fresh.
export const METRICS_HISTORY_LEN = 60;

// metricsHistory is a per-cell ring buffer of MetricsSample. Pushes wrap
// at METRICS_HISTORY_LEN. Reactive: callers can read .value and re-render
// when it changes.
class MetricsHistory {
  #map = $state<Record<string, MetricsSample[]>>({});
  get value(): Record<string, MetricsSample[]> {
    return this.#map;
  }
  push(cellId: string, sample: MetricsSample): void {
    const cur = this.#map[cellId] ?? [];
    const next = cur.length < METRICS_HISTORY_LEN
      ? [...cur, sample]
      : [...cur.slice(1), sample];
    this.#map = { ...this.#map, [cellId]: next };
  }
  clear(): void {
    this.#map = {};
  }
}

export const metricsHistoryStore = new MetricsHistory();
```

- [ ] **Step 2: Typecheck**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

- [ ] **Step 3: Commit**

```bash
cd .
git add web-admin/src/lib/stores.svelte.ts
git commit -m "$(cat <<'EOF'
web-admin: metricsHistoryStore — per-cell rolling sample buffer

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Frontend — `Sparkline` component (TDD)

**Files:**
- Create: `web-admin/src/components/Sparkline.svelte`
- Create: `web-admin/src/components/Sparkline.test.ts`

A zero-dep canvas line chart. Renders a series of numbers as a polyline scaled to fit the canvas, with optional min/max clamps and a single accent color.

- [ ] **Step 1: Write the failing test for the scaling helper**

Create `web-admin/src/components/Sparkline.test.ts`:

```ts
import { describe, it, expect } from "vitest";
import { scaleSeries } from "./Sparkline.helpers";

describe("scaleSeries", () => {
  it("maps min→0 and max→height when no clamps", () => {
    const out = scaleSeries([10, 20, 30], 100, 50);
    // x positions: 0, 50, 100 (3 points across width 100)
    expect(out.map((p) => p.x)).toEqual([0, 50, 100]);
    // y positions: 50 (min, drawn at canvas bottom), 25, 0 (max, top)
    expect(out[0].y).toBeCloseTo(50, 1);
    expect(out[2].y).toBeCloseTo(0, 1);
  });

  it("returns flat midline when all values equal", () => {
    const out = scaleSeries([5, 5, 5], 100, 50);
    for (const p of out) expect(p.y).toBeCloseTo(25, 1);
  });

  it("applies min/max clamps", () => {
    const out = scaleSeries([0, 50, 100], 100, 50, { min: 0, max: 200 });
    // top of the series is at value 100, clamped scale 0..200, so
    // 100/200 = 0.5 of canvas → y = 25
    expect(out[2].y).toBeCloseTo(25, 1);
  });

  it("returns empty for empty input", () => {
    expect(scaleSeries([], 100, 50)).toEqual([]);
  });
});
```

- [ ] **Step 2: Implement the helper**

Create `web-admin/src/components/Sparkline.helpers.ts`:

```ts
export type Point = { x: number; y: number };

export type ScaleOpts = {
  min?: number; // clamp the scale's lower bound (default: data min)
  max?: number; // clamp the scale's upper bound (default: data max)
};

// scaleSeries maps a numeric series onto canvas coordinates. x is spread
// evenly across [0, width]; y maps [scaleMin, scaleMax] to [height, 0]
// (canvas y grows downward, so larger values get lower y).
export function scaleSeries(
  values: readonly number[],
  width: number,
  height: number,
  opts: ScaleOpts = {},
): Point[] {
  const n = values.length;
  if (n === 0) return [];
  let lo = opts.min ?? Math.min(...values);
  let hi = opts.max ?? Math.max(...values);
  if (hi === lo) {
    // Flat: render a midline.
    return values.map((_, i) => ({
      x: n === 1 ? width / 2 : (i * width) / (n - 1),
      y: height / 2,
    }));
  }
  const dx = n === 1 ? width / 2 : width / (n - 1);
  return values.map((v, i) => ({
    x: n === 1 ? width / 2 : i * dx,
    y: height - ((v - lo) / (hi - lo)) * height,
  }));
}
```

- [ ] **Step 3: Run + commit helpers**

```bash
cd web-admin && bun run test src/components/Sparkline.test.ts
```

Expected: 4 passing.

```bash
cd .
git add web-admin/src/components/Sparkline.helpers.ts web-admin/src/components/Sparkline.test.ts
git commit -m "$(cat <<'EOF'
web-admin: Sparkline helpers — scaleSeries with clamp + flat handling

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 4: Implement the Svelte component**

Create `web-admin/src/components/Sparkline.svelte`:

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { scaleSeries } from "./Sparkline.helpers";

  type Props = {
    values: number[];
    width?: number;
    height?: number;
    color?: string;
    /** Optional clamps so unrelated sparklines share the same y-scale. */
    min?: number;
    max?: number;
    /** Optional label drawn in the top-left corner. */
    label?: string;
    /** Optional value drawn in the top-right corner (last sample formatted). */
    valueText?: string;
  };

  let {
    values,
    width = 160,
    height = 36,
    color = "#7dd3fc",
    min,
    max,
    label,
    valueText,
  }: Props = $props();

  let canvas: HTMLCanvasElement | undefined = $state();
  let dpr = 1;

  function draw() {
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    canvas.width = width * dpr;
    canvas.height = height * dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, width, height);

    const pts = scaleSeries(values, width, height, { min, max });
    if (pts.length < 2) return;

    // Faint baseline.
    ctx.strokeStyle = "rgba(255,255,255,0.06)";
    ctx.beginPath();
    ctx.moveTo(0, height - 0.5);
    ctx.lineTo(width, height - 0.5);
    ctx.stroke();

    // Polyline.
    ctx.strokeStyle = color;
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    ctx.moveTo(pts[0].x, pts[0].y);
    for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i].x, pts[i].y);
    ctx.stroke();

    // Last-value dot.
    const last = pts[pts.length - 1];
    ctx.fillStyle = color;
    ctx.beginPath();
    ctx.arc(last.x, last.y, 1.8, 0, Math.PI * 2);
    ctx.fill();
  }

  onMount(() => {
    dpr = window.devicePixelRatio || 1;
    draw();
  });

  $effect(() => {
    // Re-render whenever inputs change.
    void values;
    void width;
    void height;
    void color;
    void min;
    void max;
    draw();
  });
</script>

<div class="relative inline-block" style:width="{width}px" style:height="{height}px">
  <canvas bind:this={canvas} style:width="{width}px" style:height="{height}px"></canvas>
  {#if label}
    <span class="absolute top-0 left-1 text-[9.5px] text-slate-500 leading-none">{label}</span>
  {/if}
  {#if valueText}
    <span class="absolute top-0 right-1 text-[9.5px] font-mono text-slate-300 leading-none">{valueText}</span>
  {/if}
</div>
```

- [ ] **Step 5: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors (3 pre-existing DataTable warnings are fine).

```bash
cd .
git add web-admin/src/components/Sparkline.svelte
git commit -m "$(cat <<'EOF'
web-admin: Sparkline.svelte — zero-dep canvas line chart

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Frontend — `BarChart` component (TDD)

**Files:**
- Create: `web-admin/src/components/BarChart.svelte`
- Create: `web-admin/src/components/BarChart.helpers.ts`
- Create: `web-admin/src/components/BarChart.test.ts`

A horizontal bar chart for the per-system tick profile drilldown — one row per system, bar width proportional to the system's avg tick µs.

- [ ] **Step 1: Write the test for the row-layout helper**

Create `web-admin/src/components/BarChart.test.ts`:

```ts
import { describe, it, expect } from "vitest";
import { layoutBars } from "./BarChart.helpers";

describe("layoutBars", () => {
  it("scales widths to the largest value", () => {
    const out = layoutBars([10, 20, 40], 100);
    expect(out.map((b) => Math.round(b.width))).toEqual([25, 50, 100]);
  });

  it("returns zero widths when all values are zero", () => {
    const out = layoutBars([0, 0, 0], 100);
    for (const b of out) expect(b.width).toBe(0);
  });

  it("handles empty input", () => {
    expect(layoutBars([], 100)).toEqual([]);
  });
});
```

- [ ] **Step 2: Implement the helper**

Create `web-admin/src/components/BarChart.helpers.ts`:

```ts
export type Bar = { value: number; width: number };

// layoutBars returns each value's pixel width scaled so the max value
// fills `maxWidth`. Returns 0 widths when every value is zero.
export function layoutBars(values: readonly number[], maxWidth: number): Bar[] {
  if (values.length === 0) return [];
  const max = values.reduce((a, b) => (b > a ? b : a), 0);
  if (max === 0) return values.map((v) => ({ value: v, width: 0 }));
  return values.map((v) => ({ value: v, width: (v / max) * maxWidth }));
}
```

- [ ] **Step 3: Run + commit helpers**

```bash
cd web-admin && bun run test src/components/BarChart.test.ts
```

Expected: 3 passing.

```bash
cd .
git add web-admin/src/components/BarChart.helpers.ts web-admin/src/components/BarChart.test.ts
git commit -m "$(cat <<'EOF'
web-admin: BarChart helpers — layoutBars scales to max

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 4: Implement the Svelte component**

Create `web-admin/src/components/BarChart.svelte`:

```svelte
<script lang="ts">
  import { layoutBars } from "./BarChart.helpers";

  type Row = { label: string; value: number; valueText?: string };

  type Props = {
    rows: Row[];
    /** Pixel cap for the longest bar. */
    maxWidth?: number;
    /** Bar fill color. */
    color?: string;
  };

  let { rows, maxWidth = 220, color = "#7dd3fc" }: Props = $props();

  let bars = $derived.by(() => layoutBars(rows.map((r) => r.value), maxWidth));
</script>

<div class="space-y-1 text-[11.5px]">
  {#each rows as r, i (r.label)}
    <div class="flex items-center gap-2">
      <div class="w-32 truncate text-slate-400 font-mono">{r.label}</div>
      <div class="grow relative h-3 bg-white/5 rounded-sm overflow-hidden">
        <div
          class="absolute inset-y-0 left-0 rounded-sm"
          style:width="{bars[i]?.width ?? 0}px"
          style:background-color={color}
        ></div>
      </div>
      <div class="w-16 text-right font-mono text-slate-300">
        {r.valueText ?? r.value.toFixed(1)}
      </div>
    </div>
  {/each}
  {#if rows.length === 0}
    <div class="text-slate-500 italic">No samples yet.</div>
  {/if}
</div>
```

- [ ] **Step 5: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/components/BarChart.svelte
git commit -m "$(cat <<'EOF'
web-admin: BarChart.svelte — horizontal bars for per-system perf

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Frontend — `routes/performance.svelte`

**Files:**
- Create: `web-admin/src/routes/performance.svelte`

The performance page shows one row per cell with four sparklines (load, tick p99 µs, real entities, bytes/sec) and a "Profile" button per row. Clicking Profile fetches `/admin/api/perf/<cellId>` and expands an inline drilldown with `BarChart`.

- [ ] **Step 1: Implement**

Create `web-admin/src/routes/performance.svelte`:

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { cellsStore, metricsHistoryStore, METRICS_HISTORY_LEN } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import { fmtBytes, fmtLoad } from "$lib/format";
  import type { CellInfo, MetricsSample, PerfSnapshot } from "$lib/types";
  import Sparkline from "../components/Sparkline.svelte";
  import BarChart from "../components/BarChart.svelte";

  let cells = $derived<CellInfo[]>(cellsStore.value ?? []);
  let history = $derived(metricsHistoryStore.value);

  // Push every cells SSE update into the per-cell ring buffer. The store
  // is shared across routes, so this populates as long as the user is on
  // /performance — when they leave, the effect tears down and we stop
  // collecting (cellsStore itself keeps updating from the global stream).
  $effect(() => {
    const off = stream.subscribe("cells", (data) => {
      const list = data as CellInfo[];
      cellsStore.set(list);
      const t = Date.now();
      for (const c of list) {
        const sample: MetricsSample = {
          t,
          load: c.load,
          tickP99Us: c.tickP99Us,
          tickP95Us: c.tickP95Us,
          entitiesReal: c.entities.real,
          bytesSent: c.bytes.sent,
          bytesRecv: c.bytes.recv,
        };
        metricsHistoryStore.push(c.id, sample);
      }
    });
    return off;
  });

  // One-shot fetch at mount (cells SSE will replace it within a tick).
  onMount(async () => {
    try {
      const initial = await apiGet<CellInfo[]>("/admin/api/cells");
      cellsStore.set(initial);
    } catch {
      // SSE takes over.
    }
  });

  // Per-cell drilldown state — which cell is expanded, plus its perf payload.
  let expandedCell = $state<string | null>(null);
  let expandedPerf = $state<PerfSnapshot | null>(null);
  let drillError = $state("");

  async function toggleProfile(cellId: string) {
    if (expandedCell === cellId) {
      expandedCell = null;
      expandedPerf = null;
      drillError = "";
      return;
    }
    expandedCell = cellId;
    expandedPerf = null;
    drillError = "";
    try {
      expandedPerf = await apiGet<PerfSnapshot>(`/admin/api/perf/${encodeURIComponent(cellId)}`);
    } catch (e) {
      drillError = (e as Error).message;
    }
  }

  function samplesFor(cellId: string): MetricsSample[] {
    return history[cellId] ?? [];
  }
  function loadSeries(cellId: string): number[] {
    return samplesFor(cellId).map((s) => s.load);
  }
  function tickSeries(cellId: string): number[] {
    return samplesFor(cellId).map((s) => s.tickP99Us);
  }
  function entitySeries(cellId: string): number[] {
    return samplesFor(cellId).map((s) => s.entitiesReal);
  }
  function bytesPerSecSeries(cellId: string): number[] {
    // Derive bytes/sec from the diff between consecutive samples. The first
    // sample has no predecessor, so we drop it.
    const xs = samplesFor(cellId);
    if (xs.length < 2) return [];
    const out: number[] = [];
    for (let i = 1; i < xs.length; i++) {
      const dt = (xs[i].t - xs[i - 1].t) / 1000; // seconds
      const db = (xs[i].bytesSent - xs[i - 1].bytesSent) + (xs[i].bytesRecv - xs[i - 1].bytesRecv);
      out.push(dt > 0 ? db / dt : 0);
    }
    return out;
  }
</script>

<main class="p-4 space-y-3">
  <div class="flex items-center justify-between">
    <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Performance</h2>
    <span class="text-[10.5px] text-slate-500">
      live · last {METRICS_HISTORY_LEN} samples per cell
    </span>
  </div>

  <div class="bg-[#0d1117] border border-white/10 rounded-lg p-3 space-y-2">
    {#if cells.length === 0}
      <div class="py-4 text-center text-slate-500 text-[12px]">No cells yet.</div>
    {/if}

    {#each cells as c (c.id)}
      <div class="border-b border-white/5 last:border-b-0 py-2">
        <div class="flex items-center gap-3">
          <div class="w-32 font-mono text-[11.5px] text-slate-200">{c.id}</div>
          <Sparkline
            values={loadSeries(c.id)}
            label="load"
            valueText={fmtLoad(c.load)}
            color="#fbbf24"
            min={0}
            max={1.2}
          />
          <Sparkline
            values={tickSeries(c.id)}
            label="tick p99 µs"
            valueText={`${c.tickP99Us}`}
            color="#a78bfa"
          />
          <Sparkline
            values={entitySeries(c.id)}
            label="entities"
            valueText={`${c.entities.real}`}
            color="#7dd3fc"
            min={0}
          />
          <Sparkline
            values={bytesPerSecSeries(c.id)}
            label="bytes/s"
            valueText={fmtBytes(c.bytes.sent + c.bytes.recv)}
            color="#34d399"
            min={0}
          />
          <button
            class="ml-auto px-2 py-0.5 text-[10.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10"
            onclick={() => toggleProfile(c.id)}
          >
            {expandedCell === c.id ? "hide profile" : "profile"}
          </button>
        </div>

        {#if expandedCell === c.id}
          <div class="mt-2 ml-32 pl-3 border-l border-white/5">
            {#if drillError}
              <div class="text-rose-300 text-[11.5px]">{drillError}</div>
            {:else if !expandedPerf}
              <div class="text-slate-500 text-[11.5px] italic">loading…</div>
            {:else if expandedPerf.systems.length === 0}
              <div class="text-slate-500 text-[11.5px] italic">
                No samples yet (cell may have just reset). Sample count: {expandedPerf.sampleCount}
              </div>
            {:else}
              {@const rows = expandedPerf.systemNames.map((n, i) => ({
                label: n,
                value: expandedPerf!.systems[i].avgUs,
                valueText: `${expandedPerf!.systems[i].avgUs}µs / p95 ${expandedPerf!.systems[i].p95Us}µs`,
              }))}
              <BarChart {rows} color="#a78bfa" />
              <div class="mt-2 text-[10.5px] text-slate-500">
                tick total: avg {expandedPerf.total.avgUs}µs · p95 {expandedPerf.total.p95Us}µs ·
                p99 {expandedPerf.total.p99Us}µs · samples {expandedPerf.sampleCount}
              </div>
            {/if}
          </div>
        {/if}
      </div>
    {/each}
  </div>
</main>
```

- [ ] **Step 2: Verify imports exist**

```bash
grep -n "fmtLoad\|fmtBytes" web-admin/src/lib/format.ts
```

Both should be exported. If `fmtBytes` doesn't exist, add it (see the Hosts route for the same pattern).

- [ ] **Step 3: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/routes/performance.svelte
git commit -m "$(cat <<'EOF'
web-admin: Performance route — per-cell sparklines + perf drilldown

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Frontend — `routes/events.svelte`

**Files:**
- Create: `web-admin/src/routes/events.svelte`

A live-tailing commit-log viewer with three filters: scenario (split/merge/migrate/all), kind (commit-step/invariant-violation/host/all), and free-text cell-id substring. Backend already publishes on the `events` topic; we tail in-memory and apply filters client-side.

- [ ] **Step 1: Implement**

Create `web-admin/src/routes/events.svelte`:

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { eventsStore } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import type { CommitEvent } from "$lib/types";

  // EVENT_RING_LEN bounds the in-memory tail so the page doesn't grow
  // unboundedly during long sessions. The backend's commit log already
  // ring-buffers; this is just the SPA-side cap.
  const EVENT_RING_LEN = 500;

  let events = $derived<CommitEvent[]>(eventsStore.value ?? []);
  let scenarioFilter = $state<"all" | "split" | "merge" | "migrate">("all");
  let kindFilter = $state<"all" | "commit-step" | "invariant-violation" | "host" | "session">("all");
  let cellSearch = $state("");
  let paused = $state(false);

  let filtered = $derived.by(() => {
    const cs = cellSearch.trim().toLowerCase();
    return events.filter((e) => {
      if (scenarioFilter !== "all" && e.scenario !== scenarioFilter) return false;
      if (kindFilter !== "all" && e.kind !== kindFilter) return false;
      if (cs) {
        const hit =
          e.affected?.some((c) => c.toLowerCase().includes(cs)) ||
          e.hostIds?.some((h) => h.toLowerCase().includes(cs));
        if (!hit) return false;
      }
      return true;
    });
  });

  // One-shot tail at mount; SSE keeps it live afterwards.
  onMount(async () => {
    try {
      const initial = await apiGet<CommitEvent[]>("/admin/api/events?n=200");
      eventsStore.set(initial);
    } catch {
      // SSE will populate.
    }
  });

  $effect(() => {
    const off = stream.subscribe("events", (data) => {
      if (paused) return;
      // Backend publishes ONE event at a time. Defensively also handle arrays.
      const incoming: CommitEvent[] = Array.isArray(data) ? (data as CommitEvent[]) : [data as CommitEvent];
      const cur = eventsStore.value ?? [];
      const next = [...incoming, ...cur].slice(0, EVENT_RING_LEN);
      eventsStore.set(next);
    });
    return off;
  });

  function fmtTime(ts: string): string {
    const d = new Date(ts);
    return d.toLocaleTimeString(undefined, { hour12: false }) + "." + String(d.getMilliseconds()).padStart(3, "0");
  }
  function rowClass(e: CommitEvent): string {
    if (e.kind === "invariant-violation") return "bg-rose-900/20";
    if (!e.success) return "bg-amber-900/20";
    return "";
  }
</script>

<main class="p-4 space-y-3">
  <div class="flex items-center justify-between">
    <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Events</h2>
    <div class="flex items-center gap-2 text-[11px]">
      <input
        type="text"
        placeholder="cell or host…"
        class="bg-white/5 border border-white/10 rounded px-2 py-1 text-[12px] text-slate-200 placeholder-slate-500 focus:outline-none w-44"
        bind:value={cellSearch}
      />
      <div class="flex bg-white/5 border border-white/10 rounded overflow-hidden">
        {#each ["all", "split", "merge", "migrate"] as s (s)}
          <button
            class="px-2 py-0.5 {scenarioFilter === s ? 'bg-accent-300/20 text-accent-300' : 'text-slate-400 hover:bg-white/5'}"
            onclick={() => (scenarioFilter = s as typeof scenarioFilter)}
          >{s}</button>
        {/each}
      </div>
      <div class="flex bg-white/5 border border-white/10 rounded overflow-hidden">
        {#each ["all", "commit-step", "invariant-violation", "host"] as k (k)}
          <button
            class="px-2 py-0.5 {kindFilter === k ? 'bg-accent-300/20 text-accent-300' : 'text-slate-400 hover:bg-white/5'}"
            onclick={() => (kindFilter = k as typeof kindFilter)}
          >{k}</button>
        {/each}
      </div>
      <button
        class="px-2 py-0.5 border border-white/10 rounded {paused ? 'bg-amber-500/15 text-amber-300' : 'bg-white/5 text-slate-300'}"
        onclick={() => (paused = !paused)}
      >
        {paused ? "paused" : "pause"}
      </button>
    </div>
  </div>

  <div class="bg-[#0d1117] border border-white/10 rounded-lg overflow-x-auto">
    <table class="w-full text-[11.5px] border-collapse font-mono">
      <thead>
        <tr class="text-left text-[10.5px] uppercase tracking-wide text-slate-500 border-b border-white/10">
          <th class="py-1.5 px-2 font-medium" style="width:130px">Time</th>
          <th class="py-1.5 px-2 font-medium" style="width:90px">Scenario</th>
          <th class="py-1.5 px-2 font-medium" style="width:140px">Kind</th>
          <th class="py-1.5 px-2 font-medium" style="width:240px">Step</th>
          <th class="py-1.5 px-2 font-medium" style="width:60px">Ms</th>
          <th class="py-1.5 px-2 font-medium">Affected</th>
          <th class="py-1.5 px-2 font-medium">Hosts</th>
          <th class="py-1.5 px-2 font-medium">Detail</th>
        </tr>
      </thead>
      <tbody>
        {#each filtered as e (e.seqNo)}
          <tr class="border-b border-white/5 {rowClass(e)}">
            <td class="py-1.5 px-2 text-slate-400">{fmtTime(e.timestamp)}</td>
            <td class="py-1.5 px-2 text-slate-300">{e.scenario || "—"}</td>
            <td class="py-1.5 px-2 text-slate-300">{e.kind}</td>
            <td class="py-1.5 px-2 text-slate-200">{e.step || "—"}</td>
            <td class="py-1.5 px-2 text-right text-slate-400">{e.durationMs}</td>
            <td class="py-1.5 px-2 text-slate-400 truncate" title={(e.affected ?? []).join(", ")}>
              {(e.affected ?? []).join(", ") || "—"}
            </td>
            <td class="py-1.5 px-2 text-slate-400 truncate" title={(e.hostIds ?? []).join(", ")}>
              {(e.hostIds ?? []).join(", ") || "—"}
            </td>
            <td class="py-1.5 px-2 text-rose-300">{e.error || (e.success ? "" : "fail")}</td>
          </tr>
        {:else}
          <tr><td colspan="8" class="py-4 text-center text-slate-500 italic">No events match.</td></tr>
        {/each}
      </tbody>
    </table>
  </div>

  <div class="text-[10.5px] text-slate-500">
    showing {filtered.length} of {events.length} · ring cap {EVENT_RING_LEN}
  </div>
</main>
```

- [ ] **Step 2: Verify the CommitEvent type**

```bash
grep -A 14 "type CommitEvent" web-admin/src/lib/types.ts
```

Confirm fields used above (`seqNo`, `kind`, `scenario`, `step`, `durationMs`, `affected`, `hostIds`, `error`, `success`, `timestamp`) all exist. If `seqNo` isn't on the type, add it (the backend's `mapCommitEvent` does emit `seqNo`):

```ts
export type CommitEvent = {
  commitId: string;
  scenario: string;
  step: string;
  stepIndex: number;
  success: boolean;
  durationMs: number;
  affected?: string[];
  hostIds?: string[];
  error?: string;
  seqNo: number;
  kind: string;
  timestamp: string;
};
```

- [ ] **Step 3: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors. If the typecheck flags missing fields on `CommitEvent`, update `web-admin/src/lib/types.ts` to match the shape above and re-run.

```bash
cd .
git add web-admin/src/routes/events.svelte web-admin/src/lib/types.ts
git commit -m "$(cat <<'EOF'
web-admin: Events route — live commit-log tail with filters

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Frontend — wire `/performance` and `/events` routes in `app.svelte`

**Files:**
- Modify: `web-admin/src/app.svelte`

- [ ] **Step 1: Add the imports**

In `web-admin/src/app.svelte`, find the existing route imports near the top of the script section:

```ts
import Cluster from "./routes/cluster.svelte";
import Hosts from "./routes/hosts.svelte";
import Gateways from "./routes/gateways.svelte";
import Players from "./routes/players.svelte";
```

Append:

```ts
import Performance from "./routes/performance.svelte";
import Events from "./routes/events.svelte";
```

- [ ] **Step 2: Extend the route switch**

Find the existing switch:

```svelte
{:else if path === "/players"}
  <Players />
{:else}
  <div class="p-8 text-slate-500">
```

Insert two new branches before the fallback:

```svelte
{:else if path === "/players"}
  <Players />
{:else if path === "/performance"}
  <Performance />
{:else if path === "/events"}
  <Events />
{:else}
  <div class="p-8 text-slate-500">
```

- [ ] **Step 3: Typecheck + tests + commit**

```bash
cd web-admin && bun run typecheck && bun run test
```

Expected: 0 errors, all tests pass.

```bash
cd .
git add web-admin/src/app.svelte
git commit -m "$(cat <<'EOF'
web-admin: route /performance /events in the app shell

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: e2e smoke (manual)

**Files:**
- (No new files; verification step.)

- [ ] **Step 1: Build everything**

```bash
cd web-admin && bun run build
cd . && go build -o /tmp/4node-test ./examples/4node-basic/
```

- [ ] **Step 2: Run with Postgres**

```bash
just db-up   # if not already running
/tmp/4node-test --admin-listen=:9101 --postgres-url='postgres://mmo:mmo@localhost:5432/mmo_4node?sslmode=disable'
```

(For distributed-mode testing instead: `just distributed`. The Performance route should populate sparklines from cells across all hosts; the Events route should show commits from the coordinator.)

- [ ] **Step 3: Open the dashboard**

Browse to `http://localhost:9101/admin/`, log in `josh` / `localdev`. Click each section:

- **Performance** — within ~1s, each cell row should show four sparklines (load, tick p99, entities, bytes/s) starting to fill in. Click "profile" on a row → drilldown shows a per-system bar chart from `/admin/api/perf/<cellId>`. Click again to collapse.
- **Events** — table populates with recent commits. Trigger one: `cell split cell_0_0` from the server console. The Events table should immediately show the split's plan-step rows. Try filters: `scenario=split`, `kind=invariant-violation`, cell substring `cell_0_0`. The "pause" button should freeze the live tail.

- [ ] **Step 4: Verify distributed-mode parity**

Restart with `just distributed`. Open `localhost:9101/admin/performance` — sparklines should populate using the per-cell metrics that flow from remote hosts via the Heartbeat metrics path. Click "profile" on a remote cell — the bar chart should render real per-system avg/p95 (RouteAllHosts fans the `perf.snapshot` to the owning host).

- [ ] **Step 5: No commit**

This task is verification only. If you find bugs, file them as new tasks.

---

### Task 12: CLAUDE.md update

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Append a paragraph after the existing admin dashboard description**

Find the existing block that documents the admin dashboard panels (added by prior plans — search for "Hosts, Gateways, and Players routes"). Append after it:

```markdown

The Performance route (`/performance`) charts per-cell sparklines (load, tick p99 µs, real entities, bytes/sec) from a 60-sample SPA-side ring buffer fed by the existing `cells` SSE topic; the per-cell drilldown calls `/admin/api/perf/<cellId>`, which routes through the `perf.snapshot` cmdsys verb (`RouteAllHosts`) so it works in distributed mode without per-host wiring. The Events route (`/events`) tails `/admin/api/events` + the `events` SSE topic with client-side filters (scenario / kind / cell-or-host substring) and a pause toggle. Sparklines are zero-dep canvas (`Sparkline.svelte`); the per-system drilldown uses `BarChart.svelte`.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
CLAUDE.md: document Performance + Events panels

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review checklist

- **Spec coverage:** §8 panel #16 (Live metric charts) → Task 8 (sparklines on `/performance`). #17 (Per-system tick profile) → Task 8 (drilldown via `BarChart` + `/admin/api/perf/<cellId>`, unblocked by Tasks 1-2). #20 (Commit-log tail) → Task 9 (`/events` route with filters).
- **Placeholder scan:** All steps contain concrete code or exact commands with expected output; no "TODO" / "implement later" / "similar to Task N" left behind.
- **Type consistency:** TS `PerfSnapshot.systems[i]` is `TimingStats{latestUs, avgUs, p50Us, p95Us, p99Us, maxUs}` — matches Go `pkg/admin/view.go::TimingStats`. `MetricsSample` field names match what `Performance` reads from `cellsStore`. `metricsHistoryStore.value` is `Record<string, MetricsSample[]>`. `EVENT_RING_LEN = 500` is local-only to the events route; the store is unbounded but the route caps it on every push.
- **No new dependencies:** Charts are zero-dep canvas; everything else uses existing project libs.
- **Backend additivity:** `Process.PerfSnapshotForCell` is new; `LocalClusterView.Perf` was a stub returning `ErrUnavailable`, now returns real data — no API breakage. `pkg/admin/view.go` types unchanged.
- **Distributed mode:** Performance sparklines piggyback on the `cells` topic, which already merges remote-cell metrics via the Heartbeat metrics path landed in the prior session. The perf drilldown uses `perf.snapshot` (`RouteAllHosts`) so it transparently fans out.

---

## Execution

Plan complete. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task, review between tasks.
2. **Inline Execution** — execute tasks in this session with checkpoints.

Which approach?
