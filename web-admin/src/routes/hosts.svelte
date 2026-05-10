<script lang="ts">
  import { onMount } from "svelte";
  import { hostsStore } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import { fmtDuration, fmtLoad } from "$lib/format";
  import type { HostInfo } from "$lib/types";
  import DataTable from "../components/DataTable.svelte";

  let hosts = $derived<HostInfo[]>(hostsStore.value ?? []);

  // One-shot fetch at mount + live updates via SSE.
  onMount(async () => {
    try {
      const initial = await apiGet<HostInfo[]>("/admin/api/hosts");
      hostsStore.set(initial);
    } catch {
      // Stream subscription will populate it shortly.
    }
  });

  $effect(() => {
    const off = stream.subscribe("hosts", (data) => {
      hostsStore.set(data as HostInfo[]);
    });
    return off;
  });

  const columns = [
    { key: "id", label: "Host", accessor: (h: HostInfo) => h.id, width: "20%" },
    {
      key: "state",
      label: "State",
      accessor: (h: HostInfo) => h.state,
      render: (h: HostInfo) => `${h.state}${h.isLocal ? " *" : ""}`,
      width: "100px",
    },
    {
      key: "roles",
      label: "Roles",
      accessor: (h: HostInfo) => (h.roles ?? []).join(","),
      render: (h: HostInfo) => (h.roles ?? []).join(", "),
      width: "120px",
    },
    {
      key: "hb",
      label: "HB age",
      accessor: (h: HostInfo) => h.heartbeatAgeMs,
      render: (h: HostInfo) =>
        h.isLocal ? "—" : fmtDuration(h.heartbeatAgeMs),
      width: "90px",
      align: "right" as const,
    },
    {
      key: "cells",
      label: "Cells",
      accessor: (h: HostInfo) => (h.cells ?? []).length,
      render: (h: HostInfo) => `${(h.cells ?? []).length}`,
      align: "right" as const,
      width: "60px",
    },
    {
      key: "entities",
      label: "Entities",
      accessor: (h: HostInfo) => h.totalEntities,
      align: "right" as const,
      width: "80px",
    },
    {
      key: "load",
      label: "Load",
      accessor: (h: HostInfo) => h.load,
      render: (h: HostInfo) => fmtLoad(h.load),
      align: "right" as const,
      width: "70px",
    },
  ];
</script>

<main class="p-4">
  <h2 class="text-accent-300 text-[11px] uppercase tracking-wide mb-3">Hosts</h2>
  <div class="bg-[#0d1117] border border-white/10 rounded-lg p-3">
    <DataTable
      rows={hosts}
      {columns}
      initialSortKey="id"
      emptyText="No hosts registered."
    />
  </div>
</main>
