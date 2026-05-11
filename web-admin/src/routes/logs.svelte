<script lang="ts">
  import { onMount } from "svelte";
  import { apiGet, apiPost, ApiError } from "$lib/api";
  import type { LogCategoriesResp } from "$lib/types";
  import LogTail from "../components/LogTail.svelte";

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

  // Toggle a category cluster-wide via the log.set cmdsys verb. The
  // verb is Route: RouteAllHosts so every host's logger is updated
  // alongside the coordinator. Optimistically reflect the new state;
  // the response confirms.
  async function toggle(cat: string, enabled: boolean) {
    // Optimistic update first.
    groups = groups.map((g) => ({
      ...g,
      categories: g.categories.map((c) => (c.name === cat ? { ...c, enabled } : c)),
    }));
    try {
      await apiPost<{ ok: boolean; result?: unknown; error?: string }>(
        "/admin/api/commands/log.set",
        { Category: cat, Enabled: enabled },
      );
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message;
      error = msg;
      // Revert on failure.
      await refresh();
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

<main class="p-4 h-full min-h-0 flex flex-col gap-3">
  <div class="flex items-center justify-between">
    <h2 class="text-phosphor-300 text-[11px] uppercase tracking-[0.18em] font-mono">Logs</h2>
    <button
      type="button"
      class="px-2 py-0.5 rounded border border-[var(--border-subtle)] bg-white/5 text-[var(--text-default)] hover:bg-white/10 text-[11px] font-mono"
      onclick={() => void refresh()}
      disabled={loading}
    >
      refresh
    </button>
  </div>

  {#if error}
    <div class="text-ember-300 text-[11.5px] font-mono">{error}</div>
  {/if}

  <div class="grow min-h-0 flex gap-3">
    <!-- Left: category toggles (narrow column) -->
    <aside class="w-[280px] shrink-0 overflow-auto bg-[var(--bg-deep)] border border-[var(--border-subtle)] rounded-lg p-3 space-y-3">
      {#if loading && groups.length === 0}
        <div class="text-[var(--text-dim)] text-[12px] font-mono">loading…</div>
      {/if}
      {#each groups as g (g.name)}
        <div>
          <div class="flex items-center justify-between mb-1">
            <h3 class="font-mono text-[11px] text-phosphor-300 tracking-[0.12em] uppercase">
              {g.name || "(uncategorized)"}
            </h3>
            <div class="flex gap-1 text-[10.5px] font-mono">
              <button
                type="button"
                class="px-1.5 py-0.5 rounded border border-[var(--border-subtle)] bg-white/5 text-[var(--text-muted)] hover:bg-white/10"
                onclick={() => setAll(g, true)}
              >all</button>
              <button
                type="button"
                class="px-1.5 py-0.5 rounded border border-[var(--border-subtle)] bg-white/5 text-[var(--text-muted)] hover:bg-white/10"
                onclick={() => setAll(g, false)}
              >none</button>
            </div>
          </div>
          <div class="space-y-0.5">
            {#each g.categories as c (c.name)}
              <label class="flex items-center gap-2 text-[11px] font-mono cursor-pointer">
                <input
                  type="checkbox"
                  class="accent-phosphor-400"
                  checked={c.enabled}
                  onchange={(e) => toggle(c.name, (e.currentTarget as HTMLInputElement).checked)}
                />
                <span class="text-[var(--text-default)]">{c.name}</span>
              </label>
            {/each}
          </div>
        </div>
      {/each}
    </aside>

    <!-- Right: live tail -->
    <div class="grow min-h-0">
      <LogTail />
    </div>
  </div>
</main>
