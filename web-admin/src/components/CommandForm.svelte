<script lang="ts">
  import { apiPost, ApiError } from "$lib/api";
  import { Loader2 } from "$lib/icons";
  import { coerceArgs, defaultValueFor } from "./CommandForm.helpers";
  import type { CommandDescribe } from "$lib/types";

  type Props = {
    describe: CommandDescribe;
    onResult: (ok: boolean, message: string, payload?: unknown) => void;
    onCancel?: () => void;
    /** When true, dim the submit button until the user touches a field
     *  — used by destructive verbs surfaced inside a ConfirmDialog body. */
    confirmStyle?: boolean;
  };
  let { describe, onResult, onCancel, confirmStyle = false }: Props = $props();

  // Form state: one string entry per field, seeded from defaults. Values is
  // a plain Record so $state tracks key changes; new fields render
  // immediately when the schema swaps.
  let values = $state<Record<string, string>>(seedDefaults());
  let busy = $state(false);
  let touched = $state(false);
  let errors = $state<Record<string, string>>({});
  let topError = $state("");

  function seedDefaults(): Record<string, string> {
    const out: Record<string, string> = {};
    for (const f of describe.argsSchema.fields) {
      out[f.name] = defaultValueFor(f);
    }
    return out;
  }

  // When describe (the schema) changes, reseed the form.
  $effect(() => {
    void describe.verb; // dependency
    values = seedDefaults();
    errors = {};
    topError = "";
    touched = false;
  });

  function setField(name: string, v: string) {
    values = { ...values, [name]: v };
    touched = true;
    if (errors[name]) {
      const next = { ...errors };
      delete next[name];
      errors = next;
    }
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (busy) return;
    const result = coerceArgs(describe.argsSchema, values);
    if (!result.args) {
      errors = result.errors;
      return;
    }
    busy = true;
    topError = "";
    try {
      const res = await apiPost<{ ok: boolean; result?: unknown; error?: string; traceId?: string }>(
        `/admin/api/commands/${encodeURIComponent(describe.verb)}`,
        result.args,
      );
      if (res.ok === false) {
        topError = res.error || "command failed";
        onResult(false, topError, res);
      } else {
        onResult(true, `${describe.verb} ok`, res);
      }
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message;
      topError = msg;
      onResult(false, msg);
    } finally {
      busy = false;
    }
  }
</script>

<form onsubmit={submit} class="space-y-2 text-[12px]">
  {#if describe.description}
    <p class="text-slate-400 text-[11.5px]">{describe.description}</p>
  {/if}

  {#each describe.argsSchema.fields as f (f.name)}
    <div>
      <label class="flex items-baseline gap-2 text-[11px] text-slate-500 mb-0.5" for="cf-{f.name}">
        <span class="font-mono text-slate-300">{f.name}</span>
        <span class="text-slate-500">{f.kind}{f.required ? "" : " · optional"}</span>
        {#if f.help}<span class="text-slate-500 italic">— {f.help}</span>{/if}
      </label>
      {#if f.enum && f.enum.length > 0}
        <select
          id="cf-{f.name}"
          class="w-full bg-black/40 border border-white/10 rounded px-2 py-1 text-slate-200 focus:outline-none focus:border-accent-300/50"
          value={values[f.name] ?? ""}
          onchange={(e) => setField(f.name, (e.currentTarget as HTMLSelectElement).value)}
        >
          {#each f.enum as opt (opt)}
            <option value={opt}>{opt}</option>
          {/each}
        </select>
      {:else if f.kind === "bool"}
        <select
          id="cf-{f.name}"
          class="w-full bg-black/40 border border-white/10 rounded px-2 py-1 text-slate-200 focus:outline-none focus:border-accent-300/50"
          value={values[f.name] ?? "false"}
          onchange={(e) => setField(f.name, (e.currentTarget as HTMLSelectElement).value)}
        >
          <option value="false">false</option>
          <option value="true">true</option>
        </select>
      {:else if f.kind === "int" || f.kind === "int32" || f.kind === "int64" || f.kind === "uint32" || f.kind === "uint64"}
        <input
          id="cf-{f.name}"
          type="number"
          step="1"
          class="w-full bg-black/40 border border-white/10 rounded px-2 py-1 text-slate-200 focus:outline-none focus:border-accent-300/50"
          value={values[f.name] ?? ""}
          oninput={(e) => setField(f.name, (e.currentTarget as HTMLInputElement).value)}
        />
      {:else if f.kind === "float32" || f.kind === "float64"}
        <input
          id="cf-{f.name}"
          type="number"
          step="any"
          class="w-full bg-black/40 border border-white/10 rounded px-2 py-1 text-slate-200 focus:outline-none focus:border-accent-300/50"
          value={values[f.name] ?? ""}
          oninput={(e) => setField(f.name, (e.currentTarget as HTMLInputElement).value)}
        />
      {:else}
        <input
          id="cf-{f.name}"
          type="text"
          class="w-full bg-black/40 border border-white/10 rounded px-2 py-1 text-slate-200 focus:outline-none focus:border-accent-300/50"
          value={values[f.name] ?? ""}
          oninput={(e) => setField(f.name, (e.currentTarget as HTMLInputElement).value)}
        />
      {/if}
      {#if errors[f.name]}
        <p class="text-rose-300 text-[10.5px] mt-0.5">{errors[f.name]}</p>
      {/if}
    </div>
  {/each}

  {#if topError}
    <p class="text-rose-300">{topError}</p>
  {/if}

  <div class="flex justify-end gap-2 pt-1">
    {#if onCancel}
      <button type="button" class="px-3 py-1 bg-white/5 border border-white/10 rounded" onclick={onCancel}>
        Cancel
      </button>
    {/if}
    <button
      type="submit"
      disabled={busy || (confirmStyle && !touched)}
      class="px-3 py-1 bg-accent-400 hover:bg-accent-500 text-slate-950 font-semibold rounded flex items-center gap-1 disabled:opacity-50"
    >
      {#if busy}<Loader2 class="w-3 h-3 animate-spin" />{/if}
      Run
    </button>
  </div>
</form>
