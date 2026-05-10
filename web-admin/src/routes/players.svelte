<script lang="ts">
  import { onMount } from "svelte";
  import { playersStore, pendingNav } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import type { PlayerInfo } from "$lib/types";
  import PlayerOpsModal from "../components/PlayerOpsModal.svelte";
  import PlayerDrawer from "../components/PlayerDrawer.svelte";
  import { Search } from "$lib/icons";

  let allPlayers = $derived<PlayerInfo[]>(playersStore.value ?? []);
  let search = $state("");
  let statusFilter = $state<"all" | "online" | "offline">("online");
  let drawerPlayer = $state<PlayerInfo | null>(null);

  // Honor a pending palette navigation: prefill the search box with the
  // picked player's username and broaden the status filter so an offline
  // player still shows up. Open the drawer directly when the live store
  // has the entry.
  $effect(() => {
    const t = pendingNav.value;
    if (!t || t.kind !== "player") return;
    search = t.username;
    statusFilter = "all";
    pendingNav.consume();
    const p = allPlayers.find((x) => x.username === t.username);
    if (p) drawerPlayer = p;
  });

  let filtered = $derived.by(() => {
    const q = search.trim().toLowerCase();
    return allPlayers.filter((p) => {
      if (statusFilter !== "all" && p.status !== statusFilter) return false;
      if (q && !p.username.toLowerCase().includes(q)) return false;
      return true;
    });
  });

  type Op = "kick" | "tp" | "tpto" | null;
  let modalOp = $state<Op>(null);
  let modalUser = $state("");
  let toast = $state<{ msg: string; ok: boolean } | null>(null);

  function openOp(op: Op, username: string) {
    modalOp = op;
    modalUser = username;
  }
  function closeOp() {
    modalOp = null;
    modalUser = "";
  }
  function onResult(ok: boolean, msg: string) {
    toast = { ok, msg };
    setTimeout(() => (toast = null), 4000);
  }

  onMount(async () => {
    try {
      const initial = await apiGet<PlayerInfo[]>("/admin/api/players?status=all");
      playersStore.set(initial);
    } catch {
      // Stream takes over.
    }
  });

  $effect(() => {
    const off = stream.subscribe("players", (data) => {
      playersStore.set(data as PlayerInfo[]);
    });
    return off;
  });
</script>

<div class="h-full flex">
  <main class="grow p-4 space-y-3 min-w-0">
  <div class="flex items-center justify-between">
    <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Players</h2>
    <div class="flex items-center gap-2 text-[11px]">
      <div class="flex items-center bg-white/5 border border-white/10 rounded">
        <Search class="w-3.5 h-3.5 ml-2 text-slate-500" />
        <input
          type="text"
          placeholder="search…"
          class="bg-transparent px-2 py-1 text-[12px] text-slate-200 placeholder-slate-500 focus:outline-none w-44"
          bind:value={search}
        />
      </div>
      <div class="flex bg-white/5 border border-white/10 rounded overflow-hidden">
        {#each ["online", "all", "offline"] as f (f)}
          <button
            class="px-2 py-0.5 {statusFilter === f ? 'bg-accent-300/20 text-accent-300' : 'text-slate-400 hover:bg-white/5'}"
            onclick={() => (statusFilter = f as typeof statusFilter)}
          >{f}</button>
        {/each}
      </div>
    </div>
  </div>

  <div class="bg-[#0d1117] border border-white/10 rounded-lg p-3">
    <table class="w-full text-[12px] border-collapse">
      <thead>
        <tr class="text-left text-[10.5px] uppercase tracking-wide text-slate-500 border-b border-white/10">
          <th class="py-1.5 px-2 font-medium" style="width:22%">Username</th>
          <th class="py-1.5 px-2 font-medium" style="width:100px">Status</th>
          <th class="py-1.5 px-2 font-medium" style="width:20%">Host</th>
          <th class="py-1.5 px-2 font-medium" style="width:20%">Cell</th>
          <th class="py-1.5 px-2 font-medium" style="width:120px">World</th>
          <th class="py-1.5 px-2 font-medium" style="width:200px">Ops</th>
        </tr>
      </thead>
      <tbody>
        {#each filtered as p (p.username)}
          <tr class="border-b border-white/5 hover:bg-white/5">
            <td class="py-1.5 px-2 font-mono">
              <button
                type="button"
                class="text-slate-200 hover:text-accent-300"
                onclick={() => (drawerPlayer = p)}
              >{p.username}</button>
            </td>
            <td class="py-1.5 px-2 {p.status === 'online' ? 'text-emerald-300' : 'text-slate-500'}">
              {p.status === "online" ? "● online" : "○ offline"}
            </td>
            <td class="py-1.5 px-2">{p.hostId ?? "—"}</td>
            <td class="py-1.5 px-2 font-mono">{p.cellId ?? "—"}</td>
            <td class="py-1.5 px-2">
              {p.worldX != null && p.worldY != null && (p.worldX !== 0 || p.worldY !== 0)
                ? `(${p.worldX.toFixed(0)}, ${p.worldY.toFixed(0)})`
                : "—"}
            </td>
            <td class="py-1.5 px-2">
              <div class="flex gap-1.5">
                <button
                  class="px-2 py-0.5 text-[10.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
                  onclick={() => openOp("tp", p.username)}
                  disabled={p.status !== "online"}
                  title={p.status === "online" ? "Teleport" : "Player offline"}
                >
                  tp
                </button>
                <button
                  class="px-2 py-0.5 text-[10.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
                  onclick={() => openOp("tpto", p.username)}
                  disabled={p.status !== "online"}
                >
                  tpto
                </button>
                <button
                  class="px-2 py-0.5 text-[10.5px] bg-rose-500/15 border border-rose-500/30 text-rose-200 rounded hover:bg-rose-500/25 disabled:opacity-50"
                  onclick={() => openOp("kick", p.username)}
                  disabled={p.status !== "online"}
                >
                  kick
                </button>
              </div>
            </td>
          </tr>
        {:else}
          <tr><td colspan="6" class="py-4 text-center text-slate-500">No players match.</td></tr>
        {/each}
      </tbody>
    </table>
  </div>

  {#if toast}
    <div
      class="text-[12px] px-3 py-1.5 rounded {toast.ok
        ? 'bg-emerald-900/30 text-emerald-200 border border-emerald-700/40'
        : 'bg-rose-900/30 text-rose-200 border border-rose-700/40'}"
    >
      {toast.msg}
    </div>
  {/if}

  <PlayerOpsModal
    op={modalOp}
    username={modalUser}
    onClose={closeOp}
    onResult={onResult}
  />
  </main>

  <PlayerDrawer
    player={drawerPlayer}
    onClose={() => (drawerPlayer = null)}
    onResult={onResult}
  />
</div>
