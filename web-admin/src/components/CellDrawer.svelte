<script lang="ts">
  import type { CellInfo } from "$lib/types";
  import { fmtBytes, fmtUsAsMs, fmtLoad } from "$lib/format";
  import { apiPost, ApiError } from "$lib/api";
  import { Close } from "$lib/icons";

  type Props = {
    cell: CellInfo | null;
    onClose: () => void;
    onAction?: (action: string, ok: boolean, message: string) => void;
  };
  let { cell, onClose, onAction }: Props = $props();

  let busy = $state(false);

  async function invoke(verb: string, args: Record<string, unknown>) {
    if (!cell || busy) return;
    busy = true;
    try {
      await apiPost(`/admin/api/commands/${verb}`, args);
      onAction?.(verb, true, `${verb} ok`);
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message;
      onAction?.(verb, false, msg);
    } finally {
      busy = false;
    }
  }
</script>

{#if cell}
  <aside class="w-72 shrink-0 bg-[#0a0e14] border-l border-white/10 p-4 overflow-y-auto">
    <div class="flex items-center justify-between mb-3">
      <div class="text-accent-300 font-mono">{cell.id}</div>
      <button class="text-slate-500 hover:text-slate-200" aria-label="Close" onclick={onClose}>
        <Close class="w-4 h-4" />
      </button>
    </div>

    <dl class="text-[12px] space-y-1.5 mb-4">
      <div class="flex justify-between"><dt class="text-slate-500">Host</dt><dd>{cell.hostId || "—"}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Depth</dt><dd>{cell.depth}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Load</dt><dd>{fmtLoad(cell.load)}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Tick p99</dt><dd>{fmtUsAsMs(cell.tickP99Us)}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Tick p95</dt><dd>{fmtUsAsMs(cell.tickP95Us)}</dd></div>
    </dl>

    <div class="text-[10.5px] uppercase text-slate-500 tracking-wide mb-1.5">Entities</div>
    <dl class="text-[12px] space-y-1.5 mb-4">
      <div class="flex justify-between"><dt class="text-slate-500">Real</dt><dd>{cell.entities.real}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Replica</dt><dd>{cell.entities.replica}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Ghost</dt><dd>{cell.entities.ghost}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Sessions</dt><dd>{cell.entities.connected}</dd></div>
    </dl>

    <div class="text-[10.5px] uppercase text-slate-500 tracking-wide mb-1.5">Bytes (cumulative)</div>
    <dl class="text-[12px] space-y-1.5 mb-4">
      <div class="flex justify-between"><dt class="text-slate-500">Sent</dt><dd>{fmtBytes(cell.bytes.sent)}</dd></div>
      <div class="flex justify-between"><dt class="text-slate-500">Recv</dt><dd>{fmtBytes(cell.bytes.recv)}</dd></div>
    </dl>

    {#if cell.neighbors && cell.neighbors.length > 0}
      <div class="text-[10.5px] uppercase text-slate-500 tracking-wide mb-1.5">Neighbors</div>
      <div class="text-[12px] text-slate-400 mb-4 font-mono">{cell.neighbors.join(", ")}</div>
    {/if}

    <div class="text-[10.5px] uppercase text-slate-500 tracking-wide mb-1.5">Actions</div>
    <div class="flex gap-2 flex-wrap">
      <button
        class="px-2 py-1 text-[11.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
        disabled={busy}
        onclick={() => invoke("cell.split", { CellID: cell!.id })}
      >
        split
      </button>
      <button
        class="px-2 py-1 text-[11.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
        disabled={busy}
        onclick={() => invoke("cell.merge", { CellID: cell!.id })}
      >
        merge
      </button>
      <button
        class="px-2 py-1 text-[11.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
        disabled={busy}
        onclick={() => {
          const target = window.prompt("Target hostID for migrate:");
          if (target) invoke("cell.migrate", { CellID: cell!.id, HostID: target });
        }}
      >
        migrate
      </button>
    </div>
  </aside>
{/if}
