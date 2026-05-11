# Admin Custom Panel API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let games register dashboard panels from Go alone — the engine SPA renders any registered `PanelDef` dynamically with one Svelte component, no per-game JS commits required. Ship the 4node-basic "Bots" panel as the worked example.

**Architecture:** A single `PanelHost.svelte` reads `PanelDef.Topics` + `PanelDef.Commands` and renders an auto-derived data table plus a toolbar. Game code publishes live data via a new `mmokit.PublishAdminTopic(coord, topic, payload)` helper that delivers into the existing admin `TopicBus`. Commands with non-empty argsSchema open a generic `ArgsModal.svelte`; zero-arg commands POST directly.

**Tech Stack:** Go (`pkg/admin`, `pkg/mmokit`, `pkg/universe`), Svelte 5 runes, Tailwind v4, Bun (NEVER npm), `@lucide/svelte` 1.14.

**Spec:** [`docs/superpowers/specs/2026-05-11-admin-custom-panel-api-design.md`](../specs/2026-05-11-admin-custom-panel-api-design.md).

---

## Quick orientation

What already exists, reusable as-is:

- **`pkg/admin/panel.go`** — `PanelDef` struct + `PanelRegistry`. Builtins registered in `pkg/admin/builtins.go::RegisterBuiltinPanels`; games call `mmokit.RegisterAdminPanel(coord, def)` to add their own. `/admin/api/panels` already serves the registry as JSON.
- **`pkg/admin/topicbus.go`** — `TopicBus.Publish(topic, payload)` already fans out to SSE subscribers via `/admin/api/stream`. We add a way for game code to reach this bus.
- **`pkg/mmokit/admin.go`** — has `adminPanelMap[*Process]*admin.PanelRegistry` lazy-init pattern. We mirror it for buses.
- **`pkg/cmdsys/schema.go`** — `Schema{Fields []FieldSchema}` already exposed at `/admin/api/commands/<verb>`'s `argsSchema` response field. ArgsModal renders from this directly.
- **`web-admin/src/lib/stores.svelte.ts`** — has `panelsStore: Store<PanelDef[]>`. Declared but never hydrated; we add the boot fetch.
- **`web-admin/src/components/Sidebar.svelte`** — has the items list + group-bucketing logic (lines 18-41). We extend it to also iterate `panelsStore.value`.
- **`web-admin/src/components/DataTable.svelte`** — sortable, accepts `rows`, `columns`, `initialSortKey`, `emptyText`. Used by `PanelHost` for the auto-table.
- **`web-admin/src/lib/stream.ts`** — `stream.subscribe(topic, handler)` returns an unsubscribe fn.
- **`examples/4node-basic/command_bots.go`** — has `snapshotCells(coord)`, `countBotsOnLoop(cell)`, `mmokit.CmdOnLoop`. The publisher reuses these.

Build / verification:

```bash
# Backend
go vet ./pkg/admin/... ./pkg/mmokit/... ./pkg/universe/...
go test ./pkg/admin/... ./pkg/mmokit/...

# Frontend
cd web-admin && bun run typecheck
cd web-admin && bun run build   # regenerates pkg/admin/static/dist/
```

---

## File structure

**New files:**

```
pkg/mmokit/admin_test.go                                  # PublishAdminTopic + adminBus tests
web-admin/src/components/PanelHost.svelte                 # dynamic renderer
web-admin/src/components/ArgsModal.svelte                 # typed args form
examples/4node-basic/admin_bots_publisher.go              # 1Hz publisher
```

**Modified files:**

```
pkg/admin/admin.go                                        # ServerOpts.Bus field
pkg/mmokit/admin.go                                       # adminBus + PublishAdminTopic + factory wires Bus
web-admin/src/lib/types.ts                                # FieldSchema + Schema types
web-admin/src/lib/stores.svelte.ts                        # (only if panelsStore needs adjusting)
web-admin/src/lib/icons.ts                                # add Bot icon
web-admin/src/components/Sidebar.svelte                   # iterate panelsStore + icon map
web-admin/src/app.svelte                                  # hydrate panelsStore + /panel/<id> route
examples/4node-basic/main.go                              # RegisterAdminPanel + startBotsPublisher
CLAUDE.md                                                 # one paragraph on the custom panel API
```

---

### Task 1: Backend — `ServerOpts.Bus` field on `admin.Server`

**Files:**
- Modify: `pkg/admin/admin.go`

The admin Server currently creates its `*TopicBus` internally. We make it injectable so the bus can be created upstream in `pkg/mmokit` and shared with game code that needs to publish.

- [ ] **Step 1: Read current Server construction**

```bash
grep -n "NewTopicBus\|ServerOpts\|bus *TopicBus\|bus:" pkg/admin/admin.go | head -10
```

