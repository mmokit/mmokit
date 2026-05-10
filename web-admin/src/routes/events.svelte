<script lang="ts">
  import { onMount } from "svelte";
  import { eventsStore } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import type { CommitEvent } from "$lib/types";

  // EVENT_RING_LEN bounds the in-memory tail so the page doesn't grow
  // unboundedly during long sessions. The backend's commit log already
  // ring-buffers; this is just the SPA-side cap.
  const EVENT_RING_LEN = 500;

  let events = $derived<CommitEvent[]>(eventsStore.value ?? []);
  let scenarioFilter = $state<"all" | "split" | "merge" | "migrate">("all");
  let kindFilter = $state<"all" | "commit-step" | "invariant-violation" | "host">("all");
  let cellSearch = $state("");
  let paused = $state(false);

  let filtered = $derived.by(() => {
    const cs = cellSearch.trim().toLowerCase();
    return events.filter((e) => {
      if (scenarioFilter !== "all" && e.scenario !== scenarioFilter) return false;
      if (kindFilter !== "all" && e.kind !== kindFilter) return false;
      if (cs) {
        const hit =
          e.affected?.some((c) => c.toLowerCase().includes(cs)) ||
          e.hostIds?.some((h) => h.toLowerCase().includes(cs)) ||
          e.step?.toLowerCase().includes(cs);
        if (!hit) return false;
      }
      return true;
    });
  });

  // One-shot tail at mount; SSE keeps it live afterwards.
  onMount(async () => {
    try {
      const initial = await apiGet<CommitEvent[]>("/admin/api/events?n=200");
      eventsStore.set(initial);
    } catch {
      // SSE will populate.
    }
  });

  $effect(() => {
    const off = stream.subscribe("events", (data) => {
      if (paused) return;
      // Backend publishes ONE event at a time. Defensively also handle arrays.
      const incoming: CommitEvent[] = Array.isArray(data) ? (data as CommitEvent[]) : [data as CommitEvent];
      const cur = eventsStore.value ?? [];
      const next = [...incoming, ...cur].slice(0, EVENT_RING_LEN);
      eventsStore.set(next);
    });
    return off;
  });

  function fmtTime(ts: string): string {
    const d = new Date(ts);
    return d.toLocaleTimeString(undefined, { hour12: false }) + "." + String(d.getMilliseconds()).padStart(3, "0");
  }
  function rowClass(e: CommitEvent): string {
    if (e.kind === "invariant-violation") return "bg-rose-900/20";
    if (!e.success) return "bg-amber-900/20";
    return "";
  }
</script>

<main class="p-4 space-y-3">
  <div class="flex items-center justify-between">
    <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Events</h2>
    <div class="flex items-center gap-2 text-[11px]">
      <input
        type="text"
        placeholder="cell, host, or step…"
        class="bg-white/5 border border-white/10 rounded px-2 py-1 text-[12px] text-slate-200 placeholder-slate-500 focus:outline-none w-44"
        bind:value={cellSearch}
      />
      <div class="flex bg-white/5 border border-white/10 rounded overflow-hidden">
        {#each ["all", "split", "merge", "migrate"] as s (s)}
          <button
            class="px-2 py-0.5 {scenarioFilter === s ? 'bg-accent-300/20 text-accent-300' : 'text-slate-400 hover:bg-white/5'}"
            onclick={() => (scenarioFilter = s as typeof scenarioFilter)}
          >{s}</button>
        {/each}
      </div>
      <div class="flex bg-white/5 border border-white/10 rounded overflow-hidden">
        {#each ["all", "commit-step", "invariant-violation", "host"] as k (k)}
          <button
            class="px-2 py-0.5 {kindFilter === k ? 'bg-accent-300/20 text-accent-300' : 'text-slate-400 hover:bg-white/5'}"
            onclick={() => (kindFilter = k as typeof kindFilter)}
          >{k}</button>
        {/each}
      </div>
      <button
        class="px-2 py-0.5 border border-white/10 rounded {paused ? 'bg-amber-500/15 text-amber-300' : 'bg-white/5 text-slate-300'}"
        onclick={() => (paused = !paused)}
      >
        {paused ? "paused" : "pause"}
      </button>
    </div>
  </div>

  <div class="bg-[#0d1117] border border-white/10 rounded-lg overflow-x-auto">
    <table class="w-full text-[11.5px] border-collapse font-mono">
      <thead>
        <tr class="text-left text-[10.5px] uppercase tracking-wide text-slate-500 border-b border-white/10">
          <th class="py-1.5 px-2 font-medium" style="width:130px">Time</th>
          <th class="py-1.5 px-2 font-medium" style="width:90px">Scenario</th>
          <th class="py-1.5 px-2 font-medium" style="width:140px">Kind</th>
          <th class="py-1.5 px-2 font-medium" style="width:240px">Step</th>
          <th class="py-1.5 px-2 font-medium" style="width:60px">Ms</th>
          <th class="py-1.5 px-2 font-medium">Affected</th>
          <th class="py-1.5 px-2 font-medium">Hosts</th>
          <th class="py-1.5 px-2 font-medium">Detail</th>
        </tr>
      </thead>
      <tbody>
        {#each filtered as e (e.seqNo)}
          <tr class="border-b border-white/5 {rowClass(e)}">
            <td class="py-1.5 px-2 text-slate-400">{fmtTime(e.timestamp)}</td>
            <td class="py-1.5 px-2 text-slate-300">{e.scenario || "—"}</td>
            <td class="py-1.5 px-2 text-slate-300">{e.kind}</td>
            <td class="py-1.5 px-2 text-slate-200">{e.step || "—"}</td>
            <td class="py-1.5 px-2 text-right text-slate-400">{e.durationMs}</td>
            <td class="py-1.5 px-2 text-slate-400 truncate" title={(e.affected ?? []).join(", ")}>
              {(e.affected ?? []).join(", ") || "—"}
            </td>
            <td class="py-1.5 px-2 text-slate-400 truncate" title={(e.hostIds ?? []).join(", ")}>
              {(e.hostIds ?? []).join(", ") || "—"}
            </td>
            <td class="py-1.5 px-2 text-rose-300">{e.error || (e.success ? "" : "fail")}</td>
          </tr>
        {:else}
          <tr><td colspan="8" class="py-4 text-center text-slate-500 italic">No events match.</td></tr>
        {/each}
      </tbody>
    </table>
  </div>

  <div class="text-[10.5px] text-slate-500">
    showing {filtered.length} of {events.length} · ring cap {EVENT_RING_LEN}
  </div>
</main>
