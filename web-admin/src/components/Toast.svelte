<script lang="ts">
  type ToastEntry = { id: number; ok: boolean; msg: string };

  type Props = {
    entries: ToastEntry[];
  };
  let { entries }: Props = $props();
</script>

<div class="fixed bottom-4 right-4 z-40 flex flex-col gap-2 text-[12px]">
  {#each entries as t (t.id)}
    <div
      class="px-3 py-1.5 rounded shadow-xl border {t.ok
        ? 'bg-emerald-900/40 border-emerald-700/40 text-emerald-200'
        : 'bg-rose-900/40 border-rose-700/40 text-rose-200'}"
    >
      {t.msg}
    </div>
  {/each}
</div>
