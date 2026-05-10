<script lang="ts">
  import { tick } from "svelte";
  import { apiGet } from "$lib/api";
  import { fuzzyScore, describe as describeVerb } from "$lib/commands";
  import type { CommandSummary, CommandDescribe } from "$lib/types";
  import CommandForm from "./CommandForm.svelte";

  type Props = {
    open: boolean;
    onClose: () => void;
    onResult: (ok: boolean, message: string, payload?: unknown) => void;
  };
  let { open, onClose, onResult }: Props = $props();

  let allCommands = $state<CommandSummary[]>([]);
  let query = $state("");
  let activeIdx = $state(0);
  let picked = $state<CommandDescribe | null>(null);
  let pickError = $state("");
  let inputEl: HTMLInputElement | undefined = $state();

  // Sorted, filtered list of visible commands.
  let visible = $derived.by<{ cmd: CommandSummary; score: number }[]>(() => {
    const out: { cmd: CommandSummary; score: number }[] = [];
    for (const c of allCommands) {
      if (c.hidden) continue;
      const s1 = fuzzyScore(c.verb, query);
      const s2 = fuzzyScore(c.description, query) * 0.3; // description matches weighted lower
      const s = Math.max(s1, s2);
      if (query && s === 0) continue;
      out.push({ cmd: c, score: s });
    }
    out.sort((a, b) => b.score - a.score);
    return out.slice(0, 50);
  });

  // Lazy fetch the catalog on first open.
  $effect(() => {
    if (!open) return;
    if (allCommands.length > 0) return;
    void (async () => {
      try {
        const list = await apiGet<CommandSummary[]>("/admin/api/commands");
        allCommands = list;
      } catch {
        allCommands = [];
      }
    })();
  });

  // Reset state + focus input on each open.
  $effect(() => {
    if (open) {
      query = "";
      activeIdx = 0;
      picked = null;
      pickError = "";
      void tick().then(() => inputEl?.focus());
    }
  });

  // Clamp activeIdx to visible bounds when the list changes.
  $effect(() => {
    if (activeIdx >= visible.length) activeIdx = Math.max(0, visible.length - 1);
  });

  async function pickByIndex(i: number) {
    const v = visible[i]?.cmd;
    if (!v) return;
    pickError = "";
    try {
      picked = await describeVerb(v.verb);
    } catch (e) {
      pickError = (e as Error).message;
    }
  }

  function backToList() {
    picked = null;
    pickError = "";
    void tick().then(() => inputEl?.focus());
  }

  function onKey(e: KeyboardEvent) {
    if (!open) return;
    if (e.key === "Escape") {
      e.preventDefault();
      if (picked) backToList();
      else onClose();
      return;
    }
    if (picked) return; // form owns Enter / arrows when in detail
    if (e.key === "ArrowDown") {
      e.preventDefault();
      activeIdx = Math.min(visible.length - 1, activeIdx + 1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      activeIdx = Math.max(0, activeIdx - 1);
    } else if (e.key === "Enter") {
      e.preventDefault();
      void pickByIndex(activeIdx);
    }
  }

  function onResultWrap(ok: boolean, message: string, payload?: unknown) {
    onResult(ok, message, payload);
    if (ok) {
      onClose();
    }
  }
</script>

<svelte:window onkeydown={onKey} />

{#if open}
  <div class="fixed inset-0 z-50 flex items-start justify-center pt-24 bg-black/60">
    <div class="w-[640px] max-h-[70vh] flex flex-col bg-[#0d1117] border border-white/10 rounded-lg shadow-2xl">
      {#if !picked}
        <div class="border-b border-white/10 p-3">
          <input
            bind:this={inputEl}
            type="text"
            placeholder="Search commands… (⌘K)"
            class="w-full bg-black/40 border border-white/10 rounded px-3 py-2 text-[13px] text-slate-200 placeholder-slate-500 focus:outline-none focus:border-accent-300/50 font-mono"
            bind:value={query}
            oninput={() => (activeIdx = 0)}
          />
        </div>
        <div class="overflow-y-auto p-1">
          {#if visible.length === 0}
            <div class="px-3 py-3 text-slate-500 text-[12px]">No matching commands.</div>
          {/if}
          {#each visible as v, i (v.cmd.verb)}
            <button
              type="button"
              class="w-full text-left px-3 py-1.5 rounded {i === activeIdx ? 'bg-accent-300/15' : 'hover:bg-white/5'}"
              onclick={() => pickByIndex(i)}
              onmouseenter={() => (activeIdx = i)}
            >
              <div class="flex items-baseline justify-between gap-2">
                <span class="font-mono text-[12.5px] text-slate-100">{v.cmd.verb}</span>
                <span class="text-[10.5px] text-slate-500">{v.cmd.route}</span>
              </div>
              {#if v.cmd.description}
                <div class="text-[11px] text-slate-400 truncate">{v.cmd.description}</div>
              {/if}
            </button>
          {/each}
        </div>
        {#if pickError}
          <div class="border-t border-white/10 p-2 text-rose-300 text-[11.5px]">{pickError}</div>
        {/if}
      {:else}
        <div class="border-b border-white/10 px-3 py-2 flex items-center justify-between">
          <span class="font-mono text-[13px] text-slate-100">{picked.verb}</span>
          <button
            type="button"
            class="text-[11px] text-slate-400 hover:text-slate-200"
            onclick={backToList}
          >
            ← back
          </button>
        </div>
        <div class="overflow-y-auto p-3">
          <CommandForm describe={picked} onCancel={backToList} onResult={onResultWrap} />
        </div>
      {/if}
    </div>
  </div>
{/if}
