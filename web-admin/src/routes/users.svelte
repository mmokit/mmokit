<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { apiGet, apiPost, ApiError } from "$lib/api";
  import type { Schema } from "$lib/types";
  import ArgsModal from "../components/ArgsModal.svelte";
  import UserDrawer from "../components/UserDrawer.svelte";

  // Wire shape from admin.operator.list. Mirrors
  // pkg/admin/operator_commands.go::adminOperatorListRow.
  type Operator = {
    username: string;
    grants: string[];
    createdAt: string;
    updatedAt: string;
  };
  type ListResp = { ok: boolean; result?: { operators?: Operator[] }; error?: string };
  type SchemaResp = { argsSchema: Schema };

  let operators = $state<Operator[]>([]);
  let loadError = $state("");
  let loading = $state(false);
  let drawer = $state<Operator | null>(null);

  let createOpen = $state(false);
  let createSchema = $state<Schema | null>(null);
  let toast = $state<{ msg: string; ok: boolean } | null>(null);

  async function refresh() {
    loading = true;
    loadError = "";
    try {
      const res = await apiPost<ListResp>("/admin/api/commands/admin.operator.list", {});
      if (res.ok === false) {
        loadError = res.error || "list failed";
        return;
      }
      operators = res.result?.operators ?? [];
    } catch (e) {
      loadError = e instanceof ApiError ? e.message : (e as Error).message;
    } finally {
      loading = false;
    }
  }

  async function openCreate() {
    try {
      if (!createSchema) {
        const s = await apiGet<SchemaResp>("/admin/api/commands/admin.operator.create");
        createSchema = s.argsSchema;
      }
      createOpen = true;
    } catch (e) {
      toast = { msg: (e as Error).message, ok: false };
    }
  }

  function onCreateResult(ok: boolean, msg: string) {
    toast = { ok, msg };
    setTimeout(() => (toast = null), 4000);
    if (ok) void refresh();
  }

  function onDrawerMutated(msg: string) {
    drawer = null;
    toast = { ok: true, msg };
    setTimeout(() => (toast = null), 4000);
    void refresh();
  }

  function formatTimestamp(s: string): string {
    if (!s) return "—";
    const t = new Date(s);
    if (Number.isNaN(t.getTime())) return s;
    return t.toLocaleString(undefined, { hour12: false });
  }

  let intervalId: number | undefined;
  onMount(() => {
    void refresh();
    intervalId = window.setInterval(() => void refresh(), 10_000);
  });
  onDestroy(() => {
    if (intervalId !== undefined) window.clearInterval(intervalId);
  });
</script>

<div class="h-full flex">
  <main class="grow p-4 space-y-3 min-w-0">
    <div class="flex items-center justify-between">
      <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Admin Users</h2>
      <button
        type="button"
        class="px-2.5 py-1 text-[11px] bg-accent-400/15 border border-accent-400/40 text-accent-200 rounded hover:bg-accent-400/25"
        onclick={openCreate}
      >
        Add operator
      </button>
    </div>

    <div class="bg-[#0d1117] border border-white/10 rounded-lg p-3">
      {#if loadError}
        <div class="text-rose-300 text-[12px]">{loadError}</div>
      {:else if loading && operators.length === 0}
        <div class="text-slate-500 text-[12px]">loading…</div>
      {:else}
        <table class="w-full text-[12px] border-collapse">
          <thead>
            <tr class="text-left text-[10.5px] uppercase tracking-wide text-slate-500 border-b border-white/10">
              <th class="py-1.5 px-2 font-medium" style="width:25%">Username</th>
              <th class="py-1.5 px-2 font-medium" style="width:35%">Grants</th>
              <th class="py-1.5 px-2 font-medium" style="width:20%">Created</th>
              <th class="py-1.5 px-2 font-medium" style="width:20%">Updated</th>
            </tr>
          </thead>
          <tbody>
            {#each operators as op (op.username)}
              <tr class="border-b border-white/5 hover:bg-white/5 cursor-pointer" onclick={() => (drawer = op)}>
                <td class="py-1.5 px-2 font-mono text-slate-200">{op.username}</td>
                <td class="py-1.5 px-2 text-slate-300 font-mono">{op.grants.join(", ") || "—"}</td>
                <td class="py-1.5 px-2 text-slate-400 tabular-nums">{formatTimestamp(op.createdAt)}</td>
                <td class="py-1.5 px-2 text-slate-400 tabular-nums">{formatTimestamp(op.updatedAt)}</td>
              </tr>
            {:else}
              <tr><td colspan="4" class="py-4 text-center text-slate-500">No operators.</td></tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </div>

    {#if toast}
      <div
        class="text-[12px] px-3 py-1.5 rounded {toast.ok
          ? 'bg-emerald-900/30 text-emerald-200 border border-emerald-700/40'
          : 'bg-rose-900/30 text-rose-200 border border-rose-700/40'}"
      >
        {toast.msg}
      </div>
    {/if}

    {#if createOpen && createSchema}
      <ArgsModal
        verb="admin.operator.create"
        schema={createSchema}
        onClose={() => (createOpen = false)}
        onResult={onCreateResult}
      />
    {/if}
  </main>

  <UserDrawer
    operator={drawer}
    onClose={() => (drawer = null)}
    onMutated={onDrawerMutated}
  />
</div>
