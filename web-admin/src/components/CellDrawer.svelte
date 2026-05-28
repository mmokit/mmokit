<script lang="ts">
  import type { CellInfo, MetricsSample, PerfSnapshot } from "$lib/types";
  import { fmtBytes, fmtUsAsMs } from "$lib/format";
  import { apiGet, apiPost, ApiError } from "$lib/api";
  import { metricsHistoryStore } from "$lib/stores.svelte";
  import { toDisplay } from "$lib/cellid";
  import { Close, Split, Merge, ArrowRightLeft, Crosshair, Network, Cpu } from "$lib/icons";
  import Sparkline from "./Sparkline.svelte";
  import BarChart from "./BarChart.svelte";

  type Props = {
    cell: CellInfo | null;
    onClose: () => void;
    onAction?: (action: string, ok: boolean, message: string) => void;
  };
  let { cell, onClose, onAction }: Props = $props();

  let busy = $state(false);
  let migratePromptOpen = $state(false);
  let migrateTarget = $state("");

  async function invoke(verb: string, args: Record<string, unknown>) {
    if (!cell || busy) return;
    busy = true;
    try {
      await apiPost(`/admin/api/commands/${verb}`, args);
      onAction?.(verb, true, `${verb} → ${cell.id} ok`);
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message;
      onAction?.(verb, false, msg);
    } finally {
      busy = false;
      migratePromptOpen = false;
      migrateTarget = "";
    }
  }

  // Per-cell history for sparklines.
  let history = $derived<MetricsSample[]>(
    cell ? (metricsHistoryStore.value[cell.id] ?? []) : [],
  );

  // === System profile (per-system tick µs) — auto-polled at 1Hz while a
  // cell is selected. Replaces the standalone /performance route's drilldown.
  //
  // The cell prop is reassigned to a new object every SSE tick (parent does
  // cells.find(...) which returns fresh references), so depending the effect
  // directly on `cell` would re-fire and reset perf=null every tick → BarChart
  // flicker. Derive the id string first: $derived uses === equality, so the
  // downstream effect only re-runs when the cell actually changes.
  let cellId = $derived(cell?.id ?? null);

  let perf = $state<PerfSnapshot | null>(null);
  let perfError = $state("");

  $effect(() => {
    const id = cellId;
    perf = null;
    perfError = "";
    if (!id) return;
    let cancelled = false;
    const fetchOnce = async () => {
      try {
        const res = await apiGet<PerfSnapshot>(
          `/admin/api/perf/${encodeURIComponent(id)}`,
        );
        if (!cancelled) {
          perf = res;
          perfError = "";
        }
      } catch (e) {
        if (!cancelled) {
          perfError = e instanceof ApiError ? e.message : (e as Error).message;
        }
      }
    };
    void fetchOnce();
    const t = window.setInterval(() => void fetchOnce(), 1000);
    return () => {
      cancelled = true;
      window.clearInterval(t);
    };
  });

  // BarChart rows for the per-system breakdown. Bars are scaled to tick
  // total (see <BarChart max={...} />) so each bar reads as "share of CPU
  // time per tick" instead of being autoscaled relative to the slowest
  // system — autoscaling makes a 38µs system look "full" which is misleading.
  let perfTotal = $derived(perf?.total.avgUs ?? 0);
  let perfRows = $derived(
    perf && perf.systems && perf.systemNames
      ? perf.systemNames.map((n, i) => {
          const avg = perf!.systems![i].avgUs;
          const p95 = perf!.systems![i].p95Us;
          const pct = perfTotal > 0 ? (avg / perfTotal) * 100 : 0;
          return {
            label: n,
            value: avg,
            valueText: `${avg}µs · ${pct.toFixed(0)}% · p95 ${p95}`,
          };
        })
      : [],
  );
  let loadSeries = $derived(history.map((h) => h.load));
  let tickSeries = $derived(history.map((h) => h.tickP99Us / 1000)); // ms
  let entSeries = $derived(history.map((h) => h.entitiesReal));
  let bwSeries = $derived(history.map((h) => h.bytesSent + h.bytesRecv));

  // Live bandwidth rate (last two samples) in bytes/sec.
  let bwRate = $derived(estimateRate(history));
  function estimateRate(h: MetricsSample[]): number {
    if (h.length < 2) return 0;
    const a = h[h.length - 2];
    const b = h[h.length - 1];
    const dt = Math.max(0.001, (b.t - a.t) / 1000);
    const db = (b.bytesSent + b.bytesRecv) - (a.bytesSent + a.bytesRecv);
    return Math.max(0, db / dt);
  }

  function fmtRate(bytesPerSec: number): string {
    if (bytesPerSec < 1024) return `${bytesPerSec.toFixed(0)} B/s`;
    if (bytesPerSec < 1024 * 1024) return `${(bytesPerSec / 1024).toFixed(1)} KB/s`;
    return `${(bytesPerSec / 1024 / 1024).toFixed(2)} MB/s`;
  }

  function loadAccent(load: number): { ring: string; text: string; bar: string } {
    if (load >= 0.85) return { ring: "stroke-ember-400", text: "text-ember-300", bar: "bg-ember-400" };
    if (load >= 0.6)  return { ring: "stroke-amber-400", text: "text-amber-300", bar: "bg-amber-400" };
    if (load >= 0.35) return { ring: "stroke-lime-400",  text: "text-lime-300",  bar: "bg-lime-400" };
    return { ring: "stroke-phosphor-400", text: "text-phosphor-300", bar: "bg-phosphor-400" };
  }

  function loadLabel(load: number): string {
    if (load >= 0.85) return "critical";
    if (load >= 0.6) return "warm";
    if (load >= 0.35) return "nominal";
    return "idle";
  }

  // Svelte action — focus on mount. Avoids the `autofocus` a11y warning.
  function autofocusOnMount(node: HTMLInputElement) {
    node.focus();
  }

  // Stable hue per host (same algo as CellMap.hostColor) for the host pill.
  function hostHue(hostId: string): number {
    let h = 2166136261 >>> 0;
    for (let i = 0; i < hostId.length; i++) {
      h = (h ^ hostId.charCodeAt(i)) >>> 0;
      h = Math.imul(h, 16777619) >>> 0;
    }
    h = Math.imul(h, 2654435769) >>> 0;
    return h % 360;
  }

  // Radial load gauge math (SVG circle).
  const GAUGE_R = 30;
  const GAUGE_C = 2 * Math.PI * GAUGE_R;

  // Cell ID display: strips the wire-form "cell_" prefix for the
  // human-readable form (mesh "cell_X_Y" → display "X_Y").
  const prettyId = toDisplay;

  // Entity composition for the breakdown bar — proportional widths.
  let composition = $derived(
    cell
      ? (() => {
          const r = cell.entities.real;
          const rep = cell.entities.replica;
          const g = cell.entities.ghost;
          const total = Math.max(1, r + rep + g);
          return {
            real:    (r / total) * 100,
            replica: (rep / total) * 100,
            ghost:   (g / total) * 100,
          };
        })()
      : null,
  );
