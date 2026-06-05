<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { stream } from "$lib/stream";
  import { ApiError } from "$lib/api";
  import {
    fetchTunables,
    setTunable,
    resetTunable,
    groupRows,
    type TuneSystem,
    type TuneRow,
  } from "$lib/tunables";

  let systems = $state<TuneSystem[]>([]);
  let error = $state("");
  let loading = $state(true);
  let toast = $state<{ msg: string; ok: boolean } | null>(null);

  function flashError(msg: string) {
    toast = { msg, ok: false };
    setTimeout(() => (toast = null), 4000);
  }

  async function refresh() {
    loading = true;
    error = "";
    try {
      systems = await fetchTunables();
    } catch (e) {
      if (e instanceof ApiError && e.kind === "rbac") {
        error = "You need the tune.list grant to view this page.";
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

  // Live updates: the "tunables" SSE topic carries { system, rows } whenever any
  // operator (or this page) mutates a value. Replace just that system's fields
  // so concurrent edits surface without a full refetch. Cleanup on unmount.
  $effect(() => {
    const off = stream.subscribe("tunables", (data) => {
      const d = data as { system?: string; rows?: TuneRow[] };
      if (!d || !d.system) return;
      const next = groupRows(d.rows ?? []);
      const incoming = next.find((s) => s.name === d.system);
      const idx = systems.findIndex((s) => s.name === d.system);
      if (idx >= 0) {
        if (incoming) {
          systems[idx] = incoming;
          systems = [...systems];
        }
      } else if (incoming) {
        systems = [...systems, incoming].sort((a, b) =>
          a.name.localeCompare(b.name),
        );
      }
    });
    return off;
  });

  // Mutate a field's value locally (optimistic) so sliders stay smooth before
  // the SSE echo lands.
  function applyLocal(system: string, field: string, value: string) {
    const si = systems.findIndex((s) => s.name === system);
    if (si < 0) return;
    const fi = systems[si].fields.findIndex((f) => f.Field === field);
    if (fi < 0) return;
    systems[si].fields[fi] = { ...systems[si].fields[fi], Value: value };
    systems = [...systems];
  }

  async function commit(system: string, field: string, value: string) {
    try {
      await setTunable(system, field, value);
    } catch (e) {
      flashError(e instanceof ApiError ? e.message : (e as Error).message);
      // Re-fetch to discard the rejected optimistic value.
      void refresh();
    }
  }

  // Reset to tag defaults. tune.reset re-publishes the post-reset rows on the
  // SSE topic; we also snap values locally so the controls move immediately.
  // Fields with no declared default ("") are left as-is (tune.reset skips them).
  async function resetField(system: string, r: TuneRow) {
    if (r.Default === "") return;
    applyLocal(system, r.Field, r.Default);
    try {
      await resetTunable(system, r.Field);
    } catch (e) {
      flashError(e instanceof ApiError ? e.message : (e as Error).message);
      void refresh();
    }
  }

  function snapSystemLocal(system: string) {
    const si = systems.findIndex((s) => s.name === system);
    if (si < 0) return;
    for (const f of systems[si].fields) {
      if (f.Default !== "") applyLocal(system, f.Field, f.Default);
    }
  }

  async function resetSystem(system: string) {
    snapSystemLocal(system);
    try {
      await resetTunable(system);
    } catch (e) {
      flashError(e instanceof ApiError ? e.message : (e as Error).message);
      void refresh();
    }
  }

  async function resetAll() {
    for (const s of systems) snapSystemLocal(s.name);
    try {
      await Promise.all(systems.map((s) => resetTunable(s.name)));
    } catch (e) {
      flashError(e instanceof ApiError ? e.message : (e as Error).message);
      void refresh();
    }
  }

  // Per-field debounce timers for the slider oninput stream (~80ms).
  const timers = new Map<string, ReturnType<typeof setTimeout>>();
  onDestroy(() => {
    for (const t of timers.values()) clearTimeout(t);
    timers.clear();
  });
  function debouncedCommit(system: string, field: string, value: string) {
    const key = `${system} ${field}`;
    const prev = timers.get(key);
    if (prev) clearTimeout(prev);
    timers.set(
      key,
      setTimeout(() => {
        timers.delete(key);
        void commit(system, field, value);
      }, 80),
    );
  }

  function isNumeric(r: TuneRow): boolean {
    return r.Kind === "int" || r.Kind === "uint" || r.Kind === "float";
  }
  function bounded(r: TuneRow): boolean {
    return isNumeric(r) && r.Min !== "" && r.Max !== "";
  }

  // Slider handler: optimistic local update + debounced commit.
  function onSlide(system: string, r: TuneRow, e: Event) {
    const v = (e.currentTarget as HTMLInputElement).value;
    applyLocal(system, r.Field, v);
    debouncedCommit(system, r.Field, v);
  }

  // Unbounded number / toggle: commit on change (no debounce needed).
  function onNumberChange(system: string, r: TuneRow, e: Event) {
    const v = (e.currentTarget as HTMLInputElement).value;
    applyLocal(system, r.Field, v);
    void commit(system, r.Field, v);
  }

  function onToggle(system: string, r: TuneRow, e: Event) {
    const v = (e.currentTarget as HTMLInputElement).checked ? "true" : "false";
    applyLocal(system, r.Field, v);
    void commit(system, r.Field, v);
  }

  function isTruthy(s: string): boolean {
    return s === "true" || s === "1";
  }
</script>

<main class="p-4 space-y-3">
  <div class="flex items-center justify-between">
    <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Tunables</h2>
    <div class="flex items-center gap-2">
      {#if systems.length > 0}
        <button
          class="px-2 py-0.5 text-[10.5px] bg-white/5 border border-white/10 rounded text-slate-300 hover:bg-amber-500/10 hover:border-amber-500/40 hover:text-amber-200"
          title="Reset every tunable in every system to its default"
          onclick={() => void resetAll()}
        >
          reset all
        </button>
      {/if}
      <button
        class="px-2 py-0.5 text-[10.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10"
        onclick={() => void refresh()}
      >
        refresh
      </button>
    </div>
  </div>

  {#if loading}
    <div class="text-slate-500 text-[12px]">Loading tunables…</div>
  {:else if error}
    <div
      class="text-[12px] px-3 py-1.5 rounded bg-rose-900/30 text-rose-200 border border-rose-700/40"
    >
      {error}
    </div>
  {:else if systems.length === 0}
    <div class="text-slate-500 text-[12px]">No tunable systems registered.</div>
  {:else}
    <div class="space-y-3">
      {#each systems as sys (sys.name)}
        <section class="bg-[#0d1117] border border-white/10 rounded-lg p-3">
          <div class="flex items-center justify-between mb-2">
            <h3
              class="text-slate-300 text-[11px] uppercase tracking-wide font-mono"
            >
              {sys.name}
            </h3>
            <button
              class="px-1.5 py-0.5 text-[10px] text-slate-500 border border-transparent rounded hover:text-amber-200 hover:border-amber-500/40"
              title="Reset all of {sys.name}'s tunables to their defaults"
              onclick={() => void resetSystem(sys.name)}
            >
              reset
            </button>
          </div>
          <div class="space-y-1.5">
            {#each sys.fields as f (f.Field)}
              <div class="flex items-center gap-3 text-[12px]">
                <span
                  class="w-44 shrink-0 font-mono text-slate-300 truncate"
                  title={f.Field}
                >
                  {f.Field}
                  <span class="text-slate-600 text-[10px]">·{f.Kind}</span>
                </span>

                {#if f.Kind === "bool"}
                  <label class="inline-flex items-center cursor-pointer">
                    <input
                      type="checkbox"
                      class="sr-only peer"
                      checked={isTruthy(f.Value)}
                      onchange={(e) => onToggle(sys.name, f, e)}
                    />
                    <span
                      class="w-9 h-5 rounded-full bg-white/10 border border-white/15 peer-checked:bg-accent-300/40 peer-checked:border-accent-300/60 relative transition-colors"
                    >
                      <span
                        class="absolute top-0.5 left-0.5 w-3.5 h-3.5 rounded-full bg-slate-300 transition-transform peer-checked:translate-x-4"
                      ></span>
                    </span>
                    <span class="ml-2 font-mono text-slate-400 w-10">
                      {isTruthy(f.Value) ? "on" : "off"}
                    </span>
                  </label>
                {:else if bounded(f)}
                  <input
                    type="range"
                    class="grow accent-accent-300"
                    min={f.Min}
                    max={f.Max}
                    step={f.Kind === "float" ? "any" : "1"}
                    value={f.Value}
                    oninput={(e) => onSlide(sys.name, f, e)}
                  />
                  <span
                    class="w-20 shrink-0 text-right font-mono tabular-nums text-accent-300"
                  >
                    {f.Value}
                  </span>
                  <span
                    class="w-28 shrink-0 text-[10px] font-mono text-slate-600 text-right"
                  >
                    [{f.Min}, {f.Max}]
                  </span>
                {:else}
                  <input
                    type="number"
                    class="w-32 bg-white/5 border border-white/10 rounded px-2 py-0.5 font-mono tabular-nums text-slate-200 focus:outline-none focus:border-accent-300/40"
                    step={f.Kind === "float" ? "any" : "1"}
                    min={f.Min || undefined}
                    max={f.Max || undefined}
                    value={f.Value}
                    onchange={(e) => onNumberChange(sys.name, f, e)}
                  />
                  <span class="text-[10px] font-mono text-slate-600">
                    default {f.Default || "—"}
                  </span>
                {/if}

                <button
                  class="ml-auto shrink-0 w-6 text-center text-[13px] leading-none text-slate-600 hover:text-amber-200 disabled:opacity-25 disabled:hover:text-slate-600"
                  title={f.Default !== ""
                    ? `Reset ${f.Field} to ${f.Default}`
                    : "No default to reset to"}
                  aria-label="Reset {f.Field}"
                  disabled={f.Default === ""}
                  onclick={() => void resetField(sys.name, f)}
                >
                  ↺
                </button>
              </div>
            {/each}
          </div>
        </section>
      {/each}
    </div>
  {/if}

  {#if toast}
    <div
      class="text-[12px] px-3 py-1.5 rounded {toast.ok
        ? 'bg-emerald-900/30 text-emerald-200 border border-emerald-700/40'
        : 'bg-rose-900/30 text-rose-200 border border-rose-700/40'}"
    >
      {toast.msg}
    </div>
  {/if}
</main>
