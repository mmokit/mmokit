<script lang="ts">
  import type { Snippet } from "svelte";
  import { Close } from "$lib/icons";

  type Props = {
    open: boolean;
    title: string;
    confirmLabel?: string;
    cancelLabel?: string;
    danger?: boolean;
    children?: Snippet;
    onConfirm: () => void;
    onCancel: () => void;
  };
  let {
    open,
    title,
    confirmLabel = "Confirm",
    cancelLabel = "Cancel",
    danger = false,
    children,
    onConfirm,
    onCancel,
  }: Props = $props();

  function onKey(e: KeyboardEvent) {
    if (!open) return;
    if (e.key === "Escape") onCancel();
    if (e.key === "Enter") onConfirm();
  }
</script>

<svelte:window onkeydown={onKey} />

{#if open}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
    <div class="w-[420px] bg-[#0d1117] border border-white/10 rounded-lg shadow-2xl">
      <header class="flex items-center justify-between border-b border-white/5 px-4 py-2">
        <h3 class="text-[13px] font-semibold text-slate-100">{title}</h3>
        <button
          type="button"
          class="text-slate-500 hover:text-slate-200"
          aria-label="Close"
          onclick={onCancel}
        >
          <Close class="w-4 h-4" />
        </button>
      </header>
      <div class="px-4 py-3 text-[12px] text-slate-300">
        {#if children}{@render children()}{/if}
      </div>
      <footer class="flex justify-end gap-2 border-t border-white/5 px-4 py-2">
        <button
          type="button"
          class="px-3 py-1 text-[11.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10"
          onclick={onCancel}
        >
          {cancelLabel}
        </button>
        <button
          type="button"
          class="px-3 py-1 text-[11.5px] {danger
            ? 'bg-rose-500 hover:bg-rose-600 text-slate-50'
            : 'bg-accent-400 hover:bg-accent-500 text-slate-950'} font-semibold rounded"
          onclick={onConfirm}
        >
          {confirmLabel}
        </button>
      </footer>
    </div>
  </div>
{/if}
