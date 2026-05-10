<script lang="ts">
  import { onMount } from "svelte";
  import { apiGet, apiPost, ApiError } from "$lib/api";
  import type { LogCategoriesResp, LogCategory } from "$lib/types";

  let groups = $state<LogCategoriesResp["groups"]>([]);
  let error = $state("");
  let loading = $state(true);

  async function refresh() {
    loading = true;
    error = "";
    try {
      const res = await apiGet<LogCategoriesResp>("/admin/api/logs/categories");
      groups = res.groups ?? [];
    } catch (e) {
      error = (e as Error).message;
    } finally {
      loading = false;
    }
  }

  async function toggle(cat: string, enabled: boolean) {
    try {
      const res = await apiPost<LogCategory>(
        `/admin/api/logs/categories/${encodeURIComponent(cat)}`,
        { enabled },
      );
      // Apply the server-confirmed value optimistically rather than full
      // refresh (avoids a flicker for the toggle).
      groups = groups.map((g) => ({
        ...g,
        categories: g.categories.map((c) => (c.name === cat ? res : c)),
      }));
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message;
      error = msg;
    }
  }

  function setAll(group: typeof groups[number], enabled: boolean) {
    for (const c of group.categories) {
      void toggle(c.name, enabled);
    }
  }

  onMount(() => {
    void refresh();
  });
</script>

<main class="p-4 space-y-3">
  <div class="flex items-center justify-between">
    <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Logs</h2>
    <button
      type="button"
      class="px-2 py-0.5 rounded border border-white/10 bg-white/5 text-slate-300 hover:bg-white/10 text-[11px]"
      onclick={() => void refresh()}
      disabled={loading}
    >
      refresh
    </button>
  </div>

  {#if error}
    <div class="text-rose-300 text-[12px]">{error}</div>
  {/if}

  {#if loading && groups.length === 0}
    <div class="text-slate-500 text-[12px]">loading…</div>
  {/if}

  <div class="grid gap-3 grid-cols-1 md:grid-cols-2 xl:grid-cols-3">
    {#each groups as g (g.name)}
      <div class="bg-[#0d1117] border border-white/10 rounded-lg p-3">
        <div class="flex items-center justify-between mb-2">
          <h3 class="font-mono text-[12px] text-slate-200">{g.name || "(uncategorized)"}</h3>
          <div class="flex gap-1 text-[10.5px]">
            <button
              type="button"
              class="px-1.5 py-0.5 rounded border border-white/10 bg-white/5 text-slate-400 hover:bg-white/10"
              onclick={() => setAll(g, true)}
            >all</button>
            <button
              type="button"
              class="px-1.5 py-0.5 rounded border border-white/10 bg-white/5 text-slate-400 hover:bg-white/10"
              onclick={() => setAll(g, false)}
            >none</button>
          </div>
        </div>
        <div class="space-y-1">
          {#each g.categories as c (c.name)}
            <label class="flex items-center gap-2 text-[11.5px] cursor-pointer">
              <input
                type="checkbox"
                class="accent-accent-400"
                checked={c.enabled}
                onchange={(e) => toggle(c.name, (e.currentTarget as HTMLInputElement).checked)}
              />
              <span class="font-mono text-slate-300">{c.name}</span>
            </label>
          {/each}
        </div>
      </div>
    {/each}
  </div>
</main>
