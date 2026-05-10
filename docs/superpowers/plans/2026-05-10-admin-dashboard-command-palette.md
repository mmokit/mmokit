# Admin Dashboard — Command Palette + Cluster Ops Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the v1-completing trio — a generic schema-derived `CommandForm`, a `⌘K` command palette that fuzzy-finds any cmdsys verb and invokes it through that form, and a cluster-ops header dropdown that surfaces `cell.split/merge/migrate` and a new `host.drain` for one-click cluster-wide actions.

**Architecture:** Backend exposes a new `host.drain` cmdsys verb that wraps the existing `Process.drainHost` helper (no new control plane). Frontend builds a single `CommandForm.svelte` component that reads a verb's `argsSchema` (already returned by `GET /admin/api/commands/<verb>`) and renders typed input fields, then POSTs through the existing `/admin/api/commands/<verb>` invoker. The palette and cluster-ops header are thin overlays/dropdowns that pick a verb, hand it to `CommandForm`, and toast the result. `ConfirmDialog` (already present from Plan 3) wraps destructive verbs.

**Tech Stack:** Go (existing `pkg/admin` + `pkg/universe`), Svelte 5 runes, Tailwind v4, Vitest. No new deps.

**Spec:** [`docs/superpowers/specs/2026-05-10-admin-dashboard-design.md`](../specs/2026-05-10-admin-dashboard-design.md) §8 panels #10 (Cluster ops panel) and #25 (Command palette).

**Prior plans:**
- [`2026-05-10-admin-dashboard-backend-foundation.md`](2026-05-10-admin-dashboard-backend-foundation.md) — `pkg/admin` + cmdsys wiring + the `/admin/api/commands` describe/invoke surface.
- [`2026-05-10-admin-dashboard-frontend-cluster.md`](2026-05-10-admin-dashboard-frontend-cluster.md) — Cluster page + `CellDrawer`.
- [`2026-05-10-admin-dashboard-hosts-gateways-players.md`](2026-05-10-admin-dashboard-hosts-gateways-players.md) — `ConfirmDialog`, `PlayerOpsModal` (the hand-rolled per-verb forms this plan generalizes).
- [`2026-05-10-admin-dashboard-performance-events.md`](2026-05-10-admin-dashboard-performance-events.md) — `/performance` and `/events`.

---

## Quick orientation

What already exists, reusable as-is:

- **Verbs.** `cell.split` (CellID), `cell.merge` (CellID), `cell.migrate` (CellID, HostID), `cell.cooldowns`, `cell.autosplit`/`automerge`, `host.list`, `host.kill` (HostID), `auth.user.kick` (Username), `auth.user.lock`, `player.tp`/`tpto`/`kick`/`info`, `perf.snapshot`/`reset`. `pkg/universe/builtins_*.go` + `pkg/services/auth/console.go`.
- **`host.drain` does NOT exist as a verb.** `Process.drainHost(ctx, hostID)` exists at `pkg/universe/coordinator.go:2927` and is wired into the `GracefulLeave` MeshControl handler. Task 1 of this plan exposes it as a cmdsys verb.
- **`GET /admin/api/commands`** lists all registered verbs (entry: `{verb, capability, description, route, hidden, aliases}`).
- **`GET /admin/api/commands/<verb>`** returns `{verb, capability, description, route, argsSchema, resultSchema, usage, examples}`. `argsSchema` is `Schema{StructName, Fields []FieldSchema}` where each `FieldSchema` has `name, kind ("string"|"int32"|"int64"|"float32"|"float64"|"bool"|...), required, named_only, default, enum, help, complete}`. Defined in `pkg/cmdsys/schema.go`.
- **`POST /admin/api/commands/<verb>`** validates args against the schema and dispatches. Returns `{ok, result|targets, traceId}` or `{ok:false, error, traceId}`.
- **`apiGet<T>` / `apiPost<T>` / `ApiError`** in `web-admin/src/lib/api.ts`.
- **`ConfirmDialog.svelte`** (modal w/ Esc/Enter), **`PlayerOpsModal.svelte`** (hand-rolled forms — supersededfor reuse but kept as the kick/tp surface in Players route).
- **`CellDrawer.svelte`** has a hand-rolled split/merge/migrate button block — the new cluster header offers the same verbs without a selected cell, complementary not redundant.
- **`TopBar.svelte`** already renders a `⌘K` chip but it has no click handler. Task 7 wires it.
- **`Caller.Grants` from session.** Calls already authenticate; the existing `callerFrom(r)` in api_commands.go picks up the operator's grants. No frontend change needed for RBAC display in v1 — the backend returns 403 if the caller can't run the verb, and the form surfaces that.

What's deliberately out of scope:

- **Tab completion for `complete:"cells"|"hosts"|"players"` fields.** v1 just renders a plain text/number input. The `complete` field is exposed in the schema but unused by the form — Phase 2 hooks it up to the corresponding live store (`cellsStore`, `hostsStore`, `playersStore`) for inline suggestions.
- **Result rendering.** v1 toasts "verb ok" or "error: …"; the verb's `result` payload is logged to the browser console for inspection. Custom result panes are Phase 2.
- **Verb history / favorites.** Not in v1.
- **Aliases.** The `Aliases` field is exposed but the palette only matches against the canonical `verb`. (Multiple aliases for one verb is rare in our codebase.)
- **Hidden verbs.** Server already filters `Hidden: true` in `handleCommandsList`'s output IFF — actually, it doesn't filter; it returns the `Hidden` flag and the frontend is responsible. The palette will hide verbs whose `hidden==true`.

Build/test commands stay the same:

- Backend: `go test ./pkg/admin/... ./pkg/universe/...`, `go vet ./...`
- Frontend: `cd web-admin && bun run test`, `bun run typecheck`, `bun run build`
- e2e: build the binary + 4node-basic, log in to `localhost:9101/admin/`, hit `⌘K`.

