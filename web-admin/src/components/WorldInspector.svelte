<script lang="ts">
  import { worldStore, ROSTERS } from "$lib/world-store.svelte";
  import { ApiError } from "$lib/api";
  import ConfirmDialog from "./ConfirmDialog.svelte";

  // Local edit buffer — keyed by per-entity-type field names that match
  // the cmdsys world.update args (Name, Radius, Tier, Roster, Density, Kind, Variant).
  // Each input writes to dirty; Apply POSTs world.update; Discard clears dirty.
  let dirty = $state<Record<string, unknown>>({});
  let error = $state<string | null>(null);
  let busy = $state(false);
  let confirmDeleteOpen = $state(false);

  // Reset the edit buffer whenever the selection changes.
  $effect(() => {
    // touch reactive deps
    worldStore.selectedID;
    worldStore.selectedType;
    dirty = {};
    error = null;
  });

  // Selected entity (typed) — derive every render.
  let sel = $derived(worldStore.selected());

  // Per-field getter: prefers dirty buffer over current snapshot value.
  function get<T>(field: string, fallback: T): T {
    if (Object.prototype.hasOwnProperty.call(dirty, field)) {
      return dirty[field] as T;
    }
    return fallback;
  }

  function setField(field: string, value: unknown, baseline: unknown): void {
    // If user reverted to baseline, drop the key (so Apply stays disabled).
    if (value === baseline) {
      if (Object.prototype.hasOwnProperty.call(dirty, field)) {
        const next = { ...dirty };
        delete next[field];
        dirty = next;
      }
      return;
    }
    dirty = { ...dirty, [field]: value };
  }

  let isDirty = $derived(Object.keys(dirty).length > 0);

  async function onApply(): Promise<void> {
    if (!sel || !isDirty) return;
    busy = true;
    error = null;
    try {
      await worldStore.update(sel.type, sel.entity.id, dirty);
      dirty = {};
    } catch (e) {
      error = e instanceof ApiError ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  function onDiscard(): void {
    dirty = {};
    error = null;
  }

  function onDeleteClick(): void {
    if (!sel) return;
    confirmDeleteOpen = true;
  }

  async function onDeleteConfirm(): Promise<void> {
    if (!sel) return;
    confirmDeleteOpen = false;
    busy = true;
    error = null;
    try {
      await worldStore.del(sel.type, sel.entity.id);
      worldStore.clearSelection();
      dirty = {};
    } catch (e) {
      error = e instanceof ApiError ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  // ── small input helper styles ──
  const inputCls =
    "w-full bg-[var(--bg-deep)] border border-[var(--border-subtle)] rounded px-2 py-1 font-mono text-[11.5px] text-[var(--text-bright)] focus:outline-none focus:border-phosphor-400/70 focus:bg-[var(--bg-elevated)]";
  const labelCls = "block font-mono text-[9.5px] text-[var(--text-dim)] tracking-[0.16em] uppercase mb-1";
</script>

<aside
  class="w-[260px] shrink-0 border-l border-[var(--border-subtle)] bg-[var(--bg-deep)]/70 flex flex-col text-[12px] overflow-y-auto"
>
  <div class="px-3 pt-3 pb-2 border-b border-[var(--border-faint)]">
    <div class="font-mono text-[9.5px] text-[var(--text-dim)] tracking-[0.2em] uppercase">
      Inspector
    </div>
  </div>

  {#if !sel}
    <div class="grow flex items-center justify-center text-[var(--text-dim)] font-mono text-[11px] px-4 text-center">
      no entity selected — click one with the select tool (V)
    </div>
  {:else}
    {@const e = sel.entity}
    <div class="px-3 py-3 border-b border-[var(--border-faint)]">
      <div class="font-mono text-[10px] text-[var(--text-muted)] tracking-[0.14em] uppercase">{sel.type}</div>
      <div class="font-mono text-[12.5px] text-phosphor-300 mt-0.5 truncate" title={e.id}>{e.id}</div>
      <div class="mt-1 font-mono text-[10.5px] text-[var(--text-muted)] tabular-nums">
        x {e.world_pos[0].toFixed(0)} · y {e.world_pos[1].toFixed(0)}
      </div>
    </div>

    <div class="px-3 py-3 flex flex-col gap-3">
      {#if sel.type === "station"}
        {@const s = sel.entity as import("$lib/world-store.svelte").WorldStation}
        <div>
          <label class={labelCls} for="ins-name">Name</label>
          <input
            id="ins-name"
            type="text"
            class={inputCls}
            value={get("Name", s.name)}
            oninput={(ev) => setField("Name", (ev.currentTarget as HTMLInputElement).value, s.name)}
          />
        </div>
        <div>
          <label class={labelCls} for="ins-radius">Radius</label>
          <input
            id="ins-radius"
            type="number"
            min="0"
            class={inputCls}
            value={get("Radius", s.radius ?? 0)}
            oninput={(ev) => setField("Radius", Number((ev.currentTarget as HTMLInputElement).value), s.radius ?? 0)}
          />
        </div>
      {:else if sel.type === "poi"}
        {@const p = sel.entity as import("$lib/world-store.svelte").WorldPOI}
        <div>
          <span class={labelCls}>Tier</span>
          <div class="flex gap-1">
            {#each [1, 2, 3] as t (t)}
              {@const active = (get("Tier", p.tier) as number) === t}
              <button
                type="button"
                onclick={() => setField("Tier", t, p.tier)}
                class="grow py-1 rounded font-mono text-[11px] border transition-colors
                  {active
                    ? 'bg-phosphor-300/15 border-phosphor-400/70 text-phosphor-300'
                    : 'bg-transparent border-[var(--border-subtle)] text-[var(--text-default)] hover:border-[var(--text-dim)]'}"
              >T{t}</button>
            {/each}
          </div>
        </div>
        <div>
          <label class={labelCls} for="ins-roster">Roster</label>
          <select
            id="ins-roster"
            class={inputCls}
            value={get("Roster", p.roster)}
            onchange={(ev) => setField("Roster", (ev.currentTarget as HTMLSelectElement).value, p.roster)}
          >
            {#each ROSTERS as r (r)}
              <option value={r}>{r}</option>
            {/each}
          </select>
        </div>
      {:else if sel.type === "dungeon"}
        {@const d = sel.entity as import("$lib/world-store.svelte").WorldDungeon}
        <div>
          <label class={labelCls} for="ins-dname">Name</label>
          <input
            id="ins-dname"
            type="text"
            class={inputCls}
            value={get("Name", d.name)}
            oninput={(ev) => setField("Name", (ev.currentTarget as HTMLInputElement).value, d.name)}
          />
        </div>
      {:else if sel.type === "belt"}
        {@const b = sel.entity as import("$lib/world-store.svelte").WorldBelt}
        <div>
          <label class={labelCls} for="ins-bradius">Radius</label>
          <input
            id="ins-bradius"
            type="number"
            min="1"
            class={inputCls}
            value={get("Radius", b.radius)}
            oninput={(ev) => setField("Radius", Number((ev.currentTarget as HTMLInputElement).value), b.radius)}
          />
        </div>
        <div>
          <label class={labelCls} for="ins-density">Density · <span class="text-[var(--text-bright)]">{(get("Density", b.density) as number).toFixed(2)}</span></label>
          <input
            id="ins-density"
            type="range"
            min="0.1"
            max="3"
            step="0.05"
            value={get("Density", b.density)}
            oninput={(ev) => setField("Density", Number((ev.currentTarget as HTMLInputElement).value), b.density)}
            class="w-full"
          />
        </div>
      {:else if sel.type === "decoration"}
        {@const dc = sel.entity as import("$lib/world-store.svelte").WorldDecoration}
        <div>
          <label class={labelCls} for="ins-kind">Kind</label>
          <input
            id="ins-kind"
            type="text"
            class={inputCls}
            value={get("Kind", dc.kind)}
            oninput={(ev) => setField("Kind", (ev.currentTarget as HTMLInputElement).value, dc.kind)}
          />
        </div>
        <div>
          <label class={labelCls} for="ins-variant">Variant</label>
          <input
            id="ins-variant"
            type="text"
            class={inputCls}
            value={get("Variant", dc.variant ?? "")}
            oninput={(ev) => setField("Variant", (ev.currentTarget as HTMLInputElement).value, dc.variant ?? "")}
          />
        </div>
      {/if}
    </div>

    {#if error}
      <div class="mx-3 mb-2 px-2.5 py-1.5 rounded bg-ember-400/10 border border-ember-400/30 font-mono text-[10.5px] text-ember-300 break-words">
        {error}
      </div>
    {/if}

    <!-- Action buttons -->
    <div class="px-3 pb-3 mt-auto flex flex-col gap-1.5">
      <div class="flex gap-1.5">
        <button
          type="button"
          disabled={!isDirty || busy}
          onclick={onApply}
          class="grow py-1.5 rounded font-mono text-[11px] uppercase tracking-[0.1em] transition-colors
            {isDirty && !busy
              ? 'bg-phosphor-300/15 border border-phosphor-400/70 text-phosphor-300 hover:bg-phosphor-300/25'
              : 'bg-white/[0.025] border border-[var(--border-subtle)] text-[var(--text-dim)] cursor-not-allowed'}"
        >Apply</button>
        <button
          type="button"
          disabled={!isDirty || busy}
          onclick={onDiscard}
          class="px-2.5 py-1.5 rounded font-mono text-[11px] uppercase tracking-[0.1em] border border-[var(--border-subtle)] text-[var(--text-default)] hover:bg-white/[0.04] disabled:text-[var(--text-dim)] disabled:cursor-not-allowed"
        >Discard</button>
      </div>
      <button
        type="button"
        disabled={busy}
        onclick={onDeleteClick}
        class="w-full py-1.5 rounded font-mono text-[11px] uppercase tracking-[0.1em] border border-ember-400/30 text-ember-300 hover:bg-ember-400/15 disabled:opacity-50 disabled:cursor-not-allowed"
      >Delete</button>
    </div>
  {/if}
</aside>

<ConfirmDialog
  open={confirmDeleteOpen && !!sel}
  title="Delete {sel?.type ?? ''}"
  confirmLabel={busy ? "Deleting…" : "Delete"}
  cancelLabel="Cancel"
  danger
  onConfirm={onDeleteConfirm}
  onCancel={() => (confirmDeleteOpen = false)}
>
  {#if sel}
    Permanently remove <span class="font-mono text-[var(--text-bright)]">{sel.entity.id}</span>
    from <span class="font-mono">world/{sel.type}s.json</span> and despawn it from the running cluster?
    This writes to disk immediately and cannot be undone without git.
  {/if}
</ConfirmDialog>
