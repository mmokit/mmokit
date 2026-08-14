# Admin Dashboard — Detail Panels + Audit / Logs / Settings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fill the dead sidebar entries (`/audit`, `/logs`, `/settings`) and add per-row detail drawers for nodes and players — closing the v1 navigation gaps and giving operators real introspection surfaces.

**Architecture:** Three new top-level routes are thin reads over existing backend state — `/admin/api/audit` already returns the audit ring; `/admin/api/logs/...` (new) wraps `pkg/logger`'s in-memory category set; `/settings` is a read-only operator/server-config view. Two new drawers (`NodeDrawer`, `PlayerDrawer`) follow the existing `CellDrawer` pattern: clicking a row opens the drawer with full detail and per-row actions; the `⌘K` palette deep-links into them via the existing `pendingNav` store.

**Tech Stack:** Go (existing `pkg/admin` + `pkg/logger`), Svelte 5 runes, Tailwind v4, Vitest. No new deps.

**Spec:** [`docs/superpowers/specs/2026-05-10-admin-dashboard-design.md`](../specs/2026-05-10-admin-dashboard-design.md) §8 — covers the unfilled sidebar groups (Diagnose: Logs; Config: Settings) and the detail-page items (#7 host detail, #9 gateway detail, #13 player detail) flagged for Phase 2 in §9 but practical to land now.

**Prior plans:**
- [`2026-05-10-admin-dashboard-backend-foundation.md`](2026-05-10-admin-dashboard-backend-foundation.md) — `pkg/admin` skeleton (`AuditLog`, `/admin/api/audit`, session model, lockout).
- [`2026-05-10-admin-dashboard-hosts-gateways-players.md`](2026-05-10-admin-dashboard-hosts-gateways-players.md) — `nodes.svelte` / `players.svelte` (which we extend).
- [`2026-05-10-admin-dashboard-command-palette.md`](2026-05-10-admin-dashboard-command-palette.md) — `pendingNav` store (which the drawers consume).

---

## Quick orientation

What already exists, reusable as-is:

- **Audit:** `pkg/admin/audit.go::AuditLog.Recent(n)`. Wire serves it at `GET /admin/api/audit?n=200` already (gated on the `admin.audit` capability). Each `AuditEntry` carries `{TraceID, Username, IP, Verb, ArgsJSON, OK, Error, StartedAt, FinishedAt}`.
- **Logger:** `pkg/logger/logger.go` exposes `Logger.Categories() []string`, `Logger.Groups() []string`, `Logger.CategoriesInGroup(group) []string`, `Logger.Enable(...string)`, `Logger.Disable(...string)`, `Logger.IsEnabled(cat) bool`. Categories use `"group:sub"` naming (e.g. `"mesh:cell"`, `"events:split"`). The `*Logger` instance is reachable from `*universe.Process.Log`.
- **Operator config:** `pkg/admin/admin.go::Server` carries `cfg Config` with `SessionTTL`, `LockoutMax`, `LockoutWin`, `Operators`. Each operator has `{Username, PasswordHash, Grants}`. The session record (`/admin/api/auth/session`) already returns `{user, grants, expiresAt}`.
- **Session model:** `pkg/admin/session.go::SessionRecord` carries `LastSeenAt` etc; the `SessionStore` interface only exposes Create/Lookup/Touch/Delete — there's no `List`. Active-sessions table is therefore deferred (would need a new interface method).
- **Drawer pattern:** `web-admin/src/components/CellDrawer.svelte` is the existing per-cell detail drawer; `nodes.svelte` + `players.svelte` need analogous components plus a row-click handler.
- **`pendingNav` store:** writes to it from `CommandPalette` for cell/node/player navigation. Currently the cluster route reads `kind:"cell"` and selects; nodes/players read theirs and prefill search. Detail drawers extend this — the drawer opens on the same signal (no new field needed; just consume to also set the drawer's selected ID).
- **`PlayerOpsModal.svelte`** already wraps the kick/tp/tpto verbs. The new `PlayerDrawer` mounts the same modal.
- **Sidebar entries** for Logs and Settings already exist in `Sidebar.svelte`. They just route to the "not yet implemented" fallback today. We add `Audit` to the sidebar in Task 6.

What's deliberately out of scope:

- **Live log tail** — would require a logger Hook + new SSE topic + ring buffer. The Logs panel just toggles categories for v1; tail is a Phase 2 addition.
- **Active-sessions table on Settings** — needs a `SessionStore.List()` method and matching backend wiring. Defer.
- **Per-player full inventory / bank / equipment** — exists via the game's `player.info` cmdsys verb, which returns game-specific data. The drawer surfaces what `PlayerInfo` already has (username/status/host/cell/world/lastLogin) plus a `player.info` "load full" button that runs the verb and dumps the result into a `<pre>`. Game-specific custom views are Phase 2 (custom panel API #28).
- **Bulk operations** — kicking multiple players, draining multiple hosts. Single-row only.
- **Postgres-backed audit** — the in-memory ring is fine for v1; a persistence backing is Phase 2.

Build/test commands stay the same:

- Backend: `go test ./pkg/admin/... ./pkg/logger/...`, `go vet ./...`
- Frontend: `cd web-admin && bun run test`, `bun run typecheck`, `bun run build`

---

## File structure

**Backend additions:**

```text
pkg/admin/
├── api_logs.go                   # NEW — GET /admin/api/logs/categories,
│                                 # POST /admin/api/logs/categories/<cat>
├── api_logs_test.go              # NEW — handler tests
└── admin.go                      # MODIFY — wire the new routes + carry *logger.Logger
```

**Frontend additions:**

```text
web-admin/src/
├── lib/
│   ├── types.ts                  # MODIFY — AuditEntry, LogCategory types
│   └── stores.svelte.ts          # (no change — pendingNav already covers selection)
├── components/
│   ├── NodeDrawer.svelte         # NEW — host/gateway detail
│   ├── PlayerDrawer.svelte       # NEW — player detail + ops
│   └── Sidebar.svelte            # MODIFY — add Audit entry
├── routes/
│   ├── audit.svelte              # NEW
│   ├── logs.svelte               # NEW
│   ├── settings.svelte           # NEW
│   ├── nodes.svelte              # MODIFY — row click → drawer; pendingNav opens it
│   └── players.svelte            # MODIFY — row click → drawer; pendingNav opens it
└── app.svelte                    # MODIFY — route /audit /logs /settings
```

---

### Task 1: Backend — log categories HTTP endpoints

**Files:**
- Create: `pkg/admin/api_logs.go`
- Modify: `pkg/admin/admin.go` (carry the logger + mount routes)

The Logs panel needs to list categories grouped by prefix and toggle them. Keep the surface tiny: one GET that returns `{group → [{name, enabled}]}` and one POST that flips a single category.

- [ ] **Step 1: Read the existing admin.go to find the right insertion point**

```bash
grep -n "ServerOpts\|type Server struct\|Mount\|func NewServer" pkg/admin/admin.go | head -10
```

You'll see `Server` carries dependency-injected fields (view, registry, dispatcher, sessions, audit, lockout, panels, bus). We add a `Logger` field carrying the `*logger.Logger` instance.

- [ ] **Step 2: Extend Server + ServerOpts with the logger**

In `pkg/admin/admin.go`, add to the `Server` struct (alongside `audit *AuditLog`):

```go
logger *logger.Logger
```

(Add `"github.com/zenion/mmokit/pkg/logger"` to the import block.)

In `ServerOpts`, add:

```go
Logger *logger.Logger
```

In `NewServer` (which copies opts → server fields), add:

```go
logger: opts.Logger,
```

- [ ] **Step 3: Wire ServerOpts.Logger from the universe layer**

Find the construction site:

```bash
grep -n "admin.ServerOpts{" pkg/universe/*.go pkg/mmokit/*.go 2>&1 | head -5
```

In whichever file builds the `admin.ServerOpts{}` (likely `pkg/universe/bootstrap.go` or `pkg/mmokit/admin.go`), add `Logger: c.Log,` (where `c` is the `*Process` — verify the field name; it's `c.Log` per `coordinator.go`).

- [ ] **Step 4: Create the handler file**

Create `pkg/admin/api_logs.go`:

```go
package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// logCategoriesResp is the GET /admin/api/logs/categories shape.
type logCategoriesResp struct {
	Groups []logGroup `json:"groups"`
}

type logGroup struct {
	Name       string        `json:"name"`
	Categories []logCategory `json:"categories"`
}

type logCategory struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// handleLogCategories — GET /admin/api/logs/categories. Returns all known
// categories grouped by prefix (the "group:sub" naming convention).
// Categories with no group prefix collapse into a synthetic "" group at
// the end.
func (s *Server) handleLogCategories(w http.ResponseWriter, r *http.Request) {
	if s.logger == nil {
		writeJSON(w, http.StatusOK, logCategoriesResp{Groups: []logGroup{}})
		return
	}
	cats := s.logger.Categories()
	byGroup := make(map[string][]logCategory)
	for _, c := range cats {
		group := ""
		if i := strings.Index(c, ":"); i > 0 {
			group = c[:i]
		}
		byGroup[group] = append(byGroup[group], logCategory{
			Name:    c,
			Enabled: s.logger.IsEnabled(c),
		})
	}
	groupNames := make([]string, 0, len(byGroup))
	for g := range byGroup {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)
	out := logCategoriesResp{Groups: make([]logGroup, 0, len(groupNames))}
	for _, g := range groupNames {
		entries := byGroup[g]
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		out.Groups = append(out.Groups, logGroup{Name: g, Categories: entries})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleLogToggle — POST /admin/api/logs/categories/<cat>
// Body: {"enabled": true|false}. Capability gate: "admin.logs".
func (s *Server) handleLogToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.logger == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "logger unavailable")
		return
	}
	cat := strings.TrimPrefix(r.URL.Path, "/admin/api/logs/categories/")
	if cat == "" || cat == r.URL.Path {
		writeJSONError(w, http.StatusBadRequest, "category required")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if body.Enabled {
		s.logger.Enable(cat)
	} else {
		s.logger.Disable(cat)
	}
	writeJSON(w, http.StatusOK, logCategory{Name: cat, Enabled: s.logger.IsEnabled(cat)})
}
```

If `pkg/admin` already imports a `cmdsys.Check` helper (used in `handleAudit`), gate the toggle behind `admin.logs`:

```bash
grep -n "cmdsys.Check\|callerFrom" pkg/admin/api_read.go | head -5
```

If yes, add at the top of `handleLogToggle`:

```go
caller, _ := callerFrom(r)
if !cmdsys.Check(caller, "admin.logs") {
    writeJSONError(w, http.StatusForbidden, "missing admin.logs grant")
    return
}
```

(And import `"github.com/zenion/mmokit/pkg/cmdsys"` in api_logs.go.)

- [ ] **Step 5: Mount the routes**

In `pkg/admin/admin.go::Mount`, after the existing `mux.Handle("/admin/api/audit"...)` line, add:

```go
mux.Handle("/admin/api/logs/categories", s.requireSession(http.HandlerFunc(s.handleLogCategories)))
mux.Handle("/admin/api/logs/categories/", s.requireSession(http.HandlerFunc(s.handleLogToggle)))
```

- [ ] **Step 6: Verify**

```bash
go vet ./pkg/admin/...
```

Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add pkg/admin/admin.go pkg/admin/api_logs.go pkg/universe/bootstrap.go pkg/mmokit/admin.go 2>/dev/null
git commit -m "$(cat <<'EOF'
admin: log categories list + toggle endpoints

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

(Stage `pkg/universe/bootstrap.go` or `pkg/mmokit/admin.go` only if you actually touched the wiring site there.)

---

### Task 2: Backend — handler tests for `/admin/api/logs/...`

**Files:**
- Create: `pkg/admin/api_logs_test.go`

- [ ] **Step 1: Find existing `httptest.Server` patterns**

```bash
grep -n "httptest.NewRequest\|httptest.NewRecorder\|fakeView\|newTestServer" pkg/admin/api_read_test.go | head -10
```

Reuse the same pattern. The test fixture probably constructs a `Server` directly without the registry/dispatcher; the logs handlers only need `Server.logger`, so a minimal fixture suffices.

- [ ] **Step 2: Write the tests**

Create `pkg/admin/api_logs_test.go`:

```go
package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zenion/mmokit/pkg/logger"
)

func TestLogCategories_Empty(t *testing.T) {
	t.Parallel()
	s := &Server{logger: logger.New()}
	req := httptest.NewRequest(http.MethodGet, "/admin/api/logs/categories", nil)
	rec := httptest.NewRecorder()
	s.handleLogCategories(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp logCategoriesResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Groups) != 0 {
		t.Fatalf("expected 0 groups on a fresh logger, got %d", len(resp.Groups))
	}
}

func TestLogCategories_GroupedAndSorted(t *testing.T) {
	t.Parallel()
	l := logger.New("mesh:cell")
	l.RegisterCategories("events:split", "events:merge", "mesh:cell", "mesh:transfer", "admin")
	s := &Server{logger: l}
	req := httptest.NewRequest(http.MethodGet, "/admin/api/logs/categories", nil)
	rec := httptest.NewRecorder()
	s.handleLogCategories(rec, req)
	var resp logCategoriesResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Expect "" (admin alone) + "events" + "mesh" sorted alphabetically.
	gotNames := make([]string, len(resp.Groups))
	for i, g := range resp.Groups {
		gotNames[i] = g.Name
	}
	want := []string{"", "events", "mesh"}
	if strings.Join(gotNames, ",") != strings.Join(want, ",") {
		t.Fatalf("group order = %v, want %v", gotNames, want)
	}
	// mesh:cell should report Enabled=true (passed to New).
	for _, g := range resp.Groups {
		if g.Name != "mesh" {
			continue
		}
		for _, c := range g.Categories {
			if c.Name == "mesh:cell" && !c.Enabled {
				t.Fatalf("mesh:cell expected enabled=true")
			}
		}
	}
}

func TestLogToggle_EnableThenDisable(t *testing.T) {
	t.Parallel()
	l := logger.New()
	l.RegisterCategories("mesh:cell")
	s := &Server{logger: l}

	enable := func(enabled bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]bool{"enabled": enabled})
		req := httptest.NewRequest(http.MethodPost, "/admin/api/logs/categories/mesh:cell", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleLogToggle(rec, req)
		return rec
	}

	rec := enable(true)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable status = %d, body = %s", rec.Code, rec.Body)
	}
	if !l.IsEnabled("mesh:cell") {
		t.Fatalf("mesh:cell not enabled after toggle")
	}

	rec = enable(false)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body = %s", rec.Code, rec.Body)
	}
	if l.IsEnabled("mesh:cell") {
		t.Fatalf("mesh:cell still enabled after disable")
	}
}

func TestLogToggle_MissingCategoryReturns400(t *testing.T) {
	t.Parallel()
	s := &Server{logger: logger.New()}
	req := httptest.NewRequest(http.MethodPost, "/admin/api/logs/categories/", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	s.handleLogToggle(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
```

If the test fails to compile because `handleLogToggle` calls `callerFrom(r)` and the test request has no auth context, drop the cmdsys.Check guard from the handler body OR add a test helper that injects a caller. For v1 the requireSession middleware is the auth gate; the handler-level `cmdsys.Check` is defense-in-depth. If the handler calls `callerFrom` and chokes on the missing context, just remove the inline check — the route is already protected by `requireSession` which fails closed.

- [ ] **Step 3: Run + commit**

```bash
go test ./pkg/admin/ -run "TestLog" -v
```

Expected: 4 passing.

```bash
git add pkg/admin/api_logs_test.go
git commit -m "$(cat <<'EOF'
admin: handler tests for /admin/api/logs/categories

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Frontend — types for AuditEntry / LogCategory

**Files:**
- Modify: `web-admin/src/lib/types.ts`

- [ ] **Step 1: Append the new types**

Append to `web-admin/src/lib/types.ts`:

```ts
// AuditEntry mirrors pkg/admin/audit.go::AuditEntry. Returned by
// GET /admin/api/audit?n=N (newest first).
export type AuditEntry = {
  traceId?: string;
  username: string;
  ip: string;
  verb: string;
  args?: string;
  ok: boolean;
  error?: string;
  startedAt: string;
  finishedAt: string;
};

// LogCategory mirrors pkg/admin/api_logs.go::logCategory.
export type LogCategory = { name: string; enabled: boolean };

// LogGroup mirrors pkg/admin/api_logs.go::logGroup.
export type LogGroup = { name: string; categories: LogCategory[] };

// LogCategoriesResp mirrors pkg/admin/api_logs.go::logCategoriesResp.
export type LogCategoriesResp = { groups: LogGroup[] };
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
web-admin: AuditEntry / LogCategory / LogGroup types

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Frontend — `routes/audit.svelte`

**Files:**
- Create: `web-admin/src/routes/audit.svelte`

A bounded table over `/admin/api/audit?n=200`. No filters in v1 — the user can paginate via `n=` if they want more. Columns: time, user, IP, verb, OK, error, args (truncated). Failures highlighted.

- [ ] **Step 1: Implement**

Create `web-admin/src/routes/audit.svelte`:

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { apiGet, ApiError } from "$lib/api";
  import type { AuditEntry } from "$lib/types";

  let entries = $state<AuditEntry[]>([]);
  let error = $state("");
  let loading = $state(true);
  let limit = $state(200);

  async function refresh() {
    loading = true;
    error = "";
    try {
      const res = await apiGet<AuditEntry[]>(`/admin/api/audit?n=${limit}`);
      entries = res ?? [];
    } catch (e) {
      if (e instanceof ApiError && e.kind === "rbac") {
        error = "You need the admin.audit grant to view this page.";
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

  function fmtTime(ts: string): string {
    const d = new Date(ts);
    return d.toLocaleString(undefined, { hour12: false });
  }
  function durationMs(a: AuditEntry): number {
    return new Date(a.finishedAt).getTime() - new Date(a.startedAt).getTime();
  }
  function rowClass(a: AuditEntry): string {
    if (!a.ok) return "bg-rose-900/15";
    return "";
  }
  function truncate(s: string | undefined, n: number): string {
    if (!s) return "";
    return s.length > n ? s.slice(0, n) + "…" : s;
  }
</script>

<main class="p-4 space-y-3">
  <div class="flex items-center justify-between">
    <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Audit log</h2>
    <div class="flex items-center gap-2 text-[11px] text-slate-400">
      <span>showing last</span>
      <select
        class="bg-white/5 border border-white/10 rounded px-2 py-0.5 text-slate-200"
        bind:value={limit}
        onchange={() => void refresh()}
      >
        <option value={50}>50</option>
        <option value={200}>200</option>
        <option value={1000}>1000</option>
      </select>
      <button
        type="button"
        class="px-2 py-0.5 rounded border border-white/10 bg-white/5 text-slate-300 hover:bg-white/10"
        onclick={() => void refresh()}
        disabled={loading}
      >
        refresh
      </button>
    </div>
  </div>

  {#if error}
    <div class="text-rose-300 text-[12px]">{error}</div>
  {/if}

  <div class="bg-[#0d1117] border border-white/10 rounded-lg overflow-x-auto">
    <table class="w-full text-[11.5px] border-collapse font-mono">
      <thead>
        <tr class="text-left text-[10.5px] uppercase tracking-wide text-slate-500 border-b border-white/10">
          <th class="py-1.5 px-2 font-medium" style="width:170px">Time</th>
          <th class="py-1.5 px-2 font-medium" style="width:110px">User</th>
          <th class="py-1.5 px-2 font-medium" style="width:140px">IP</th>
          <th class="py-1.5 px-2 font-medium" style="width:200px">Verb</th>
          <th class="py-1.5 px-2 font-medium" style="width:60px">OK</th>
          <th class="py-1.5 px-2 font-medium" style="width:80px">ms</th>
          <th class="py-1.5 px-2 font-medium">Args</th>
          <th class="py-1.5 px-2 font-medium">Error</th>
        </tr>
      </thead>
      <tbody>
        {#each entries as e (e.traceId || e.startedAt)}
          <tr class="border-b border-white/5 {rowClass(e)}">
            <td class="py-1.5 px-2 text-slate-400">{fmtTime(e.startedAt)}</td>
            <td class="py-1.5 px-2 text-slate-200">{e.username || "—"}</td>
            <td class="py-1.5 px-2 text-slate-400">{e.ip || "—"}</td>
            <td class="py-1.5 px-2 text-slate-200">{e.verb || "—"}</td>
            <td class="py-1.5 px-2 {e.ok ? 'text-emerald-300' : 'text-rose-300'}">
              {e.ok ? "ok" : "fail"}
            </td>
            <td class="py-1.5 px-2 text-right text-slate-400">{durationMs(e)}</td>
            <td class="py-1.5 px-2 text-slate-400 truncate" title={e.args ?? ""}>
              {truncate(e.args, 80)}
            </td>
            <td class="py-1.5 px-2 text-rose-300 truncate" title={e.error ?? ""}>
              {truncate(e.error, 80)}
            </td>
          </tr>
        {:else}
          <tr><td colspan="8" class="py-4 text-center text-slate-500 italic">
            {loading ? "loading…" : "No entries."}
          </td></tr>
        {/each}
      </tbody>
    </table>
  </div>

  <div class="text-[10.5px] text-slate-500">
    {entries.length} entries · in-memory ring (Postgres backing is Phase 2)
  </div>
</main>
```

- [ ] **Step 2: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/routes/audit.svelte
git commit -m "$(cat <<'EOF'
web-admin: Audit route — table over /admin/api/audit

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Frontend — `routes/logs.svelte`

**Files:**
- Create: `web-admin/src/routes/logs.svelte`

Lists categories grouped by prefix; each row has a checkbox that POSTs the toggle and refreshes the local view.

- [ ] **Step 1: Implement**

Create `web-admin/src/routes/logs.svelte`:

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { apiGet, apiPost, ApiError } from "$lib/api";
  import type { LogCategoriesResp, LogCategory } from "$lib/types";

  let groups = $state<LogCategoriesResp["groups"]>([]);
  let error = $state("");
  let loading = $state(true);

  async function refresh() {
    loading = true;
    error = "";
    try {
      const res = await apiGet<LogCategoriesResp>("/admin/api/logs/categories");
      groups = res.groups ?? [];
    } catch (e) {
      error = (e as Error).message;
    } finally {
      loading = false;
    }
  }

  async function toggle(cat: string, enabled: boolean) {
    try {
      const res = await apiPost<LogCategory>(
        `/admin/api/logs/categories/${encodeURIComponent(cat)}`,
        { enabled },
      );
      // Apply the server-confirmed value optimistically rather than full
      // refresh (avoids a flicker for the toggle).
      groups = groups.map((g) => ({
        ...g,
        categories: g.categories.map((c) => (c.name === cat ? res : c)),
      }));
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message;
      error = msg;
    }
  }

  function setAll(group: typeof groups[number], enabled: boolean) {
    for (const c of group.categories) {
      void toggle(c.name, enabled);
    }
  }

  onMount(() => {
    void refresh();
  });
</script>

<main class="p-4 space-y-3">
  <div class="flex items-center justify-between">
    <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Logs</h2>
    <button
      type="button"
      class="px-2 py-0.5 rounded border border-white/10 bg-white/5 text-slate-300 hover:bg-white/10 text-[11px]"
      onclick={() => void refresh()}
      disabled={loading}
    >
      refresh
    </button>
  </div>

  {#if error}
    <div class="text-rose-300 text-[12px]">{error}</div>
  {/if}

  {#if loading && groups.length === 0}
    <div class="text-slate-500 text-[12px]">loading…</div>
  {/if}

  <div class="grid gap-3 grid-cols-1 md:grid-cols-2 xl:grid-cols-3">
    {#each groups as g (g.name)}
      <div class="bg-[#0d1117] border border-white/10 rounded-lg p-3">
        <div class="flex items-center justify-between mb-2">
          <h3 class="font-mono text-[12px] text-slate-200">{g.name || "(uncategorized)"}</h3>
          <div class="flex gap-1 text-[10.5px]">
            <button
              type="button"
              class="px-1.5 py-0.5 rounded border border-white/10 bg-white/5 text-slate-400 hover:bg-white/10"
              onclick={() => setAll(g, true)}
            >all</button>
            <button
              type="button"
              class="px-1.5 py-0.5 rounded border border-white/10 bg-white/5 text-slate-400 hover:bg-white/10"
              onclick={() => setAll(g, false)}
            >none</button>
          </div>
        </div>
        <div class="space-y-1">
          {#each g.categories as c (c.name)}
            <label class="flex items-center gap-2 text-[11.5px] cursor-pointer">
              <input
                type="checkbox"
                class="accent-accent-400"
                checked={c.enabled}
                onchange={(e) => toggle(c.name, (e.currentTarget as HTMLInputElement).checked)}
              />
              <span class="font-mono text-slate-300">{c.name}</span>
            </label>
          {/each}
        </div>
      </div>
    {/each}
  </div>
</main>
```

- [ ] **Step 2: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/routes/logs.svelte
git commit -m "$(cat <<'EOF'
web-admin: Logs route — log category list with per-row toggles

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Frontend — `routes/settings.svelte` + sidebar Audit entry

**Files:**
- Create: `web-admin/src/routes/settings.svelte`
- Modify: `web-admin/src/components/Sidebar.svelte`

Settings is read-only in v1: shows the current operator (from `sessionStore`), session expiry, plus a sign-out button. Active-sessions list is deferred per the orientation note.

- [ ] **Step 1: Add the Audit entry to the sidebar**

In `web-admin/src/components/Sidebar.svelte`, find the existing items array. Add an `Audit` entry under the Diagnose group, between `events` and `logs`. Choose an icon — `ScrollText` is already imported as `Scroll`; let's pick a different one for Audit. Add to the icons import (find the import line):

```svelte
import { Globe, Boxes, Users, Activity, List, Scroll, Settings, ShieldCheck } from "$lib/icons";
```

Then in `web-admin/src/lib/icons.ts` append:

```ts
export { default as ShieldCheck } from "@lucide/svelte/icons/shield-check";
```

In Sidebar.svelte's items array, insert after the `events` entry:

```svelte
{ id: "audit", label: "Audit", icon: ShieldCheck, group: "DIAGNOSE", path: "/audit" },
```

- [ ] **Step 2: Implement settings.svelte**

Create `web-admin/src/routes/settings.svelte`:

```svelte
<script lang="ts">
  import { sessionStore } from "$lib/stores.svelte";
  import { auth } from "$lib/auth";

  let session = $derived(sessionStore.value);

  function fmtTime(ts?: string): string {
    if (!ts) return "—";
    return new Date(ts).toLocaleString(undefined, { hour12: false });
  }

  async function signOut() {
    try {
      await auth.logout();
    } finally {
      sessionStore.set(null);
    }
  }
</script>

<main class="p-4 space-y-4">
  <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Settings</h2>

  <div class="bg-[#0d1117] border border-white/10 rounded-lg p-4 space-y-2 max-w-2xl">
    <h3 class="text-[12px] text-slate-200 font-semibold">Session</h3>
    <div class="grid grid-cols-[120px_1fr] gap-x-3 gap-y-1 text-[12px]">
      <span class="text-slate-500">Operator</span>
      <span class="font-mono text-slate-200">{session?.user ?? "—"}</span>
      <span class="text-slate-500">Grants</span>
      <span class="font-mono text-slate-300">
        {(session?.grants ?? []).join(", ") || "—"}
      </span>
      <span class="text-slate-500">Expires</span>
      <span class="text-slate-300">{fmtTime(session?.expiresAt)}</span>
    </div>
    <div class="pt-2">
      <button
        type="button"
        class="px-3 py-1 text-[12px] bg-rose-500/15 hover:bg-rose-500/25 text-rose-200 border border-rose-500/30 rounded"
        onclick={signOut}
      >
        Sign out
      </button>
    </div>
  </div>

  <div class="bg-[#0d1117] border border-white/10 rounded-lg p-4 space-y-2 max-w-2xl">
    <h3 class="text-[12px] text-slate-200 font-semibold">About</h3>
    <p class="text-slate-400 text-[12px]">
      mmokit admin dashboard · embedded into the coordinator at startup. Operator
      passwords are argon2id-hashed; sessions live in memory and expire on
      coordinator restart. The audit log is a bounded ring; persistence is
      Phase 2.
    </p>
    <p class="text-slate-500 text-[11.5px]">
      Server-side configuration (operator list, lockout window, listen address) is
      file-driven via <code>Config.Admin</code>; this page only displays the live
      session view. Active-session table and per-operator administration land in
      Phase 2.
    </p>
  </div>
</main>
```

- [ ] **Step 3: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/routes/settings.svelte web-admin/src/components/Sidebar.svelte web-admin/src/lib/icons.ts
git commit -m "$(cat <<'EOF'
web-admin: Settings route + Audit sidebar entry

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Frontend — wire `/audit` `/logs` `/settings` in `app.svelte`

**Files:**
- Modify: `web-admin/src/app.svelte`

- [ ] **Step 1: Add imports**

In the existing route-import block (alongside `Players`, `Performance`, etc.):

```svelte
import Audit from "./routes/audit.svelte";
import Logs from "./routes/logs.svelte";
import Settings from "./routes/settings.svelte";
```

- [ ] **Step 2: Add the route branches**

Find the existing `{:else if path === "/events"}` line and extend the chain just before the fallback:

```svelte
{:else if path === "/events"}
  <Events />
{:else if path === "/audit"}
  <Audit />
{:else if path === "/logs"}
  <Logs />
{:else if path === "/settings"}
  <Settings />
{:else}
  <div class="p-8 text-slate-500">
    Panel <code>{path}</code> — not yet implemented.
  </div>
{/if}
```

- [ ] **Step 3: Typecheck + tests + commit**

```bash
cd web-admin && bun run typecheck && bun run test
```

Expected: 0 errors. All tests pass.

```bash
cd .
git add web-admin/src/app.svelte
git commit -m "$(cat <<'EOF'
web-admin: route /audit /logs /settings in the app shell

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Frontend — `NodeDrawer` component

**Files:**
- Create: `web-admin/src/components/NodeDrawer.svelte`

Slides in from the right when a node row is clicked. Distinguishes host vs gateway via a `kind` field. For hosts, lists owned cells with click-through to the cluster view (via `pendingNav`). For gateways, shows mode + sessions count + bytes total.

- [ ] **Step 1: Implement**

Create `web-admin/src/components/NodeDrawer.svelte`:

```svelte
<script lang="ts">
  import { Close } from "$lib/icons";
  import { pendingNav, cellsStore } from "$lib/stores.svelte";
  import { navigate } from "$lib/router";
  import { fmtBytes, fmtDuration, fmtLoad } from "$lib/format";
  import type { HostInfo, GatewayInfo, CellInfo } from "$lib/types";

  type Node =
    | { kind: "host"; data: HostInfo }
    | { kind: "gateway"; data: GatewayInfo };

  type Props = {
    node: Node | null;
    onClose: () => void;
  };
  let { node, onClose }: Props = $props();

  let cells = $derived<CellInfo[]>(cellsStore.value ?? []);

  // For host detail, look up the live CellInfo for each owned cell so we
  // can show entity counts inline. cellsStore drives this — no extra fetch.
  let ownedCells = $derived.by<CellInfo[]>(() => {
    if (!node || node.kind !== "host") return [];
    const ids = new Set(node.data.cells ?? []);
    return cells.filter((c) => ids.has(c.id));
  });

  function gotoCell(id: string) {
    pendingNav.set({ kind: "cell", id });
    navigate("/cluster");
    onClose();
  }
</script>

{#if node}
  <aside class="w-[360px] shrink-0 bg-[#0a0e14] border-l border-white/5 flex flex-col">
    <header class="flex items-center justify-between border-b border-white/5 px-4 py-2">
      <div>
        <div class="text-[10px] uppercase tracking-wide text-slate-500">{node.kind}</div>
        <div class="font-mono text-slate-100 text-[13px]">
          {node.kind === "host" ? node.data.id : node.data.id}
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

    <div class="flex-1 overflow-auto p-4 space-y-3 text-[12px]">
      {#if node.kind === "host"}
        {@const h = node.data}
        <div class="grid grid-cols-[110px_1fr] gap-x-3 gap-y-1">
          <span class="text-slate-500">State</span>
          <span class="text-slate-200">{h.state}</span>
          <span class="text-slate-500">Where</span>
          <span class="text-slate-300">{h.isLocal ? "in-proc" : "remote"}</span>
          <span class="text-slate-500">Roles</span>
          <span class="font-mono text-slate-300">{(h.roles ?? []).join(", ") || "—"}</span>
          <span class="text-slate-500">HB age</span>
          <span class="text-slate-300">{h.isLocal ? "—" : fmtDuration(h.heartbeatAgeMs)}</span>
          <span class="text-slate-500">Load</span>
          <span class="text-slate-300">{fmtLoad(h.load)}</span>
          <span class="text-slate-500">Entities</span>
          <span class="text-slate-300">{h.totalEntities}</span>
        </div>

        <div>
          <div class="text-[10.5px] uppercase tracking-wide text-slate-500 mb-1">
            Owned cells ({(h.cells ?? []).length})
          </div>
          {#if (h.cells ?? []).length === 0}
            <div class="text-slate-500 italic">No cells owned.</div>
          {:else}
            <div class="space-y-0.5 max-h-[40vh] overflow-y-auto">
              {#each h.cells ?? [] as cellId (cellId)}
                {@const live = ownedCells.find((c) => c.id === cellId)}
                <button
                  type="button"
                  class="w-full text-left px-2 py-1 rounded hover:bg-white/5 flex items-center justify-between"
                  onclick={() => gotoCell(cellId)}
                >
                  <span class="font-mono text-slate-200">{cellId}</span>
                  <span class="text-[10.5px] text-slate-500">
                    {live ? `${live.entities.real} ent · ${fmtLoad(live.load)}` : "—"}
                  </span>
                </button>
              {/each}
            </div>
          {/if}
        </div>
      {:else}
        {@const g = node.data}
        <div class="grid grid-cols-[110px_1fr] gap-x-3 gap-y-1">
          <span class="text-slate-500">Where</span>
          <span class="text-slate-300">{g.isLocal ? "in-proc" : "remote"}</span>
          <span class="text-slate-500">Mode</span>
          <span class="font-mono text-slate-300">{g.mode || "—"}</span>
          <span class="text-slate-500">Sessions</span>
          <span class="text-slate-300">{g.sessions}</span>
          <span class="text-slate-500">Bytes sent</span>
          <span class="text-slate-300">{fmtBytes(g.bytesSent)}</span>
          <span class="text-slate-500">Bytes recv</span>
          <span class="text-slate-300">{fmtBytes(g.bytesRecv)}</span>
        </div>
        <p class="text-slate-500 text-[11.5px] italic">
          Per-gateway session list lands when SessionStore exposes a List
          method (Phase 2).
        </p>
      {/if}
    </div>
  </aside>
{/if}
```

- [ ] **Step 2: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/components/NodeDrawer.svelte
git commit -m "$(cat <<'EOF'
web-admin: NodeDrawer.svelte — host/gateway detail

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Frontend — `PlayerDrawer` component

**Files:**
- Create: `web-admin/src/components/PlayerDrawer.svelte`

Shows the live `PlayerInfo` fields plus the kick/tp/tpto buttons that already exist on the inline row UI. Adds a "Load full info" button that POSTs `player.info` and dumps the result.

- [ ] **Step 1: Implement**

Create `web-admin/src/components/PlayerDrawer.svelte`:

```svelte
<script lang="ts">
  import { Close } from "$lib/icons";
  import { apiPost, ApiError } from "$lib/api";
  import { pendingNav } from "$lib/stores.svelte";
  import { navigate } from "$lib/router";
  import PlayerOpsModal from "./PlayerOpsModal.svelte";
  import type { PlayerInfo } from "$lib/types";

  type Props = {
    player: PlayerInfo | null;
    onClose: () => void;
    onResult: (ok: boolean, msg: string) => void;
  };
  let { player, onClose, onResult }: Props = $props();

  type Op = "kick" | "tp" | "tpto" | null;
  let modalOp = $state<Op>(null);

  let infoLoading = $state(false);
  let infoError = $state("");
  let infoPayload = $state<unknown>(null);

  // Reset detail-load state when the drawer's player swaps.
  $effect(() => {
    void player?.username;
    infoLoading = false;
    infoError = "";
    infoPayload = null;
    modalOp = null;
  });

  async function loadInfo() {
    if (!player || infoLoading) return;
    infoLoading = true;
    infoError = "";
    infoPayload = null;
    try {
      const res = await apiPost<{ ok: boolean; result?: unknown; error?: string }>(
        "/admin/api/commands/player.info",
        { Username: player.username },
      );
      if (res.ok === false) {
        infoError = res.error || "command failed";
      } else {
        infoPayload = res.result;
      }
    } catch (e) {
      infoError = e instanceof ApiError ? e.message : (e as Error).message;
    } finally {
      infoLoading = false;
    }
  }

  function gotoCell(id: string) {
    pendingNav.set({ kind: "cell", id });
    navigate("/cluster");
    onClose();
  }
</script>

{#if player}
  <aside class="w-[400px] shrink-0 bg-[#0a0e14] border-l border-white/5 flex flex-col">
    <header class="flex items-center justify-between border-b border-white/5 px-4 py-2">
      <div>
        <div class="text-[10px] uppercase tracking-wide text-slate-500">player</div>
        <div class="font-mono text-slate-100 text-[13px]">{player.username}</div>
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

    <div class="flex-1 overflow-auto p-4 space-y-3 text-[12px]">
      <div class="grid grid-cols-[110px_1fr] gap-x-3 gap-y-1">
        <span class="text-slate-500">Status</span>
        <span class="{player.status === 'online' ? 'text-emerald-300' : 'text-slate-400'}">
          {player.status === "online" ? "● online" : "○ offline"}
        </span>
        <span class="text-slate-500">Host</span>
        <span class="font-mono text-slate-300">{player.hostId ?? "—"}</span>
        <span class="text-slate-500">Cell</span>
        <span class="font-mono text-slate-300">
          {#if player.cellId}
            <button
              type="button"
              class="text-slate-200 hover:text-accent-300 underline-offset-2 hover:underline"
              onclick={() => gotoCell(player!.cellId!)}
            >{player.cellId}</button>
          {:else}—{/if}
        </span>
        <span class="text-slate-500">World</span>
        <span class="text-slate-300">
          {player.worldX != null && player.worldY != null && (player.worldX !== 0 || player.worldY !== 0)
            ? `(${player.worldX.toFixed(0)}, ${player.worldY.toFixed(0)})`
            : "—"}
        </span>
        <span class="text-slate-500">Last login</span>
        <span class="text-slate-300">
          {player.lastLogin && new Date(player.lastLogin).getTime() > 0
            ? new Date(player.lastLogin).toLocaleString(undefined, { hour12: false })
            : "—"}
        </span>
      </div>

      <div class="flex flex-wrap gap-2 pt-1">
        <button
          class="px-2 py-0.5 text-[11px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
          disabled={player.status !== "online"}
          onclick={() => (modalOp = "tp")}
        >tp</button>
        <button
          class="px-2 py-0.5 text-[11px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
          disabled={player.status !== "online"}
          onclick={() => (modalOp = "tpto")}
        >tpto</button>
        <button
          class="px-2 py-0.5 text-[11px] bg-rose-500/15 border border-rose-500/30 text-rose-200 rounded hover:bg-rose-500/25 disabled:opacity-50"
          disabled={player.status !== "online"}
          onclick={() => (modalOp = "kick")}
        >kick</button>
        <button
          class="px-2 py-0.5 text-[11px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50 ml-auto"
          disabled={infoLoading}
          onclick={() => void loadInfo()}
        >
          {infoLoading ? "loading…" : "load full info"}
        </button>
      </div>

      {#if infoError}
        <div class="text-rose-300 text-[11.5px]">{infoError}</div>
      {/if}
      {#if infoPayload != null}
        <pre class="text-[10.5px] text-slate-300 bg-black/40 border border-white/5 rounded p-2 overflow-auto max-h-[40vh]">{JSON.stringify(infoPayload, null, 2)}</pre>
      {/if}
    </div>

    <PlayerOpsModal
      op={modalOp}
      username={player.username}
      onClose={() => (modalOp = null)}
      onResult={(ok, msg) => onResult(ok, msg)}
    />
  </aside>
{/if}
```

- [ ] **Step 2: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/components/PlayerDrawer.svelte
git commit -m "$(cat <<'EOF'
web-admin: PlayerDrawer.svelte — player detail with ops + player.info

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Frontend — wire `NodeDrawer` into `nodes.svelte`

**Files:**
- Modify: `web-admin/src/routes/nodes.svelte`

Add a `selected` state that records the selected node row. Row click opens it. Adapt the layout so the table sits next to the drawer when one is selected. `pendingNav.kind === "node"` opens the drawer directly (replaces the prefill-search-only behavior).

- [ ] **Step 1: Read the existing nodes.svelte to find the markup root**

```bash
sed -n '140,200p' web-admin/src/routes/nodes.svelte
```

You'll see a `<main class="p-4 space-y-3">` wrapping the header and table. Wrap that in a flex container so the drawer can sit alongside.

- [ ] **Step 2: Add selection state + drawer**

In the script section, after the existing state declarations, add:

```ts
import NodeDrawer from "../components/NodeDrawer.svelte";

type SelectedNode =
  | { kind: "host"; data: HostInfo }
  | { kind: "gateway"; data: GatewayInfo }
  | null;

let selected = $state<SelectedNode>(null);

function selectRow(r: NodeRow) {
  if (r.kind === "host") {
    const h = hosts.find((x) => x.id === r.id);
    if (h) selected = { kind: "host", data: h };
  } else {
    const g = gateways.find((x) => x.id === r.id);
    if (g) selected = { kind: "gateway", data: g };
  }
}
```

Replace the existing `pendingNav` $effect with one that ALSO opens the drawer when the picked node is found in the live stores (search prefill stays as a fallback for not-yet-loaded data):

```ts
$effect(() => {
  const t = pendingNav.value;
  if (!t || t.kind !== "node") return;
  search = t.id;
  typeFilter = "all";
  pendingNav.consume();
  // Try to open the drawer for this node directly. If the live store
  // hasn't populated yet (race at app boot), search remains prefilled
  // and the user clicks the row when it appears.
  const h = hosts.find((x) => x.id === t.id);
  if (h) {
    selected = { kind: "host", data: h };
    return;
  }
  const g = gateways.find((x) => x.id === t.id);
  if (g) selected = { kind: "gateway", data: g };
});
```

Pass an `onRowClick` to the `<DataTable>`. Find the existing DataTable mount:

```svelte
<DataTable
  {rows}
  {columns}
  initialSortKey="kind"
  emptyText="No nodes registered."
/>
```

Replace with:

```svelte
<DataTable
  {rows}
  {columns}
  initialSortKey="kind"
  emptyText="No nodes registered."
  onRowClick={selectRow}
/>
```

(`DataTable` already accepts `onRowClick?: (row: T) => void` per its existing props; no DataTable change needed.)

- [ ] **Step 3: Wrap the page in a flex container with the drawer**

Replace the outer `<main class="p-4 space-y-3">…</main>` with a flex layout:

```svelte
<div class="h-full flex">
  <main class="grow p-4 space-y-3 min-w-0">
    <!-- existing header + table content unchanged -->
    …
  </main>

  <NodeDrawer node={selected} onClose={() => (selected = null)} />
</div>
```

- [ ] **Step 4: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/routes/nodes.svelte
git commit -m "$(cat <<'EOF'
web-admin: nodes route — row click opens NodeDrawer; ⌘K opens directly

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: Frontend — wire `PlayerDrawer` into `players.svelte`

**Files:**
- Modify: `web-admin/src/routes/players.svelte`

Username column click opens the drawer. Inline row buttons (kick/tp/tpto) stay — they're the fast path for common moderation. `pendingNav.kind === "player"` opens the drawer directly.

- [ ] **Step 1: Add drawer state + import**

In `web-admin/src/routes/players.svelte` script, alongside the existing state:

```ts
import PlayerDrawer from "../components/PlayerDrawer.svelte";

let drawerPlayer = $state<PlayerInfo | null>(null);
```

Replace the existing `pendingNav` $effect with one that also opens the drawer:

```ts
$effect(() => {
  const t = pendingNav.value;
  if (!t || t.kind !== "player") return;
  search = t.username;
  statusFilter = "all";
  pendingNav.consume();
  const p = allPlayers.find((x) => x.username === t.username);
  if (p) drawerPlayer = p;
});
```

- [ ] **Step 2: Make the username column clickable**

Find the existing `<td class="py-1.5 px-2 font-mono">{p.username}</td>` and replace with:

```svelte
<td class="py-1.5 px-2 font-mono">
  <button
    type="button"
    class="text-slate-200 hover:text-accent-300"
    onclick={() => (drawerPlayer = p)}
  >{p.username}</button>
</td>
```

- [ ] **Step 3: Wrap the page in flex + mount the drawer**

Replace the existing outer `<main class="p-4 space-y-3">…</main>` with:

```svelte
<div class="h-full flex">
  <main class="grow p-4 space-y-3 min-w-0">
    <!-- existing header + table + toast + PlayerOpsModal unchanged -->
    …
  </main>

  <PlayerDrawer
    player={drawerPlayer}
    onClose={() => (drawerPlayer = null)}
    onResult={onResult}
  />
</div>
```

(`onResult` is the existing toast-pushing handler defined in players.svelte.)

- [ ] **Step 4: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: 0 errors.

```bash
cd .
git add web-admin/src/routes/players.svelte
git commit -m "$(cat <<'EOF'
web-admin: players route — username click opens PlayerDrawer; ⌘K opens directly

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: e2e smoke (manual)

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

- [ ] **Step 3: Walk the new panels**

Browse to `http://localhost:9101/admin/`, log in. For each:

- **Audit (`/audit`)** — table populates with at minimum your login entry. The "limit" dropdown (50/200/1000) refetches; "refresh" reloads.
- **Logs (`/logs`)** — categories grouped by prefix. Toggle one (e.g. `mesh:cell`) and watch the server stdout — the corresponding log lines appear/disappear within milliseconds.
- **Settings (`/settings`)** — your username + grants + session expiry rendered. "Sign out" returns to the login page.

- [ ] **Step 4: Drawers**

- **NodeDrawer** — go to `/nodes`, click a row. Drawer slides in with full host/gateway detail. For a host, the owned-cells list is clickable — clicking a cell ID jumps to `/cluster` with that cell selected.
- **PlayerDrawer** — connect a game client so a player exists. Go to `/players`, click the username — drawer opens. Try the ops buttons (tp/tpto/kick); confirm the toast fires. Click "load full info" — JSON dump appears below.

- **⌘K → drawer** — open the palette, type a host ID → Enter → Nodes route loads with the drawer already open on that host. Same with a player → Players route + drawer open.

- [ ] **Step 5: No commit**

Verification only.

---

### Task 13: CLAUDE.md update

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Append a paragraph after the existing dashboard description**

Find the existing block (search for "Command palette") and append after it:

```markdown

The Audit, Logs, and Settings routes (`/audit`, `/logs`, `/settings`) round out the sidebar. Audit reads the in-memory `AuditLog` ring via `/admin/api/audit?n=N`; Logs lists categories grouped by prefix and toggles them through new `/admin/api/logs/categories` endpoints (GET list, POST per-category enable). Settings is read-only — operator info from `sessionStore`; active-session table awaits a `SessionStore.List` method (Phase 2). Per-row drawers (`NodeDrawer.svelte`, `PlayerDrawer.svelte`) replicate the Cluster page's `CellDrawer` pattern: clicking a row opens detail + actions; the ⌘K palette opens drawers directly when the picked entity is in the live store. PlayerDrawer's "load full info" button POSTs the `player.info` cmdsys verb so game-specific fields are accessible without a custom panel.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
CLAUDE.md: document Audit / Logs / Settings panels + node + player drawers

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review checklist

- **Spec coverage:** §8 Diagnose group → Logs (Task 5), Audit (Task 4); §8 Config group → Settings (Task 6); §9 Phase 2 detail pages → NodeDrawer (Task 8), PlayerDrawer (Task 9). Live log tail and active-sessions table explicitly deferred.
- **Placeholder scan:** All steps contain concrete code or exact commands. No "TBD" / "implement later" patterns.
- **Type consistency:** TS `AuditEntry` field names match Go json tags (`startedAt`/`finishedAt`/`ok`/`traceId`). `LogCategory.enabled` matches the JSON field. `pendingNav.kind` strings (`"cell" | "node" | "player"`) match the existing store discriminator. The DataTable `onRowClick` prop is already part of the component's existing API per the prior plan; no DataTable change needed.
- **No new dependencies:** All work uses existing libs.
- **Backend additivity:** Two new HTTP endpoints (`logs/categories` GET + POST). `Server.logger` is a new field but defaults to nil-safe in handlers. No existing endpoints renamed or changed.
- **Distributed mode:** `/admin/api/logs/categories` toggles categories on the coordinator's Logger only. Remote hosts run their own Loggers and aren't affected — that's correct for v1 (each host can be SSH'd to and toggled via console). Cross-cluster log toggle is a Phase 2 cmdsys verb (`log.set`) ride.

---

## Execution

Plan complete. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task, review between tasks.
2. **Inline Execution** — execute tasks in this session with checkpoints.

Which approach?
