<script lang="ts">
  import { Close } from "$lib/icons";
  import { apiPost, ApiError } from "$lib/api";
  import { pendingNav } from "$lib/stores.svelte";
  import { navigate } from "$lib/router";
  import PlayerOpsModal from "./PlayerOpsModal.svelte";
  import type { PlayerInfo } from "$lib/types";

  type Props = {
    player: PlayerInfo | null;
    onClose: () => void;
    onResult: (ok: boolean, msg: string) => void;
  };
  let { player, onClose, onResult }: Props = $props();

  type Op = "kick" | "tp" | "tpto" | null;
  let modalOp = $state<Op>(null);

  let infoLoading = $state(false);
  let infoError = $state("");
  let infoPayload = $state<unknown>(null);

  // Reset detail-load state when the drawer's player swaps.
  $effect(() => {
    void player?.username;
    infoLoading = false;
    infoError = "";
    infoPayload = null;
    modalOp = null;
  });

  async function loadInfo() {
    if (!player || infoLoading) return;
    infoLoading = true;
    infoError = "";
    infoPayload = null;
    try {
      const res = await apiPost<{ ok: boolean; result?: unknown; error?: string }>(
        "/admin/api/commands/player.info",
        { Username: player.username },
      );
      if (res.ok === false) {
        infoError = res.error || "command failed";
      } else {
        infoPayload = res.result;
      }
    } catch (e) {
      infoError = e instanceof ApiError ? e.message : (e as Error).message;
    } finally {
      infoLoading = false;
    }
  }

  function gotoCell(id: string) {
    pendingNav.set({ kind: "cell", id });
    navigate("/cluster");
    onClose();
  }
</script>

{#if player}
  <aside class="w-[400px] shrink-0 bg-[#0a0e14] border-l border-white/5 flex flex-col">
    <header class="flex items-center justify-between border-b border-white/5 px-4 py-2">
      <div>
        <div class="text-[10px] uppercase tracking-wide text-slate-500">player</div>
        <div class="font-mono text-slate-100 text-[13px]">{player.username}</div>
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
      <div class="grid grid-cols-[110px_1fr] gap-x-3 gap-y-1">
        <span class="text-slate-500">Status</span>
        <span class="{player.status === 'online' ? 'text-emerald-300' : 'text-slate-400'}">
          {player.status === "online" ? "● online" : "○ offline"}
        </span>
        <span class="text-slate-500">Host</span>
        <span class="font-mono text-slate-300">{player.hostId ?? "—"}</span>
        <span class="text-slate-500">Cell</span>
        <span class="font-mono text-slate-300">
          {#if player.cellId}
            <button
              type="button"
              class="text-slate-200 hover:text-accent-300 underline-offset-2 hover:underline"
              onclick={() => gotoCell(player!.cellId!)}
            >{player.cellId}</button>
          {:else}—{/if}
        </span>
        <span class="text-slate-500">World</span>
        <span class="text-slate-300">
          {player.worldX != null && player.worldY != null && (player.worldX !== 0 || player.worldY !== 0)
            ? `(${player.worldX.toFixed(0)}, ${player.worldY.toFixed(0)})`
            : "—"}
        </span>
        <span class="text-slate-500">Last login</span>
        <span class="text-slate-300">
          {player.lastLogin && new Date(player.lastLogin).getTime() > 0
            ? new Date(player.lastLogin).toLocaleString(undefined, { hour12: false })
            : "—"}
        </span>
      </div>

      <div class="flex flex-wrap gap-2 pt-1">
        <button
          class="px-2 py-0.5 text-[11px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
          disabled={player.status !== "online"}
          onclick={() => (modalOp = "tp")}
        >tp</button>
        <button
          class="px-2 py-0.5 text-[11px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
          disabled={player.status !== "online"}
          onclick={() => (modalOp = "tpto")}
        >tpto</button>
        <button
          class="px-2 py-0.5 text-[11px] bg-rose-500/15 border border-rose-500/30 text-rose-200 rounded hover:bg-rose-500/25 disabled:opacity-50"
          disabled={player.status !== "online"}
          onclick={() => (modalOp = "kick")}
        >kick</button>
        <button
          class="px-2 py-0.5 text-[11px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50 ml-auto"
          disabled={infoLoading}
          onclick={() => void loadInfo()}
        >
          {infoLoading ? "loading…" : "load full info"}
        </button>
      </div>

      {#if infoError}
        <div class="text-rose-300 text-[11.5px]">{infoError}</div>
      {/if}
      {#if infoPayload != null}
        <pre class="text-[10.5px] text-slate-300 bg-black/40 border border-white/5 rounded p-2 overflow-auto max-h-[40vh]">{JSON.stringify(infoPayload, null, 2)}</pre>
      {/if}
    </div>

    <PlayerOpsModal
      op={modalOp}
      username={player.username}
      onClose={() => (modalOp = null)}
      onResult={(ok, msg) => onResult(ok, msg)}
    />
  </aside>
{/if}
