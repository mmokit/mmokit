<script lang="ts">
  import { onMount, tick } from "svelte";
  import { apiGet } from "$lib/api";
  import { stream } from "$lib/stream";
  import type { LogEntry } from "$lib/types";

  // filterHosts, when non-empty, restricts visible entries to the named
  // hosts. Empty / undefined = no host filtering.
  // onHostsChanged fires whenever the discovered host set changes — the
  // logs page uses this to render the host checkbox list.
  type Props = {
    filterHosts?: Set<string>;
    onHostsChanged?: (hosts: string[]) => void;
  };
  let { filterHosts, onHostsChanged }: Props = $props();

  const CLIENT_CAP = 1000;

  let entries = $state<LogEntry[]>([]);   // oldest first (natural reading)
  let paused = $state(false);
  let catFilter = $state("");
  let scrollEl = $state<HTMLDivElement | null>(null);
  let userScrolled = $state(false);

  // Hash a host_id to a stable tint class. Same FNV approach the
  // cluster map uses for "color by host" — kept inline here so we
  // don't need to refactor the map's helper out of CellMap.
  function hostTint(host: string): string {
    let h = 2166136261;
    for (let i = 0; i < host.length; i++) {
      h ^= host.charCodeAt(i);
      h = Math.imul(h, 16777619);
    }
    const palette = [
      "text-phosphor-300",
      "text-amber-300",
      "text-lime-300",
      "text-ember-300",
      "text-plasma-300",
      "text-sky-300",
      "text-fuchsia-300",
    ];
    return palette[Math.abs(h) % palette.length];
  }

  function fmtTime(iso: string): string {
    const d = new Date(iso);
    const pad = (n: number) => n.toString().padStart(2, "0");
    return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${d.getMilliseconds().toString().padStart(3, "0")}`;
  }

  let visible = $derived.by<LogEntry[]>(() => {
    const cq = catFilter.trim().toLowerCase();
    const hostSet = filterHosts && filterHosts.size > 0 ? filterHosts : null;
    return entries.filter((e) => {
      if (cq && !e.cat.toLowerCase().includes(cq) && !e.msg.toLowerCase().includes(cq)) return false;
      if (hostSet && !hostSet.has(e.host)) return false;
      return true;
    });
  });

  // Publish the discovered host set upward so the logs page can render
  // checkboxes. Recomputes when entries change; we just emit the latest
  // sorted list — parent dedupes against its own state.
  $effect(() => {
    if (!onHostsChanged) return;
    const seen = new Set<string>();
    for (const e of entries) seen.add(e.host);
    onHostsChanged(Array.from(seen).sort());
  });

  onMount(async () => {
    try {
      const recent = await apiGet<LogEntry[]>("/admin/api/logs/recent?n=200");
      // /recent returns newest-first; flip for natural top-to-bottom reading.
      entries = (recent ?? []).slice().reverse();
    } catch {
      // empty start is fine
    }
  });

  $effect(() => {
    const off = stream.subscribe("logs", (data) => {
      if (paused) return;
      const next = [...entries, data as LogEntry];
      entries = next.length > CLIENT_CAP ? next.slice(next.length - CLIENT_CAP) : next;
    });
    return off;
  });

  // Auto-scroll to bottom on new content unless the user scrolled up.
  $effect(() => {
    void visible.length;
    if (!scrollEl || userScrolled || paused) return;
    void tick().then(() => {
      if (scrollEl) scrollEl.scrollTop = scrollEl.scrollHeight;
    });
  });

  function onScroll() {
    if (!scrollEl) return;
    const atBottom =
      scrollEl.scrollHeight - scrollEl.scrollTop - scrollEl.clientHeight < 50;
    userScrolled = !atBottom;
  }

  function jumpToBottom() {
    if (!scrollEl) return;
    scrollEl.scrollTop = scrollEl.scrollHeight;
    userScrolled = false;
  }
</script>

<div class="flex flex-col h-full min-h-0 gap-2">
  <div class="flex items-center gap-2 text-[11px] font-mono">
    <input
      type="text"
      placeholder="filter cat / msg"
      class="bg-white/5 border border-[var(--border-subtle)] rounded px-2 py-1 text-[var(--text-bright)] focus:outline-none focus:border-[var(--border-phosphor)] w-56"
      bind:value={catFilter}
    />
    <button
      type="button"
      class="px-2 py-0.5 rounded border border-[var(--border-subtle)] {paused ? 'bg-ember-300/15 text-ember-200 border-ember-300/40' : 'bg-white/5 text-[var(--text-default)] hover:bg-white/10'}"
      onclick={() => (paused = !paused)}
    >{paused ? "paused" : "pause"}</button>
    {#if userScrolled}
      <button
        type="button"
        class="px-2 py-0.5 rounded border border-[var(--border-phosphor)] bg-phosphor-300/15 text-phosphor-200 hover:bg-phosphor-300/25"
        onclick={jumpToBottom}
      >jump to live</button>
    {/if}
    <span class="ml-auto text-[var(--text-dim)] tabular-nums">
      {visible.length} / {entries.length}
    </span>
  </div>

  <div
    bind:this={scrollEl}
    onscroll={onScroll}
    class="grow min-h-0 overflow-auto bg-[var(--bg-deep)] border border-[var(--border-subtle)] rounded-lg p-2 font-mono text-[11px] leading-[1.45] text-[var(--text-default)]"
  >
    {#each visible as e, i (i)}
      <div class="whitespace-pre-wrap break-words">
        <span class="text-[var(--text-dim)]">{fmtTime(e.t)}</span>
        <span class="{hostTint(e.host)} ml-1">[{e.host}]</span>
        <span class="text-[var(--text-muted)] ml-1">{e.cat}</span>
        <span class="text-[var(--text-bright)] ml-2">{e.msg}</span>
      </div>
    {:else}
      <div class="text-[var(--text-dim)] italic text-center py-4">
        No log lines match — enable a category on the left, check more hosts, or clear the filter.
      </div>
    {/each}
  </div>
</div>
