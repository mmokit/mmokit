<script lang="ts">
  import { onMount } from "svelte";
  import { hostsStore, gatewaysStore, pendingNav } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import { fmtBytes, fmtDuration, fmtLoad } from "$lib/format";
  import type { HostInfo, GatewayInfo } from "$lib/types";
  import DataTable from "../components/DataTable.svelte";

  // Unified row type: a "node" is either a host or a gateway. Rows carry
  // the shared fields directly and the type-specific fields wrapped in a
  // discriminated union so accessors can pull whichever metric matters.
  type NodeRow = {
    id: string;
    kind: "host" | "gateway";
    where: "local" | "remote";
    state: string; // host: live/dead/etc; gateway: mode (local-shortcut/etc) or "—"
    hbAgeMs: number; // hosts only; -1 sentinel for gateways → renders as "—"
    countLabel: string; // "cells" | "sessions"
    count: number;
    metricLabel: string; // "load" | "bytes"
    metricNumber: number; // numeric for sort
    metricText: string; // human-readable for display
  };

  let hosts = $derived<HostInfo[]>(hostsStore.value ?? []);
  let gateways = $derived<GatewayInfo[]>(gatewaysStore.value ?? []);
  let typeFilter = $state<"all" | "host" | "gateway">("all");
  let search = $state("");

  // Honor a pending palette navigation for nodes — pre-fill the search
  // box with the picked node's ID so the table narrows to it.
  $effect(() => {
    const t = pendingNav.value;
    if (t && t.kind === "node") {
      search = t.id;
      typeFilter = "all";
      pendingNav.consume();
    }
  });

  let rows = $derived.by<NodeRow[]>(() => {
    const out: NodeRow[] = [];
    for (const h of hosts) {
      out.push({
        id: h.id,
        kind: "host",
        where: h.isLocal ? "local" : "remote",
        state: h.state,
        hbAgeMs: h.isLocal ? -1 : h.heartbeatAgeMs,
        countLabel: "cells",
        count: (h.cells ?? []).length,
        metricLabel: "load",
        metricNumber: h.load,
        metricText: fmtLoad(h.load),
      });
    }
    for (const g of gateways) {
      out.push({
        id: g.id,
        kind: "gateway",
        where: g.isLocal ? "local" : "remote",
        state: g.mode || "—",
        hbAgeMs: -1,
        countLabel: "sessions",
        count: g.sessions,
        metricLabel: "bytes",
        metricNumber: g.bytesSent + g.bytesRecv,
        metricText: fmtBytes(g.bytesSent + g.bytesRecv),
      });
    }
    let filtered = typeFilter === "all" ? out : out.filter((r) => r.kind === typeFilter);
    const q = search.trim().toLowerCase();
    if (q) filtered = filtered.filter((r) => r.id.toLowerCase().includes(q));
    return filtered;
  });

  // One-shot fetch at mount + live updates via SSE.
  onMount(async () => {
    try {
      const [h, g] = await Promise.all([
        apiGet<HostInfo[]>("/admin/api/hosts"),
        apiGet<GatewayInfo[]>("/admin/api/gateways"),
      ]);
      hostsStore.set(h);
      gatewaysStore.set(g);
    } catch {
      // SSE will populate.
    }
  });

  $effect(() => {
    const offHosts = stream.subscribe("hosts", (data) => {
      hostsStore.set(data as HostInfo[]);
    });
    const offGateways = stream.subscribe("gateways", (data) => {
      gatewaysStore.set(data as GatewayInfo[]);
    });
    return () => {
      offHosts();
      offGateways();
    };
  });

  const columns = [
    { key: "id", label: "ID", accessor: (r: NodeRow) => r.id, width: "20%" },
    {
      key: "kind",
      label: "Type",
      accessor: (r: NodeRow) => r.kind,
      render: (r: NodeRow) => r.kind,
      width: "90px",
    },
    {
      key: "where",
      label: "Where",
      accessor: (r: NodeRow) => r.where,
      render: (r: NodeRow) => (r.where === "local" ? "in-proc" : "remote"),
      width: "100px",
    },
    {
      key: "state",
      label: "State / Mode",
      accessor: (r: NodeRow) => r.state,
      width: "150px",
    },
    {
      key: "hb",
      label: "HB age",
      accessor: (r: NodeRow) => (r.hbAgeMs < 0 ? Number.MAX_SAFE_INTEGER : r.hbAgeMs),
      render: (r: NodeRow) => (r.hbAgeMs < 0 ? "—" : fmtDuration(r.hbAgeMs)),
      width: "90px",
      align: "right" as const,
    },
    {
      key: "count",
      label: "Cells / Sessions",
      accessor: (r: NodeRow) => r.count,
      render: (r: NodeRow) => `${r.count} ${r.countLabel}`,
      align: "right" as const,
      width: "150px",
    },
    {
      key: "metric",
      label: "Load / Bytes",
      accessor: (r: NodeRow) => r.metricNumber,
      render: (r: NodeRow) => r.metricText,
      align: "right" as const,
      width: "120px",
    },
  ];
</script>

<main class="p-4 space-y-3">
  <div class="flex items-center justify-between">
    <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Nodes</h2>
    <div class="flex items-center gap-2 text-[11px]">
      <input
        type="text"
        placeholder="search id…"
        class="bg-white/5 border border-white/10 rounded px-2 py-1 text-[12px] text-slate-200 placeholder-slate-500 focus:outline-none w-44"
        bind:value={search}
      />
      <div class="flex bg-white/5 border border-white/10 rounded overflow-hidden">
        {#each ["all", "host", "gateway"] as f (f)}
          <button
            class="px-2 py-0.5 {typeFilter === f
              ? 'bg-accent-300/20 text-accent-300'
              : 'text-slate-400 hover:bg-white/5'}"
            onclick={() => (typeFilter = f as typeof typeFilter)}
          >{f}</button>
        {/each}
      </div>
    </div>
  </div>

  <div class="bg-[#0d1117] border border-white/10 rounded-lg p-3">
    <DataTable
      {rows}
      {columns}
      initialSortKey="kind"
      emptyText="No nodes registered."
    />
  </div>
</main>
