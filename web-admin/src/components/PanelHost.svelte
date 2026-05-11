<script lang="ts">
  import { onMount } from "svelte";
  import { apiGet, apiPost, ApiError } from "$lib/api";
  import { stream } from "$lib/stream";
  import type { PanelDef, Schema } from "$lib/types";
  import DataTable from "./DataTable.svelte";
  import ArgsModal from "./ArgsModal.svelte";

  type Props = { panel: PanelDef };
  let { panel }: Props = $props();

  // Latest payload from the panel's primary topic. We support one topic
  // in v1; multi-topic composition is a v2 feature.
  let payload = $state<unknown>(null);

  // Cached argsSchema per declared verb. Populated on mount.
  let schemas = $state<Record<string, Schema>>({});
  let schemaError = $state<Record<string, string>>({});

  // Modal state: which verb is being prompted, and its schema.
  let modalVerb = $state<string | null>(null);
  let toast = $state<{ ok: boolean; msg: string } | null>(null);

  function pushToast(ok: boolean, msg: string) {
    toast = { ok, msg };
    setTimeout(() => (toast = null), 4000);
  }

  // Subscribe to the panel's primary topic. Re-subscribes when the panel
  // changes (e.g. user navigates between two registered panels without a
  // full route remount).
  $effect(() => {
    const topic = panel.topics?.[0];
    if (!topic) return;
    const off = stream.subscribe(topic, (data) => {
      payload = data;
    });
    return off;
  });

  // Fetch the argsSchema for each command up front so the toolbar can
  // decide direct-POST vs modal at click time.
  onMount(async () => {
    for (const verb of panel.commands ?? []) {
      try {
        const info = await apiGet<{ argsSchema?: Schema }>(`/admin/api/commands/${verb}`);
        schemas = { ...schemas, [verb]: info.argsSchema ?? { struct: "", fields: [] } };
      } catch (e) {
        schemaError = { ...schemaError, [verb]: (e as Error).message };
      }
    }
  });

  async function runVerb(verb: string) {
    const schema = schemas[verb];
    if (!schema) {
      pushToast(false, `${verb}: schema not loaded`);
      return;
    }
    if ((schema.fields ?? []).length === 0) {
      try {
        const res = await apiPost<{ ok: boolean; result?: unknown; error?: string }>(
          `/admin/api/commands/${verb}`,
          {},
        );
        if (res.ok === false) {
          pushToast(false, res.error || `${verb}: failed`);
        } else {
          pushToast(true, `${verb}: ok`);
        }
      } catch (e) {
        const msg = e instanceof ApiError ? e.message : (e as Error).message;
        pushToast(false, msg);
      }
      return;
    }
    modalVerb = verb;
  }

  // Derive rows + columns from the latest payload.
  // - Array of objects → DataTable.
  // - Plain object → scalar key/value strip.
  // - Null/undefined/primitive → placeholder.
  let rows = $derived.by<Record<string, unknown>[]>(() => {
    if (Array.isArray(payload) && payload.every((r) => r != null && typeof r === "object")) {
      return payload as Record<string, unknown>[];
    }
    return [];
  });

  let columns = $derived.by(() => {
    if (rows.length === 0) return [];
    // Union of keys across the first 5 rows.
    const keys = new Set<string>();
    for (const r of rows.slice(0, 5)) {
      for (const k of Object.keys(r)) keys.add(k);
    }
    return Array.from(keys).map((k) => ({
      key: k,
      label: k,
      accessor: (r: Record<string, unknown>) => r[k] as string | number,
      render: (r: Record<string, unknown>) => {
        const v = r[k];
        if (v === null || v === undefined) return "—";
        if (typeof v === "object") return JSON.stringify(v);
        return String(v);
      },
    }));
  });

  let isScalarObject = $derived(
    rows.length === 0 &&
      payload != null &&
      typeof payload === "object" &&
      !Array.isArray(payload),
  );
</script>

<main class="p-4 space-y-3">
  <div class="flex items-center justify-between">
    <h2 class="text-phosphor-300 text-[11px] uppercase tracking-[0.18em] font-mono">{panel.label}</h2>
    <div class="flex gap-1.5">
      {#each panel.commands ?? [] as verb (verb)}
        {@const err = schemaError[verb]}
        <button
          type="button"
          class="px-2 py-0.5 text-[11px] font-mono rounded border border-[var(--border-subtle)] bg-white/5 text-[var(--text-default)] hover:bg-white/10 hover:text-[var(--text-bright)] disabled:opacity-50"
          title={err ? `schema error: ${err}` : verb}
          disabled={!schemas[verb]}
          onclick={() => runVerb(verb)}
        >
          {verb}
        </button>
      {/each}
    </div>
  </div>

  <div class="bg-[var(--bg-deep)] border border-[var(--border-subtle)] rounded-lg p-3">
    {#if rows.length > 0}
      <DataTable
        rows={rows}
        columns={columns}
        emptyText="No data."
      />
    {:else if isScalarObject}
      <div class="grid grid-cols-[160px_1fr] gap-x-3 gap-y-1 text-[12px]">
        {#each Object.entries(payload as Record<string, unknown>) as [k, v] (k)}
          <span class="text-[var(--text-dim)] font-mono">{k}</span>
          <span class="text-[var(--text-bright)] font-mono tabular-nums">
            {typeof v === "object" ? JSON.stringify(v) : String(v)}
          </span>
        {/each}
      </div>
    {:else}
      <div class="text-[var(--text-dim)] italic text-[12px] py-4 text-center font-mono">
        Waiting for topic {panel.topics?.[0] ?? "(none)"}…
      </div>
    {/if}
  </div>

  {#if toast}
    <div
      class="text-[12px] font-mono px-3 py-1.5 rounded border {toast.ok
        ? 'bg-lime-900/30 text-lime-200 border-lime-700/40'
        : 'bg-ember-900/30 text-ember-200 border-ember-700/40'}"
    >
      {toast.msg}
    </div>
  {/if}

  {#if modalVerb && schemas[modalVerb]}
    <ArgsModal
      verb={modalVerb}
      schema={schemas[modalVerb]}
      onClose={() => (modalVerb = null)}
      onResult={(ok, msg) => pushToast(ok, msg)}
    />
  {/if}
</main>
