<script lang="ts">
  import { worldStore, type Tool } from "$lib/world-store.svelte";

  type ToolDef = {
    id: Tool;
    label: string;
    hotkey: string;
    color: string;
    glyph: string;
  };

  const tools: ToolDef[] = [
    { id: "select",     label: "Select",     hotkey: "V", color: "var(--phosphor-300)",   glyph: "↖" },
    { id: "station",    label: "Station",    hotkey: "1", color: "rgb(103, 232, 238)",    glyph: "◈" },
    { id: "poi",        label: "POI",        hotkey: "2", color: "rgb(244, 114, 182)",    glyph: "✦" },
    { id: "dungeon",    label: "Dungeon",    hotkey: "3", color: "rgb(251, 146, 60)",     glyph: "◊" },
    { id: "belt",       label: "Belt",       hotkey: "4", color: "rgb(163, 230, 53)",     glyph: "◯" },
    { id: "region",     label: "Region",     hotkey: "5", color: "rgb(180, 158, 250)",    glyph: "▢" },
    { id: "decoration", label: "Decoration", hotkey: "6", color: "rgb(232, 237, 247)",    glyph: "✺" },
  ];

  type LayerKey = keyof typeof worldStore.layers;
  const layerRows: { key: LayerKey; label: string }[] = [
    { key: "cells",       label: "Cells" },
    { key: "tiers",       label: "Tiers" },
    { key: "grid",        label: "Grid" },
    { key: "stations",    label: "Stations" },
    { key: "pois",        label: "POIs" },
    { key: "dungeons",    label: "Dungeons" },
    { key: "belts",       label: "Belts" },
    { key: "regions",     label: "Regions" },
    { key: "decorations", label: "Decorations" },
  ];

  function selectTool(t: Tool): void {
    worldStore.tool = t;
  }

  function toggleLayer(k: LayerKey): void {
    worldStore.layers = { ...worldStore.layers, [k]: !worldStore.layers[k] };
  }
</script>

<aside
  class="w-[200px] shrink-0 border-r border-[var(--border-subtle)] bg-[var(--bg-deep)]/70 flex flex-col text-[12px] overflow-y-auto"
>
  <!-- Tool palette -->
  <div class="px-3 pt-3 pb-2">
    <div class="font-mono text-[9.5px] text-[var(--text-dim)] tracking-[0.2em] uppercase mb-1.5">
      Tools
    </div>
    <div class="flex flex-col gap-0.5">
      {#each tools as t (t.id)}
        {@const active = worldStore.tool === t.id}
        <button
          type="button"
          onclick={() => selectTool(t.id)}
          class="group flex items-center gap-2 px-2 py-1.5 rounded transition-colors
            {active
              ? 'bg-phosphor-300/[0.08] text-phosphor-300 border-l-2 border-phosphor-400'
              : 'text-[var(--text-default)] hover:bg-white/[0.025] hover:text-[var(--text-bright)] border-l-2 border-transparent'}"
        >
          <span
            class="w-4 text-center font-mono text-[14px] leading-none"
            style:color={active ? t.color : "var(--text-muted)"}
          >{t.glyph}</span>
          <span class="grow text-left">{t.label}</span>
          <kbd
            class="font-mono text-[9.5px] px-1 py-px rounded
              {active ? 'bg-phosphor-300/15 text-phosphor-300' : 'bg-white/[0.04] text-[var(--text-dim)]'}"
          >{t.hotkey}</kbd>
        </button>
      {/each}
    </div>
  </div>

  <!-- Layers -->
  <div class="px-3 pt-3 pb-3 border-t border-[var(--border-faint)]">
    <div class="font-mono text-[9.5px] text-[var(--text-dim)] tracking-[0.2em] uppercase mb-1.5">
      Layers
    </div>
    <div class="flex flex-col gap-0.5">
      {#each layerRows as r (r.key)}
        {@const on = worldStore.layers[r.key]}
        <button
          type="button"
          onclick={() => toggleLayer(r.key)}
          class="flex items-center gap-2 px-2 py-1 rounded transition-colors text-left
            {on
              ? 'text-[var(--text-bright)] hover:bg-white/[0.025]'
              : 'text-[var(--text-dim)] hover:bg-white/[0.025]'}"
        >
          <span
            class="w-3 h-3 rounded-sm border flex items-center justify-center
              {on
                ? 'bg-phosphor-300/20 border-phosphor-300/60'
                : 'bg-transparent border-[var(--border-subtle)]'}"
          >
            {#if on}
              <span class="w-1.5 h-1.5 rounded-sm bg-phosphor-300"></span>
            {/if}
          </span>
          <span class="grow text-[11.5px]">{r.label}</span>
        </button>
      {/each}
    </div>
  </div>

  <!-- Stats -->
  <div class="mt-auto px-3 py-3 border-t border-[var(--border-faint)] font-mono text-[10px] text-[var(--text-dim)]">
    <div class="flex justify-between"><span>stations</span><span class="text-[var(--text-default)]">{worldStore.snapshot.stations.length}</span></div>
    <div class="flex justify-between"><span>pois</span><span class="text-[var(--text-default)]">{worldStore.snapshot.pois.length}</span></div>
    <div class="flex justify-between"><span>dungeons</span><span class="text-[var(--text-default)]">{worldStore.snapshot.dungeons.length}</span></div>
    <div class="flex justify-between"><span>belts</span><span class="text-[var(--text-default)]">{worldStore.snapshot.belts.length}</span></div>
    <div class="flex justify-between"><span>decor</span><span class="text-[var(--text-default)]">{worldStore.snapshot.decorations.length}</span></div>
  </div>
</aside>
