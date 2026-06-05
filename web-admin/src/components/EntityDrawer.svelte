<script lang="ts">
  import { Close } from "$lib/icons";
  import { apiPost, ApiError } from "$lib/api";
  import ConfirmDialog from "./ConfirmDialog.svelte";
  import type { EntityInspectRow, EntityInspectResult } from "$lib/types";

  type Props = {
    netID: number;
    onClose: () => void;
    onDespawned: () => void;
  };
  let { netID, onClose, onDespawned }: Props = $props();

  let data = $state<EntityInspectResult | null>(null);
  let loading = $state(false);
  let error = $state<string | null>(null);
  let dirty = $state<Record<string, string>>({});
  let applying = $state(false);
  let confirmOpen = $state(false);

  function keyOf(r: EntityInspectRow): string {
    return `${r.Component}/${r.Field}`;
  }

  function valueOf(r: EntityInspectRow): string {
    return dirty[keyOf(r)] ?? r.Value;
  }

  function setField(r: EntityInspectRow, value: string): void {
    const k = keyOf(r);
    if (value === r.Value) {
      if (Object.prototype.hasOwnProperty.call(dirty, k)) {
        const next = { ...dirty };
        delete next[k];
        dirty = next;
      }
      return;
    }
    dirty = { ...dirty, [k]: value };
  }

  let dirtyCount = $derived(Object.keys(dirty).length);

  // Group components into [component, rows][] preserving insertion order.
  let groups = $derived.by(() => {
    const m = new Map<string, EntityInspectRow[]>();
    for (const r of data?.Components ?? []) {
      const arr = m.get(r.Component);
      if (arr) arr.push(r);
      else m.set(r.Component, [r]);
    }
    return [...m.entries()];
  });

  function numericType(t: string): boolean {
    return t.startsWith("int") || t.startsWith("uint") || t.startsWith("float");
  }

  async function inspect(): Promise<void> {
    loading = true;
    error = null;
    try {
      const resp = await apiPost<{ ok: boolean; result?: EntityInspectResult }>(
        "/admin/api/commands/entity.inspect",
        { NetID: netID },
      );
      data = resp.result ?? null;
      dirty = {};
    } catch (e) {
      error = e instanceof ApiError ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  async function apply(): Promise<void> {
    if (dirtyCount === 0 || !data) return;
    applying = true;
    error = null;
    try {
      for (const r of data.Components) {
        const k = keyOf(r);
        if (!Object.prototype.hasOwnProperty.call(dirty, k)) continue;
        await apiPost("/admin/api/commands/entity.modify", {
          NetID: netID,
          Component: r.Component,
          Field: r.Field,
          Value: dirty[k],
        });
      }
      await inspect();
    } catch (e) {
      error = e instanceof ApiError ? e.message : String(e);
    } finally {
      applying = false;
    }
  }

  async function despawn(): Promise<void> {
    confirmOpen = false;
    error = null;
    try {
      await apiPost("/admin/api/commands/entity.despawn", { NetID: netID });
      onDespawned();
    } catch (e) {
      error = e instanceof ApiError ? e.message : String(e);
    }
  }

  // Re-inspect whenever netID changes.
  $effect(() => {
    void netID;
    void inspect();
  });
</script>

<aside class="flex w-96 flex-col border-l border-white/10 bg-black/30">
  <header class="flex items-center justify-between border-b border-white/10 px-4 py-2">
    <div>
      <div class="text-[10px] uppercase tracking-wide text-slate-500">entity</div>
      <div class="font-mono text-slate-100 text-[13px]">
        {netID}{#if data?.Kind}<span class="text-slate-500"> · {data.Kind}</span>{/if}
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

  <div class="flex-1 overflow-auto p-4 text-[12px]">
    {#if loading}
      <div class="text-slate-500">loading…</div>
    {:else if error}
      <div class="mb-3 rounded border border-rose-500/30 bg-rose-500/10 px-2.5 py-1.5 text-[11.5px] text-rose-300 wrap-break-word">
        {error}
      </div>
    {/if}

    {#if data && !loading}
      <div class="space-y-4">
        {#each groups as [comp, fields] (comp)}
          <div>
            <div class="mb-1.5 text-[10px] uppercase tracking-wide text-slate-500">{comp}</div>
            <div class="grid grid-cols-[110px_1fr] items-center gap-x-3 gap-y-1.5">
              {#each fields as r (r.Field)}
                <span class="text-slate-400 truncate" title={r.Field}>{r.Field}</span>
                {#if r.Editable && r.Type === "bool"}
                  <input
                    type="checkbox"
                    class="h-4 w-4 accent-accent-400"
                    checked={valueOf(r) === "true"}
                    onchange={(ev) =>
                      setField(r, (ev.currentTarget as HTMLInputElement).checked ? "true" : "false")}
                  />
                {:else if r.Editable}
                  <input
                    type={numericType(r.Type) ? "number" : "text"}
                    class="w-full rounded border border-white/10 bg-black/40 px-2 py-1 font-mono text-[11.5px] text-slate-100 focus:border-accent-400/70 focus:outline-none"
                    value={valueOf(r)}
                    oninput={(ev) => setField(r, (ev.currentTarget as HTMLInputElement).value)}
                  />
                {:else}
                  <span class="font-mono text-[11.5px] text-slate-500 truncate" title={r.Value}>{r.Value}</span>
                {/if}
              {/each}
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </div>

  <footer class="flex items-center gap-2 border-t border-white/10 px-4 py-2">
    <button
      type="button"
      class="px-3 py-1 text-[11.5px] rounded bg-accent-400 hover:bg-accent-500 text-slate-950 font-semibold disabled:opacity-40 disabled:cursor-not-allowed"
      disabled={dirtyCount === 0 || applying}
      onclick={() => void apply()}
    >
      Apply ({dirtyCount})
    </button>
    <button
      type="button"
      class="px-3 py-1 text-[11.5px] rounded bg-white/5 border border-white/10 hover:bg-white/10 disabled:opacity-40 disabled:cursor-not-allowed"
      disabled={dirtyCount === 0}
      onclick={() => (dirty = {})}
    >
      Discard
    </button>
    <span class="flex-1"></span>
    <button
      type="button"
      class="px-3 py-1 text-[11.5px] rounded bg-rose-500/15 border border-rose-500/30 text-rose-200 hover:bg-rose-500/25"
      onclick={() => (confirmOpen = true)}
    >
      Despawn
    </button>
  </footer>
</aside>

<ConfirmDialog
  open={confirmOpen}
  title={`Despawn entity ${netID}?`}
  confirmLabel="Despawn"
  danger={true}
  onConfirm={despawn}
  onCancel={() => (confirmOpen = false)}>
  This permanently removes the entity from the world.
</ConfirmDialog>
