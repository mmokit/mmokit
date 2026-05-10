<script lang="ts">
  import { alertsStore } from "$lib/stores.svelte";
  import { AlertTriangle, Close } from "$lib/icons";

  let dismissed = $state(new Set<string>());

  function key(seqNo: number, commitId: string): string {
    return `${commitId}#${seqNo}`;
  }

  let visible = $derived(
    (alertsStore.value ?? [])
      .filter((a) => !dismissed.has(key(a.seqNo, a.commitId)))
      .slice(0, 1),
  );
</script>

{#each visible as a (key(a.seqNo, a.commitId))}
  <div class="flex items-center gap-2 bg-rose-900/30 border border-rose-700/40 text-rose-200 px-3 py-1.5 rounded text-[11.5px]">
    <AlertTriangle class="w-3.5 h-3.5 shrink-0" />
    <span class="grow">
      {a.scenario}/{a.step} — {a.error || "invariant violation"}
    </span>
    <button
      type="button"
      class="opacity-60 hover:opacity-100"
      aria-label="Dismiss"
      onclick={() => {
        dismissed = new Set([...dismissed, key(a.seqNo, a.commitId)]);
      }}
    >
      <Close class="w-3 h-3" />
    </button>
  </div>
{/each}