You'll find `bus := NewTopicBus(0)` inside `NewServer`. The `Server` struct already has a `bus *TopicBus` field.

- [ ] **Step 2: Add `Bus` to ServerOpts**

In `pkg/admin/admin.go`, add to the `ServerOpts` struct (between the existing fields):

```go
// Bus optionally injects a pre-created topic bus. Used by mmokit so game
// code can publish to admin topics before the Server is constructed. If
// nil, NewServer creates one internally.
Bus *TopicBus
```

- [ ] **Step 3: Use the injected bus when present**

In `NewServer`, replace:

```go
bus := NewTopicBus(0)
```

with:

```go
bus := opts.Bus
if bus == nil {
    bus = NewTopicBus(0)
}
```

- [ ] **Step 4: Verify**

```bash
go vet ./pkg/admin/...
go test ./pkg/admin/...
```

Both must be clean. Existing tests don't set `Bus`, so they hit the fallback branch — behavior preserved.

- [ ] **Step 5: Commit**

```bash
git add pkg/admin/admin.go
git commit -m "$(cat <<'EOF'
admin: ServerOpts.Bus field for injected topic bus

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Backend — `mmokit.PublishAdminTopic`

**Files:**
- Modify: `pkg/mmokit/admin.go`

Add a per-`*Process` bus accessor mirroring `adminPanelRegistry`. Game code calls `mmokit.PublishAdminTopic(coord, topic, payload)` from any goroutine; the factory wires the SAME bus into `admin.ServerOpts.Bus` so SSE subscribers receive published payloads.

- [ ] **Step 1: Read the existing pattern**

```bash
sed -n '1,90p' pkg/mmokit/admin.go
```

You'll see `adminPanelMu / adminPanelMap` (the panel-registry lazy cache). Mirror it for buses.

- [ ] **Step 2: Add the bus map + accessor**

In `pkg/mmokit/admin.go`, right after the `adminPanelMap` block (before `RegisterAdminPanel`), add:

```go
// adminBusMap caches the *admin.TopicBus per *universe.Process so game-side
// code can publish to admin topics before the admin Server is constructed.
// DefaultAdminServerFactory pulls the same bus into ServerOpts so the
// SSE multiplexer fans payloads out to subscribers.
var (
	adminBusMu  sync.Mutex
	adminBusMap = map[*universe.Process]*admin.TopicBus{}
)

func adminBus(c *universe.Process) *admin.TopicBus {
	adminBusMu.Lock()
	defer adminBusMu.Unlock()
	b, ok := adminBusMap[c]
	if !ok {
		b = admin.NewTopicBus(0)
		adminBusMap[c] = b
	}
	return b
}

