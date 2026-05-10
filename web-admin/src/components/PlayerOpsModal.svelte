<script lang="ts">
  import { apiPost, ApiError } from "$lib/api";
  import { Loader2 } from "$lib/icons";
  import ConfirmDialog from "./ConfirmDialog.svelte";

  type Op = "kick" | "tp" | "tpto" | null;

  type Props = {
    op: Op;
    username: string;
    onClose: () => void;
    onResult: (ok: boolean, message: string) => void;
  };
  let { op, username, onClose, onResult }: Props = $props();

  let busy = $state(false);
  let error = $state("");

  // tp form state
  let x = $state(0);
  let y = $state(0);

  // tpto form state
  let target = $state("");

  async function invoke(verb: string, args: Record<string, unknown>) {
    if (busy) return;
    busy = true;
    error = "";
    try {
      await apiPost(`/admin/api/commands/${verb}`, args);
      onResult(true, `${verb} ok (${username})`);
      onClose();
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message;
      error = msg;
      onResult(false, msg);
    } finally {
      busy = false;
    }
  }

  function submitTp(e: SubmitEvent) {
    e.preventDefault();
    void invoke("player.tp", { Username: username, X: x, Y: y });
  }
  function submitTpto(e: SubmitEvent) {
    e.preventDefault();
    if (!target.trim()) return;
    void invoke("player.tpto", { Username: username, Target: target.trim() });
  }
  function confirmKick() {
    void invoke("player.kick", { Username: username });
  }
</script>

{#if op === "kick"}
  <ConfirmDialog
    open={true}
    title="Kick {username}"
    confirmLabel={busy ? "Kicking…" : "Kick"}
    danger
    onConfirm={confirmKick}
    onCancel={onClose}
  >
    {#snippet children()}
      <p>Disconnect the player's session. They'll be returned to login.</p>
      {#if error}<p class="mt-2 text-rose-300">{error}</p>{/if}
    {/snippet}
  </ConfirmDialog>
{:else if op === "tp" || op === "tpto"}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
    <div class="w-[420px] bg-[#0d1117] border border-white/10 rounded-lg shadow-2xl">
      <header class="border-b border-white/5 px-4 py-2">
        <h3 class="text-[13px] font-semibold text-slate-100">
          {op === "tp" ? "Teleport" : "Teleport to player"}: {username}
        </h3>
      </header>
      {#if op === "tp"}
        <form onsubmit={submitTp} class="px-4 py-3 space-y-3 text-[12px]">
          <div>
            <label class="block text-[11px] text-slate-500 mb-1" for="tpx">World X</label>
            <input
              id="tpx"
              type="number"
              step="any"
              class="w-full bg-black/40 border border-white/10 rounded px-2 py-1 text-slate-200 focus:outline-none focus:border-accent-300/50"
              bind:value={x}
              required
            />
          </div>
          <div>
            <label class="block text-[11px] text-slate-500 mb-1" for="tpy">World Y</label>
            <input
              id="tpy"
              type="number"
              step="any"
              class="w-full bg-black/40 border border-white/10 rounded px-2 py-1 text-slate-200 focus:outline-none focus:border-accent-300/50"
              bind:value={y}
              required
            />
          </div>
          {#if error}<div class="text-rose-300">{error}</div>{/if}
          <div class="flex justify-end gap-2 pt-1">
            <button type="button" class="px-3 py-1 bg-white/5 border border-white/10 rounded" onclick={onClose}>
              Cancel
            </button>
            <button
              type="submit"
              disabled={busy}
              class="px-3 py-1 bg-accent-400 hover:bg-accent-500 text-slate-950 font-semibold rounded flex items-center gap-1 disabled:opacity-50"
            >
              {#if busy}<Loader2 class="w-3 h-3 animate-spin" />{/if}
              Teleport
            </button>
          </div>
        </form>
      {:else}
        <form onsubmit={submitTpto} class="px-4 py-3 space-y-3 text-[12px]">
          <div>
            <label class="block text-[11px] text-slate-500 mb-1" for="tptarget">Destination player</label>
            <input
              id="tptarget"
              type="text"
              class="w-full bg-black/40 border border-white/10 rounded px-2 py-1 text-slate-200 focus:outline-none focus:border-accent-300/50"
              bind:value={target}
              required
            />
          </div>
          {#if error}<div class="text-rose-300">{error}</div>{/if}
          <div class="flex justify-end gap-2 pt-1">
            <button type="button" class="px-3 py-1 bg-white/5 border border-white/10 rounded" onclick={onClose}>
              Cancel
            </button>
            <button
              type="submit"
              disabled={busy}
              class="px-3 py-1 bg-accent-400 hover:bg-accent-500 text-slate-950 font-semibold rounded flex items-center gap-1 disabled:opacity-50"
            >
              {#if busy}<Loader2 class="w-3 h-3 animate-spin" />{/if}
              Teleport
            </button>
          </div>
        </form>
      {/if}
    </div>
  </div>
{/if}