</script>

{#if cell}
  {@const accent = loadAccent(cell.load)}
  <aside
    class="w-[340px] shrink-0 border-l border-[var(--border-subtle)] bg-gradient-to-b from-[var(--bg-elevated)] to-[var(--bg-deep)] overflow-y-auto"
    style="animation: page-in 240ms cubic-bezier(0.16, 1, 0.3, 1) both;"
  >
    <!-- Header: cell ID + close -->
    <header class="px-4 pt-4 pb-3 border-b border-[var(--border-faint)] sticky top-0 bg-[var(--bg-elevated)]/95 backdrop-blur z-10">
      <div class="flex items-start justify-between gap-2">
        <div>
          <div class="font-mono text-[9.5px] uppercase tracking-[0.18em] text-[var(--text-dim)] mb-0.5">
            cell · depth {cell.depth}
          </div>
          <div class="font-mono text-[16px] font-semibold text-phosphor-300 tracking-tight leading-none">
            {prettyId(cell.id)}
          </div>
        </div>
        <button
          class="text-[var(--text-muted)] hover:text-[var(--text-bright)] transition-colors -mr-1 -mt-1 p-1"
          aria-label="Close"
          onclick={onClose}
        >
          <Close class="w-4 h-4" />
        </button>
      </div>

      <!-- Host pill (color-coded by host hue) -->
      <div class="mt-2.5 flex items-center gap-2">
        <span
          class="inline-flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-[0.1em] px-2 py-1 rounded border"
          style="
            color: hsl({hostHue(cell.hostId || '?')}, 70%, 70%);
            border-color: hsla({hostHue(cell.hostId || '?')}, 70%, 55%, 0.4);
            background: hsla({hostHue(cell.hostId || '?')}, 70%, 50%, 0.08);
          "
        >
          <span
            class="w-1.5 h-1.5 rounded-full"
            style="background: hsl({hostHue(cell.hostId || '?')}, 75%, 60%)"
          ></span>
          {cell.hostId || "unassigned"}
        </span>
        <span
          class="font-mono text-[10px] uppercase tracking-[0.1em] px-2 py-1 rounded border
            {cell.load >= 0.85 ? 'border-ember-400/40 text-ember-300 bg-ember-400/[0.06]'
              : cell.load >= 0.6 ? 'border-amber-400/40 text-amber-300 bg-amber-400/[0.06]'
              : 'border-lime-400/30 text-lime-300 bg-lime-400/[0.05]'}"
        >
          {loadLabel(cell.load)}
        </span>
      </div>
    </header>

    <!-- Hero: Load gauge + tick stats -->
    <section class="px-4 py-4 flex items-center gap-4 border-b border-[var(--border-faint)]">
      <!-- Radial gauge -->
      <div class="relative shrink-0">
        <svg width="78" height="78" viewBox="0 0 78 78">
          <!-- Track -->
          <circle
            cx="39" cy="39" r={GAUGE_R}
            fill="none" stroke="rgba(148, 163, 189, 0.08)" stroke-width="6"
          />
          <!-- Fill -->
          <circle
            cx="39" cy="39" r={GAUGE_R}
            fill="none" stroke-width="6" stroke-linecap="round"
            class={accent.ring}
            stroke-dasharray={GAUGE_C}
            stroke-dashoffset={GAUGE_C * (1 - Math.min(1, cell.load))}
            transform="rotate(-90 39 39)"
            style="transition: stroke-dashoffset 400ms cubic-bezier(0.16, 1, 0.3, 1);"
          />
          <!-- Ticks at 60% / 85% -->
          {#each [0.6, 0.85] as t (t)}
            {@const angle = (t * 360 - 90) * (Math.PI / 180)}
            <line
              x1={39 + Math.cos(angle) * (GAUGE_R - 6)}
              y1={39 + Math.sin(angle) * (GAUGE_R - 6)}
              x2={39 + Math.cos(angle) * (GAUGE_R + 4)}
              y2={39 + Math.sin(angle) * (GAUGE_R + 4)}
              stroke="rgba(148, 163, 189, 0.4)"
              stroke-width="1"
            />
          {/each}
        </svg>
        <div class="absolute inset-0 flex flex-col items-center justify-center">
          <span class="font-mono text-[20px] font-semibold {accent.text} tabular-nums leading-none">
            {Math.round(cell.load * 100)}
          </span>
          <span class="font-mono text-[9px] text-[var(--text-dim)] uppercase tracking-[0.1em] mt-0.5">
            tick %
          </span>
        </div>
      </div>

      <!-- Tick p99 + p95 -->
      <div class="grow grid grid-cols-2 gap-3">
        <div>
          <div class="stat-label mb-1">tick p99</div>
          <div class="font-mono text-[18px] font-semibold text-[var(--text-bright)] leading-none tabular-nums">
            {fmtUsAsMs(cell.tickP99Us)}
          </div>
          <div class="mt-1.5">
            <Sparkline
              values={tickSeries}
              width={130}
              height={20}
              color={cell.load >= 0.85 ? "#fb7185" : cell.load >= 0.6 ? "#fbbf24" : "#a3e635"}
            />
          </div>
        </div>
        <div>
          <div class="stat-label mb-1">tick p95</div>
          <div class="font-mono text-[18px] font-semibold text-[var(--text-bright)] leading-none tabular-nums">
            {fmtUsAsMs(cell.tickP95Us)}
          </div>
          <div class="mt-1.5">
            <Sparkline
              values={loadSeries.map((v) => v * 100)}
              width={130}
              height={20}
              color="#22d3da"
              min={0}
              max={100}
            />
          </div>
        </div>
      </div>
    </section>

    <!-- System profile — per-system avg µs, auto-polled at 1Hz -->
    <section class="px-4 py-3.5 border-b border-[var(--border-faint)]">
      <div class="flex items-center justify-between mb-2.5">
        <div class="flex items-center gap-1.5">
          <Cpu class="w-3 h-3 text-[var(--text-muted)]" />
          <h3 class="eyebrow">system profile</h3>
        </div>
        {#if perf}
          <span class="font-mono text-[10px] text-[var(--text-muted)] tabular-nums tracking-tight">
            n={perf.sampleCount}
          </span>
        {/if}
      </div>

      {#if perfError}
        <div class="font-mono text-[10.5px] text-ember-300 tracking-tight">
          ✕ {perfError}
        </div>
      {:else if !perf}
        <div class="font-mono text-[10.5px] text-[var(--text-dim)] italic tracking-tight">
          loading…
        </div>
      {:else if !perf.systems || perf.systems.length === 0}
        <div class="font-mono text-[10.5px] text-[var(--text-dim)] italic tracking-tight">
          no samples yet (cell may have just reset)
        </div>
      {:else}
        <BarChart
          rows={perfRows}
          maxWidth={96}
          max={perfTotal}
          color="#22d3da"
          labelWidth={74}
          valueWidth={130}
        />

        <!-- Tick total — secondary stats line -->
        <div class="mt-3 pt-2.5 border-t border-[var(--border-faint)] grid grid-cols-3 gap-2 font-mono text-[10px] tabular-nums">
          <div>
            <div class="text-[var(--text-dim)] uppercase tracking-[0.1em] mb-0.5">tick avg</div>
            <div class="text-[var(--text-default)]">{perf.total.avgUs}µs</div>
          </div>
          <div>
            <div class="text-[var(--text-dim)] uppercase tracking-[0.1em] mb-0.5">p95</div>
            <div class="text-[var(--text-default)]">{perf.total.p95Us}µs</div>
          </div>
          <div>
            <div class="text-[var(--text-dim)] uppercase tracking-[0.1em] mb-0.5">p99</div>
            <div class="text-amber-300">{perf.total.p99Us}µs</div>
          </div>
        </div>
      {/if}
    </section>

    <!-- Entities — composition bar + sparkline -->
    <section class="px-4 py-3.5 border-b border-[var(--border-faint)]">
      <div class="flex items-center justify-between mb-2">
        <div class="flex items-center gap-1.5">
          <Crosshair class="w-3 h-3 text-[var(--text-muted)]" />
          <h3 class="eyebrow">entities</h3>
        </div>
        <span class="font-mono text-[10px] text-[var(--text-muted)] tabular-nums">
          {cell.entities.real + cell.entities.replica + cell.entities.ghost} total
        </span>
      </div>

      <!-- Composition bar (real / replica / ghost stacked horizontally) -->
      {#if composition}
        <div class="flex h-1.5 mb-2 rounded-sm overflow-hidden bg-white/[0.04]">
          {#if composition.real > 0}
            <div class="bg-phosphor-400" style="width: {composition.real}%" title="real"></div>
          {/if}
          {#if composition.replica > 0}
            <div class="bg-plasma-400/80" style="width: {composition.replica}%" title="replica"></div>
          {/if}
          {#if composition.ghost > 0}
            <div class="bg-steel-400/60" style="width: {composition.ghost}%" title="ghost"></div>
          {/if}
        </div>
      {/if}

      <!-- Entity counts grid -->
      <div class="grid grid-cols-4 gap-2">
        <div>
          <div class="flex items-center gap-1 mb-0.5">
            <span class="w-1 h-1 bg-phosphor-400 rounded-full"></span>
            <span class="font-mono text-[9px] uppercase tracking-[0.1em] text-[var(--text-muted)]">real</span>
          </div>
          <div class="font-mono text-[13px] font-semibold text-phosphor-200 tabular-nums">{cell.entities.real}</div>
        </div>
        <div>
          <div class="flex items-center gap-1 mb-0.5">
            <span class="w-1 h-1 bg-plasma-400 rounded-full"></span>
            <span class="font-mono text-[9px] uppercase tracking-[0.1em] text-[var(--text-muted)]">replica</span>
          </div>
          <div class="font-mono text-[13px] font-semibold text-plasma-200 tabular-nums">{cell.entities.replica}</div>
        </div>
        <div>
          <div class="flex items-center gap-1 mb-0.5">
            <span class="w-1 h-1 bg-steel-400 rounded-full"></span>
            <span class="font-mono text-[9px] uppercase tracking-[0.1em] text-[var(--text-muted)]">ghost</span>
          </div>
          <div class="font-mono text-[13px] font-semibold text-steel-300 tabular-nums">{cell.entities.ghost}</div>
        </div>
        <div>
          <div class="flex items-center gap-1 mb-0.5">
            <span class="w-1 h-1 bg-amber-400 rounded-full"></span>
            <span class="font-mono text-[9px] uppercase tracking-[0.1em] text-[var(--text-muted)]">sess</span>
          </div>
          <div class="font-mono text-[13px] font-semibold text-amber-200 tabular-nums">{cell.entities.connected}</div>
        </div>
      </div>

      {#if entSeries.length > 1}
        <div class="mt-2.5">
          <Sparkline
            values={entSeries}
            width={308}
            height={28}
            color="#67e8ee"
            label="real entities · 15s"
            valueText={`${cell.entities.real}`}
          />
        </div>
      {/if}
    </section>

    <!-- Network -->
    <section class="px-4 py-3.5 border-b border-[var(--border-faint)]">
      <div class="flex items-center justify-between mb-2">
        <div class="flex items-center gap-1.5">
          <Network class="w-3 h-3 text-[var(--text-muted)]" />
          <h3 class="eyebrow">network</h3>
        </div>
        <span class="font-mono text-[10px] text-phosphor-300 tabular-nums">
          {fmtRate(bwRate)}
        </span>
      </div>

      <div class="grid grid-cols-2 gap-3">
        <div>
          <div class="stat-label mb-0.5">tx · cum</div>
          <div class="font-mono text-[12.5px] text-[var(--text-bright)] tabular-nums">
            {fmtBytes(cell.bytes.sent)}
          </div>
        </div>
        <div>
          <div class="stat-label mb-0.5">rx · cum</div>
          <div class="font-mono text-[12.5px] text-[var(--text-bright)] tabular-nums">
            {fmtBytes(cell.bytes.recv)}
          </div>
        </div>
      </div>

      {#if bwSeries.length > 1}
        <div class="mt-2.5">
          <Sparkline
            values={bwSeries}
            width={308}
            height={28}
            color="#f472b6"
            label="i/o · 15s"
          />
        </div>
      {/if}
    </section>

    {#if cell.neighbors && cell.neighbors.length > 0}
      <section class="px-4 py-3.5 border-b border-[var(--border-faint)]">
        <h3 class="eyebrow mb-2">neighbors · aoi spillover</h3>
        <div class="flex flex-wrap gap-1">
          {#each cell.neighbors as nb (nb)}
            <span class="font-mono text-[10.5px] px-1.5 py-0.5 rounded bg-[var(--bg-surface)] border border-[var(--border-faint)] text-[var(--text-default)]">
              {prettyId(nb)}
            </span>
          {/each}
        </div>
      </section>
    {/if}

    <!-- Topology actions -->
    <section class="px-4 py-4">
      <h3 class="eyebrow mb-3">topology actions</h3>
      <div class="space-y-2">
        <button
          class="w-full flex items-center gap-2.5 px-3 py-2 rounded border border-[var(--border-subtle)] hover:border-phosphor-400/40 hover:bg-phosphor-400/[0.04] transition-colors text-left disabled:opacity-50 group"
          disabled={busy}
          onclick={() => invoke("cell.split", { CellID: cell!.id })}
        >
          <Split class="w-3.5 h-3.5 text-[var(--text-muted)] group-hover:text-phosphor-300" />
          <div class="grow">
            <div class="font-mono text-[11px] text-[var(--text-bright)] uppercase tracking-[0.08em]">
              split
            </div>
            <div class="font-mono text-[9.5px] text-[var(--text-dim)] tracking-tight">
              quadtree → 4 children at depth {cell.depth + 1}
            </div>
          </div>
        </button>

        <button
          class="w-full flex items-center gap-2.5 px-3 py-2 rounded border border-[var(--border-subtle)] hover:border-phosphor-400/40 hover:bg-phosphor-400/[0.04] transition-colors text-left disabled:opacity-50 group"
          disabled={busy || cell.depth === 0}
          onclick={() => invoke("cell.merge", { CellID: cell!.id })}
          title={cell.depth === 0 ? "Cannot merge a depth-0 cell" : ""}
        >
          <Merge class="w-3.5 h-3.5 text-[var(--text-muted)] group-hover:text-phosphor-300" />
          <div class="grow">
            <div class="font-mono text-[11px] text-[var(--text-bright)] uppercase tracking-[0.08em]">
              merge
            </div>
            <div class="font-mono text-[9.5px] text-[var(--text-dim)] tracking-tight">
              collapse siblings → parent at depth {Math.max(0, cell.depth - 1)}
            </div>
          </div>
        </button>

        {#if !migratePromptOpen}
          <button
            class="w-full flex items-center gap-2.5 px-3 py-2 rounded border border-[var(--border-subtle)] hover:border-phosphor-400/40 hover:bg-phosphor-400/[0.04] transition-colors text-left disabled:opacity-50 group"
            disabled={busy}
            onclick={() => (migratePromptOpen = true)}
          >
            <ArrowRightLeft class="w-3.5 h-3.5 text-[var(--text-muted)] group-hover:text-phosphor-300" />
            <div class="grow">
              <div class="font-mono text-[11px] text-[var(--text-bright)] uppercase tracking-[0.08em]">
                migrate
              </div>
              <div class="font-mono text-[9.5px] text-[var(--text-dim)] tracking-tight">
                hand off to a different host
              </div>
            </div>
          </button>
        {:else}
          <div class="px-3 py-2 rounded border border-phosphor-400/30 bg-phosphor-400/[0.04]">
            <div class="font-mono text-[10px] uppercase tracking-[0.1em] text-phosphor-300 mb-1.5">
              migrate target
            </div>
            <input
              type="text"
              placeholder="target host id…"
              class="w-full field text-[11.5px]"
              bind:value={migrateTarget}
              use:autofocusOnMount
              onkeydown={(e) => {
                if (e.key === "Enter" && migrateTarget.trim())
                  invoke("cell.migrate", { CellID: cell!.id, HostID: migrateTarget.trim() });
                if (e.key === "Escape") {
                  migratePromptOpen = false;
                  migrateTarget = "";
                }
              }}
            />
            <div class="mt-2 flex gap-1.5">
              <button
                class="btn-phosphor flex-1 disabled:opacity-50"
                disabled={busy || !migrateTarget.trim()}
                onclick={() => invoke("cell.migrate", { CellID: cell!.id, HostID: migrateTarget.trim() })}
              >
                exec
              </button>
              <button
                class="btn-ghost"
                onclick={() => { migratePromptOpen = false; migrateTarget = ""; }}
              >cancel</button>
            </div>
          </div>
        {/if}
      </div>
    </section>
  </aside>
{/if}
