<script lang="ts">
  import { apiPost, ApiError } from "$lib/api";
  import type { Schema, FieldSchema } from "$lib/types";

  type Props = {
    verb: string;
    schema: Schema;
    onClose: () => void;
    onResult: (ok: boolean, msg: string) => void;
  };
  let { verb, schema, onClose, onResult }: Props = $props();

  // Per-field input state, keyed by field name. Seeded once at mount
  // from the schema's declared defaults — the modal unmounts on close
  // (via {#if modalVerb} in PanelHost) so a fresh open gets fresh
  // defaults.
  const initialValues: Record<string, string> = {};
  const initialChecks: Record<string, boolean> = {};
  for (const f of schema.fields ?? []) {
    initialValues[f.name] = f.default ?? "";
    initialChecks[f.name] = f.default === "true";
  }
  let values = $state<Record<string, string>>(initialValues);
  let checks = $state<Record<string, boolean>>(initialChecks);
  let error = $state("");
  let submitting = $state(false);

  // Focus the first non-bool input when the modal opens so the user
  // can type immediately without clicking into the form.
  const firstFocusable = (schema.fields ?? []).find((f) => f.kind !== "bool")?.name ?? "";

  function coerce(f: FieldSchema): unknown {
    const raw = values[f.name] ?? "";
    switch (f.kind) {
      case "string":
        return raw;
      case "int32":
      case "int64": {
        const n = parseInt(raw, 10);
        if (Number.isNaN(n)) throw new Error(`${f.name}: not an integer`);
        return n;
      }
      case "float32":
      case "float64": {
        const n = parseFloat(raw);
        if (Number.isNaN(n)) throw new Error(`${f.name}: not a number`);
        return n;
      }
      case "bool":
        return checks[f.name] ?? false;
      default:
        // Slices, nested objects: expect JSON text.
        if (raw === "") return null;
        try {
          return JSON.parse(raw);
        } catch (e) {
          throw new Error(`${f.name}: invalid JSON: ${(e as Error).message}`);
        }
    }
  }

  async function submit(e: Event) {
    e.preventDefault();
    if (submitting) return;
    error = "";
    submitting = true;
    try {
      const payload: Record<string, unknown> = {};
      for (const f of schema.fields ?? []) {
        if (f.required) {
          const raw = (values[f.name] ?? "").trim();
          if (raw === "" && f.kind !== "bool") {
            throw new Error(`${f.name}: required`);
          }
        }
        payload[f.name] = coerce(f);
      }
      const res = await apiPost<{ ok: boolean; result?: unknown; error?: string }>(
        `/admin/api/commands/${verb}`,
        payload,
      );
      if (res.ok === false) {
        error = res.error || "command failed";
        return;
      }
      onResult(true, `${verb}: ok`);
      onClose();
    } catch (e) {
      error = e instanceof ApiError ? e.message : (e as Error).message;
    } finally {
      submitting = false;
    }
  }
</script>

<div class="fixed inset-0 bg-black/60 flex items-center justify-center z-50" onclick={onClose} role="presentation">
  <form
    class="bg-[var(--bg-deep)] border border-[var(--border-subtle)] rounded-lg p-4 w-[420px] max-w-[90vw] space-y-3 shadow-xl"
    onsubmit={submit}
    onclick={(e: Event) => e.stopPropagation()}
  >
    <div class="flex items-center justify-between">
      <h3 class="text-[13px] text-phosphor-300 font-mono tracking-tight">{verb}</h3>
      <button type="button" class="text-[var(--text-dim)] hover:text-[var(--text-bright)] text-[11px] font-mono uppercase tracking-[0.16em]" onclick={onClose}>esc</button>
    </div>

    {#if (schema.fields ?? []).length === 0}
      <div class="text-[12px] text-[var(--text-dim)] italic">No arguments — submit to run.</div>
    {/if}

    {#each schema.fields ?? [] as f (f.name)}
      <label class="block text-[11.5px]">
        <span class="text-[var(--text-muted)] font-mono">
          {f.name}
          {#if f.required}<span class="text-ember-300">*</span>{/if}
          <span class="text-[var(--text-dim)] ml-1">({f.kind})</span>
        </span>
        {#if f.kind === "bool"}
          <input
            type="checkbox"
            class="mt-1 accent-phosphor-400"
            bind:checked={checks[f.name]}
          />
        {:else if f.kind === "int32" || f.kind === "int64"}
          <input
            type="number"
            step="1"
            class="mt-1 w-full bg-white/5 border border-[var(--border-subtle)] rounded px-2 py-1 text-[var(--text-bright)] font-mono tabular-nums focus:outline-none focus:border-[var(--border-phosphor)]"
            bind:value={values[f.name]}
            autofocus={f.name === firstFocusable}
            placeholder={f.default}
          />
        {:else if f.kind === "float32" || f.kind === "float64"}
          <input
            type="number"
            step="any"
            class="mt-1 w-full bg-white/5 border border-[var(--border-subtle)] rounded px-2 py-1 text-[var(--text-bright)] font-mono tabular-nums focus:outline-none focus:border-[var(--border-phosphor)]"
            bind:value={values[f.name]}
            autofocus={f.name === firstFocusable}
            placeholder={f.default}
          />
        {:else if f.kind === "string"}
          <input
            type="text"
            class="mt-1 w-full bg-white/5 border border-[var(--border-subtle)] rounded px-2 py-1 text-[var(--text-bright)] font-mono focus:outline-none focus:border-[var(--border-phosphor)]"
            bind:value={values[f.name]}
            autofocus={f.name === firstFocusable}
            placeholder={f.default}
          />
        {:else}
          <input
            type="text"
            class="mt-1 w-full bg-white/5 border border-[var(--border-subtle)] rounded px-2 py-1 text-[var(--text-bright)] font-mono focus:outline-none focus:border-[var(--border-phosphor)]"
            bind:value={values[f.name]}
            autofocus={f.name === firstFocusable}
            placeholder={'JSON (e.g. ["a","b"] or {"k":1})'}
          />
        {/if}
        {#if f.help}
          <span class="text-[10.5px] text-[var(--text-dim)]">{f.help}</span>
        {/if}
      </label>
    {/each}

    {#if error}
      <div class="text-ember-300 text-[11.5px] font-mono">{error}</div>
    {/if}

    <div class="flex justify-end gap-2">
      <button
        type="button"
        class="px-3 py-1 text-[11px] font-mono uppercase tracking-[0.16em] bg-white/5 border border-[var(--border-subtle)] rounded text-[var(--text-default)] hover:bg-white/10"
        onclick={onClose}
      >
        cancel
      </button>
      <button
        type="submit"
        class="px-3 py-1 text-[11px] font-mono uppercase tracking-[0.16em] bg-phosphor-300/15 border border-[var(--border-phosphor)] rounded text-phosphor-200 hover:bg-phosphor-300/25 disabled:opacity-50"
        disabled={submitting}
      >
        {submitting ? "…" : "run"}
      </button>
    </div>
  </form>
</div>
