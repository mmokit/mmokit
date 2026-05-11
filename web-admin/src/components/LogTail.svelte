<script lang="ts">
  import { onMount, tick, untrack } from "svelte";
  import { apiGet } from "$lib/api";
  import { stream } from "$lib/stream";
  import type { LogEntry } from "$lib/types";

  // filterHosts / filterCats: undefined = "show all". Set (possibly
  // empty) = explicit allowlist. Empty Set therefore means "hide
  // everything from that dimension".
  // onHostsChanged / onCatsChanged fire whenever the discovered set
  // changes — the logs page uses these to render the checkbox lists.
  type Props = {
    filterHosts?: Set<string>;
    filterCats?: Set<string>;
    onHostsChanged?: (hosts: string[]) => void;
    onCatsChanged?: (cats: string[]) => void;
  };
  let { filterHosts, filterCats, onHostsChanged, onCatsChanged }: Props = $props();

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
    // filter* === undefined: parent says "no filter on that dimension".
    // filter* === Set (possibly empty): explicit allowlist; empty Set
    // means "match nothing" (used when user unchecks everything).
    const hostSet = filterHosts ?? null;
    const catSet = filterCats ?? null;
    return entries.filter((e) => {
      if (cq && !e.cat.toLowerCase().includes(cq) && !e.msg.toLowerCase().includes(cq)) return false;
      if (hostSet && !hostSet.has(e.host)) return false;
      if (catSet && !catSet.has(e.cat)) return false;
      return true;
    });
  });

  // Publish the discovered host + category sets upward so the logs page
  // can render checkbox lists. Recomputes when entries change. The
  // callbacks are wrapped in untrack() so any state reads inside the
  // parent's handlers don't leak into THIS effect's dependency set —
  // otherwise the parent updating discovered*/selected* would re-fire
  // this effect and trigger an infinite reactive loop.
  $effect(() => {
    const hostCB = onHostsChanged;
    const catCB = onCatsChanged;
    if (!hostCB && !catCB) return;
    const hosts = new Set<string>();
    const cats = new Set<string>();
    for (const e of entries) {
      hosts.add(e.host);
      cats.add(e.cat);
    }
    const sortedHosts = Array.from(hosts).sort();
    const sortedCats = Array.from(cats).sort();
    untrack(() => {
      hostCB?.(sortedHosts);
      catCB?.(sortedCats);
    });
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
