<script lang="ts">
  import { cellsStore } from "$lib/stores.svelte";
  import CellMap from "../components/CellMap.svelte";
  import CellDrawer from "../components/CellDrawer.svelte";
  import type { CellInfo } from "$lib/types";

  let selected = $state<string | null>(null);
  let colorMode = $state<"load" | "host" | "entities">("load");
  let toast = $state<{ msg: string; ok: boolean } | null>(null);

  let cells = $derived<CellInfo[]>(cellsStore.value ?? []);
  let selectedCell = $derived<CellInfo | null>(
    selected ? (cells.find((c) => c.id === selected) ?? null) : null,
  );

  const colorModes: Array<"load" | "host" | "entities"> = ["load", "host", "entities"];
</script>

<div class="h-full flex">
  <main class="grow flex flex-col p-4 gap-3 min-w-0">
    <div class="flex items-center justify-between">
      <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Live cell map</h2>
      <div class="flex items-center gap-2 text-[11px] text-slate-400">
        <span>color:</span>
        <div class="flex bg-white/5 border border-white/10 rounded overflow-hidden">
          {#each colorModes as m (m)}
            <button
              class="px-2 py-0.5 {colorMode === m ? 'bg-accent-300/20 text-accent-300' : 'hover:bg-white/5'}"
              onclick={() => (colorMode = m)}
            >{m}</button>
          {/each}
        </div>
      </div>
    </div>

    <div class="grow rounded-lg border border-accent-300/15 bg-gradient-to-br from-[#0f1a2e] to-[#0a1119] overflow-hidden min-h-[300px]">
      <CellMap
        {selected}
        {colorMode}
        onSelect={(id) => (selected = id)}
      />
    </div>

    {#if toast}
      <div
        class="text-[12px] px-3 py-1.5 rounded {toast.ok ? 'bg-emerald-900/30 text-emerald-200 border border-emerald-700/40' : 'bg-rose-900/30 text-rose-200 border border-rose-700/40'}"
      >
        {toast.msg}
      </div>
    {/if}
  </main>

  <CellDrawer
    cell={selectedCell}
    onClose={() => (selected = null)}
    onAction={(_verb, ok, msg) => {
      toast = { ok, msg };
      setTimeout(() => (toast = null), 4000);
    }}
  />
</div>
