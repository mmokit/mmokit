<script lang="ts">
  import { ApiError } from "$lib/api";
  import { listEntities } from "$lib/entities";
  import type { EntityListRow } from "$lib/types";
  import EntityDrawer from "../components/EntityDrawer.svelte";
  import { Search } from "$lib/icons";

  let rows = $state<EntityListRow[]>([]);
  let loading = $state(false);
  let error = $state("");
  let kindFilter = $state("");
  let cellFilter = $state("");
  let search = $state("");
  let selected = $state<EntityListRow | null>(null);

  async function refresh() {
    loading = true;
    error = "";
    try {
      rows = await listEntities(kindFilter.trim() || undefined);
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message;
      rows = [];
    } finally {
      loading = false;
    }
  }

  // Initial fetch on mount. refresh() reads kindFilter, but it is not
  // reactively tracked here (called inside an async fn), so this effect runs
  // once. Re-fetches are driven by the Refresh button / Enter on the filters.
  $effect(() => {
    refresh();
  });

  // Distinct kinds present in the current result set — drives the kind filter
  // dropdown.
  let kinds = $derived.by(() => {
    const s = new Set<string>();
    for (const r of rows) if (r.Kind) s.add(r.Kind);
    return [...s].sort();
  });

  let filtered = $derived.by(() => {
    const q = search.trim().toLowerCase();
    const cell = cellFilter.trim();
    return rows.filter((r) => {
      if (cell && r.CellID !== cell) return false;
      if (q && !String(r.NetID).includes(q) && !r.Kind.toLowerCase().includes(q))
        return false;
      return true;
    });
  });

  function onFilterKey(e: KeyboardEvent) {
    if (e.key === "Enter") refresh();
  }
</script>

<div class="h-full flex">
  <main class="grow p-4 space-y-3 min-w-0">
    <div class="flex items-center justify-between">
      <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Entities</h2>
      <div class="flex items-center gap-2 text-[11px]">
        <div class="flex items-center bg-white/5 border border-white/10 rounded">
          <Search class="w-3.5 h-3.5 ml-2 text-slate-500" />
          <input
            type="text"
            placeholder="netID or kind…"
            class="bg-transparent px-2 py-1 text-[12px] text-slate-200 placeholder-slate-500 focus:outline-none w-44"
            bind:value={search}
          />
        </div>
        <select
          class="bg-white/5 border border-white/10 rounded px-2 py-1 text-[12px] text-slate-200 focus:outline-none"
          bind:value={kindFilter}
          onchange={refresh}
          title="Filter by kind (server-side)"
        >
          <option value="">all kinds</option>
          {#each kinds as k (k)}
            <option value={k}>{k}</option>
          {/each}
        </select>
        <input
          type="text"
          placeholder="cell id…"
          class="bg-white/5 border border-white/10 rounded px-2 py-1 text-[12px] text-slate-200 placeholder-slate-500 focus:outline-none w-36"
          bind:value={cellFilter}
          onkeydown={onFilterKey}
        />
        <button
          class="px-2 py-1 bg-white/5 border border-white/10 rounded hover:bg-white/10 text-slate-200 disabled:opacity-50"
          onclick={refresh}
          disabled={loading}
        >
          {loading ? "…" : "Refresh"}
        </button>
      </div>
    </div>

    {#if error}
      <div class="text-[12px] px-3 py-1.5 rounded bg-rose-900/30 text-rose-200 border border-rose-700/40">
        {error}
      </div>
    {/if}

    <div class="bg-[#0d1117] border border-white/10 rounded-lg p-3">
      <table class="w-full text-[12px] border-collapse">
        <thead>
          <tr class="text-left text-[10.5px] uppercase tracking-wide text-slate-500 border-b border-white/10">
            <th class="py-1.5 px-2 font-medium" style="width:120px">NetID</th>
            <th class="py-1.5 px-2 font-medium" style="width:22%">Kind</th>
            <th class="py-1.5 px-2 font-medium" style="width:22%">Cell</th>
            <th class="py-1.5 px-2 font-medium" style="width:120px">X</th>
            <th class="py-1.5 px-2 font-medium" style="width:120px">Y</th>
          </tr>
        </thead>
        <tbody>
          {#each filtered as r (r.NetID)}
            <tr
              class="border-b border-white/5 hover:bg-white/5 cursor-pointer {selected?.NetID === r.NetID ? 'bg-white/5' : ''}"
              onclick={() => (selected = r)}
            >
              <td class="py-1.5 px-2 font-mono text-slate-200">{r.NetID}</td>
              <td class="py-1.5 px-2">{r.Kind || "—"}</td>
              <td class="py-1.5 px-2 font-mono">{r.CellID || "—"}</td>
              <td class="py-1.5 px-2 tabular-nums">{r.WorldX.toFixed(0)}</td>
              <td class="py-1.5 px-2 tabular-nums">{r.WorldY.toFixed(0)}</td>
            </tr>
          {:else}
            <tr>
              <td colspan="5" class="py-4 text-center text-slate-500">
                {loading ? "Loading…" : "No entities match."}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </main>

  {#if selected}
    <EntityDrawer
      netID={selected.NetID}
      onClose={() => (selected = null)}
      onDespawned={() => {
        selected = null;
        refresh();
      }}
    />
  {/if}
</div>
