<script lang="ts">
  import { onMount } from "svelte";
  import { cellsStore, metricsHistoryStore, METRICS_HISTORY_LEN } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import { fmtBytes, fmtLoad } from "$lib/format";
  import type { CellInfo, MetricsSample, PerfSnapshot } from "$lib/types";
  import Sparkline from "../components/Sparkline.svelte";
  import BarChart from "../components/BarChart.svelte";

  // Sort cells by ID so the row order is stable across SSE ticks. Without
  // this, the underlying map iteration on the backend reshuffles the array
  // every second and rows visually jump around.
  let cells = $derived<CellInfo[]>(
    [...(cellsStore.value ?? [])].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)),
  );
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

  function toggleProfile(cellId: string) {
    if (expandedCell === cellId) {
      expandedCell = null;
      expandedPerf = null;
      drillError = "";
      return;
    }
    expandedCell = cellId;
    expandedPerf = null;
    drillError = "";
  }

  // Poll the perf endpoint at 1Hz while a row is expanded so the bar chart
  // tracks current samples instead of freezing at click time. The effect
  // tears down (clearInterval) when expandedCell flips back to null or the
  // component unmounts.
  $effect(() => {
    const cellId = expandedCell;
    if (!cellId) return;
    let cancelled = false;
    const fetchOnce = async () => {
      try {
        const res = await apiGet<PerfSnapshot>(`/admin/api/perf/${encodeURIComponent(cellId)}`);
        if (!cancelled && expandedCell === cellId) {
          expandedPerf = res;
          drillError = "";
        }
      } catch (e) {
        if (!cancelled && expandedCell === cellId) {
          drillError = (e as Error).message;
        }
      }
    };
    void fetchOnce();
    const id = setInterval(() => void fetchOnce(), 1000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  });

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
            {:else if !expandedPerf.systems || expandedPerf.systems.length === 0}
              <div class="text-slate-500 text-[11.5px] italic">
                No samples yet (cell may have just reset). Sample count: {expandedPerf.sampleCount}
              </div>
            {:else}
              {@const systems = expandedPerf.systems}
              {@const names = expandedPerf.systemNames ?? []}
              {@const rows = names.map((n, i) => ({
                label: n,
                value: systems[i].avgUs,
                valueText: `${systems[i].avgUs}µs / p95 ${systems[i].p95Us}µs`,
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
