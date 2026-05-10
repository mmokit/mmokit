<script lang="ts">
  import { onMount } from "svelte";
  import { auth } from "$lib/auth";
  import { sessionStore, clusterStore, paletteOpen } from "$lib/stores.svelte";
  import { route } from "$lib/router";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import type { ClusterInfo } from "$lib/types";
  import Login from "./routes/login.svelte";
  import Cluster from "./routes/cluster.svelte";
  import Nodes from "./routes/nodes.svelte";
  import Players from "./routes/players.svelte";
  import Performance from "./routes/performance.svelte";
  import Events from "./routes/events.svelte";
  import Audit from "./routes/audit.svelte";
  import Logs from "./routes/logs.svelte";
  import Settings from "./routes/settings.svelte";
  import Sidebar from "./components/Sidebar.svelte";
  import TopBar from "./components/TopBar.svelte";
  import CommandPalette from "./components/CommandPalette.svelte";

  let path = $state("/cluster");
  let booting = $state(true);
  let loggedIn = $derived(sessionStore.value !== null);

  $effect(() => {
    const off = route.subscribe((p: string) => (path = p));
    return off;
  });

  onMount(async () => {
    const s = await auth.session();
    sessionStore.set(s);
    if (s) {
      hydrateCluster();
    }
    booting = false;
  });

  async function hydrateCluster() {
    try {
      const c = await apiGet<ClusterInfo>("/admin/api/cluster");
      clusterStore.set(c);
    } catch {
      // 401 etc. — auth gate redirects to login
    }
    // Subscribe to topics: cells (per-cell snapshots), alerts (invariant violations).
    // Each panel that needs live data subscribes itself; this keeps the global
    // stream alive while the user is logged in.
    stream.subscribe("cells", () => {
      // CellMap reads cellsStore directly via its own subscription (Task 15).
    });
  }

  async function onLogin() {
    const s = await auth.session();
    sessionStore.set(s);
    if (s) hydrateCluster();
  }

  function onGlobalKey(e: KeyboardEvent) {
    // Cmd+K / Ctrl+K toggles palette unless the user is typing in an input.
    const target = e.target as HTMLElement | null;
    const editable = target && (
      target.tagName === "INPUT" ||
      target.tagName === "TEXTAREA" ||
      target.isContentEditable
    );
    if ((e.metaKey || e.ctrlKey) && (e.key === "k" || e.key === "K")) {
      e.preventDefault();
      paletteOpen.toggle();
      return;
    }
    if (e.key === "Escape" && paletteOpen.value && !editable) {
      paletteOpen.set(false);
    }
  }

</script>

<svelte:window onkeydown={onGlobalKey} />

{#if booting}
  <div class="h-full flex items-center justify-center text-slate-400">loading…</div>
{:else if !loggedIn}
  <Login onLoggedIn={onLogin} />
{:else}
  <div class="h-full flex">
    <Sidebar />
    <div class="grow flex flex-col min-w-0">
      <TopBar />
      <div class="grow overflow-auto">
        {#if path === "/cluster"}
          <Cluster />
        {:else if path === "/nodes" || path === "/hosts" || path === "/gateways"}
          <Nodes />
        {:else if path === "/players"}
          <Players />
        {:else if path === "/performance"}
          <Performance />
        {:else if path === "/events"}
          <Events />
        {:else if path === "/audit"}
          <Audit />
        {:else if path === "/logs"}
          <Logs />
        {:else if path === "/settings"}
          <Settings />
        {:else}
          <div class="p-8 text-slate-500">
            Panel <code>{path}</code> — not yet implemented.
          </div>
        {/if}
      </div>
    </div>
    <CommandPalette
      open={paletteOpen.value}
      onClose={() => paletteOpen.set(false)}
    />
  </div>
{/if}
