<script lang="ts">
  import { tick } from "svelte";
  import { fuzzyScore } from "$lib/commands";
  import {
    cellsStore,
    hostsStore,
    gatewaysStore,
    playersStore,
    pendingNav,
  } from "$lib/stores.svelte";
  import { navigate } from "$lib/router";
  import type { CellInfo, HostInfo, GatewayInfo, PlayerInfo } from "$lib/types";

  type Props = {
    open: boolean;
    onClose: () => void;
  };
  let { open, onClose }: Props = $props();

  type Entry =
    | { kind: "cell"; id: string; label: string; sublabel: string }
    | { kind: "node"; id: string; label: string; sublabel: string }
    | { kind: "player"; username: string; label: string; sublabel: string };

  let cells = $derived<CellInfo[]>(cellsStore.value ?? []);
  let hosts = $derived<HostInfo[]>(hostsStore.value ?? []);
  let gateways = $derived<GatewayInfo[]>(gatewaysStore.value ?? []);
  let players = $derived<PlayerInfo[]>(playersStore.value ?? []);

  // Flatten the live stores into a single search corpus. Re-derives whenever
  // any underlying list updates so the palette stays current while open.
  let entries = $derived.by<Entry[]>(() => {
    const out: Entry[] = [];
    for (const c of cells) {
      out.push({
        kind: "cell",
        id: c.id,
        label: c.id,
        sublabel: `cell · ${c.entities.real} ent · host ${c.hostId || "—"}`,
      });
    }
    for (const h of hosts) {
      out.push({
        kind: "node",
        id: h.id,
        label: h.id,
        sublabel: `host · ${(h.cells ?? []).length} cells · ${h.totalEntities} ent`,
      });
    }
    for (const g of gateways) {
      out.push({
        kind: "node",
        id: g.id,
        label: g.id,
        sublabel: `gateway · ${g.sessions} sessions${g.mode ? ` · ${g.mode}` : ""}`,
      });
    }
    for (const p of players) {
      out.push({
        kind: "player",
        username: p.username,
        label: p.username,
        sublabel: `player · ${p.status}${p.cellId ? ` · ${p.cellId}` : ""}`,
      });
    }
    return out;
  });

  let query = $state("");
  let activeIdx = $state(0);
  let inputEl: HTMLInputElement | undefined = $state();

  // Sorted, filtered list. Score by fuzzy match against the label; show
  // empty-query state as the first 50 entries in their natural order
  // (cells, then hosts/gateways, then players) so the panel is informative
  // even before the user types.
  let visible = $derived.by<{ entry: Entry; score: number }[]>(() => {
    if (!query) {
      return entries.slice(0, 50).map((e) => ({ entry: e, score: 0 }));
    }
    const out: { entry: Entry; score: number }[] = [];
    for (const e of entries) {
      const s = fuzzyScore(e.label, query);
      if (s === 0) continue;
      out.push({ entry: e, score: s });
    }
    out.sort((a, b) => b.score - a.score);
    return out.slice(0, 50);
  });

  // Reset state + focus input on each open.
  $effect(() => {
    if (open) {
      query = "";
      activeIdx = 0;
      void tick().then(() => inputEl?.focus());
    }
  });

  // Clamp activeIdx to visible bounds when the list changes.
  $effect(() => {
    if (activeIdx >= visible.length) activeIdx = Math.max(0, visible.length - 1);
  });

  function pick(i: number) {
    const e = visible[i]?.entry;
    if (!e) return;
    switch (e.kind) {
      case "cell":
        pendingNav.set({ kind: "cell", id: e.id });
        navigate("/cluster");
        break;
      case "node":
        pendingNav.set({ kind: "node", id: e.id });
        navigate("/nodes");
        break;
      case "player":
        pendingNav.set({ kind: "player", username: e.username });
        navigate("/players");
        break;
    }
    onClose();
  }

  function onKey(e: KeyboardEvent) {
    if (!open) return;
    if (e.key === "Escape") {
      e.preventDefault();
      onClose();
      return;
    }
    if (e.key === "ArrowDown") {
      e.preventDefault();
      activeIdx = Math.min(visible.length - 1, activeIdx + 1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      activeIdx = Math.max(0, activeIdx - 1);
    } else if (e.key === "Enter") {
      e.preventDefault();
      pick(activeIdx);
    }
  }

  function kindBadge(kind: Entry["kind"]): string {
    switch (kind) {
      case "cell":
        return "bg-amber-500/15 text-amber-300";
      case "node":
        return "bg-sky-500/15 text-sky-300";
      case "player":
        return "bg-emerald-500/15 text-emerald-300";
    }
  }
</script>

<svelte:window onkeydown={onKey} />

{#if open}
  <div class="fixed inset-0 z-50 flex items-start justify-center pt-24 bg-black/60">
    <div class="w-[640px] max-h-[70vh] flex flex-col bg-[#0d1117] border border-white/10 rounded-lg shadow-2xl">
      <div class="border-b border-white/10 p-3">
        <input
          bind:this={inputEl}
          type="text"
          placeholder="Find cell / node / player… (⌘K)"
          class="w-full bg-black/40 border border-white/10 rounded px-3 py-2 text-[13px] text-slate-200 placeholder-slate-500 focus:outline-none focus:border-accent-300/50 font-mono"
          bind:value={query}
          oninput={() => (activeIdx = 0)}
        />
      </div>
      <div class="overflow-y-auto p-1">
        {#if visible.length === 0}
          <div class="px-3 py-3 text-slate-500 text-[12px]">
            {query ? "No matches." : "Nothing live yet."}
          </div>
        {/if}
        {#each visible as v, i (v.entry.kind + ":" + (v.entry.kind === "player" ? v.entry.username : v.entry.id))}
          <button
            type="button"
            class="w-full text-left px-3 py-1.5 rounded {i === activeIdx ? 'bg-accent-300/15' : 'hover:bg-white/5'}"
            onclick={() => pick(i)}
            onmouseenter={() => (activeIdx = i)}
          >
            <div class="flex items-baseline justify-between gap-2">
              <span class="font-mono text-[12.5px] text-slate-100">{v.entry.label}</span>
              <span class="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded {kindBadge(v.entry.kind)}">
                {v.entry.kind}
              </span>
            </div>
            <div class="text-[11px] text-slate-400 truncate">{v.entry.sublabel}</div>
          </button>
        {/each}
      </div>
    </div>
  </div>
{/if}
