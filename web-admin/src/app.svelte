<script lang="ts">
  import { onMount } from "svelte";
  import { auth } from "$lib/auth";
  import { sessionStore, clusterStore } from "$lib/stores.svelte";
  import { route } from "$lib/router";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import type { ClusterInfo } from "$lib/types";
  import Login from "./routes/login.svelte";
  import Cluster from "./routes/cluster.svelte";
  import Sidebar from "./components/Sidebar.svelte";
  import TopBar from "./components/TopBar.svelte";

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
</script>

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
        {:else}
          <div class="p-8 text-slate-500">
            Panel <code>{path}</code> — not yet implemented.
          </div>
        {/if}
      </div>
    </div>
  </div>
{/if}