// PublishAdminTopic publishes payload on topic to the admin dashboard's
// SSE multiplexer. Game-registered admin panels subscribe to topics by
// name (PanelDef.Topics) — this is the matching push surface.
//
// No-op when no subscribers are listening. Safe to call from any
// goroutine. The bus is per-Process so test fixtures get isolation.
func PublishAdminTopic(coord *universe.Process, topic string, payload any) {
	adminBus(coord).Publish(topic, payload)
}
```

- [ ] **Step 3: Wire the bus through `DefaultAdminServerFactory`**

In the same file, locate the `admin.ServerOpts{...}` block inside `DefaultAdminServerFactory`. Add one field to the struct literal:

```go
Bus: adminBus(c),
```

Place it next to the existing `Panels: adminPanelRegistry(c)` line so the parallel symmetry is obvious.

- [ ] **Step 4: Verify**

```bash
go vet ./pkg/mmokit/... ./pkg/admin/...
go build ./...   # NO — use go vet only per CLAUDE.md
```

Actually run:

```bash
go vet ./pkg/mmokit/... ./pkg/admin/... ./pkg/universe/...
```

Clean.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/admin.go
git commit -m "$(cat <<'EOF'
mmokit: PublishAdminTopic + per-Process bus map

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Backend — tests for `PublishAdminTopic`

**Files:**
- Create: `pkg/mmokit/admin_test.go`

Verify the bus round-trips a payload from `PublishAdminTopic` to a `Subscribe`-d consumer.

- [ ] **Step 1: Check existing mmokit test infrastructure**

```bash
ls pkg/mmokit/*_test.go
grep -n "func New\|func.*Subscriber" pkg/admin/topicbus.go | head -8
```

Note the `Subscriber` interface:

```go
type Subscriber interface {
    Topics() []string
    Receive(topic string, payload any) error
}
```

- [ ] **Step 2: Write the test**

Create `pkg/mmokit/admin_test.go`:

```go
package mmokit

import (
	"sync"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/universe"
)

// fakeSub captures topics + payloads delivered by the TopicBus. Implements
// admin.Subscriber.
type fakeSub struct {
	mu       sync.Mutex
	topics   []string
	received []received
	notify   chan struct{}
}

type received struct {
	topic   string
	payload any
}

func (f *fakeSub) Topics() []string { return f.topics }

func (f *fakeSub) Receive(topic string, payload any) error {
	f.mu.Lock()
	f.received = append(f.received, received{topic: topic, payload: payload})
	f.mu.Unlock()
	select {
	case f.notify <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeSub) wait(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		f.mu.Lock()
		got := len(f.received)
		f.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-f.notify:
		case <-deadline:
			t.Fatalf("waited for %d deliveries, got %d", n, got)
		}
	}
}

func TestPublishAdminTopic_RoutesToBus(t *testing.T) {
	t.Parallel()
	// Use a non-nil pointer key — adminBus only needs *Process for map
	// identity, not for any field access.
	proc := &universe.Process{}
	t.Cleanup(func() {
		adminBusMu.Lock()
		delete(adminBusMap, proc)
		adminBusMu.Unlock()
	})

	bus := adminBus(proc)
	sub := &fakeSub{topics: []string{"bots"}, notify: make(chan struct{}, 8)}
	bus.Subscribe(sub, sub.topics...)
	t.Cleanup(func() { bus.Unsubscribe(sub) })

	PublishAdminTopic(proc, "bots", []int{1, 2, 3})
	sub.wait(t, 1, 500*time.Millisecond)

	sub.mu.Lock()
	defer sub.mu.Unlock()
	if len(sub.received) != 1 {
		t.Fatalf("got %d deliveries, want 1", len(sub.received))
	}
	got := sub.received[0]
	if got.topic != "bots" {
		t.Fatalf("topic = %q, want \"bots\"", got.topic)
	}
	payload, ok := got.payload.([]int)
	if !ok || len(payload) != 3 || payload[0] != 1 {
		t.Fatalf("payload = %#v, want [1 2 3]", got.payload)
	}
}

func TestAdminBus_PerProcessIsolation(t *testing.T) {
	t.Parallel()
	procA := &universe.Process{}
	procB := &universe.Process{}
	t.Cleanup(func() {
		adminBusMu.Lock()
		delete(adminBusMap, procA)
		delete(adminBusMap, procB)
		adminBusMu.Unlock()
	})

	subA := &fakeSub{topics: []string{"T"}, notify: make(chan struct{}, 8)}
	subB := &fakeSub{topics: []string{"T"}, notify: make(chan struct{}, 8)}
	adminBus(procA).Subscribe(subA, "T")
	adminBus(procB).Subscribe(subB, "T")
	t.Cleanup(func() {
		adminBus(procA).Unsubscribe(subA)
		adminBus(procB).Unsubscribe(subB)
	})

	PublishAdminTopic(procA, "T", "A-only")
	subA.wait(t, 1, 500*time.Millisecond)

	subB.mu.Lock()
	defer subB.mu.Unlock()
	if len(subB.received) != 0 {
		t.Fatalf("procB sub received %d deliveries, want 0", len(subB.received))
	}
}
```

- [ ] **Step 3: Run**

```bash
go test ./pkg/mmokit/ -run "TestPublishAdminTopic|TestAdminBus" -v
```

Both tests pass.

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/admin_test.go
git commit -m "$(cat <<'EOF'
mmokit: tests for PublishAdminTopic + per-Process bus isolation

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Frontend — `FieldSchema` + `Schema` types

**Files:**
- Modify: `web-admin/src/lib/types.ts`

ArgsModal renders from the server-side schema. Add the matching TS types.

- [ ] **Step 1: Append at the end of `web-admin/src/lib/types.ts`**

```ts
// FieldSchema mirrors pkg/cmdsys/schema.go::FieldSchema. Returned as part
// of the GET /admin/api/commands/<verb> response's argsSchema field.
export type FieldSchema = {
  name: string;
  kind: string;          // "string", "int32", "int64", "float32", "float64", "bool", "[]<elem>", "{...}"
  required: boolean;
  named_only: boolean;
  default: string;
  enum: string[] | null;
  help?: string;
  rest?: boolean;
  complete?: string;
};

// Schema mirrors pkg/cmdsys/schema.go::Schema.
export type Schema = {
  struct: string;
  fields: FieldSchema[];
};
```

- [ ] **Step 2: Typecheck**

```bash
cd web-admin && bun run typecheck
```

0 errors.

- [ ] **Step 3: Commit**

```bash
cd .
git add web-admin/src/lib/types.ts
git commit -m "$(cat <<'EOF'
web-admin: FieldSchema + Schema types

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Frontend — `ArgsModal.svelte`

**Files:**
- Create: `web-admin/src/components/ArgsModal.svelte`

Generic typed-arg form. Opened by `PanelHost` when a toolbar button's verb has a non-empty schema.

- [ ] **Step 1: Implement**

Create `web-admin/src/components/ArgsModal.svelte`:

```svelte
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

  // Per-field input state, keyed by field name. Stored as strings;
  // coerced to typed values on submit.
  let values = $state<Record<string, string>>({});
  let checks = $state<Record<string, boolean>>({});
  let error = $state("");
  let submitting = $state(false);

  // Seed defaults the first time the modal renders for this verb.
  $effect(() => {
    void verb; // recompute when verb swaps
    const v: Record<string, string> = {};
    const c: Record<string, boolean> = {};
    for (const f of schema.fields ?? []) {
      v[f.name] = f.default ?? "";
      c[f.name] = f.default === "true";
    }
    values = v;
    checks = c;
  });

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

<div class="fixed inset-0 bg-black/40 flex items-center justify-center z-50" onclick={onClose} role="presentation">
  <form
    class="bg-[#0d1117] border border-white/10 rounded-lg p-4 w-[420px] max-w-[90vw] space-y-3"
    onsubmit={submit}
    onclick={(e: Event) => e.stopPropagation()}
  >
    <div class="flex items-center justify-between">
      <h3 class="text-[13px] text-accent-300 font-mono">{verb}</h3>
      <button type="button" class="text-slate-500 hover:text-slate-200 text-[12px]" onclick={onClose}>esc</button>
    </div>

    {#if (schema.fields ?? []).length === 0}
      <div class="text-[12px] text-slate-500 italic">No arguments — submit to run.</div>
    {/if}

    {#each schema.fields ?? [] as f (f.name)}
      <label class="block text-[11.5px]">
        <span class="text-slate-400">
          {f.name}
          {#if f.required}<span class="text-rose-400">*</span>{/if}
          <span class="text-slate-600 ml-1">({f.kind})</span>
        </span>
        {#if f.kind === "bool"}
          <input
            type="checkbox"
            class="mt-1 accent-accent-400"
            bind:checked={checks[f.name]}
          />
        {:else if f.kind === "int32" || f.kind === "int64"}
          <input
            type="number"
            step="1"
            class="mt-1 w-full bg-white/5 border border-white/10 rounded px-2 py-1 text-slate-200"
            bind:value={values[f.name]}
            placeholder={f.default}
          />
        {:else if f.kind === "float32" || f.kind === "float64"}
          <input
            type="number"
            step="any"
            class="mt-1 w-full bg-white/5 border border-white/10 rounded px-2 py-1 text-slate-200"
            bind:value={values[f.name]}
            placeholder={f.default}
          />
        {:else if f.kind === "string"}
          <input
            type="text"
            class="mt-1 w-full bg-white/5 border border-white/10 rounded px-2 py-1 text-slate-200"
            bind:value={values[f.name]}
            placeholder={f.default}
          />
        {:else}
          <input
            type="text"
            class="mt-1 w-full bg-white/5 border border-white/10 rounded px-2 py-1 text-slate-200 font-mono"
            bind:value={values[f.name]}
            placeholder='JSON (e.g. ["a","b"] or {"k":1})'
          />
        {/if}
        {#if f.help}
          <span class="text-[10.5px] text-slate-500">{f.help}</span>
        {/if}
      </label>
    {/each}

    {#if error}
      <div class="text-rose-300 text-[11.5px]">{error}</div>
    {/if}

    <div class="flex justify-end gap-2">
      <button
        type="button"
        class="px-3 py-1 text-[11.5px] bg-white/5 border border-white/10 rounded text-slate-300 hover:bg-white/10"
        onclick={onClose}
      >
        cancel
      </button>
      <button
        type="submit"
        class="px-3 py-1 text-[11.5px] bg-accent-300/20 border border-accent-300/40 rounded text-accent-200 hover:bg-accent-300/30 disabled:opacity-50"
        disabled={submitting}
      >
        {submitting ? "…" : "run"}
      </button>
    </div>
  </form>
</div>
```

- [ ] **Step 2: Typecheck**

```bash
cd web-admin && bun run typecheck
```

0 errors.

- [ ] **Step 3: Commit**

```bash
cd .
git add web-admin/src/components/ArgsModal.svelte
git commit -m "$(cat <<'EOF'
web-admin: ArgsModal.svelte — typed args form for cmdsys verbs

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Frontend — `PanelHost.svelte`

**Files:**
- Create: `web-admin/src/components/PanelHost.svelte`

The single dynamic renderer. Reads any `PanelDef`, subscribes to the first declared topic, renders a DataTable when the payload is a row array (or a scalar dump otherwise), and shows toolbar buttons for each declared command.

- [ ] **Step 1: Implement**

Create `web-admin/src/components/PanelHost.svelte`:

```svelte
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
    <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">{panel.label}</h2>
    <div class="flex gap-1.5">
      {#each panel.commands ?? [] as verb (verb)}
        {@const err = schemaError[verb]}
        <button
          type="button"
          class="px-2 py-0.5 text-[11px] rounded border border-white/10 bg-white/5 text-slate-200 hover:bg-white/10 disabled:opacity-50"
          title={err ? `schema error: ${err}` : verb}
          disabled={!schemas[verb]}
          onclick={() => runVerb(verb)}
        >
          {verb}
        </button>
      {/each}
    </div>
  </div>

  <div class="bg-[#0d1117] border border-white/10 rounded-lg p-3">
    {#if rows.length > 0}
      <DataTable
        rows={rows}
        columns={columns}
        emptyText="No data."
      />
    {:else if isScalarObject}
      <div class="grid grid-cols-[160px_1fr] gap-x-3 gap-y-1 text-[12px]">
        {#each Object.entries(payload as Record<string, unknown>) as [k, v] (k)}
          <span class="text-slate-500 font-mono">{k}</span>
          <span class="text-slate-200 font-mono">
            {typeof v === "object" ? JSON.stringify(v) : String(v)}
          </span>
        {/each}
      </div>
    {:else}
      <div class="text-slate-500 italic text-[12px] py-4 text-center">
        Waiting for topic {panel.topics?.[0] ?? "(none)"}…
      </div>
    {/if}
  </div>

  {#if toast}
    <div
      class="text-[12px] px-3 py-1.5 rounded {toast.ok
        ? 'bg-emerald-900/30 text-emerald-200 border border-emerald-700/40'
        : 'bg-rose-900/30 text-rose-200 border border-rose-700/40'}"
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
```

- [ ] **Step 2: Typecheck**

```bash
cd web-admin && bun run typecheck
```

0 errors. The `DataTable` cell `accessor` signature is `(row) => string | number` per the existing component contract; that's why `render` is split out for object/null formatting.

- [ ] **Step 3: Commit**

```bash
cd .
git add web-admin/src/components/PanelHost.svelte
git commit -m "$(cat <<'EOF'
web-admin: PanelHost.svelte — dynamic renderer for game-registered panels

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Frontend — icons + Sidebar wiring

**Files:**
- Modify: `web-admin/src/lib/icons.ts`
- Modify: `web-admin/src/components/Sidebar.svelte`

Add a `Bot` icon and a small icon lookup so PanelDef.Icon string values map to actual Svelte components. Render game-registered panels alongside builtins.

- [ ] **Step 1: Add Bot icon**

Append to `web-admin/src/lib/icons.ts`:

```ts
export { default as Bot } from "@lucide/svelte/icons/bot";
export { default as Database } from "@lucide/svelte/icons/database";
export { default as Layers } from "@lucide/svelte/icons/layers";
export { default as Zap } from "@lucide/svelte/icons/zap";
export { default as Gauge } from "@lucide/svelte/icons/gauge";
```

(Stub set of common game-panel icon names. Games whose Icon string is missing fall back to `Boxes`.)

- [ ] **Step 2: Modify Sidebar.svelte**

Read the current Sidebar first to be sure of the structure:

```bash
sed -n '1,45p' web-admin/src/components/Sidebar.svelte
```

Replace the existing top-of-file imports + state with:

```svelte
<script lang="ts">
  import {
    Globe, Boxes, Users, Activity, List, Scroll, Settings, ShieldCheck,
    Bot, Database, Layers, Zap, Gauge, Server,
  } from "$lib/icons";
  import { navigate, route } from "$lib/router";
  import { panelsStore } from "$lib/stores.svelte";
  import type { PanelDef } from "$lib/types";

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  type IconComponent = any;

  type Item = {
    id: string;
    label: string;
    icon: IconComponent;
    group: string;
    path: string;
  };

  // Map PanelDef.Icon string names → Svelte components. Unknown names
  // fall back to Boxes.
  const iconMap: Record<string, IconComponent> = {
    Bot, Database, Layers, Zap, Gauge, Server,
    Globe, Boxes, Users, Activity, List, Scroll, Settings, ShieldCheck,
  };

  function iconFor(name: string): IconComponent {
    return iconMap[name] ?? Boxes;
  }

  const builtinItems: Item[] = [
    { id: "cluster", label: "Cells", icon: Globe, group: "CLUSTER", path: "/cluster" },
    { id: "nodes", label: "Nodes", icon: Boxes, group: "CLUSTER", path: "/nodes" },
    { id: "players", label: "Players", icon: Users, group: "PEOPLE", path: "/players" },
    { id: "performance", label: "Performance", icon: Activity, group: "DIAGNOSE", path: "/performance" },
    { id: "events", label: "Events", icon: List, group: "DIAGNOSE", path: "/events" },
    { id: "audit", label: "Audit", icon: ShieldCheck, group: "DIAGNOSE", path: "/audit" },
    { id: "logs", label: "Logs", icon: Scroll, group: "DIAGNOSE", path: "/logs" },
    { id: "settings", label: "Settings", icon: Settings, group: "CONFIG", path: "/settings" },
  ];

  let panels = $derived<PanelDef[]>(panelsStore.value ?? []);

  // Merge game-registered panels (those NOT already represented by a
  // builtin path) under their declared group, upper-cased to match the
  // builtin group convention.
  let items = $derived.by<Item[]>(() => {
    const builtinPaths = new Set(builtinItems.map((b) => b.path));
    const extras: Item[] = [];
    for (const p of panels) {
      const path = `/panel/${p.id}`;
      if (builtinPaths.has(path)) continue;
      extras.push({
        id: p.id,
        label: p.label,
        icon: iconFor(p.icon),
        group: (p.group || "PANELS").toUpperCase(),
        path,
      });
    }
    return [...builtinItems, ...extras];
  });

  let currentPath = $state("/cluster");
  $effect(() => {
    const off = route.subscribe((p: string) => (currentPath = p));
    return off;
  });

  // Group items by group, preserving insertion order. Same algorithm as
  // before — it works for the dynamic list too.
  let groups = $derived.by<{ name: string; items: Item[] }[]>(() => {
    const out: { name: string; items: Item[] }[] = [];
    for (const it of items) {
      const last = out[out.length - 1];
      if (last && last.name === it.group) last.items.push(it);
      else out.push({ name: it.group, items: [it] });
    }
    return out;
  });
</script>
```

(Keep the markup section below the script unchanged — it already iterates `groups`.)

- [ ] **Step 3: Typecheck**

```bash
cd web-admin && bun run typecheck
```

0 errors.

- [ ] **Step 4: Commit**

```bash
cd .
git add web-admin/src/lib/icons.ts web-admin/src/components/Sidebar.svelte
git commit -m "$(cat <<'EOF'
web-admin: Sidebar renders game-registered panels from panelsStore

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Frontend — `app.svelte` hydration + `/panel/<id>` route

**Files:**
- Modify: `web-admin/src/app.svelte`

Fetch `/admin/api/panels` at boot to populate `panelsStore`. Add a route branch that renders `PanelHost` for any `/panel/<id>` path.

- [ ] **Step 1: Add imports**

In `web-admin/src/app.svelte`, alongside the existing imports:

```svelte
import { panelsStore } from "$lib/stores.svelte";
import type { PanelDef } from "$lib/types";
import PanelHost from "./components/PanelHost.svelte";
```

- [ ] **Step 2: Hydrate panelsStore inside hydrateCluster()**

Find `async function hydrateCluster()`. Add a parallel fetch alongside the existing `apiGet<ClusterInfo>("/admin/api/cluster")` call:

```ts
async function hydrateCluster() {
  try {
    const [c, panels] = await Promise.all([
      apiGet<ClusterInfo>("/admin/api/cluster"),
      apiGet<PanelDef[]>("/admin/api/panels"),
    ]);
    clusterStore.set(c);
    panelsStore.set(panels ?? []);
  } catch {
    // 401 etc. — auth gate redirects to login
  }
  stream.subscribe("cells", () => {
    // CellMap reads cellsStore directly via its own subscription.
  });
}
```

- [ ] **Step 3: Add the `/panel/<id>` route branch**

Find the `{:else if path === "/settings"}` branch. Add immediately after it (before the fallback):

```svelte
{:else if path.startsWith("/panel/")}
  {@const id = path.slice("/panel/".length)}
  {@const def = (panelsStore.value ?? []).find((p) => p.id === id)}
  {#if def}
    <PanelHost panel={def} />
  {:else}
    <div class="p-8 text-slate-500">
      Panel <code>{id}</code> not registered.
    </div>
  {/if}
```

- [ ] **Step 4: Typecheck + build**

```bash
cd web-admin && bun run typecheck && bun run build
```

0 errors, build succeeds (regenerates `pkg/admin/static/dist/`).

- [ ] **Step 5: Commit**

```bash
cd .
git add web-admin/src/app.svelte
git commit -m "$(cat <<'EOF'
web-admin: hydrate panelsStore at boot + /panel/<id> route

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Game-side — `admin_bots_publisher.go`

**Files:**
- Create: `examples/4node-basic/admin_bots_publisher.go`

1 Hz goroutine that walks local cells, counts bots per cell, and publishes to the `bots` topic.

- [ ] **Step 1: Implement**

Create `examples/4node-basic/admin_bots_publisher.go`:

```go
package main

import (
	"context"
	"time"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// BotRow is one row of the "bots" admin topic. The PanelHost auto-derives
// columns from the row keys (JSON tags), so any rename here is reflected
// in the table header on the next publish.
type BotRow struct {
	CellID string `json:"cellId"`
	Count  int    `json:"count"`
}

// startBotsPublisher runs a 1Hz goroutine that publishes the per-cell
// bot count to the admin "bots" topic. Local cells only — cross-host
// enumeration is deferred to a future bot.count cmdsys verb that fans
// out via RouteAllHosts.
func startBotsPublisher(ctx context.Context, coord *mmokit.Process) {
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		// Publish once immediately so the panel doesn't show "waiting
		// for topic…" for a full second after page open.
		mmokit.PublishAdminTopic(coord, "bots", collectBotRows(ctx, coord))
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				mmokit.PublishAdminTopic(coord, "bots", collectBotRows(ctx, coord))
			}
		}
	}()
}

// collectBotRows snapshots the local cell set and asks each one's game
// loop for its current bot count. Cells transferred away during the
// snapshot just drop out — best-effort consistency is fine for a 1Hz
// telemetry topic.
func collectBotRows(ctx context.Context, coord *mmokit.Process) []BotRow {
	cells := snapshotCells(coord)
	rows := make([]BotRow, 0, len(cells))
	for _, cell := range cells {
		n, err := mmokit.CmdOnLoop(ctx, cell.Engine, func() (int, error) {
			return countBotsOnLoop(cell), nil
		})
		if err != nil {
			continue
		}
		rows = append(rows, BotRow{
			CellID: string(cell.MeshID),
			Count:  n,
		})
	}
	return rows
}
```

- [ ] **Step 2: Verify**

```bash
go vet ./examples/4node-basic/...
```

Clean. (The file uses `snapshotCells` and `countBotsOnLoop` from `command_bots.go` in the same package.)

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/admin_bots_publisher.go
git commit -m "$(cat <<'EOF'
4node-basic: admin_bots_publisher — 1Hz bot-count topic

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Game-side — panel registration + publisher startup

**Files:**
- Modify: `examples/4node-basic/main.go`

Register the `bots` PanelDef and start the publisher.

- [ ] **Step 1: Check current imports**

```bash
grep -n "^import\|^	\"\|^	[a-z]" examples/4node-basic/main.go | head -20
```

The file already imports `"context"` indirectly? Check:

```bash
grep -n "context\b" examples/4node-basic/main.go
```

If `context` isn't imported, you'll need to add it. The publisher takes a `context.Context`.

- [ ] **Step 2: Register the panel**

In `examples/4node-basic/main.go`, **after** the existing `mmokit.RegisterKind` lines (around line 82) and **before** `process.OnPlayerJoin(...)`, add:

```go
if err := mmokit.RegisterAdminPanel(process, mmokit.AdminPanelDef{
    ID:       "bots",
    Label:    "Bots",
    Icon:     "Bot",
    Group:    "Game",
    Topics:   []string{"bots"},
    Commands: []string{"bot.spawn", "bot.clear", "bot.list"},
}); err != nil {
    log.Fatalf("4node-basic: register bots panel: %v", err)
}
```

- [ ] **Step 3: Start the publisher**

Immediately before the final `process.Start()` call, add:

```go
botsCtx, botsCancel := context.WithCancel(context.Background())
defer botsCancel()
startBotsPublisher(botsCtx, process)
```

Add `"context"` to the import block if not already present.

- [ ] **Step 4: Build + verify**

```bash
go vet ./examples/4node-basic/...
```

Clean.

```bash
cd .
go build -o /tmp/4node-test ./examples/4node-basic/
```

Build succeeds.

- [ ] **Step 5: Commit**

```bash
git add examples/4node-basic/main.go
git commit -m "$(cat <<'EOF'
4node-basic: register Bots admin panel + start publisher

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: Manual e2e smoke

**Files:** none (verification step).

- [ ] **Step 1: Rebuild + run**

```bash
cd examples/4node-basic
just dev   # rebuilds admin SPA + 4node-basic binary, starts vite + server
```

- [ ] **Step 2: Walk the panel**

Browse to `http://localhost:9101/admin/`, log in as `josh / localdev`.

1. **Sidebar shows the Bots entry** under a new "GAME" group, with the Bot icon.
2. Click → URL becomes `/panel/bots`. Panel header reads "Bots". Toolbar has 3 buttons: `bot.spawn`, `bot.clear`, `bot.list`.
3. Empty table initially (no bots spawned). The "Waiting for topic…" placeholder disappears within ~1s — first publish lands and the table shows one row per cell with `count: 0`.
4. From the server console: `bot spawn 30 cell_0_0`. Within 1s the table updates: `cell_0_0 | 30`. Other cells stay at 0.
5. Click the `bot.spawn` toolbar button → ArgsModal opens asking for `count` (int) and `cellID` (string). Submit with `count: 10`, `cellID: cell_0_3`. Toast: ok. Table shows `cell_0_3 | 10`.
6. Click `bot.clear` → no modal (zero-arg). Toast: ok. Table rows go to `count: 0`.
7. Click `bot.list` → no modal (zero-arg). Toast: ok with the verb's result (best-effort — the table is the live view).

- [ ] **Step 3: Failure modes**

- Refresh the page → sidebar still shows Bots (panelsStore hydrates on boot).
- Stop the server, navigate to `/panel/bots` directly → "Panel `bots` not registered." (panelsStore is empty until login completes).

- [ ] **Step 4: No commit**

Verification only.

---

### Task 12: CLAUDE.md update

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Append after the existing Audit/Logs/Settings paragraph**

Find the recent admin paragraph (search for "PlayerDrawer's"). Append immediately after it:

```markdown

Games register custom admin panels with `mmokit.RegisterAdminPanel(coord, AdminPanelDef{...})` and push live data via `mmokit.PublishAdminTopic(coord, topic, payload)`. The SPA's `PanelHost.svelte` is the single renderer for every game-registered panel — it subscribes to the declared topic, auto-derives a `DataTable` from row-array payloads, and renders one toolbar button per declared cmdsys verb. Buttons with non-empty `argsSchema` open a generic `ArgsModal.svelte` that builds typed inputs from `pkg/cmdsys/schema.go::FieldSchema`. Zero-arg verbs POST directly. The 4node-basic Bots panel (`/panel/bots`, registered in `examples/4node-basic/main.go`) is the worked example — it publishes a per-cell bot count via `admin_bots_publisher.go` and exposes `bot.spawn` / `bot.clear` / `bot.list` from the toolbar. Game-side code never imports `pkg/admin` directly; the `mmokit` facade owns the per-`*Process` bus (`adminBusMap`) so panels and publishers exist alongside the rest of the game wiring in Go.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
CLAUDE.md: document the custom admin panel API + Bots example

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review

**Spec coverage:**

| Spec § | Plan task |
|---|---|
| §4.1 ServerOpts.Bus | Task 1 |
| §4.2 mmokit.PublishAdminTopic + factory wire | Task 2 |
| §4.3 publish test | Task 3 |
| §5.1 PanelHost.svelte | Task 6 |
| §5.2 ArgsModal.svelte | Task 5 |
| §5.3 Sidebar.svelte | Task 7 |
| §5.4 app.svelte hydration + route | Task 8 |
| §6.1 panel registration | Task 10 |
| §6.2 publisher | Task 9 |
| §6.3 smoke | Task 11 |
| §8 file structure | tracked across all tasks |

Frontend types (§4 implies, §8 lists): Task 4.

**Placeholder scan:** No "TBD", "implement later", "fill in details". Every step has either exact code or an exact bash command.

**Type consistency:**
- `PublishAdminTopic(coord *universe.Process, topic string, payload any)` — same signature in Task 2 and Task 9 caller.
- `BotRow{CellID, Count}` — single definition in Task 9; no `HostID` field per the spec's local-only scope decision.
- `Schema{struct, fields}` and `FieldSchema{name, kind, required, ...}` JSON tags match exactly between Go (`pkg/cmdsys/schema.go`) and TS (Task 4).
- `panelsStore` already exists with type `Store<PanelDef[]>`; no changes needed beyond hydration.

**Scope discipline:**
- v2 surfaces declared but not implemented: `PanelDef.Component`, `PanelDef.Visualization === "chart"`. Plan leaves both alone.
- Cross-host bot enumeration explicitly deferred (Task 9 inline comment).
- No frontend unit tests; smoke covers PanelHost + ArgsModal.

**Distributed mode note:** Task 9's publisher walks `coord.Cells` (local only). In `--mode=coordinator,host` the local cells are the full set, so the panel is accurate. In `--mode=coordinator` (no local cells), the publisher emits an empty array — the panel renders correctly but always empty. This is fine for v1; the future cross-host roll-up uses a `bot.count` cmdsys verb with `RouteAllHosts`.

---

## Execution

Plan complete. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task, review between tasks.
2. **Inline Execution** — execute tasks in this session with checkpoints.

Which approach?
