<script lang="ts">
  import { onMount } from "svelte";
  import { gatewaysStore } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import { fmtBytes } from "$lib/format";
  import type { GatewayInfo } from "$lib/types";
  import DataTable from "../components/DataTable.svelte";

  let gateways = $derived<GatewayInfo[]>(gatewaysStore.value ?? []);

  onMount(async () => {
    try {
      const initial = await apiGet<GatewayInfo[]>("/admin/api/gateways");
      gatewaysStore.set(initial);
    } catch {
      // Stream takes over.
    }
  });

  $effect(() => {
    const off = stream.subscribe("gateways", (data) => {
      gatewaysStore.set(data as GatewayInfo[]);
    });
    return off;
  });

  const columns = [
    { key: "id", label: "Gateway", accessor: (g: GatewayInfo) => g.id, width: "20%" },
    {
      key: "local",
      label: "Where",
      accessor: (g: GatewayInfo) => (g.isLocal ? 0 : 1),
      render: (g: GatewayInfo) => (g.isLocal ? "in-proc" : "remote"),
      width: "100px",
    },
    {
      key: "mode",
      label: "Mode",
      accessor: (g: GatewayInfo) => g.mode || "—",
      width: "140px",
    },
    {
      key: "sessions",
      label: "Sessions",
      accessor: (g: GatewayInfo) => g.sessions,
      align: "right" as const,
      width: "100px",
    },
    {
      key: "bytesSent",
      label: "Sent",
      accessor: (g: GatewayInfo) => g.bytesSent,
      render: (g: GatewayInfo) => fmtBytes(g.bytesSent),
      align: "right" as const,
      width: "120px",
    },
    {
      key: "bytesRecv",
      label: "Recv",
      accessor: (g: GatewayInfo) => g.bytesRecv,
      render: (g: GatewayInfo) => fmtBytes(g.bytesRecv),
      align: "right" as const,
      width: "120px",
    },
  ];
</script>

<main class="p-4">
  <h2 class="text-accent-300 text-[11px] uppercase tracking-wide mb-3">Gateways</h2>
  <div class="bg-[#0d1117] border border-white/10 rounded-lg p-3">
    <DataTable
      rows={gateways}
      {columns}
      initialSortKey="id"
      emptyText="No gateways registered."
    />
  </div>
</main>
