<script lang="ts">
  import { paletteOpen } from "$lib/stores.svelte";

  let open = $state(false);

  function pick(verb: string) {
    open = false;
    paletteOpen.openAt(verb);
  }

  function onWindowClick(e: MouseEvent) {
    if (!open) return;
    const root = (e.target as HTMLElement).closest("[data-cluster-ops]");
    if (!root) open = false;
  }
</script>

<svelte:window onclick={onWindowClick} />

<div class="relative" data-cluster-ops>
  <button
    type="button"
    class="px-2 py-0.5 rounded border border-white/10 bg-white/5 text-slate-300 hover:bg-white/10"
    onclick={() => (open = !open)}
    title="Cluster ops"
  >
    ops ▾
  </button>
  {#if open}
    <div
      class="absolute right-0 top-full mt-1 w-44 bg-[#0d1117] border border-white/10 rounded-lg shadow-2xl py-1 z-40 text-[12px]"
    >
      <button class="w-full text-left px-3 py-1 hover:bg-white/5" onclick={() => pick("cell.split")}>
        Split cell…
      </button>
      <button class="w-full text-left px-3 py-1 hover:bg-white/5" onclick={() => pick("cell.merge")}>
        Merge cell…
      </button>
      <button class="w-full text-left px-3 py-1 hover:bg-white/5" onclick={() => pick("cell.migrate")}>
        Migrate cell…
      </button>
      <div class="my-1 border-t border-white/5"></div>
      <button class="w-full text-left px-3 py-1 hover:bg-white/5" onclick={() => pick("host.drain")}>
        Drain host…
      </button>
      <button class="w-full text-left px-3 py-1 hover:bg-rose-500/10 text-rose-300" onclick={() => pick("host.kill")}>
        Kill host (sim crash)…
      </button>
    </div>
  {/if}
</div>
