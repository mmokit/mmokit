<script lang="ts">
  import { Circle, Command, Bell } from "$lib/icons";
  import { cellsStore, alertsStore, sessionStore, paletteOpen } from "$lib/stores.svelte";
  import AlertBanner from "./AlertBanner.svelte";

  // Derive counts from the live cells SSE stream so they tick automatically.
  // The one-shot /admin/api/cluster snapshot is fine for boot but doesn't update.
  let cells = $derived(cellsStore.value ?? []);
  let cellCount = $derived(cells.length);
  let entityCount = $derived(cells.reduce((sum, c) => sum + c.entities.real, 0));
  let sessionCount = $derived(cells.reduce((sum, c) => sum + c.entities.connected, 0));

  let alertCount = $derived((alertsStore.value ?? []).length);
  let user = $derived(sessionStore.value?.user ?? "");

  // Health: green if cells snapshot recent and no alerts; yellow if alerts; gray while loading.
  let health = $derived(alertCount > 0 ? "warn" : cellCount > 0 ? "ok" : "stale");
</script>

<header class="border-b border-white/5 bg-[#0a0e14] flex items-center gap-4 px-4 py-2 text-[12px]">
  <div class="text-slate-400">
    Cluster &rsaquo; <span class="text-slate-100 font-semibold">Cells</span>
  </div>
  <div class="grow flex items-center justify-center gap-3">
    <AlertBanner />
  </div>
  <div class="flex items-center gap-3 text-slate-400">
    <span class="flex items-center gap-1.5">
      <Circle
        class="w-2 h-2 fill-current {health === 'ok' ? 'text-emerald-500' : health === 'warn' ? 'text-amber-400' : 'text-slate-600'}"
      />
      {cellCount > 0
        ? `${cellCount} cells · ${entityCount} ent · ${sessionCount} sess`
        : "loading…"}
    </span>
    <button
      type="button"
      class="px-2 py-0.5 rounded border border-white/10 bg-white/5 text-slate-300 hover:bg-white/10 flex items-center gap-1"
      title="Command palette (⌘K)"
      onclick={() => paletteOpen.set(true)}
    >
      <Command class="w-3 h-3" /> ⌘K
    </button>
    <span class="relative inline-flex items-center text-rose-300" title="Active alerts">
      <Bell class="w-3.5 h-3.5" />
      {#if alertCount > 0}
        <span class="ml-1 text-[10px] font-semibold">{alertCount}</span>
      {/if}
    </span>
    {#if user}
      <span class="text-slate-300">{user}</span>
    {/if}
  </div>
</header>
