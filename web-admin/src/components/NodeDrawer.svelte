<script lang="ts">
  import { Close } from "$lib/icons";
  import { pendingNav, cellsStore } from "$lib/stores.svelte";
  import { navigate } from "$lib/router";
  import { fmtBytes, fmtDuration, fmtLoad } from "$lib/format";
  import type { HostInfo, GatewayInfo, CellInfo } from "$lib/types";

  type Node =
    | { kind: "host"; data: HostInfo }
    | { kind: "gateway"; data: GatewayInfo };

  type Props = {
    node: Node | null;
    onClose: () => void;
  };
  let { node, onClose }: Props = $props();

  let cells = $derived<CellInfo[]>(cellsStore.value ?? []);

  // For host detail, look up the live CellInfo for each owned cell so we
  // can show entity counts inline. cellsStore drives this — no extra fetch.
  let ownedCells = $derived.by<CellInfo[]>(() => {
    if (!node || node.kind !== "host") return [];
    const ids = new Set(node.data.cells ?? []);
    return cells.filter((c) => ids.has(c.id));
  });

  function gotoCell(id: string) {
    pendingNav.set({ kind: "cell", id });
    navigate("/cluster");
    onClose();
  }
</script>

{#if node}
  <aside class="w-[360px] shrink-0 bg-[#0a0e14] border-l border-white/5 flex flex-col">
    <header class="flex items-center justify-between border-b border-white/5 px-4 py-2">
      <div>
        <div class="text-[10px] uppercase tracking-wide text-slate-500">{node.kind}</div>
        <div class="font-mono text-slate-100 text-[13px]">
          {node.kind === "host" ? node.data.id : node.data.id}
        </div>
      </div>
      <button
        type="button"
        class="text-slate-500 hover:text-slate-200"
        aria-label="Close"
        onclick={onClose}
      >
        <Close class="w-4 h-4" />
      </button>
    </header>

    <div class="flex-1 overflow-auto p-4 space-y-3 text-[12px]">
      {#if node.kind === "host"}
        {@const h = node.data}
        <div class="grid grid-cols-[110px_1fr] gap-x-3 gap-y-1">
          <span class="text-slate-500">State</span>
          <span class="text-slate-200">{h.state}</span>
          <span class="text-slate-500">Where</span>
          <span class="text-slate-300">{h.isLocal ? "in-proc" : "remote"}</span>
          <span class="text-slate-500">Roles</span>
          <span class="font-mono text-slate-300">{(h.roles ?? []).join(", ") || "—"}</span>
          <span class="text-slate-500">HB age</span>
          <span class="text-slate-300">{h.isLocal ? "—" : fmtDuration(h.heartbeatAgeMs)}</span>
          <span class="text-slate-500">Load</span>
          <span class="text-slate-300">{fmtLoad(h.load)}</span>
          <span class="text-slate-500">Entities</span>
          <span class="text-slate-300">{h.totalEntities}</span>
        </div>

        <div>
          <div class="text-[10.5px] uppercase tracking-wide text-slate-500 mb-1">
            Owned cells ({(h.cells ?? []).length})
          </div>
          {#if (h.cells ?? []).length === 0}
            <div class="text-slate-500 italic">No cells owned.</div>
          {:else}
            <div class="space-y-0.5 max-h-[40vh] overflow-y-auto">
              {#each h.cells ?? [] as cellId (cellId)}
                {@const live = ownedCells.find((c) => c.id === cellId)}
                <button
                  type="button"
                  class="w-full text-left px-2 py-1 rounded hover:bg-white/5 flex items-center justify-between"
                  onclick={() => gotoCell(cellId)}
                >
                  <span class="font-mono text-slate-200">{cellId}</span>
                  <span class="text-[10.5px] text-slate-500">
                    {live ? `${live.entities.real} ent · ${fmtLoad(live.load)}` : "—"}
                  </span>
                </button>
              {/each}
            </div>
          {/if}
        </div>
      {:else}
        {@const g = node.data}
        <div class="grid grid-cols-[110px_1fr] gap-x-3 gap-y-1">
          <span class="text-slate-500">Where</span>
          <span class="text-slate-300">{g.isLocal ? "in-proc" : "remote"}</span>
          <span class="text-slate-500">Mode</span>
          <span class="font-mono text-slate-300">{g.mode || "—"}</span>
          <span class="text-slate-500">Sessions</span>
          <span class="text-slate-300">{g.sessions}</span>
          <span class="text-slate-500">Bytes sent</span>
          <span class="text-slate-300">{fmtBytes(g.bytesSent)}</span>
          <span class="text-slate-500">Bytes recv</span>
          <span class="text-slate-300">{fmtBytes(g.bytesRecv)}</span>
        </div>
        <p class="text-slate-500 text-[11.5px] italic">
          Per-gateway session list lands when SessionStore exposes a List
          method (Phase 2).
        </p>
      {/if}
    </div>
  </aside>
{/if}