---

## File structure

**Backend additions:**

```text
pkg/universe/
├── builtins_host.go              # MODIFY — append host.drain registration
└── builtins_host_drain_test.go   # NEW — registration smoke test
```

**Frontend additions:**

```text
web-admin/src/
├── lib/
│   ├── types.ts                  # MODIFY — CommandSchema / CommandSummary types
│   └── commands.ts               # NEW — fuzzy match helper + describe cache
├── components/
│   ├── CommandForm.svelte        # NEW — schema-derived form
│   ├── CommandForm.test.ts       # NEW — typed-coercion tests
│   ├── CommandForm.helpers.ts    # NEW — coerceArgs(schema, formValues) typed
│   ├── CommandPalette.svelte     # NEW — ⌘K overlay (search + list + form)
│   ├── ClusterOpsMenu.svelte     # NEW — header dropdown for cell/host ops
│   └── TopBar.svelte             # MODIFY — wire ⌘K button + render menu/palette
└── app.svelte                    # MODIFY — global keydown for ⌘K
```

---

### Task 1: Backend — `host.drain` cmdsys verb

**Files:**
- Modify: `pkg/universe/builtins_host.go`

- [ ] **Step 1: Read the existing builtins_host.go to find the registration block end**

```bash
grep -n "host.list\|host.kill\|return nil$\|return fmt.Errorf" pkg/universe/builtins_host.go | head -20
```

