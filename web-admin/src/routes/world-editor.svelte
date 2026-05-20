<script lang="ts">
  import { onMount } from "svelte";
  import { sessionStore } from "$lib/stores.svelte";
  import { worldStore } from "$lib/world-store.svelte";
  import WorldPalette from "../components/WorldPalette.svelte";
  import WorldCanvas from "../components/WorldCanvas.svelte";
  import WorldInspector from "../components/WorldInspector.svelte";

  // RBAC gate. world.* verbs declare Capability: "world.edit"; we mirror
  // that on the client so the route degrades gracefully for operators
  // without the grant. Wildcard "*.*" also opens the gate.
  let session = $derived(sessionStore.value);
  let allowed = $derived.by(() => {
    const grants = session?.grants ?? [];
    return grants.includes("world.edit") || grants.includes("*.*");
  });

  let live = $state(false);
  let loadError = $state<string | null>(null);

  onMount(() => {
    if (!allowed) return;
    void (async () => {
      try {
        await worldStore.refresh();
        loadError = null;
      } catch (e) {
        loadError = (e as Error).message ?? "world.list failed";
      }
    })();
    const off = worldStore.subscribeLive();
    live = true;
    return () => {
      off();
      live = false;
    };
  });
</script>

{#if !allowed}
  <div class="h-full flex items-center justify-center p-8">
    <div
      class="max-w-md text-center border border-[var(--border-subtle)] rounded p-6 bg-[var(--bg-elevated)]/80"
    >
      <div class="font-mono text-[11px] text-ember-300 tracking-[0.18em] uppercase mb-2">
        access denied
      </div>
      <div class="text-[12px] text-[var(--text-default)] mb-1">
        World editor requires the
        <code class="text-phosphor-300">world.edit</code> grant.
      </div>
      <div class="text-[11px] text-[var(--text-muted)]">
        Ask an admin to grant it via the Users page.
      </div>
    </div>
  </div>
{:else}
  <div class="h-full flex flex-col">
    <!-- Top bar -->
    <div
      class="shrink-0 flex items-center gap-3 px-3 py-2 border-b border-[var(--border-subtle)] bg-[var(--bg-deep)]/60"
    >
      <div class="font-mono text-[12px] font-semibold text-phosphor-300 tracking-tight">
        WORLD EDITOR
      </div>
      <div class="font-mono text-[9.5px] text-[var(--text-dim)] tracking-[0.16em] uppercase">
        manifest · live
      </div>
      <div class="grow"></div>
      <div class="flex items-center gap-1.5 font-mono text-[9.5px] text-[var(--text-muted)] tracking-tight">
        <span
          class="w-1.5 h-1.5 rounded-full {live
            ? 'bg-lime-400 breathe'
            : 'bg-[var(--text-dim)]'}"
        ></span>
        <span class="uppercase tracking-[0.16em]">{live ? "stream" : "offline"}</span>
      </div>
    </div>

    {#if loadError}
      <div class="shrink-0 px-3 py-1.5 bg-ember-400/10 border-b border-ember-400/30 font-mono text-[11px] text-ember-300">
        load failed: {loadError}
      </div>
    {/if}

    <!-- Main three-pane area -->
    <div class="grow min-h-0 flex">
      <WorldPalette />
      <WorldCanvas />
      <WorldInspector />
    </div>

    <!-- Status bar -->
    <div
      class="shrink-0 flex items-center gap-4 px-3 py-1.5 border-t border-[var(--border-subtle)] bg-[var(--bg-deep)]/80 font-mono text-[10px] text-[var(--text-muted)] tabular-nums"
    >
      <div>
        <span class="text-[var(--text-dim)]">tool</span>
        <span class="ml-1.5 text-phosphor-300 uppercase tracking-[0.1em]">{worldStore.tool}</span>
      </div>
      <div>
        <span class="text-[var(--text-dim)]">cursor</span>
        <span class="ml-1.5 text-[var(--text-default)]">
          {worldStore.cursor[0].toFixed(0)},
          {worldStore.cursor[1].toFixed(0)}
        </span>
      </div>
      <div>
        <span class="text-[var(--text-dim)]">zoom</span>
        <span class="ml-1.5 text-[var(--text-default)]">{(worldStore.zoom * 100).toFixed(2)}%</span>
      </div>
      <div class="grow"></div>
      <div class="flex gap-3">
        <span><span class="text-[var(--text-dim)]">stations</span> <span class="text-phosphor-300">{worldStore.snapshot.stations.length}</span></span>
        <span><span class="text-[var(--text-dim)]">pois</span> <span class="text-plasma-300">{worldStore.snapshot.pois.length}</span></span>
        <span><span class="text-[var(--text-dim)]">dungeons</span> <span class="text-amber-300">{worldStore.snapshot.dungeons.length}</span></span>
        <span><span class="text-[var(--text-dim)]">belts</span> <span class="text-lime-300">{worldStore.snapshot.belts.length}</span></span>
        <span><span class="text-[var(--text-dim)]">decor</span> <span class="text-[var(--text-default)]">{worldStore.snapshot.decorations.length}</span></span>
      </div>
    </div>
  </div>
{/if}
