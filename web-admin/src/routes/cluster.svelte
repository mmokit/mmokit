<script lang="ts">
  import { cellsStore, hostsStore, pendingNav, metricsHistoryStore } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { Crosshair, Cpu, Layers, Wifi, Radio } from "$lib/icons";
  import CellMap from "../components/CellMap.svelte";
  import CellDrawer from "../components/CellDrawer.svelte";
  import type { CellInfo, HostInfo, MetricsSample } from "$lib/types";
  import { onMount } from "svelte";
  import { apiGet } from "$lib/api";

  let selected = $state<string | null>(null);
  let colorMode = $state<"load" | "host" | "entities">("load");
  let toast = $state<{ msg: string; ok: boolean } | null>(null);

  let cells = $derived<CellInfo[]>(cellsStore.value ?? []);
  let hosts = $derived(hostsStore.value ?? []);
  let selectedCell = $derived<CellInfo | null>(
    selected ? (cells.find((c) => c.id === selected) ?? null) : null,
  );

  const colorModes: Array<"load" | "host" | "entities"> = ["load", "host", "entities"];
  const colorModeLabel: Record<typeof colorModes[number], string> = {
    load: "tick budget",
    host: "host owner",
    entities: "entity density",
  };

  // Push every cells SSE update into the per-cell ring buffer so sparklines
  // in the drawer have history without first visiting /performance.
  $effect(() => {
    const off = stream.subscribe("cells", (data) => {
      const list = data as CellInfo[];
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

  // Hydrate hosts roster so the cluster summary shows accurate node count
  // even before the user visits /nodes.
  onMount(() => {
    const off = stream.subscribe("hosts", (data) => hostsStore.set(data as HostInfo[]));
    apiGet<HostInfo[]>("/admin/api/hosts").then((d) => hostsStore.set(d)).catch(() => {});
    return off;
  });

  // Honor a pending palette navigation: when the user picked a cell in
  // ⌘K we land here with a `kind: "cell"` target. consume() clears the
  // signal so back-then-forward doesn't re-select.
  $effect(() => {
    const t = pendingNav.value;
    if (t && t.kind === "cell") {
      selected = t.id;
      pendingNav.consume();
    }
  });

  // Cluster-wide summary stats (derived from live SSE).
  let totalEntities = $derived(cells.reduce((s, c) => s + c.entities.real, 0));
  let totalSessions = $derived(cells.reduce((s, c) => s + c.entities.connected, 0));
  let totalReplicas = $derived(cells.reduce((s, c) => s + c.entities.replica, 0));
  let totalBytes = $derived(cells.reduce((s, c) => s + c.bytes.sent + c.bytes.recv, 0));
  let liveHosts = $derived(hosts.filter((h) => h.state === "live").length);
  let totalHosts = $derived(hosts.length);
  let avgLoad = $derived(
    cells.length === 0 ? 0 : cells.reduce((s, c) => s + c.load, 0) / cells.length,
  );
  let hotCell = $derived(
    cells.length === 0 ? null : cells.reduce((a, b) => (a.load > b.load ? a : b)),
  );

  function fmtCount(n: number): string {
    if (n < 1000) return `${n}`;
    if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`;
    return `${(n / 1_000_000).toFixed(1)}M`;
  }
  function fmtBytes(n: number): string {
    if (n < 1024) return `${n}B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)}MB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(2)}GB`;
  }
  function loadColor(load: number): string {
    if (load >= 0.85) return "text-ember-300";
    if (load >= 0.6) return "text-amber-300";
    if (load >= 0.35) return "text-lime-300";
    return "text-phosphor-300";
  }
</script>

<div class="h-full flex page-in">
  <main class="grow flex flex-col min-w-0 relative">
    <!-- Cluster summary header — six stat tiles -->
    <section
      class="grid grid-cols-6 gap-px bg-[var(--border-faint)] border-b border-[var(--border-subtle)] stagger"
    >
      <!-- AVG LOAD -->
      <div class="bg-[var(--bg-deep)] px-4 py-3 flex flex-col gap-1.5 relative overflow-hidden">
        <div class="flex items-center justify-between">
          <span class="stat-label">avg load</span>
          <Cpu class="w-3 h-3 text-[var(--text-dim)]" />
        </div>
        <div class="flex items-baseline gap-1">
          <span class="text-[22px] font-mono font-semibold {loadColor(avgLoad)} tracking-tight tabular-nums">
            {Math.round(avgLoad * 100)}
          </span>
          <span class="text-[var(--text-muted)] text-[11px] font-mono">%</span>
        </div>
        <!-- horizontal load bar -->
        <div class="relative h-[3px] bg-white/5 rounded-full overflow-hidden">
          <div
            class="absolute inset-y-0 left-0 transition-all duration-500
              {avgLoad >= 0.85 ? 'bg-ember-400' : avgLoad >= 0.6 ? 'bg-amber-400' : 'bg-lime-400'}"
            style="width: {Math.min(100, avgLoad * 100)}%"
          ></div>
        </div>
      </div>

      <!-- CELLS -->
      <div class="bg-[var(--bg-deep)] px-4 py-3 flex flex-col gap-1.5">
        <div class="flex items-center justify-between">
          <span class="stat-label">cells</span>
          <Layers class="w-3 h-3 text-[var(--text-dim)]" />
        </div>
        <div class="flex items-baseline gap-2">
          <span class="text-[22px] font-mono font-semibold text-[var(--text-bright)] tracking-tight tabular-nums">
            {cells.length}
          </span>
          {#if hotCell}
            <span class="text-[10px] font-mono text-[var(--text-dim)] truncate">
              hot · <span class="text-amber-300">{hotCell.id}</span>
            </span>
          {/if}
        </div>
        <div class="text-[10px] font-mono text-[var(--text-muted)] tracking-tight">
          {cells.filter((c) => c.depth > 0).length} split · {cells.filter((c) => c.depth === 0).length} root
        </div>
      </div>

      <!-- HOSTS -->
      <div class="bg-[var(--bg-deep)] px-4 py-3 flex flex-col gap-1.5">
        <div class="flex items-center justify-between">
          <span class="stat-label">hosts</span>
          <Radio class="w-3 h-3 text-[var(--text-dim)]" />
        </div>
        <div class="flex items-baseline gap-1.5">
          <span class="text-[22px] font-mono font-semibold text-[var(--text-bright)] tabular-nums">
            {liveHosts}
          </span>
          <span class="text-[var(--text-muted)] text-[11px] font-mono">/ {totalHosts}</span>
        </div>
        <div class="flex items-center gap-1">
          <span class="w-1 h-1 bg-lime-400 rounded-full breathe"></span>
          <span class="text-[10px] font-mono text-[var(--text-muted)] tracking-tight">
            {liveHosts === totalHosts ? "all live" : `${totalHosts - liveHosts} offline`}
          </span>
        </div>
      </div>

      <!-- ENTITIES -->
      <div class="bg-[var(--bg-deep)] px-4 py-3 flex flex-col gap-1.5">
        <div class="flex items-center justify-between">
          <span class="stat-label">entities</span>
          <Crosshair class="w-3 h-3 text-[var(--text-dim)]" />
        </div>
        <div class="flex items-baseline gap-1.5">
          <span class="text-[22px] font-mono font-semibold text-[var(--text-bright)] tabular-nums">
            {fmtCount(totalEntities)}
          </span>
        </div>
        <div class="text-[10px] font-mono text-[var(--text-muted)] tracking-tight">
          <span class="text-plasma-300">{fmtCount(totalReplicas)}</span> replicas
        </div>
      </div>

      <!-- SESSIONS -->
      <div class="bg-[var(--bg-deep)] px-4 py-3 flex flex-col gap-1.5">
        <div class="flex items-center justify-between">
          <span class="stat-label">sessions</span>
          <Wifi class="w-3 h-3 text-[var(--text-dim)]" />
        </div>
        <div class="flex items-baseline gap-1.5">
          <span class="text-[22px] font-mono font-semibold text-[var(--text-bright)] tabular-nums">
            {totalSessions}
          </span>
        </div>
        <div class="text-[10px] font-mono text-[var(--text-muted)] tracking-tight">
          across {cells.filter((c) => c.entities.connected > 0).length} cells
        </div>
      </div>

      <!-- BANDWIDTH -->
      <div class="bg-[var(--bg-deep)] px-4 py-3 flex flex-col gap-1.5">
        <div class="flex items-center justify-between">
          <span class="stat-label">cumulative i/o</span>
          <span class="w-1.5 h-1.5 bg-phosphor-400 rounded-full breathe"></span>
        </div>
        <div class="flex items-baseline gap-1.5">
          <span class="text-[18px] font-mono font-semibold text-phosphor-300 tabular-nums">
            {fmtBytes(totalBytes)}
          </span>
        </div>
        <div class="text-[10px] font-mono text-[var(--text-muted)] tracking-tight">
          since boot
        </div>
      </div>
    </section>

    <!-- Map header — color-mode toggle + legend -->
    <header class="flex items-center justify-between px-5 pt-4 pb-3">
      <div class="flex items-baseline gap-3">
        <h1 class="font-mono text-[13px] uppercase tracking-[0.18em] text-[var(--text-bright)]">
          live cell map
        </h1>
        <span class="font-mono text-[10px] text-[var(--text-dim)] tracking-tight">
          ◉ {selected ?? "no selection — click a cell"}
        </span>
      </div>
      <div class="flex items-center gap-3">
        <!-- Legend chip -->
        <div class="flex items-center gap-2 font-mono text-[10px] text-[var(--text-muted)]">
          <span class="uppercase tracking-[0.14em]">color by</span>
          <div class="flex bg-[var(--bg-surface)] border border-[var(--border-subtle)] rounded overflow-hidden">
            {#each colorModes as m (m)}
              <button
                class="px-2.5 py-1 transition-colors font-mono text-[10px] uppercase tracking-[0.08em]
                  {colorMode === m
                    ? 'bg-phosphor-300/15 text-phosphor-300 border-x border-phosphor-300/30 first:border-l-0 last:border-r-0'
                    : 'text-[var(--text-muted)] hover:text-[var(--text-default)] hover:bg-white/[0.02]'}"
                onclick={() => (colorMode = m)}
              >{m}</button>
            {/each}
          </div>
          <span class="text-[var(--text-dim)] hidden lg:inline">{colorModeLabel[colorMode]}</span>
        </div>
      </div>
    </header>

    <!-- The map itself -->
    <div class="grow px-5 pb-5 min-h-[300px] flex">
      <div
        class="grow relative rounded-lg overflow-hidden
               border border-phosphor-400/10
               bg-[radial-gradient(ellipse_at_center,_rgba(34,211,218,0.045)_0%,_rgba(4,6,11,0.95)_75%)]
               shadow-[0_0_60px_-20px_rgba(34,211,218,0.25),inset_0_0_1px_rgba(34,211,218,0.4)]"
      >
        <!-- corner ticks: gives the map a "scope" / tactical-display frame -->
        <span class="absolute top-0 left-0 w-3 h-3 border-t border-l border-phosphor-400/40 pointer-events-none"></span>
        <span class="absolute top-0 right-0 w-3 h-3 border-t border-r border-phosphor-400/40 pointer-events-none"></span>
        <span class="absolute bottom-0 left-0 w-3 h-3 border-b border-l border-phosphor-400/40 pointer-events-none"></span>
        <span class="absolute bottom-0 right-0 w-3 h-3 border-b border-r border-phosphor-400/40 pointer-events-none"></span>

        <CellMap
          {selected}
          {colorMode}
          onSelect={(id) => (selected = id)}
        />
      </div>
    </div>

    <!-- toast -->
    {#if toast}
      <div
        class="absolute bottom-6 left-1/2 -translate-x-1/2 font-mono text-[11px] px-3 py-1.5 rounded border tracking-tight
          {toast.ok
            ? 'bg-lime-400/[0.08] text-lime-300 border-lime-400/30'
            : 'bg-ember-400/[0.08] text-ember-300 border-ember-400/30'}"
      >
        {toast.msg}
      </div>
    {/if}
  </main>

  <CellDrawer
    cell={selectedCell}
    onClose={() => (selected = null)}
    onAction={(_verb, ok, msg) => {
      toast = { ok, msg };
      setTimeout(() => (toast = null), 4000);
    }}
  />
</div>
