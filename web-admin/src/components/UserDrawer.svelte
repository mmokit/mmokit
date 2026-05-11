<script lang="ts">
  import { Close } from "$lib/icons";
  import { apiPost, ApiError } from "$lib/api";
  import ConfirmDialog from "./ConfirmDialog.svelte";

  type Operator = {
    username: string;
    grants: string[];
    createdAt: string;
    updatedAt: string;
  };

  type Props = {
    operator: Operator | null;
    onClose: () => void;
    onMutated: (msg: string) => void;
  };
  let { operator, onClose, onMutated }: Props = $props();

  let rotating = $state(false);
  let rotateValue = $state("");
  let rotateError = $state("");

  let confirmDeleteOpen = $state(false);
  let deleteError = $state("");
  let busy = $state(false);

  // Reset internal state when the drawer's operator swaps.
  $effect(() => {
    void operator?.username;
    rotating = false;
    rotateValue = "";
    rotateError = "";
    confirmDeleteOpen = false;
    deleteError = "";
    busy = false;
  });

  async function submitRotate(e: Event) {
    e.preventDefault();
    if (!operator || busy) return;
    const pwd = rotateValue.trim();
    if (pwd === "") {
      rotateError = "password required";
      return;
    }
    busy = true;
    rotateError = "";
    try {
      const res = await apiPost<{ ok: boolean; error?: string }>(
        "/admin/api/commands/admin.operator.password",
        { Username: operator.username, Password: pwd },
      );
      if (res.ok === false) {
        rotateError = res.error || "rotate failed";
        return;
      }
      rotating = false;
      rotateValue = "";
      onMutated(`${operator.username}: password rotated`);
    } catch (e) {
      rotateError = e instanceof ApiError ? e.message : (e as Error).message;
    } finally {
      busy = false;
    }
  }

  async function confirmDelete() {
    if (!operator || busy) return;
    busy = true;
    deleteError = "";
    try {
      const res = await apiPost<{ ok: boolean; error?: string }>(
        "/admin/api/commands/admin.operator.delete",
        { Username: operator.username },
      );
      if (res.ok === false) {
        deleteError = res.error || "delete failed";
        confirmDeleteOpen = false;
        return;
      }
      confirmDeleteOpen = false;
      onMutated(`${operator.username}: deleted`);
    } catch (e) {
      deleteError = e instanceof ApiError ? e.message : (e as Error).message;
      confirmDeleteOpen = false;
    } finally {
      busy = false;
    }
  }

  function formatTimestamp(s: string): string {
    if (!s) return "—";
    const t = new Date(s);
    if (Number.isNaN(t.getTime())) return s;
    return t.toLocaleString(undefined, { hour12: false });
  }
</script>

{#if operator}
  <aside class="w-[400px] shrink-0 bg-[#0a0e14] border-l border-white/5 flex flex-col">
    <header class="flex items-center justify-between border-b border-white/5 px-4 py-2">
      <div>
        <div class="text-[10px] uppercase tracking-wide text-slate-500">operator</div>
        <div class="font-mono text-slate-100 text-[13px]">{operator.username}</div>
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
        <span class="text-slate-500">Grants</span>
        <span class="font-mono text-slate-300">{operator.grants.join(", ") || "—"}</span>
        <span class="text-slate-500">Created</span>
        <span class="text-slate-300 tabular-nums">{formatTimestamp(operator.createdAt)}</span>
        <span class="text-slate-500">Updated</span>
        <span class="text-slate-300 tabular-nums">{formatTimestamp(operator.updatedAt)}</span>
      </div>

      {#if !rotating}
        <div class="flex flex-wrap gap-2 pt-1">
          <button
            type="button"
            class="px-2 py-0.5 text-[11px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
            disabled={busy}
            onclick={() => { rotating = true; rotateError = ""; }}
          >
            rotate password
          </button>
          <button
            type="button"
            class="px-2 py-0.5 text-[11px] bg-rose-500/15 border border-rose-500/30 text-rose-200 rounded hover:bg-rose-500/25 disabled:opacity-50"
            disabled={busy}
            onclick={() => { confirmDeleteOpen = true; deleteError = ""; }}
          >
            delete
          </button>
        </div>
      {:else}
        <form class="space-y-2 pt-1" onsubmit={submitRotate}>
          <label class="block text-[11.5px]">
            <span class="text-[var(--text-muted)] font-mono">New password</span>
            <input
              type="password"
              autocomplete="new-password"
              class="mt-1 w-full bg-white/5 border border-[var(--border-subtle)] rounded px-2 py-1 text-[var(--text-bright)] font-mono focus:outline-none focus:border-[var(--border-phosphor)]"
              bind:value={rotateValue}
            />
          </label>
          {#if rotateError}
            <div class="text-rose-300 text-[11.5px]">{rotateError}</div>
          {/if}
          <div class="flex gap-2">
            <button
              type="submit"
              class="px-2.5 py-0.5 text-[11px] bg-accent-400/15 border border-accent-400/40 text-accent-200 rounded hover:bg-accent-400/25 disabled:opacity-50"
              disabled={busy}
            >
              {busy ? "saving…" : "save"}
            </button>
            <button
              type="button"
              class="px-2.5 py-0.5 text-[11px] bg-white/5 border border-white/10 rounded hover:bg-white/10"
              onclick={() => { rotating = false; rotateValue = ""; rotateError = ""; }}
              disabled={busy}
            >
              cancel
            </button>
          </div>
        </form>
      {/if}

      {#if deleteError}
        <div class="text-rose-300 text-[11.5px]">{deleteError}</div>
      {/if}
    </div>

    <ConfirmDialog
      open={confirmDeleteOpen}
      title="Delete {operator.username}?"
      confirmLabel="Delete"
      danger
      onConfirm={confirmDelete}
      onCancel={() => (confirmDeleteOpen = false)}
    >
      Deleting an operator is permanent. This action cannot be undone.
    </ConfirmDialog>
  </aside>
{/if}
