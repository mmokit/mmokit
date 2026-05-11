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
  <div
    class="flex items-center gap-2 bg-ember-400/[0.06] border border-ember-400/30 text-ember-300 px-3 py-1 rounded font-mono text-[10.5px] tracking-tight"
  >
    <AlertTriangle class="w-3 h-3 shrink-0 text-ember-400" />
    <span class="text-[var(--text-dim)] uppercase tracking-[0.12em]">violation</span>
    <span class="text-ember-200">
      {a.scenario}<span class="text-ember-400/50 mx-0.5">/</span>{a.step}
    </span>
    {#if a.error}
      <span class="text-[var(--text-default)]">— {a.error}</span>
    {/if}
    <button
      type="button"
      class="opacity-60 hover:opacity-100 ml-1"
      aria-label="Dismiss"
      onclick={() => {
        dismissed = new Set([...dismissed, key(a.seqNo, a.commitId)]);
      }}
    >
      <Close class="w-3 h-3" />
    </button>
  </div>
{/each}
