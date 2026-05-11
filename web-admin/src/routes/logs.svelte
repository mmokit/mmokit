<script lang="ts">
  import { onMount } from "svelte";
  import { apiGet, apiPost, ApiError } from "$lib/api";
  import type { LogCategoriesResp } from "$lib/types";
  import LogTail from "../components/LogTail.svelte";

  let groups = $state<LogCategoriesResp["groups"]>([]);
  let error = $state("");
  let loading = $state(true);

  // Hosts discovered by LogTail (from the entries it has seen). When a
  // new host appears we add it to selectedHosts so its lines stay
  // visible by default; existing selections (user-checked / unchecked)
  // are preserved across discovery updates.
  let discoveredHosts = $state<string[]>([]);
  let selectedHosts = $state<Set<string>>(new Set());

  function onHostsChanged(hosts: string[]) {
    // Add genuinely new hosts as "selected" by default.
    let dirty = false;
    const next = new Set(selectedHosts);
    for (const h of hosts) {
      if (!discoveredHosts.includes(h)) {
        next.add(h);
        dirty = true;
      }
    }
    discoveredHosts = hosts;
    if (dirty) selectedHosts = next;
  }

  function toggleHost(host: string, checked: boolean) {
    const next = new Set(selectedHosts);
    if (checked) next.add(host);
    else next.delete(host);
    selectedHosts = next;
  }

  function selectAllHosts(checked: boolean) {
    selectedHosts = checked ? new Set(discoveredHosts) : new Set();
  }

  // Filter passed to LogTail: undefined = no host filter (every host
  // visible). We emit the set only when at least one host is unchecked
  // — when ALL discovered hosts are selected the filter is a no-op.
  let hostFilter = $derived(
    selectedHosts.size === discoveredHosts.length ? undefined : selectedHosts,
  );

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
    <!-- Left: host filter + category toggles -->
    <aside class="w-[280px] shrink-0 overflow-auto bg-[var(--bg-deep)] border border-[var(--border-subtle)] rounded-lg p-3 space-y-3">
      <div>
        <div class="flex items-center justify-between mb-1">
          <h3 class="font-mono text-[11px] text-phosphor-300 tracking-[0.12em] uppercase">
            Hosts
          </h3>
          <div class="flex gap-1 text-[10.5px] font-mono">
            <button
              type="button"
              class="px-1.5 py-0.5 rounded border border-[var(--border-subtle)] bg-white/5 text-[var(--text-muted)] hover:bg-white/10"
              onclick={() => selectAllHosts(true)}
            >all</button>
            <button
              type="button"
              class="px-1.5 py-0.5 rounded border border-[var(--border-subtle)] bg-white/5 text-[var(--text-muted)] hover:bg-white/10"
              onclick={() => selectAllHosts(false)}
            >none</button>
          </div>
        </div>
        <div class="space-y-0.5">
          {#each discoveredHosts as h (h)}
            <label class="flex items-center gap-2 text-[11px] font-mono cursor-pointer">
              <input
                type="checkbox"
                class="accent-phosphor-400"
                checked={selectedHosts.has(h)}
                onchange={(e) => toggleHost(h, (e.currentTarget as HTMLInputElement).checked)}
              />
              <span class="text-[var(--text-default)]">{h}</span>
            </label>
          {:else}
            <div class="text-[var(--text-dim)] text-[10.5px] italic font-mono">
              waiting for log entries…
            </div>
          {/each}
        </div>
      </div>

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
      <LogTail filterHosts={hostFilter} {onHostsChanged} />
    </div>
  </div>
</main>