Identify where `host.kill` finishes (it's the last verb registered in the file) so the new `host.drain` registration is appended before the `return nil` of `registerHostBuiltins`.

- [ ] **Step 2: Append the verb**

Add to `pkg/universe/builtins_host.go` near where `host.kill` is registered (just before the closing `return nil` of `registerHostBuiltins`). First add the Args/Result types at the top of the file's type block (the existing `hostListArgs` / `hostKillArgs` block):

```go
type hostDrainArgs struct {
	HostID string `cmd:"help=host ID to drain,complete=hosts"`
}

type hostDrainResult struct {
	HostID    string
	Cells     int    // number of cells migrated
	Note      string
	OK        bool
}
```

Then register the verb:

```go
if err := reg.Register(cmdsys.Command{
	Verb:        "host.drain",
	Capability:  "host.drain",
	Description: "migrate every cell currently owned by hostID to surviving hosts",
	Examples:    []string{"host drain host-2"},
	Route:       cmdsys.RouteLocal,
	Args:        hostDrainArgs{},
	Result:      hostDrainResult{},
	Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(hostDrainArgs)
		if args.HostID == "" {
			return nil, fmt.Errorf("host.drain: host ID required")
		}
		// Count cells owned BEFORE drain so the result is meaningful even
		// when drainHost completes the migrations and clears the ownership map.
		owned := 0
		if c.hostRegistry != nil {
			for _, h := range c.hostRegistry.LiveHosts() {
				if h.ID == args.HostID {
					owned = len(h.OwnedCells)
					break
				}
			}
		}
		ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := c.drainHost(ctx2, args.HostID); err != nil {
			return hostDrainResult{HostID: args.HostID, Cells: owned, OK: false, Note: err.Error()}, nil
		}
		return hostDrainResult{
			HostID: args.HostID,
			Cells:  owned,
			OK:     true,
			Note:   fmt.Sprintf("drained %d cells", owned),
		}, nil
	},
}); err != nil {
	return fmt.Errorf("host.drain: %w", err)
}
```

If `drainHost` actually has a different signature (e.g. returns `(int, error)` for cells migrated), inspect with:

```bash
grep -A 5 "func (c \*Process) drainHost" pkg/universe/coordinator.go
```

and adjust the handler accordingly. If `RemoteHost` doesn't have `OwnedCells` (it might be `Cells` or a separate accessor), find the right field:

```bash
grep -n "type RemoteHost struct" pkg/universe/host_registry.go
```

If `time.Second` isn't already imported in `builtins_host.go`, add `"time"` to the import block.

- [ ] **Step 3: Verify compile**

```bash
go vet ./pkg/universe/...
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/builtins_host.go
git commit -m "$(cat <<'EOF'
universe: host.drain cmdsys verb wraps drainHost

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Backend — registration smoke test for `host.drain`

**Files:**
- Create: `pkg/universe/builtins_host_drain_test.go`

- [ ] **Step 1: Read the existing test fixture pattern**

```bash
grep -n "newTestProcess\|registerHostBuiltins" pkg/universe/builtins_host_test.go pkg/universe/builtins_player_test.go 2>&1 | head -10
```

Find the helper used to construct a `*Process` with the registry wired. Reuse it.

- [ ] **Step 2: Write the test**

Create `pkg/universe/builtins_host_drain_test.go`:

```go
package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// TestHostDrainRegistration confirms the verb registers with the right shape.
// Behavior coverage (actual drain) is exercised by the cmdsys integration
// tests + s7_graceful_shutdown_test.go which already drives drainHost end
// to end via GracefulLeave.
func TestHostDrainRegistration(t *testing.T) {
	t.Parallel()
	coord := newTestProcessForBuiltins(t)
	cmd, ok := coord.registry.Lookup("host.drain")
	if !ok {
		t.Fatalf("host.drain not registered")
	}
	if cmd.Route != cmdsys.RouteLocal {
		t.Fatalf("host.drain route = %v, want RouteLocal", cmd.Route)
	}
	if cmd.Capability != "host.drain" {
		t.Fatalf("host.drain capability = %q, want host.drain", cmd.Capability)
	}
	// Sanity: schema includes a HostID field.
	schema, err := cmdsys.SchemaOf(cmd.Args)
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	hasHostID := false
	for _, f := range schema.Fields {
		if f.Name == "HostID" {
			hasHostID = true
			if f.Required != true {
				t.Fatalf("HostID should be required by default")
			}
			break
		}
	}
	if !hasHostID {
		t.Fatalf("host.drain args missing HostID field; got %+v", schema.Fields)
	}
}
```

If the helper isn't `newTestProcessForBuiltins` (the actual name varies — check what existing tests in the same package use), adapt. Read one existing test for the package's convention:

```bash
sed -n '1,30p' pkg/universe/builtins_host_test.go
```

- [ ] **Step 3: Run + commit**

```bash
go test ./pkg/universe/ -run TestHostDrainRegistration -v
```

Expected: PASS.

```bash
git add pkg/universe/builtins_host_drain_test.go
git commit -m "$(cat <<'EOF'
universe: registration test for host.drain verb

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Frontend — schema + summary types

**Files:**
- Modify: `web-admin/src/lib/types.ts`

- [ ] **Step 1: Append the cmdsys schema types**

Append to `web-admin/src/lib/types.ts`:

```ts
// CommandSummary mirrors the per-entry shape returned by GET /admin/api/commands.
export type CommandSummary = {
  verb: string;
  capability: string;
  description: string;
  route: string;
  hidden?: boolean;
  aliases?: string[];
};

// FieldSchema mirrors pkg/cmdsys/schema.go::FieldSchema.
export type FieldSchema = {
  name: string;
  kind: string; // "string", "int32", "int64", "float32", "float64", "bool", "[]<elem>", "{...}"
  required: boolean;
  named_only: boolean;
  default: string;
  enum: string[] | null;
  help?: string;
  rest?: boolean;
  complete?: string;
};

export type Schema = {
  struct: string;
  fields: FieldSchema[];
};

// CommandDescribe is the response shape from GET /admin/api/commands/<verb>.
export type CommandDescribe = {
  verb: string;
  capability: string;
  description: string;
  route: string;
  argsSchema: Schema;
  resultSchema: Schema;
  usage?: string;
  examples?: string[] | null;
};
```

- [ ] **Step 2: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/lib/types.ts
git commit -m "$(cat <<'EOF'
web-admin: CommandSummary / Schema / CommandDescribe types

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Frontend — fuzzy match helper + describe cache

**Files:**
- Create: `web-admin/src/lib/commands.ts`
- Create: `web-admin/src/lib/commands.test.ts`

A tiny scoring helper for the palette plus a one-shot describe cache so opening + closing the palette repeatedly doesn't refetch the same schema.

- [ ] **Step 1: Write the failing test**

Create `web-admin/src/lib/commands.test.ts`:

```ts
import { describe, it, expect } from "vitest";
import { fuzzyScore } from "./commands";

describe("fuzzyScore", () => {
  it("returns 0 for non-matching query", () => {
    expect(fuzzyScore("cell.split", "xyz")).toBe(0);
  });
  it("returns higher score for prefix match than mid-string match", () => {
    const prefix = fuzzyScore("cell.split", "cell");
    const mid = fuzzyScore("entity.cell", "cell");
    expect(prefix).toBeGreaterThan(mid);
  });
  it("matches characters in order even when not contiguous", () => {
    // "cs" matches "cell.split" (c at 0, s at 5).
    expect(fuzzyScore("cell.split", "cs")).toBeGreaterThan(0);
  });
  it("returns 0 when query characters appear in the wrong order", () => {
    // "sc" — s at 5, c at 0 — out of order, no match.
    expect(fuzzyScore("cell.split", "sc")).toBe(0);
  });
  it("is case-insensitive", () => {
    expect(fuzzyScore("Cell.Split", "cs")).toBeGreaterThan(0);
  });
});
```

- [ ] **Step 2: Implement**

Create `web-admin/src/lib/commands.ts`:

```ts
import type { CommandDescribe } from "./types";
import { apiGet } from "./api";

// fuzzyScore returns a positive number when every character of `query`
// appears in `text` in order (case-insensitive), 0 otherwise. Score
// favors earlier matches and contiguous runs so prefix matches outrank
// scattered hits — good enough for a verb palette without a fuzzy lib.
export function fuzzyScore(text: string, query: string): number {
  if (!query) return 1; // empty query matches everything weakly
  const t = text.toLowerCase();
  const q = query.toLowerCase();
  let score = 0;
  let textIdx = 0;
  let prevMatch = -2; // index of last matched char in text
  for (let qi = 0; qi < q.length; qi++) {
    const ch = q[qi];
    let found = -1;
    for (let ti = textIdx; ti < t.length; ti++) {
      if (t[ti] === ch) {
        found = ti;
        break;
      }
    }
    if (found < 0) return 0;
    // Higher score when match is at position 0, contiguous with the
    // previous match, or near the start of the string.
    score += 100 - found; // earlier matches → higher
    if (found === prevMatch + 1) score += 50; // contiguous bonus
    prevMatch = found;
    textIdx = found + 1;
  }
  return score;
}

// describeCache memoizes GET /admin/api/commands/<verb> for the lifetime
// of the SPA session. Schemas are stable across a coordinator process —
// no need to refetch every time the palette opens. clearCache() is
// available for tests.
const cache = new Map<string, CommandDescribe>();

export async function describe(verb: string): Promise<CommandDescribe> {
  const hit = cache.get(verb);
  if (hit) return hit;
  const res = await apiGet<CommandDescribe>(`/admin/api/commands/${encodeURIComponent(verb)}`);
  cache.set(verb, res);
  return res;
}

export function clearDescribeCache(): void {
  cache.clear();
}
```

- [ ] **Step 3: Run + commit**

```bash
cd web-admin && bun run test src/lib/commands.test.ts
```

Expected: 5 passing.

```bash
cd .
git add web-admin/src/lib/commands.ts web-admin/src/lib/commands.test.ts
git commit -m "$(cat <<'EOF'
web-admin: commands lib — fuzzyScore + describe cache

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Frontend — `CommandForm` typed coercion helpers

**Files:**
- Create: `web-admin/src/components/CommandForm.helpers.ts`
- Create: `web-admin/src/components/CommandForm.test.ts`

Form values arrive from `<input>` as strings; cmdsys requires the right primitive types per the schema's `kind` field. A small helper coerces them and surfaces validation errors.

- [ ] **Step 1: Write the failing test**

Create `web-admin/src/components/CommandForm.test.ts`:

```ts
import { describe, it, expect } from "vitest";
import { coerceArgs, defaultValueFor } from "./CommandForm.helpers";
import type { Schema } from "$lib/types";

const schema: Schema = {
  struct: "Args",
  fields: [
    { name: "Name", kind: "string", required: true, named_only: false, default: "", enum: [] },
    { name: "Count", kind: "int32", required: true, named_only: false, default: "", enum: [] },
    { name: "Ratio", kind: "float64", required: false, named_only: false, default: "0.5", enum: [] },
    { name: "Active", kind: "bool", required: false, named_only: false, default: "false", enum: [] },
  ],
};

describe("coerceArgs", () => {
  it("coerces numeric and bool fields from string inputs", () => {
    const { args, errors } = coerceArgs(schema, { Name: "alice", Count: "42", Ratio: "0.75", Active: "true" });
    expect(errors).toEqual({});
    expect(args).toEqual({ Name: "alice", Count: 42, Ratio: 0.75, Active: true });
  });

  it("flags missing required fields", () => {
    const { args, errors } = coerceArgs(schema, { Count: "1" });
    expect(args).toBeNull();
    expect(errors.Name).toMatch(/required/i);
  });

  it("flags non-numeric input on int field", () => {
    const { args, errors } = coerceArgs(schema, { Name: "alice", Count: "abc" });
    expect(args).toBeNull();
    expect(errors.Count).toMatch(/integer|number/i);
  });

  it("uses default when optional field is empty", () => {
    const { args, errors } = coerceArgs(schema, { Name: "alice", Count: "1" });
    expect(errors).toEqual({});
    expect(args!.Ratio).toBeCloseTo(0.5);
    expect(args!.Active).toBe(false);
  });
});

describe("defaultValueFor", () => {
  it("returns default literal for non-empty defaults", () => {
    expect(defaultValueFor({ name: "x", kind: "int32", required: false, named_only: false, default: "7", enum: [] })).toBe("7");
  });
  it("returns empty string when no default and not required", () => {
    expect(defaultValueFor({ name: "x", kind: "string", required: false, named_only: false, default: "", enum: [] })).toBe("");
  });
});
```

- [ ] **Step 2: Implement**

Create `web-admin/src/components/CommandForm.helpers.ts`:

```ts
import type { Schema, FieldSchema } from "$lib/types";

// CoerceResult carries either the typed args object or per-field error
// messages. errors is keyed by field name; absent keys mean valid.
export type CoerceResult = {
  args: Record<string, unknown> | null;
  errors: Record<string, string>;
};

// defaultValueFor returns the string the form should pre-fill into the
// input for this field — either the schema's `default` literal or empty.
export function defaultValueFor(f: FieldSchema): string {
  return f.default || "";
}

// coerceArgs validates `values` against the schema and returns typed
// args ready for JSON.stringify. On any error returns args=null with
// per-field error messages.
export function coerceArgs(schema: Schema, values: Record<string, string>): CoerceResult {
  const errors: Record<string, string> = {};
  const out: Record<string, unknown> = {};
  for (const f of schema.fields) {
    const raw = values[f.name];
    const isEmpty = raw === undefined || raw === "";
    if (isEmpty) {
      if (f.required && !f.default) {
        errors[f.name] = `${f.name} is required`;
        continue;
      }
      // Fall through with the default literal so coercion runs uniformly.
      const def = f.default;
      if (def === "" && !f.required) {
        // Skip optional empty-default fields entirely; backend uses Go zero value.
        continue;
      }
      out[f.name] = coerceOne(f, def, errors);
      continue;
    }
    out[f.name] = coerceOne(f, raw, errors);
  }
  if (Object.keys(errors).length > 0) {
    return { args: null, errors };
  }
  return { args: out, errors };
}

function coerceOne(f: FieldSchema, raw: string, errors: Record<string, string>): unknown {
  switch (f.kind) {
    case "string":
      return raw;
    case "bool":
      if (raw === "true" || raw === "1") return true;
      if (raw === "false" || raw === "0" || raw === "") return false;
      errors[f.name] = `${f.name} must be true or false`;
      return null;
    case "int":
    case "int32":
    case "int64":
    case "uint32":
    case "uint64": {
      if (!/^-?\d+$/.test(raw)) {
        errors[f.name] = `${f.name} must be an integer`;
        return null;
      }
      // We send as JSON numbers; backend uses json.Decoder on the typed Args
      // struct so the int kind is honored on receive. JS Number is fine for
      // values within Number.MAX_SAFE_INTEGER (~ 2^53). Beyond that, send as
      // string and let JSON UnmarshalString do the conversion — out of scope
      // for v1 (no current verb takes such a value).
      return Number.parseInt(raw, 10);
    }
    case "float32":
    case "float64": {
      const n = Number.parseFloat(raw);
      if (Number.isNaN(n)) {
        errors[f.name] = `${f.name} must be a number`;
        return null;
      }
      return n;
    }
    default:
      // Unknown / nested struct kinds — pass through as string for v1.
      return raw;
  }
}
```

- [ ] **Step 3: Run + commit**

```bash
cd web-admin && bun run test src/components/CommandForm.test.ts
```

Expected: 6 passing.

```bash
cd .
git add web-admin/src/components/CommandForm.helpers.ts web-admin/src/components/CommandForm.test.ts
git commit -m "$(cat <<'EOF'
web-admin: CommandForm helpers — coerceArgs with per-field validation

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Frontend — `CommandForm` Svelte component

**Files:**
- Create: `web-admin/src/components/CommandForm.svelte`

A schema-derived form. Renders one input per `FieldSchema`, validates on submit via `coerceArgs`, POSTs to `/admin/api/commands/<verb>`, surfaces success/error to a callback.

- [ ] **Step 1: Implement**

Create `web-admin/src/components/CommandForm.svelte`:

```svelte
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
```

- [ ] **Step 2: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/components/CommandForm.svelte
git commit -m "$(cat <<'EOF'
web-admin: CommandForm.svelte — schema-derived input renderer

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Frontend — `CommandPalette` overlay

**Files:**
- Create: `web-admin/src/components/CommandPalette.svelte`

A modal overlay opened with `⌘K` / `Ctrl+K`. Two states: list (search + scored results) and detail (the picked verb + its `CommandForm`). Esc closes; Enter on the list fires the top result; clicking a list item opens detail.

- [ ] **Step 1: Implement**

Create `web-admin/src/components/CommandPalette.svelte`:

```svelte
<script lang="ts">
  import { onMount, tick } from "svelte";
  import { apiGet } from "$lib/api";
  import { fuzzyScore, describe as describeVerb } from "$lib/commands";
  import type { CommandSummary, CommandDescribe } from "$lib/types";
  import CommandForm from "./CommandForm.svelte";

  type Props = {
    open: boolean;
    onClose: () => void;
    onResult: (ok: boolean, message: string, payload?: unknown) => void;
  };
  let { open, onClose, onResult }: Props = $props();

  let allCommands = $state<CommandSummary[]>([]);
  let query = $state("");
  let activeIdx = $state(0);
  let picked = $state<CommandDescribe | null>(null);
  let pickError = $state("");
  let inputEl: HTMLInputElement | undefined = $state();

  // Sorted, filtered list of visible commands.
  let visible = $derived.by<{ cmd: CommandSummary; score: number }[]>(() => {
    const out: { cmd: CommandSummary; score: number }[] = [];
    for (const c of allCommands) {
      if (c.hidden) continue;
      const s1 = fuzzyScore(c.verb, query);
      const s2 = fuzzyScore(c.description, query) * 0.3; // description matches weighted lower
      const s = Math.max(s1, s2);
      if (query && s === 0) continue;
      out.push({ cmd: c, score: s });
    }
    out.sort((a, b) => b.score - a.score);
    return out.slice(0, 50);
  });

  // Lazy fetch the catalog on first open.
  $effect(() => {
    if (!open) return;
    if (allCommands.length > 0) return;
    void (async () => {
      try {
        const list = await apiGet<CommandSummary[]>("/admin/api/commands");
        allCommands = list;
      } catch {
        allCommands = [];
      }
    })();
  });

  // Reset state + focus input on each open.
  $effect(() => {
    if (open) {
      query = "";
      activeIdx = 0;
      picked = null;
      pickError = "";
      void tick().then(() => inputEl?.focus());
    }
  });

  // Clamp activeIdx to visible bounds when the list changes.
  $effect(() => {
    if (activeIdx >= visible.length) activeIdx = Math.max(0, visible.length - 1);
  });

  async function pickByIndex(i: number) {
    const v = visible[i]?.cmd;
    if (!v) return;
    pickError = "";
    try {
      picked = await describeVerb(v.verb);
    } catch (e) {
      pickError = (e as Error).message;
    }
  }

  function backToList() {
    picked = null;
    pickError = "";
    void tick().then(() => inputEl?.focus());
  }

  function onKey(e: KeyboardEvent) {
    if (!open) return;
    if (e.key === "Escape") {
      e.preventDefault();
      if (picked) backToList();
      else onClose();
      return;
    }
    if (picked) return; // form owns Enter / arrows when in detail
    if (e.key === "ArrowDown") {
      e.preventDefault();
      activeIdx = Math.min(visible.length - 1, activeIdx + 1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      activeIdx = Math.max(0, activeIdx - 1);
    } else if (e.key === "Enter") {
      e.preventDefault();
      void pickByIndex(activeIdx);
    }
  }

  function onResultWrap(ok: boolean, message: string, payload?: unknown) {
    onResult(ok, message, payload);
    if (ok) {
      onClose();
    }
  }
</script>

<svelte:window onkeydown={onKey} />

{#if open}
  <div class="fixed inset-0 z-50 flex items-start justify-center pt-24 bg-black/60">
    <div class="w-[640px] max-h-[70vh] flex flex-col bg-[#0d1117] border border-white/10 rounded-lg shadow-2xl">
      {#if !picked}
        <div class="border-b border-white/10 p-3">
          <input
            bind:this={inputEl}
            type="text"
            placeholder="Search commands… (⌘K)"
            class="w-full bg-black/40 border border-white/10 rounded px-3 py-2 text-[13px] text-slate-200 placeholder-slate-500 focus:outline-none focus:border-accent-300/50 font-mono"
            bind:value={query}
            oninput={() => (activeIdx = 0)}
          />
        </div>
        <div class="overflow-y-auto p-1">
          {#if visible.length === 0}
            <div class="px-3 py-3 text-slate-500 text-[12px]">No matching commands.</div>
          {/if}
          {#each visible as v, i (v.cmd.verb)}
            <button
              type="button"
              class="w-full text-left px-3 py-1.5 rounded {i === activeIdx ? 'bg-accent-300/15' : 'hover:bg-white/5'}"
              onclick={() => pickByIndex(i)}
              onmouseenter={() => (activeIdx = i)}
            >
              <div class="flex items-baseline justify-between gap-2">
                <span class="font-mono text-[12.5px] text-slate-100">{v.cmd.verb}</span>
                <span class="text-[10.5px] text-slate-500">{v.cmd.route}</span>
              </div>
              {#if v.cmd.description}
                <div class="text-[11px] text-slate-400 truncate">{v.cmd.description}</div>
              {/if}
            </button>
          {/each}
        </div>
        {#if pickError}
          <div class="border-t border-white/10 p-2 text-rose-300 text-[11.5px]">{pickError}</div>
        {/if}
      {:else}
        <div class="border-b border-white/10 px-3 py-2 flex items-center justify-between">
          <span class="font-mono text-[13px] text-slate-100">{picked.verb}</span>
          <button
            type="button"
            class="text-[11px] text-slate-400 hover:text-slate-200"
            onclick={backToList}
          >
            ← back
          </button>
        </div>
        <div class="overflow-y-auto p-3">
          <CommandForm describe={picked} onCancel={backToList} onResult={onResultWrap} />
        </div>
      {/if}
    </div>
  </div>
{/if}
```

- [ ] **Step 2: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/components/CommandPalette.svelte
git commit -m "$(cat <<'EOF'
web-admin: CommandPalette.svelte — ⌘K overlay over /admin/api/commands

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Frontend — wire `⌘K` in `TopBar` + global keydown in `app.svelte`

**Files:**
- Modify: `web-admin/src/components/TopBar.svelte`
- Modify: `web-admin/src/app.svelte`

The TopBar already renders a `⌘K` chip — make it open the palette. App-level keydown handler on `Cmd+K` / `Ctrl+K` toggles it from anywhere.

- [ ] **Step 1: Add a palette toggle store**

Append to `web-admin/src/lib/stores.svelte.ts` (after `metricsHistoryStore`):

```ts
class PaletteOpen {
  #open = $state(false);
  get value(): boolean {
    return this.#open;
  }
  set(v: boolean): void {
    this.#open = v;
  }
  toggle(): void {
    this.#open = !this.#open;
  }
}

export const paletteOpen = new PaletteOpen();
```

- [ ] **Step 2: Wire the TopBar `⌘K` chip**

In `web-admin/src/components/TopBar.svelte`, add the import and click handler. Find:

```svelte
import { Circle, Command, Bell } from "$lib/icons";
import { cellsStore, alertsStore, sessionStore } from "$lib/stores.svelte";
```

Add `paletteOpen` to the import:

```svelte
import { cellsStore, alertsStore, sessionStore, paletteOpen } from "$lib/stores.svelte";
```

Then find the existing `<button … title="Command palette (⌘K)">` and add `onclick={() => paletteOpen.set(true)}`:

```svelte
<button
  type="button"
  class="px-2 py-0.5 rounded border border-white/10 bg-white/5 text-slate-300 hover:bg-white/10 flex items-center gap-1"
  title="Command palette (⌘K)"
  onclick={() => paletteOpen.set(true)}
>
  <Command class="w-3 h-3" /> ⌘K
</button>
```

- [ ] **Step 3: Render palette + handle global keydown in `app.svelte`**

In `web-admin/src/app.svelte`, add the import after the other imports:

```ts
import { paletteOpen } from "$lib/stores.svelte";
import CommandPalette from "./components/CommandPalette.svelte";
```

Add a global keydown handler inside the script section (after the existing `$effect` that subscribes to `route`):

```ts
function onGlobalKey(e: KeyboardEvent) {
  // Cmd+K / Ctrl+K toggles palette unless the user is typing in an input.
  const target = e.target as HTMLElement | null;
  const editable = target && (
    target.tagName === "INPUT" ||
    target.tagName === "TEXTAREA" ||
    target.isContentEditable
  );
  if ((e.metaKey || e.ctrlKey) && (e.key === "k" || e.key === "K")) {
    e.preventDefault();
    paletteOpen.toggle();
    return;
  }
  if (e.key === "Escape" && paletteOpen.value && !editable) {
    paletteOpen.set(false);
  }
}

function onPaletteResult(ok: boolean, message: string, _payload?: unknown) {
  // The palette currently delegates result rendering to a console log + a
  // future toast. The trace ID is on payload when present; for v1 we just
  // log and rely on the per-command form for the in-modal feedback.
  console[ok ? "log" : "warn"]("cmd palette:", message, _payload);
}
```

Find the `<svelte:window` … or main shell. The existing `app.svelte` doesn't have a `<svelte:window>`; add one in the markup just before the booting check:

```svelte
<svelte:window onkeydown={onGlobalKey} />

{#if booting}
```

Inside the logged-in shell, mount `<CommandPalette>` once at the top level (so it overlays everything) — add it inside the logged-in `{:else}` branch before the closing `</div>`:

```svelte
    </div>
    <CommandPalette
      open={paletteOpen.value}
      onClose={() => paletteOpen.set(false)}
      onResult={onPaletteResult}
    />
  </div>
{/if}
```

The `paletteOpen.value` access is reactive — `app.svelte` re-renders when it flips.

- [ ] **Step 4: Typecheck + tests + commit**

```bash
cd web-admin && bun run typecheck && bun run test
```

Expected: 0 errors. All tests pass.

```bash
cd .
git add web-admin/src/app.svelte web-admin/src/components/TopBar.svelte web-admin/src/lib/stores.svelte.ts
git commit -m "$(cat <<'EOF'
web-admin: wire ⌘K palette open from TopBar + global keydown

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Frontend — `ClusterOpsMenu` header dropdown

**Files:**
- Create: `web-admin/src/components/ClusterOpsMenu.svelte`

A small dropdown next to the ⌘K chip with quick links to the four cluster-wide ops: split, merge, migrate (with cellID picker built into the verb's form), and host.drain. Each item opens the palette pre-populated to the selected verb so the user gets the same `CommandForm` UI as ⌘K.

- [ ] **Step 1: Add a "preselect verb" channel on paletteOpen**

We need to open the palette AT a specific verb, skipping the search step. Extend the paletteOpen store. In `web-admin/src/lib/stores.svelte.ts`, replace the `PaletteOpen` class with:

```ts
class Palette {
  #open = $state(false);
  #verb = $state<string | null>(null);
  get value(): boolean {
    return this.#open;
  }
  get verb(): string | null {
    return this.#verb;
  }
  set(v: boolean): void {
    this.#open = v;
    if (!v) this.#verb = null;
  }
  toggle(): void {
    this.#open = !this.#open;
    if (!this.#open) this.#verb = null;
  }
  openAt(verb: string): void {
    this.#verb = verb;
    this.#open = true;
  }
}

export const paletteOpen = new Palette();
```

- [ ] **Step 2: Make CommandPalette honor the preselected verb**

In `web-admin/src/components/CommandPalette.svelte`, change the props to optionally accept `initialVerb` and add an effect that pre-fetches the describe and jumps straight to the form when set. Find the props block:

```ts
type Props = {
  open: boolean;
  onClose: () => void;
  onResult: (ok: boolean, message: string, payload?: unknown) => void;
};
let { open, onClose, onResult }: Props = $props();
```

Replace with:

```ts
type Props = {
  open: boolean;
  initialVerb?: string | null;
  onClose: () => void;
  onResult: (ok: boolean, message: string, payload?: unknown) => void;
};
let { open, initialVerb = null, onClose, onResult }: Props = $props();
```

Then update the open-effect — find:

```ts
// Reset state + focus input on each open.
$effect(() => {
  if (open) {
    query = "";
    activeIdx = 0;
    picked = null;
    pickError = "";
    void tick().then(() => inputEl?.focus());
  }
});
```

Replace with:

```ts
$effect(() => {
  if (!open) return;
  query = "";
  activeIdx = 0;
  picked = null;
  pickError = "";
  if (initialVerb) {
    void (async () => {
      try {
        picked = await describeVerb(initialVerb);
      } catch (e) {
        pickError = (e as Error).message;
      }
    })();
  } else {
    void tick().then(() => inputEl?.focus());
  }
});
```

In `web-admin/src/app.svelte`, pass `initialVerb={paletteOpen.verb}` into the palette mount:

```svelte
<CommandPalette
  open={paletteOpen.value}
  initialVerb={paletteOpen.verb}
  onClose={() => paletteOpen.set(false)}
  onResult={onPaletteResult}
/>
```

- [ ] **Step 3: Implement ClusterOpsMenu**

Create `web-admin/src/components/ClusterOpsMenu.svelte`:

```svelte
<script lang="ts">
  import { paletteOpen } from "$lib/stores.svelte";

  let open = $state(false);

  function pick(verb: string) {
    open = false;
    paletteOpen.openAt(verb);
  }

  function onWindowClick(e: MouseEvent) {
    if (!open) return;
    const root = (e.target as HTMLElement).closest("[data-cluster-ops]");
    if (!root) open = false;
  }
</script>

<svelte:window onclick={onWindowClick} />

<div class="relative" data-cluster-ops>
  <button
    type="button"
    class="px-2 py-0.5 rounded border border-white/10 bg-white/5 text-slate-300 hover:bg-white/10"
    onclick={() => (open = !open)}
    title="Cluster ops"
  >
    ops ▾
  </button>
  {#if open}
    <div
      class="absolute right-0 top-full mt-1 w-44 bg-[#0d1117] border border-white/10 rounded-lg shadow-2xl py-1 z-40 text-[12px]"
    >
      <button class="w-full text-left px-3 py-1 hover:bg-white/5" onclick={() => pick("cell.split")}>
        Split cell…
      </button>
      <button class="w-full text-left px-3 py-1 hover:bg-white/5" onclick={() => pick("cell.merge")}>
        Merge cell…
      </button>
      <button class="w-full text-left px-3 py-1 hover:bg-white/5" onclick={() => pick("cell.migrate")}>
        Migrate cell…
      </button>
      <div class="my-1 border-t border-white/5"></div>
      <button class="w-full text-left px-3 py-1 hover:bg-white/5" onclick={() => pick("host.drain")}>
        Drain host…
      </button>
      <button class="w-full text-left px-3 py-1 hover:bg-rose-500/10 text-rose-300" onclick={() => pick("host.kill")}>
        Kill host (sim crash)…
      </button>
    </div>
  {/if}
</div>
```

- [ ] **Step 4: Mount the menu in TopBar**

In `web-admin/src/components/TopBar.svelte`, add the import:

```svelte
import ClusterOpsMenu from "./ClusterOpsMenu.svelte";
```

Insert `<ClusterOpsMenu />` in the right-side flex group, before the `⌘K` chip:

```svelte
<div class="flex items-center gap-3 text-slate-400">
  <span class="flex items-center gap-1.5">
    …existing health pill…
  </span>
  <ClusterOpsMenu />
  <button … ⌘K button …
```

- [ ] **Step 5: Typecheck + commit**

```bash
cd web-admin && bun run typecheck && bun run test
```

Expected: 0 errors. All tests pass.

```bash
cd .
git add web-admin/src/lib/stores.svelte.ts web-admin/src/components/CommandPalette.svelte web-admin/src/components/ClusterOpsMenu.svelte web-admin/src/components/TopBar.svelte web-admin/src/app.svelte
git commit -m "$(cat <<'EOF'
web-admin: ClusterOpsMenu — TopBar dropdown opens palette at preselected verb

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Frontend — toast on palette result

**Files:**
- Create: `web-admin/src/components/Toast.svelte`
- Modify: `web-admin/src/app.svelte`

The palette currently `console.log`s results; that's invisible to the user. A small bottom-right toast surface confirms success and shows errors.

- [ ] **Step 1: Implement Toast component**

Create `web-admin/src/components/Toast.svelte`:

```svelte
<script lang="ts">
  type ToastEntry = { id: number; ok: boolean; msg: string };

  type Props = {
    entries: ToastEntry[];
  };
  let { entries }: Props = $props();
</script>

<div class="fixed bottom-4 right-4 z-40 flex flex-col gap-2 text-[12px]">
  {#each entries as t (t.id)}
    <div
      class="px-3 py-1.5 rounded shadow-xl border {t.ok
        ? 'bg-emerald-900/40 border-emerald-700/40 text-emerald-200'
        : 'bg-rose-900/40 border-rose-700/40 text-rose-200'}"
    >
      {t.msg}
    </div>
  {/each}
</div>
```

- [ ] **Step 2: Replace console.log in app.svelte with toast state**

In `web-admin/src/app.svelte`, replace the existing `onPaletteResult` body and add the toast state + import:

```ts
import Toast from "./components/Toast.svelte";

let toasts = $state<{ id: number; ok: boolean; msg: string }[]>([]);
let toastSeq = 0;

function pushToast(ok: boolean, msg: string) {
  const id = ++toastSeq;
  toasts = [...toasts, { id, ok, msg }];
  setTimeout(() => {
    toasts = toasts.filter((t) => t.id !== id);
  }, 4000);
}

function onPaletteResult(ok: boolean, message: string, payload?: unknown) {
  pushToast(ok, ok ? message : `${message}`);
  // Keep payload accessible for debugging when dev tools are open.
  console[ok ? "log" : "warn"]("cmd:", message, payload);
}
```

Mount the `<Toast>` in the logged-in shell (just before `</div>` next to where `<CommandPalette>` lives):

```svelte
    <CommandPalette
      open={paletteOpen.value}
      initialVerb={paletteOpen.verb}
      onClose={() => paletteOpen.set(false)}
      onResult={onPaletteResult}
    />
    <Toast entries={toasts} />
  </div>
```

- [ ] **Step 3: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/components/Toast.svelte web-admin/src/app.svelte
git commit -m "$(cat <<'EOF'
web-admin: Toast component for cmd palette results

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: e2e smoke (manual)

**Files:**
- (No new files; verification step.)

- [ ] **Step 1: Build everything**

```bash
cd web-admin && bun run build
cd . && go build -o /tmp/4node-test ./examples/4node-basic/
```

- [ ] **Step 2: Run with Postgres**

```bash
just db-up
/tmp/4node-test --admin-listen=:9101 --postgres-url='postgres://mmo:mmo@localhost:5432/mmo_4node?sslmode=disable'
```

- [ ] **Step 3: ⌘K palette**

Browse to `http://localhost:9101/admin/`, log in. Hit `⌘K` (or Ctrl+K). The palette overlays. Type `cell` — verbs like `cell.split`, `cell.merge`, `cell.migrate` rank to the top. ↑/↓ navigates; Enter opens the form. Type `0_0` (or `cell_0_0` depending on the example) into the CellID field, click Run. Expect a toast `cell.split ok` and the cell map subdivides. Hit `⌘K` again, type `host.drain`, fill the HostID, Run — expect a toast (or `no surviving hosts` error in single-host all-in-one).

- [ ] **Step 4: Cluster ops menu**

Click the `ops ▾` chip in the TopBar. The dropdown shows split/merge/migrate/drain/kill. Click Split → palette opens at `cell.split` — same behavior as the ⌘K-driven path.

- [ ] **Step 5: Distributed mode**

Restart with `just distributed`. ⌘K → host.drain → enter a remote host's ID. Expect cells to migrate; toast `host.drain ok` with the migrated count.

- [ ] **Step 6: No commit**

Verification only.

---

### Task 12: CLAUDE.md update

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Append a paragraph after the existing admin dashboard description**

Find the existing block that documents the admin dashboard panels (added by prior plans — search for "Performance route"). Append after:

```markdown

The Command palette (`⌘K`) lists every registered cmdsys verb from `/admin/api/commands` with fuzzy match; clicking a row opens a generic `CommandForm.svelte` that builds inputs from the verb's `argsSchema` (typed coercion in `CommandForm.helpers.ts`) and POSTs to `/admin/api/commands/<verb>`. The TopBar `ops ▾` dropdown (`ClusterOpsMenu.svelte`) opens the palette pre-targeted at a specific verb — used for `cell.split/merge/migrate`, `host.drain`, and `host.kill`. `host.drain` is a thin cmdsys wrapper around `Process.drainHost` (already used by the `GracefulLeave` MeshControl handler).
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
CLAUDE.md: document command palette + cluster ops menu

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review checklist

- **Spec coverage:** §8 #10 (Cluster ops panel) → Task 9 (`ClusterOpsMenu` header dropdown) plus Task 1 (`host.drain` verb). §8 #25 (Command palette) → Tasks 4 (fuzzy + cache), 5–6 (`CommandForm`), 7 (`CommandPalette`), 8 (⌘K wiring), 10 (toast).
- **Placeholder scan:** All steps are concrete code or exact commands. No "TBD" / "implement later" patterns.
- **Type consistency:** TS `Schema.fields[i].kind` matches Go `cmdsys.FieldSchema.Kind` — both are union of literal kind names. `CommandDescribe.argsSchema` is the same shape as Go `cmdsys.SchemaOf` returns. Backend `host.drain` Args struct has `HostID string` — matches the form field `HostID` and the test assertion. The `paletteOpen` store evolves from a simple toggle (Task 8) to a `(open, verb)` pair (Task 9 step 1) — Task 8's tests (none beyond typecheck) won't be invalidated.
- **No new dependencies:** All work uses existing libs.
- **Backend additivity:** Only one new verb (`host.drain`); `Process.drainHost` is an existing private helper now exposed.
- **Distributed mode:** `host.drain` runs on the coordinator (RouteLocal — coordinator owns the orchestrator). `cell.split/merge/migrate` already work in distributed mode. Palette `/admin/api/commands` lists all verbs registered on the coordinator process; works identically in single-process and `--mode=coordinator,gateway` setups.

---

## Execution

Plan complete. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task, review between tasks.
2. **Inline Execution** — execute tasks in this session with checkpoints.

Which approach?
