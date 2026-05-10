<script lang="ts">
  import { onMount } from "svelte";
  import { apiGet, ApiError } from "$lib/api";
  import type { AuditEntry } from "$lib/types";

  let entries = $state<AuditEntry[]>([]);
  let error = $state("");
  let loading = $state(true);
  let limit = $state(200);

  async function refresh() {
    loading = true;
    error = "";
    try {
      const res = await apiGet<AuditEntry[]>(`/admin/api/audit?n=${limit}`);
      entries = res ?? [];
    } catch (e) {
      if (e instanceof ApiError && e.kind === "rbac") {
        error = "You need the admin.audit grant to view this page.";
      } else {
        error = (e as Error).message;
      }
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void refresh();
  });

  function fmtTime(ts: string): string {
    const d = new Date(ts);
    return d.toLocaleString(undefined, { hour12: false });
  }
  function durationMs(a: AuditEntry): number {
    return new Date(a.finishedAt).getTime() - new Date(a.startedAt).getTime();
  }
  function rowClass(a: AuditEntry): string {
    if (!a.ok) return "bg-rose-900/15";
    return "";
  }
  function truncate(s: string | undefined, n: number): string {
    if (!s) return "";
    return s.length > n ? s.slice(0, n) + "…" : s;
  }
</script>

<main class="p-4 space-y-3">
  <div class="flex items-center justify-between">
    <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Audit log</h2>
    <div class="flex items-center gap-2 text-[11px] text-slate-400">
      <span>showing last</span>
      <select
        class="bg-white/5 border border-white/10 rounded px-2 py-0.5 text-slate-200"
        bind:value={limit}
        onchange={() => void refresh()}
      >
        <option value={50}>50</option>
        <option value={200}>200</option>
        <option value={1000}>1000</option>
      </select>
      <button
        type="button"
        class="px-2 py-0.5 rounded border border-white/10 bg-white/5 text-slate-300 hover:bg-white/10"
        onclick={() => void refresh()}
        disabled={loading}
      >
        refresh
      </button>
    </div>
  </div>

  {#if error}
    <div class="text-rose-300 text-[12px]">{error}</div>
  {/if}

  <div class="bg-[#0d1117] border border-white/10 rounded-lg overflow-x-auto">
    <table class="w-full text-[11.5px] border-collapse font-mono">
      <thead>
        <tr class="text-left text-[10.5px] uppercase tracking-wide text-slate-500 border-b border-white/10">
          <th class="py-1.5 px-2 font-medium" style="width:170px">Time</th>
          <th class="py-1.5 px-2 font-medium" style="width:110px">User</th>
          <th class="py-1.5 px-2 font-medium" style="width:140px">IP</th>
          <th class="py-1.5 px-2 font-medium" style="width:200px">Verb</th>
          <th class="py-1.5 px-2 font-medium" style="width:60px">OK</th>
          <th class="py-1.5 px-2 font-medium" style="width:80px">ms</th>
          <th class="py-1.5 px-2 font-medium">Args</th>
          <th class="py-1.5 px-2 font-medium">Error</th>
        </tr>
      </thead>
      <tbody>
        {#each entries as e (e.traceId || e.startedAt)}
          <tr class="border-b border-white/5 {rowClass(e)}">
            <td class="py-1.5 px-2 text-slate-400">{fmtTime(e.startedAt)}</td>
            <td class="py-1.5 px-2 text-slate-200">{e.username || "—"}</td>
            <td class="py-1.5 px-2 text-slate-400">{e.ip || "—"}</td>
            <td class="py-1.5 px-2 text-slate-200">{e.verb || "—"}</td>
            <td class="py-1.5 px-2 {e.ok ? 'text-emerald-300' : 'text-rose-300'}">
              {e.ok ? "ok" : "fail"}
            </td>
            <td class="py-1.5 px-2 text-right text-slate-400">{durationMs(e)}</td>
            <td class="py-1.5 px-2 text-slate-400 truncate" title={e.args ?? ""}>
              {truncate(e.args, 80)}
            </td>
            <td class="py-1.5 px-2 text-rose-300 truncate" title={e.error ?? ""}>
              {truncate(e.error, 80)}
            </td>
          </tr>
        {:else}
          <tr><td colspan="8" class="py-4 text-center text-slate-500 italic">
            {loading ? "loading…" : "No entries."}
          </td></tr>
        {/each}
      </tbody>
    </table>
  </div>

  <div class="text-[10.5px] text-slate-500">
    {entries.length} entries · in-memory ring (Postgres backing is Phase 2)
  </div>
</main>
