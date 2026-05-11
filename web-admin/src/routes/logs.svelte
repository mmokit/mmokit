<script lang="ts">
  import LogTail from "../components/LogTail.svelte";

  // Host + category filtering are both purely client-side: the server
  // emits whatever its own logger.Logger is configured for; this page
  // just lets you hide rows from view. Discovered sets come from
  // LogTail's entries buffer.

  let discoveredHosts = $state<string[]>([]);
  let selectedHosts = $state<Set<string>>(new Set());

  function onHostsChanged(hosts: string[]) {
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

  let hostFilter = $derived(
    selectedHosts.size === discoveredHosts.length ? undefined : selectedHosts,
  );

  // Categories — discovered from entries the same way as hosts.
  let discoveredCats = $state<string[]>([]);
  let selectedCats = $state<Set<string>>(new Set());

  function onCatsChanged(cats: string[]) {
    let dirty = false;
    const next = new Set(selectedCats);
    for (const c of cats) {
      if (!discoveredCats.includes(c)) {
        next.add(c);
        dirty = true;
      }
    }
    discoveredCats = cats;
    if (dirty) selectedCats = next;
  }

  function toggleCat(cat: string, checked: boolean) {
    const next = new Set(selectedCats);
    if (checked) next.add(cat);
    else next.delete(cat);
    selectedCats = next;
  }

  // Group categories by their "prefix:" segment so the UI matches the
  // server's logical grouping (mesh:cell, mesh:transfer → "mesh"). Cats
  // without a colon land in the synthetic "" group.
  let catGroups = $derived.by<{ name: string; cats: string[] }[]>(() => {
    const by: Record<string, string[]> = {};
    for (const c of discoveredCats) {
      const i = c.indexOf(":");
      const g = i > 0 ? c.slice(0, i) : "";
      (by[g] ??= []).push(c);
    }
    const groups = Object.keys(by).sort();
    return groups.map((g) => ({ name: g, cats: by[g].sort() }));
  });

  function setAllInGroup(name: string, checked: boolean) {
    const next = new Set(selectedCats);
    for (const g of catGroups) {
      if (g.name !== name) continue;
      for (const c of g.cats) {
        if (checked) next.add(c);
        else next.delete(c);
      }
    }
    selectedCats = next;
  }

  function selectAllCats(checked: boolean) {
    selectedCats = checked ? new Set(discoveredCats) : new Set();
  }

  let catFilter = $derived(
    selectedCats.size === discoveredCats.length ? undefined : selectedCats,
  );
</script>

<main class="p-4 h-full min-h-0 flex flex-col gap-3">
  <div class="flex items-center justify-between">
    <h2 class="text-phosphor-300 text-[11px] uppercase tracking-[0.18em] font-mono">Logs</h2>
    <span class="text-[var(--text-dim)] text-[10.5px] font-mono">
      filters are client-side · server decides what it emits
    </span>
  </div>

  <div class="grow min-h-0 flex gap-3">
    <!-- Left: host + category filters -->
    <aside class="w-[280px] shrink-0 overflow-auto bg-[var(--bg-deep)] border border-[var(--border-subtle)] rounded-lg p-3 space-y-3">
      <!-- Hosts -->
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

      <!-- Categories -->
      <div>
        <div class="flex items-center justify-between mb-1">
          <h3 class="font-mono text-[11px] text-phosphor-300 tracking-[0.12em] uppercase">
            Categories
          </h3>
          <div class="flex gap-1 text-[10.5px] font-mono">
            <button
              type="button"
              class="px-1.5 py-0.5 rounded border border-[var(--border-subtle)] bg-white/5 text-[var(--text-muted)] hover:bg-white/10"
              onclick={() => selectAllCats(true)}
            >all</button>
            <button
              type="button"
              class="px-1.5 py-0.5 rounded border border-[var(--border-subtle)] bg-white/5 text-[var(--text-muted)] hover:bg-white/10"
              onclick={() => selectAllCats(false)}
            >none</button>
          </div>
        </div>
        {#each catGroups as g (g.name)}
          <div class="mt-2">
            <div class="flex items-center justify-between mb-1">
              <span class="font-mono text-[10.5px] text-[var(--text-muted)] tracking-[0.1em]">
                {g.name || "(uncategorized)"}
              </span>
              <div class="flex gap-1 text-[10px] font-mono">
                <button
                  type="button"
                  class="px-1.5 py-0.5 rounded border border-[var(--border-faint)] bg-white/5 text-[var(--text-dim)] hover:bg-white/10"
                  onclick={() => setAllInGroup(g.name, true)}
                >all</button>
                <button
                  type="button"
                  class="px-1.5 py-0.5 rounded border border-[var(--border-faint)] bg-white/5 text-[var(--text-dim)] hover:bg-white/10"
                  onclick={() => setAllInGroup(g.name, false)}
                >none</button>
              </div>
            </div>
            <div class="space-y-0.5">
              {#each g.cats as c (c)}
                <label class="flex items-center gap-2 text-[11px] font-mono cursor-pointer">
                  <input
                    type="checkbox"
                    class="accent-phosphor-400"
                    checked={selectedCats.has(c)}
                    onchange={(e) => toggleCat(c, (e.currentTarget as HTMLInputElement).checked)}
                  />
                  <span class="text-[var(--text-default)]">{c}</span>
                </label>
              {/each}
            </div>
          </div>
        {:else}
          <div class="text-[var(--text-dim)] text-[10.5px] italic font-mono">
            waiting for log entries…
          </div>
        {/each}
      </div>
    </aside>

    <!-- Right: live tail -->
    <div class="grow min-h-0">
      <LogTail
        filterHosts={hostFilter}
        filterCats={catFilter}
        {onHostsChanged}
        {onCatsChanged}
      />
    </div>
  </div>
</main>
