# Admin Dashboard — Hosts + Gateways + Players Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the three "people + deployment" panels — Hosts roster, Gateways roster, Players list with kick/tp/tpto operations — driven by live SSE streams and POST-to-cmdsys command invocations.

**Architecture:** Backend gets three new typed accessors on `*universe.Process` (`HostListEntries`, `GatewayListEntries`, `ActivePlayerSnapshots` extended with world coords + last-login) feeding `LocalClusterView` so the existing `/admin/api/{hosts,gateways,players}` endpoints return rich data. A new `players` SSE publisher pushes player presence/move updates at 1 Hz. Frontend adds three Svelte 5 routes that subscribe to those topics, render sortable tables, and surface per-row operations through a small `PlayerOpsModal` that POSTs to `/admin/api/commands/<verb>` (the typed `player.tp`/`.kick`/`.tpto` cmdsys verbs already exist).

**Tech Stack:** Go (existing `pkg/admin` + `pkg/universe`), Svelte 5 runes, Tailwind v4, Vitest. No new deps.

**Spec:** [`docs/superpowers/specs/2026-05-10-admin-dashboard-design.md`](../specs/2026-05-10-admin-dashboard-design.md) §8 panels #6 (Host roster), #8 (Gateway roster), #11 (Player list), #12 (Player ops).

**Prior plans:**
- [`2026-05-10-admin-dashboard-backend-foundation.md`](2026-05-10-admin-dashboard-backend-foundation.md) — `pkg/admin` skeleton + ClusterView + cmdsys wiring.
- [`2026-05-10-admin-dashboard-frontend-cluster.md`](2026-05-10-admin-dashboard-frontend-cluster.md) — Svelte SPA scaffold + Cluster page.

---

## Quick orientation

What already exists, reusable as-is:

- `cmdsys` verbs `player.tp` (Username, X, Y), `player.kick` (Username), `player.tpto` (Username, Target), `player.info` (Username) — all `RoutePlayerHomeOrOwner`. `pkg/universe/builtins_player.go`.
- `cmdsys` verbs `host.list` (returns pre-formatted text — not useful here), `gateway.list`, `gateway.info`, `session.list`, `session.info`. `pkg/universe/builtins_{host,gateway,session}.go`.
- `Process.HostListEntries()` and `Process.GatewayListEntries()` — **don't exist yet; this plan adds them**.
- `Process.ActivePlayerSnapshots() []PlayerSnapshot` — exists, returns `{Username, HostID, CellID, WorldX, WorldY, LastLogin}` shape but only `Username/HostID/CellID` are populated. This plan extends it to fill the rest.
- `Process.HostRegistry` lookup: `c.hostRegistry.LiveHosts() []HostInfo` (exists in `host_registry.go`).
- `Process.Hosts map[string]*Host` — local hosts map.
- `Process.Control.AllOwnedCells(fn)` — iterates `(cellID, hostID)` pairs.
- Frontend `lib/api.ts` POST helper, `lib/stream.ts` multiplexed SSE, `lib/stores.svelte.ts` (already includes `hostsStore`, `gatewaysStore`, `playersStore`).
- Frontend `lib/types.ts` already declares `HostInfo`, `GatewayInfo`, `PlayerInfo` shapes; this plan tweaks them to match the new typed surface.
- `app.svelte` route dispatch — currently only handles `/cluster`; this plan extends it.
- The publishers in `pkg/admin/publishers.go`: `cellsPublisher` (4 Hz), `hostsPublisher` (1 Hz), `commitPublisher`. **No `players` or `gateways` publisher yet** — this plan adds players; gateways piggyback on the existing `hosts` cadence (we publish both at 1 Hz from one ticker).

What's deliberately out of scope (deferred to later plans):

- **Per-host / per-gateway / per-player detail routes** (#7, #9, #13 in spec §8) — clicking a row in v1 doesn't navigate; we just expose enough info on the table.
- **Offline player listing** — `PlayerRepository.ListOffline` doesn't have a search API yet. The Players page status filter accepts "all" / "offline" but only returns online entries until that lands. The status dropdown stays in the UI as a hint of what's coming.
- **Generic `CommandForm.svelte`** that builds from any cmdsys schema — Plan 5 (cmd palette) covers that. This plan hand-writes the three player ops forms.
- **`session.kick`** — covered by `player.kick`.

Build/test commands stay the same:

- Backend: `go test ./pkg/admin/... ./pkg/universe/...`, `go vet ./...`
- Frontend: `cd web-admin && bun run test`, `bun run typecheck`, `bun run build`
- e2e: build the binary + 4node-basic, log in to `localhost:9101/admin/`, click through

---

## File structure

**Backend additions:**

```text
pkg/universe/
├── admin_accessors.go             # MODIFY — add HostListEntry, GatewayListEntry,
│                                  # extend PlayerSnapshot, new accessors
└── (no other touches)

pkg/admin/
├── view.go                        # MODIFY — extend HostInfo/GatewayInfo DTOs to
│                                  # match the richer shape; PlayerInfo already
│                                  # has the right fields
├── view_local.go                  # MODIFY — Hosts/Gateways/Players use the new
│                                  # accessors
├── view_local_test.go             # MODIFY — add a fixture-driven host/gateway test
└── publishers.go                  # MODIFY — add `players` topic; rename
                                   # hostsPublisher to "rosterPublisher" so it
                                   # also publishes gateways
```

**Frontend additions:**

```text
web-admin/src/
├── lib/
│   ├── types.ts                   # MODIFY — HostInfo / GatewayInfo / PlayerInfo
│   │                              # tweaks (some fields no longer optional)
│   └── stores.svelte.ts           # (no change — stores already exist)
├── routes/
│   ├── hosts.svelte               # NEW
│   ├── gateways.svelte            # NEW
│   └── players.svelte             # NEW
├── components/
│   ├── DataTable.svelte           # NEW — small headless table (header + rows)
│   ├── DataTable.test.ts          # NEW — sort + filter behavior
│   ├── PlayerOpsModal.svelte      # NEW — kick/tp/tpto forms
│   └── ConfirmDialog.svelte       # NEW — used by kick, future destructive ops
└── app.svelte                     # MODIFY — route dispatch for /hosts /gateways /players
```

---

### Task 1: Backend — `Process.HostListEntries`

**Files:**
- Modify: `pkg/universe/admin_accessors.go`

- [ ] **Step 1: Read the existing accessor file**

```bash
cat pkg/universe/admin_accessors.go
```

You should see `MetricsSnapshots`, `MetricsSnapshot`, `PlayerSnapshot`, `ActivePlayerSnapshots`. We're adding next to those.

- [ ] **Step 2: Read what Process exposes about hosts**

```bash
grep -n 'hostRegistry\|c\.Hosts\b\|HostInfo struct\|type Host struct\|LiveHosts(\|AllOwnedCells\|c\.Control\b' pkg/universe/coordinator.go pkg/universe/host_registry.go pkg/universe/host.go 2>/dev/null | head -25
```

Expected:
- `c.hostRegistry *HostRegistry` (private, hosts-registered-via-control-plane registry)
- `c.Hosts map[string]*Host` (local in-process hosts map)
- `c.Control` is the orchestrator with `AllOwnedCells(fn func(cellID, hostID string) bool)`
- `c.hostRegistry.LiveHosts() []HostInfo` returns rich data

If `LiveHosts` returns a different field set, adjust the implementation below to map whatever fields are actually present.

- [ ] **Step 3: Add the new entry type and accessor**

Append to `pkg/universe/admin_accessors.go`:

```go
// HostListEntry is a richer-than-LiveHostIDs view of a single host. Includes
// what the dashboard's Hosts roster needs: roles inferred from the registry,
// cells the host owns, heartbeat age, total entity count summed from those
// cells, and per-host load (max of the cells' CompositeLoad).
type HostListEntry struct {
	ID             string
	Roles          []string
	State          string // "live" | "draining" | "dead"
	IsLocal        bool
	HeartbeatAge   time.Duration
	OwnedCells     []string
	Load           float64 // max of CompositeLoad across owned cells
	TotalEntities  int     // sum of Entities.Real across owned cells
}

// HostListEntries returns one entry per host known to this Process. Combines
// the registry view (live/heartbeat/state for remote hosts) with the local
// Hosts map (the all-in-one preset's single in-process host) and the metrics
// map (load + entity count).
//
// Used by pkg/admin's LocalClusterView.Hosts(). Read-locked snapshot — safe
// to call concurrently with cluster work.
func (c *Process) HostListEntries() []HostListEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 1. Build a cellsPerHost map and metrics-by-cell.
	cellsPerHost := make(map[string][]string)
	if c.Control != nil {
		c.Control.AllOwnedCells(func(cellID, hostID string) bool {
			cellsPerHost[hostID] = append(cellsPerHost[hostID], cellID)
			return true
		})
	}
	// Fallback for the all-in-one preset where Control may not iterate
	// any cells: assign every local Cell to the single local host.
	if len(cellsPerHost) == 0 && len(c.Hosts) >= 1 {
		var localHostID string
		for id := range c.Hosts {
			localHostID = id
			break
		}
		ids := make([]string, 0, len(c.Cells))
		for id := range c.Cells {
			ids = append(ids, string(id))
		}
		cellsPerHost[localHostID] = ids
	}

	metricsByCell := make(map[string]metrics.LoadSnapshot)
	for id, node := range c.Cells {
		if node.Metrics != nil {
			metricsByCell[string(id)] = node.Metrics.Snapshot()
		}
	}

	// 2. Aggregate from the registry (rich state, heartbeats) when available.
	out := []HostListEntry{}
	seen := map[string]bool{}
	now := time.Now()
	if c.hostRegistry != nil {
		for _, h := range c.hostRegistry.LiveHosts() {
			cells := cellsPerHost[h.ID]
			ent := HostListEntry{
				ID:         h.ID,
				State:      h.State.String(),
				IsLocal:    h.Local,
				OwnedCells: cells,
			}
			if !h.Local {
				ent.HeartbeatAge = now.Sub(h.LastHeartbeat)
			}
			if h.ServiceOnly {
				ent.Roles = []string{"service"}
			} else {
				ent.Roles = []string{"host"}
			}
			ent.Load, ent.TotalEntities = aggregateCellMetrics(cells, metricsByCell)
			out = append(out, ent)
			seen[h.ID] = true
		}
	}

	// 3. Backfill any local-only host not present in the registry (the
	//    all-in-one preset hosts the gateway+coordinator+host on the same
	//    Process and may not register itself with hostRegistry).
	for id := range c.Hosts {
		if seen[id] {
			continue
		}
		cells := cellsPerHost[id]
		ent := HostListEntry{
			ID:         id,
			State:      "live",
			IsLocal:    true,
			OwnedCells: cells,
			Roles:      []string{"host"},
		}
		ent.Load, ent.TotalEntities = aggregateCellMetrics(cells, metricsByCell)
		out = append(out, ent)
	}
	return out
}

func aggregateCellMetrics(cellIDs []string, metricsByCell map[string]metrics.LoadSnapshot) (load float64, totalEntities int) {
	for _, id := range cellIDs {
		s, ok := metricsByCell[id]
		if !ok {
			continue
		}
		if s.CompositeLoad > load {
			load = s.CompositeLoad
		}
		totalEntities += s.Entities.Real
	}
	return load, totalEntities
}
```

If `c.Control.AllOwnedCells` doesn't exist (different name), find it with:

```bash
grep -n 'AllOwnedCells\|OwnedCells\b' pkg/universe/*.go | head -5
```

Adjust the call site. Same for `c.hostRegistry` field name.

- [ ] **Step 4: Verify compile**

```bash
cd .
go vet ./pkg/universe/...
```

Expected: clean. If `time` isn't already imported in `admin_accessors.go`, the existing import block should already cover it (the file uses `time.Time` for `PlayerSnapshot.LastLogin`).

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/admin_accessors.go
git commit -m "universe: HostListEntries — typed host roster for pkg/admin"
```

---

### Task 2: Backend — `Process.GatewayListEntries`

**Files:**
- Modify: `pkg/universe/admin_accessors.go`

- [ ] **Step 1: Find what gateway state Process tracks**

```bash
grep -n 'gatewayRegistry\|c\.Gateways\b\|type Gateway struct\|sessionRoutes\|GatewayMode' pkg/universe/coordinator.go pkg/universe/gateway*.go 2>/dev/null | head -20
```

You'll see:
- `c.gatewayRegistry *GatewayRegistry` — the registry of registered gateways
- `c.sessionRoutes` — keyed `SessionKey{GatewayID, ConnID}` → host/cell
- `c.gateway *Gateway` — the embedded gateway (nil when standalone)
- Each `Gateway` has byte counters via `connMgr` (read with `connMgr.BytesIn()` / `.BytesOut()` if those exist).

- [ ] **Step 2: Add the type + accessor**

Append to `pkg/universe/admin_accessors.go`:

```go
// GatewayListEntry is the dashboard-facing view of one gateway.
type GatewayListEntry struct {
	ID         string
	Sessions   int    // count of active sessions routed through this gateway
	BytesSent  uint64 // cumulative
	BytesRecv  uint64 // cumulative
	Mode       string // "local-shortcut" | "always-proxy" | "" (unknown)
	IsLocal    bool   // true for the embedded gateway in the all-in-one preset
}

// GatewayListEntries returns one entry per gateway known to this Process.
// Combines the gatewayRegistry (remote standalone gateways) with the local
// embedded gateway when present.
func (c *Process) GatewayListEntries() []GatewayListEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Sessions per gateway: walk sessionRoutes (keyed by GatewayID+ConnID).
	sessionsByGateway := make(map[string]int)
	if c.sessionRoutes != nil {
		c.sessionRoutes.Each(func(k SessionKey, _ SessionRoute) {
			sessionsByGateway[k.GatewayID]++
		})
	}

	out := []GatewayListEntry{}
	seen := map[string]bool{}

	if c.gatewayRegistry != nil {
		for _, g := range c.gatewayRegistry.LiveGateways() {
			ent := GatewayListEntry{
				ID:       g.ID,
				Sessions: sessionsByGateway[g.ID],
				IsLocal:  g.Local,
				Mode:     g.Mode,
			}
			out = append(out, ent)
			seen[g.ID] = true
		}
	}

	// Backfill the embedded gateway when it isn't in the registry (typical
	// in single-process all-in-one — the embedded gateway uses ID "inproc").
	if c.gateway != nil && !seen[c.gateway.id] {
		out = append(out, GatewayListEntry{
			ID:       c.gateway.id,
			Sessions: sessionsByGateway[c.gateway.id],
			IsLocal:  true,
			Mode:     c.cfg.GatewayMode,
		})
	}

	return out
}
```

If `sessionRoutes.Each` doesn't exist, search:

```bash
grep -n 'func .*sessionRoutes.*Each\|func .*\(SessionRoutes\)\b' pkg/universe/session_routes.go
```

If iteration uses a different method name (`Walk`, `Iter`, etc.), adjust. If the only API is internal, add a small `(s *SessionRoutes) Each(fn func(SessionKey, SessionRoute) bool)` helper that holds the read lock and ranges. If `SessionRoute` isn't the value type, name it whatever the file uses.

If `g.Mode` and `g.Local` don't exist on the registry's Gateway struct, leave those fields zero-valued and continue — the table will render `mode=—`.

- [ ] **Step 3: Verify compile**

```bash
go vet ./pkg/universe/...
```

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/admin_accessors.go pkg/universe/session_routes.go
git commit -m "universe: GatewayListEntries — typed gateway roster for pkg/admin"
```

(Only stage `session_routes.go` if you actually added an `Each` helper to it.)

---

### Task 3: Backend — extend `PlayerSnapshot` with world coords + last-login

**Files:**
- Modify: `pkg/universe/admin_accessors.go`
- Modify: `pkg/admin/view_local.go` (the mapping into `PlayerInfo`)

- [ ] **Step 1: Investigate where world coords live**

```bash
grep -n 'PlayerLocation\|c\.players\b\|playerWorldPos\|component\.Position' pkg/universe/coordinator.go pkg/component/*.go 2>/dev/null | head -10
```

`PlayerLocation` (around `coordinator.go:391`) carries `HostID`, `CellID`, `Active`. World position lives in the player entity's ECS Position component, which the coord doesn't directly index by username — it'd require an O(n) scan of the player's cell's ECS world.

For v1: read world coords from `PlayerLocation` if it carries them (it may have grown a `WorldX/WorldY` field; check). If not, leave them zero — the dashboard renders `—` for missing values. Don't add the ECS scan in this plan.

For LastLogin: `PlayerLocation` has no timestamp. The persisted `PlayerRepository.GetByUsername` returns a `PlayerRecord` with `LastLogin time.Time`. Looking it up on every snapshot is too slow for a 1Hz publisher. Defer LastLogin to "loaded on demand when the user opens a player detail page" — for v1 it stays zero in the snapshot.

So this task simplifies: just confirm what's available and update doc comments.

- [ ] **Step 2: Update the doc comment on `PlayerSnapshot`**

In `pkg/universe/admin_accessors.go`, find `PlayerSnapshot` and update its doc:

```go
// PlayerSnapshot describes one online player's location at a moment in time.
// Populated from c.players under the read lock.
//
// Fields populated in v1: Username, HostID, CellID. WorldX/WorldY/LastLogin
// are zero unless the underlying PlayerLocation tracks them — they're left
// in the struct so the wire shape is stable as those fields land. Per-player
// detail (last login, exact world position) lands via a separate
// `player.info`-backed lookup when the dashboard adds detail routes.
type PlayerSnapshot struct {
	Username  string
	HostID    string
	CellID    string
	WorldX    float32
	WorldY    float32
	LastLogin time.Time
}
```

If `PlayerLocation` actually does have `WorldX/WorldY` fields, populate them in `ActivePlayerSnapshots`:

```bash
sed -n '388,420p' pkg/universe/coordinator.go
```

If the fields exist, find the existing `ActivePlayerSnapshots` body and add:

```go
out = append(out, PlayerSnapshot{
    Username: username,
    HostID:   loc.HostID,
    CellID:   string(loc.CellID),
    WorldX:   loc.WorldX, // if the field exists; remove this line if not
    WorldY:   loc.WorldY,
})
```

If the fields don't exist, leave the body as-is.

- [ ] **Step 3: Verify**

```bash
go vet ./pkg/universe/... ./pkg/admin/...
go test ./pkg/admin/...
```

Expected: clean / pass.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/admin_accessors.go
git commit -m "universe: clarify PlayerSnapshot field availability for pkg/admin"
```

(If you didn't actually change anything, skip the commit.)

---

### Task 4: Backend — wire LocalClusterView to use the rich accessors

**Files:**
- Modify: `pkg/admin/view.go`
- Modify: `pkg/admin/view_local.go`

- [ ] **Step 1: Update `pkg/admin/view.go` HostInfo / GatewayInfo to match the richer surface**

Find `HostInfo` and ensure it has these fields with these json tags (it should — from the foundation plan):

```go
type HostInfo struct {
	ID             string   `json:"id"`
	Roles          []string `json:"roles"`
	State          string   `json:"state"`         // "live" | "draining" | "dead"
	IsLocal        bool     `json:"isLocal"`
	HeartbeatAgeMs int64    `json:"heartbeatAgeMs"`
	Cells          []string `json:"cells"`
	Load           float64  `json:"load"`
	TotalEntities  int      `json:"totalEntities"`
}
```

If any field is missing or its tag is wrong, fix it. Same for `GatewayInfo` — make sure it has:

```go
type GatewayInfo struct {
	ID          string `json:"id"`
	Sessions    int    `json:"sessions"`
	BytesSentPS uint64 `json:"bytesSent"`
	BytesRecvPS uint64 `json:"bytesRecv"`
	Mode        string `json:"mode"`
	IsLocal     bool   `json:"isLocal"`
}
```

(Note: `BytesSentPS`/`BytesRecvPS` keep their PS suffix in the Go field name to indicate the *intended* per-second semantics; the json tag is the honest cumulative `bytesSent` / `bytesRecv` per the Task 3 fixup of the previous plan. If this doesn't match the current file, align the file to this — backend wire output should be consistent across plans.)

- [ ] **Step 2: Update `LocalClusterView.Hosts()`**

In `pkg/admin/view_local.go`, replace the existing `Hosts()` with:

```go
func (v *LocalClusterView) Hosts() []HostInfo {
	entries := v.p.HostListEntries()
	out := make([]HostInfo, 0, len(entries))
	for _, e := range entries {
		cells := append([]string(nil), e.OwnedCells...)
		out = append(out, HostInfo{
			ID:             e.ID,
			Roles:          append([]string(nil), e.Roles...),
			State:          e.State,
			IsLocal:        e.IsLocal,
			HeartbeatAgeMs: e.HeartbeatAge.Milliseconds(),
			Cells:          cells,
			Load:           e.Load,
			TotalEntities:  e.TotalEntities,
		})
	}
	return out
}
```

- [ ] **Step 3: Update `LocalClusterView.Gateways()`**

```go
func (v *LocalClusterView) Gateways() []GatewayInfo {
	entries := v.p.GatewayListEntries()
	out := make([]GatewayInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, GatewayInfo{
			ID:          e.ID,
			Sessions:    e.Sessions,
			BytesSentPS: e.BytesSent,
			BytesRecvPS: e.BytesRecv,
			Mode:        e.Mode,
			IsLocal:     e.IsLocal,
		})
	}
	return out
}
```

- [ ] **Step 4: Update `LocalClusterView.Cluster()` `SessionCount`**

The cluster summary should now sum sessions across gateways:

Find the existing `Cluster()` body. Replace its `SessionCount: 0` line (or however that field is computed) with:

```go
sessionCount := 0
for _, g := range gws {
    sessionCount += g.Sessions
}
return ClusterInfo{
    ...
    SessionCount:  sessionCount,
    ...
}
```

- [ ] **Step 5: Run + commit**

```bash
go vet ./pkg/admin/...
go test ./pkg/admin/...
```

Expected: clean / pass.

```bash
git add pkg/admin/view.go pkg/admin/view_local.go
git commit -m "admin: LocalClusterView uses HostListEntries / GatewayListEntries"
```

---

### Task 5: Backend — add `players` SSE topic publisher

**Files:**
- Modify: `pkg/admin/publishers.go`

- [ ] **Step 1: Read current publishers.go**

```bash
cat pkg/admin/publishers.go
```

Note: `cellsPublisher` ticks 4Hz, `hostsPublisher` ticks 1Hz. The simplest path: have `hostsPublisher` also publish gateways and players (rename it to `rosterPublisher`). One ticker, three publishes — no extra goroutines.

- [ ] **Step 2: Replace hostsPublisher with rosterPublisher**

In `pkg/admin/publishers.go` find:

```go
go hostsPublisher(ctx, view, bus)
```

and

```go
func hostsPublisher(ctx context.Context, view ClusterView, bus *TopicBus) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			bus.Publish("hosts", view.Hosts())
		}
	}
}
```

Replace both with:

```go
go rosterPublisher(ctx, view, bus)
```

```go
// rosterPublisher fans the host roster, gateway roster, and player roster
// onto the bus at 1 Hz on a single ticker. These three topics share a cadence
// because the underlying state changes are infrequent (login/logout/cell-
// migration) — a per-second snapshot is plenty to keep tables in sync.
func rosterPublisher(ctx context.Context, view ClusterView, bus *TopicBus) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			bus.Publish("hosts", view.Hosts())
			bus.Publish("gateways", view.Gateways())
			bus.Publish("players", view.Players(PlayerFilter{Limit: 1000}))
		}
	}
}
```

(Limit 1000 caps the wire payload at a sane size; the dashboard's player list paginates the rendered rows separately.)

- [ ] **Step 3: Update startPublishers**

```go
func startPublishers(ctx context.Context, p *universe.Process, view ClusterView, bus *TopicBus) {
	go cellsPublisher(ctx, view, bus)
	go rosterPublisher(ctx, view, bus)
	go commitPublisher(ctx, p, bus)
}
```

- [ ] **Step 4: Verify + commit**

```bash
go vet ./pkg/admin/...
go test ./pkg/admin/...
```

```bash
git add pkg/admin/publishers.go
git commit -m "admin: rosterPublisher — hosts/gateways/players share one 1Hz ticker"
```

---

### Task 6: Backend — fixture test for the rich accessors

**Files:**
- Modify: `pkg/admin/view_local_test.go`

- [ ] **Step 1: Add a test asserting Hosts() returns at least one entry with cells**

Append to `pkg/admin/view_local_test.go`:

```go
func TestLocalClusterView_Hosts(t *testing.T) {
	t.Parallel()
	v := NewLocalClusterView(newTestProcessForView(t))
	hosts := v.Hosts()
	if len(hosts) == 0 {
		t.Fatalf("expected >=1 host, got 0")
	}
	for _, h := range hosts {
		if h.ID == "" {
			t.Fatalf("host missing ID: %+v", h)
		}
		if h.State == "" {
			t.Fatalf("host %s missing State", h.ID)
		}
	}
}

func TestLocalClusterView_Gateways(t *testing.T) {
	t.Parallel()
	v := NewLocalClusterView(newTestProcessForView(t))
	// All-in-one preset has the embedded gateway registered as "inproc".
	gws := v.Gateways()
	if len(gws) == 0 {
		t.Fatalf("expected >=1 gateway, got 0")
	}
}

func TestLocalClusterView_Players_EmptyOnFreshFixture(t *testing.T) {
	t.Parallel()
	v := NewLocalClusterView(newTestProcessForView(t))
	if got := v.Players(PlayerFilter{}); len(got) != 0 {
		t.Fatalf("expected 0 players in fresh fixture, got %d", len(got))
	}
}
```

If the existing `newTestProcessForView` helper isn't available, find it:

```bash
grep -n 'newTestProcessForView\|func newTestProcess' pkg/admin/*_test.go
```

It was added in Task 3 of the foundation plan.

- [ ] **Step 2: Run + commit**

```bash
go test ./pkg/admin/ -run "Hosts|Gateways|Players_EmptyOnFreshFixture" -v
```

All passing.

```bash
git add pkg/admin/view_local_test.go
git commit -m "admin: fixture tests for LocalClusterView Hosts/Gateways/Players"
```

---

### Task 7: Frontend — types tweaks

**Files:**
- Modify: `web-admin/src/lib/types.ts`

- [ ] **Step 1: Open the types file and verify the shapes**

```bash
cat web-admin/src/lib/types.ts
```

Confirm `HostInfo`, `GatewayInfo`, `PlayerInfo` match the backend `view.go` shapes from Task 4. If any field is wrong (e.g. `state` was `"live" | "draining" | "dead"` but the backend may now also produce `"unknown"`), relax to `string`.

- [ ] **Step 2: Update HostInfo / GatewayInfo if needed**

Make sure these match the backend exactly:

```ts
export type HostInfo = {
  id: string;
  roles: string[] | null;
  state: string; // "live" | "draining" | "dead"
  isLocal: boolean;
  heartbeatAgeMs: number;
  cells: string[] | null;
  load: number;
  totalEntities: number;
};

export type GatewayInfo = {
  id: string;
  sessions: number;
  bytesSent: number;
  bytesRecv: number;
  mode: string;
  isLocal: boolean;
};
```

Add `isLocal` if missing.

- [ ] **Step 3: Typecheck + commit**

```bash
cd web-admin
bun run typecheck
```

Expected: clean.

```bash
cd .
git add web-admin/src/lib/types.ts
git commit -m "web-admin: align HostInfo/GatewayInfo/PlayerInfo with extended backend"
```

---

### Task 8: Frontend — `DataTable` component

**Files:**
- Create: `web-admin/src/components/DataTable.svelte`
- Create: `web-admin/src/components/DataTable.test.ts`

A small, headless-ish sortable table. Header row clicks toggle sort direction; rows are rendered via a render-prop snippet so each route can compose its own cells (Hosts has different columns from Players).

- [ ] **Step 1: Write the test for the sort helper**

Create `web-admin/src/components/DataTable.test.ts`:

```ts
import { describe, it, expect } from "vitest";
import { sortRows, type SortDir } from "./DataTable.helpers";

describe("sortRows", () => {
  type Row = { id: string; load: number };
  const rows: Row[] = [
    { id: "b", load: 0.5 },
    { id: "a", load: 0.9 },
    { id: "c", load: 0.1 },
  ];

  it("sorts ascending by string", () => {
    const out = sortRows(rows, (r) => r.id, "asc");
    expect(out.map((r) => r.id)).toEqual(["a", "b", "c"]);
  });

  it("sorts descending by number", () => {
    const out = sortRows(rows, (r) => r.load, "desc");
    expect(out.map((r) => r.id)).toEqual(["a", "b", "c"]);
  });

  it("returns original array (not a mutation)", () => {
    const out = sortRows(rows, (r) => r.id, "asc");
    expect(out).not.toBe(rows);
    expect(rows[0].id).toBe("b"); // original unchanged
  });

  it("handles undefined gracefully (sorts to end)", () => {
    type R = { id: string; v?: number };
    const xs: R[] = [{ id: "a", v: 1 }, { id: "b" }, { id: "c", v: 2 }];
    const out = sortRows(xs, (r) => r.v, "asc" as SortDir);
    expect(out[out.length - 1].id).toBe("b");
  });
});
```

- [ ] **Step 2: Implement the helpers**

Create `web-admin/src/components/DataTable.helpers.ts`:

```ts
export type SortDir = "asc" | "desc";

// sortRows returns a new array sorted by the given accessor. Numbers and
// strings sort naturally; undefined/null values are pushed to the end
// regardless of direction.
export function sortRows<T>(
  rows: readonly T[],
  accessor: (r: T) => string | number | undefined | null,
  dir: SortDir,
): T[] {
  const out = rows.slice();
  out.sort((a, b) => {
    const av = accessor(a);
    const bv = accessor(b);
    if (av == null && bv == null) return 0;
    if (av == null) return 1;
    if (bv == null) return -1;
    if (av < bv) return dir === "asc" ? -1 : 1;
    if (av > bv) return dir === "asc" ? 1 : -1;
    return 0;
  });
  return out;
}
```

- [ ] **Step 3: Run + commit helpers (test goes green)**

```bash
cd web-admin && bun run test src/components/DataTable.test.ts
```

Expected: 4 passing.

```bash
cd .
git add web-admin/src/components/DataTable.helpers.ts web-admin/src/components/DataTable.test.ts
git commit -m "web-admin: DataTable.helpers — typed sortRows with stable null handling"
```

- [ ] **Step 4: Implement the Svelte component**

Create `web-admin/src/components/DataTable.svelte`:

```svelte
<script lang="ts" generics="T">
  import { ChevronDown, ChevronRight } from "$lib/icons";
  import { sortRows, type SortDir } from "./DataTable.helpers";

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  type IconComp = any;

  type Column<U> = {
    key: string;            // unique key (used for sort state + as :key)
    label: string;
    /** Cell accessor — used for sorting; render() controls display. */
    accessor?: (row: U) => string | number | undefined | null;
    /** Optional render override; defaults to String(accessor(row)). */
    render?: (row: U) => unknown;
    align?: "left" | "right" | "center";
    width?: string;        // CSS width hint, e.g. "120px" / "20%"
  };

  type Props = {
    rows: T[];
    columns: Column<T>[];
    initialSortKey?: string;
    initialSortDir?: SortDir;
    /** Empty-state text shown when rows.length === 0. */
    emptyText?: string;
    /** Optional click-row handler. */
    onRowClick?: (row: T) => void;
  };

  let {
    rows,
    columns,
    initialSortKey,
    initialSortDir = "asc",
    emptyText = "No data.",
    onRowClick,
  }: Props = $props();

  let sortKey = $state(initialSortKey ?? columns[0]?.key ?? "");
  let sortDir = $state<SortDir>(initialSortDir);

  let sorted = $derived(() => {
    const col = columns.find((c) => c.key === sortKey);
    if (!col?.accessor) return rows;
    return sortRows(rows, col.accessor, sortDir);
  });

  function toggleSort(key: string) {
    if (sortKey === key) {
      sortDir = sortDir === "asc" ? "desc" : "asc";
    } else {
      sortKey = key;
      sortDir = "asc";
    }
  }

  function arrow(key: string): IconComp | null {
    if (key !== sortKey) return null;
    return sortDir === "asc" ? ChevronRight : ChevronDown;
  }
</script>

<div class="overflow-x-auto">
  <table class="w-full text-[12px] border-collapse">
    <thead>
      <tr class="text-left text-[10.5px] uppercase tracking-wide text-slate-500 border-b border-white/10">
        {#each columns as col (col.key)}
          {@const arr = arrow(col.key)}
          <th
            class="py-1.5 px-2 font-medium cursor-pointer hover:text-slate-300 select-none"
            style:width={col.width ?? "auto"}
            style:text-align={col.align ?? "left"}
            onclick={() => col.accessor && toggleSort(col.key)}
          >
            <span class="inline-flex items-center gap-1">
              {col.label}
              {#if arr}
                <svelte:component this={arr} class="w-3 h-3" />
              {/if}
            </span>
          </th>
        {/each}
      </tr>
    </thead>
    <tbody>
      {#each sorted() as row, i (i)}
        <tr
          class="border-b border-white/5 hover:bg-white/5 {onRowClick ? 'cursor-pointer' : ''}"
          onclick={() => onRowClick?.(row)}
        >
          {#each columns as col (col.key)}
            <td class="py-1.5 px-2" style:text-align={col.align ?? "left"}>
              {#if col.render}
                {@render renderCell(col.render(row))}
              {:else if col.accessor}
                {col.accessor(row) ?? "—"}
              {:else}
                —
              {/if}
            </td>
          {/each}
        </tr>
      {:else}
        <tr>
          <td colspan={columns.length} class="py-4 text-center text-slate-500">
            {emptyText}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>

{#snippet renderCell(value: unknown)}
  {#if value == null}—{:else}{value}{/if}
{/snippet}
```

If `<svelte:component this=...>` errors on Svelte 5 (it's deprecated in favor of capitalized dynamic components), replace with:

```svelte
{#if arr}
  {@const Arr = arr}
  <Arr class="w-3 h-3" />
{/if}
```

- [ ] **Step 5: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

Expected: clean.

```bash
cd .
git add web-admin/src/components/DataTable.svelte
git commit -m "web-admin: DataTable.svelte — sortable table with column render hooks"
```

---

### Task 9: Frontend — `routes/hosts.svelte`

**Files:**
- Create: `web-admin/src/routes/hosts.svelte`

- [ ] **Step 1: Implement**

Create `web-admin/src/routes/hosts.svelte`:

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { hostsStore } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import { fmtDuration, fmtLoad } from "$lib/format";
  import type { HostInfo } from "$lib/types";
  import DataTable from "../components/DataTable.svelte";

  let hosts = $derived<HostInfo[]>(hostsStore.value ?? []);

  // One-shot fetch at mount + live updates via SSE.
  onMount(async () => {
    try {
      const initial = await apiGet<HostInfo[]>("/admin/api/hosts");
      hostsStore.set(initial);
    } catch {
      // Stream subscription will populate it shortly.
    }
  });

  $effect(() => {
    const off = stream.subscribe("hosts", (data) => {
      hostsStore.set(data as HostInfo[]);
    });
    return off;
  });

  const columns = [
    { key: "id", label: "Host", accessor: (h: HostInfo) => h.id, width: "20%" },
    {
      key: "state",
      label: "State",
      accessor: (h: HostInfo) => h.state,
      render: (h: HostInfo) => `${h.state}${h.isLocal ? " *" : ""}`,
      width: "100px",
    },
    {
      key: "roles",
      label: "Roles",
      accessor: (h: HostInfo) => (h.roles ?? []).join(","),
      render: (h: HostInfo) => (h.roles ?? []).join(", "),
      width: "120px",
    },
    {
      key: "hb",
      label: "HB age",
      accessor: (h: HostInfo) => h.heartbeatAgeMs,
      render: (h: HostInfo) =>
        h.isLocal ? "—" : fmtDuration(h.heartbeatAgeMs),
      width: "90px",
      align: "right" as const,
    },
    {
      key: "cells",
      label: "Cells",
      accessor: (h: HostInfo) => (h.cells ?? []).length,
      render: (h: HostInfo) => `${(h.cells ?? []).length}`,
      align: "right" as const,
      width: "60px",
    },
    {
      key: "entities",
      label: "Entities",
      accessor: (h: HostInfo) => h.totalEntities,
      align: "right" as const,
      width: "80px",
    },
    {
      key: "load",
      label: "Load",
      accessor: (h: HostInfo) => h.load,
      render: (h: HostInfo) => fmtLoad(h.load),
      align: "right" as const,
      width: "70px",
    },
  ];
</script>

<main class="p-4">
  <h2 class="text-accent-300 text-[11px] uppercase tracking-wide mb-3">Hosts</h2>
  <div class="bg-[#0d1117] border border-white/10 rounded-lg p-3">
    <DataTable
      rows={hosts}
      {columns}
      initialSortKey="id"
      emptyText="No hosts registered."
    />
  </div>
</main>
```

- [ ] **Step 2: Typecheck**

```bash
cd web-admin && bun run typecheck
```

Expected: clean. If `DataTable` typing causes friction (the `Column<T>[]` generic doesn't infer through), declare the array as `const columns: Column<HostInfo>[] = [...]` (and `import type { Column } from "../components/DataTable.svelte"` if exported, otherwise inline the structural type).

- [ ] **Step 3: Commit**

```bash
git add web-admin/src/routes/hosts.svelte
git commit -m "web-admin: Hosts roster route with sortable DataTable"
```

---

### Task 10: Frontend — `routes/gateways.svelte`

**Files:**
- Create: `web-admin/src/routes/gateways.svelte`

- [ ] **Step 1: Implement**

Create `web-admin/src/routes/gateways.svelte`:

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { gatewaysStore } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import { fmtBytes } from "$lib/format";
  import type { GatewayInfo } from "$lib/types";
  import DataTable from "../components/DataTable.svelte";

  let gateways = $derived<GatewayInfo[]>(gatewaysStore.value ?? []);

  onMount(async () => {
    try {
      const initial = await apiGet<GatewayInfo[]>("/admin/api/gateways");
      gatewaysStore.set(initial);
    } catch {
      // Stream takes over.
    }
  });

  $effect(() => {
    const off = stream.subscribe("gateways", (data) => {
      gatewaysStore.set(data as GatewayInfo[]);
    });
    return off;
  });

  const columns = [
    { key: "id", label: "Gateway", accessor: (g: GatewayInfo) => g.id, width: "20%" },
    {
      key: "local",
      label: "Where",
      accessor: (g: GatewayInfo) => (g.isLocal ? 0 : 1),
      render: (g: GatewayInfo) => (g.isLocal ? "in-proc" : "remote"),
      width: "100px",
    },
    {
      key: "mode",
      label: "Mode",
      accessor: (g: GatewayInfo) => g.mode || "—",
      width: "140px",
    },
    {
      key: "sessions",
      label: "Sessions",
      accessor: (g: GatewayInfo) => g.sessions,
      align: "right" as const,
      width: "100px",
    },
    {
      key: "bytesSent",
      label: "Sent",
      accessor: (g: GatewayInfo) => g.bytesSent,
      render: (g: GatewayInfo) => fmtBytes(g.bytesSent),
      align: "right" as const,
      width: "120px",
    },
    {
      key: "bytesRecv",
      label: "Recv",
      accessor: (g: GatewayInfo) => g.bytesRecv,
      render: (g: GatewayInfo) => fmtBytes(g.bytesRecv),
      align: "right" as const,
      width: "120px",
    },
  ];
</script>

<main class="p-4">
  <h2 class="text-accent-300 text-[11px] uppercase tracking-wide mb-3">Gateways</h2>
  <div class="bg-[#0d1117] border border-white/10 rounded-lg p-3">
    <DataTable
      rows={gateways}
      {columns}
      initialSortKey="id"
      emptyText="No gateways registered."
    />
  </div>
</main>
```

- [ ] **Step 2: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

```bash
cd .
git add web-admin/src/routes/gateways.svelte
git commit -m "web-admin: Gateways roster route"
```

---

### Task 11: Frontend — `ConfirmDialog` component

**Files:**
- Create: `web-admin/src/components/ConfirmDialog.svelte`

- [ ] **Step 1: Implement**

A minimal modal with a backdrop, headline, body slot, confirm/cancel buttons. Used by `kick` and any future destructive op.

```svelte
<script lang="ts">
  import type { Snippet } from "svelte";
  import { Close } from "$lib/icons";

  type Props = {
    open: boolean;
    title: string;
    confirmLabel?: string;
    cancelLabel?: string;
    danger?: boolean;
    children?: Snippet;
    onConfirm: () => void;
    onCancel: () => void;
  };
  let {
    open,
    title,
    confirmLabel = "Confirm",
    cancelLabel = "Cancel",
    danger = false,
    children,
    onConfirm,
    onCancel,
  }: Props = $props();

  function onKey(e: KeyboardEvent) {
    if (!open) return;
    if (e.key === "Escape") onCancel();
    if (e.key === "Enter") onConfirm();
  }
</script>

<svelte:window onkeydown={onKey} />

{#if open}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
    <div class="w-[420px] bg-[#0d1117] border border-white/10 rounded-lg shadow-2xl">
      <header class="flex items-center justify-between border-b border-white/5 px-4 py-2">
        <h3 class="text-[13px] font-semibold text-slate-100">{title}</h3>
        <button
          type="button"
          class="text-slate-500 hover:text-slate-200"
          aria-label="Close"
          onclick={onCancel}
        >
          <Close class="w-4 h-4" />
        </button>
      </header>
      <div class="px-4 py-3 text-[12px] text-slate-300">
        {#if children}{@render children()}{/if}
      </div>
      <footer class="flex justify-end gap-2 border-t border-white/5 px-4 py-2">
        <button
          type="button"
          class="px-3 py-1 text-[11.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10"
          onclick={onCancel}
        >
          {cancelLabel}
        </button>
        <button
          type="button"
          class="px-3 py-1 text-[11.5px] {danger
            ? 'bg-rose-500 hover:bg-rose-600 text-slate-50'
            : 'bg-accent-400 hover:bg-accent-500 text-slate-950'} font-semibold rounded"
          onclick={onConfirm}
        >
          {confirmLabel}
        </button>
      </footer>
    </div>
  </div>
{/if}
```

- [ ] **Step 2: Commit**

```bash
git add web-admin/src/components/ConfirmDialog.svelte
git commit -m "web-admin: ConfirmDialog component (modal w/ Esc/Enter shortcuts)"
```

---

### Task 12: Frontend — `PlayerOpsModal` component

**Files:**
- Create: `web-admin/src/components/PlayerOpsModal.svelte`

This component encapsulates the three player op forms (kick / tp / tpto). Opened from the players table; closes on success or cancel.

- [ ] **Step 1: Implement**

```svelte
<script lang="ts">
  import { apiPost, ApiError } from "$lib/api";
  import { Loader2 } from "$lib/icons";
  import ConfirmDialog from "./ConfirmDialog.svelte";

  type Op = "kick" | "tp" | "tpto" | null;

  type Props = {
    op: Op;
    username: string;
    onClose: () => void;
    onResult: (ok: boolean, message: string) => void;
  };
  let { op, username, onClose, onResult }: Props = $props();

  let busy = $state(false);
  let error = $state("");

  // tp form state
  let x = $state(0);
  let y = $state(0);

  // tpto form state
  let target = $state("");

  async function invoke(verb: string, args: Record<string, unknown>) {
    if (busy) return;
    busy = true;
    error = "";
    try {
      await apiPost(`/admin/api/commands/${verb}`, args);
      onResult(true, `${verb} ok (${username})`);
      onClose();
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message;
      error = msg;
      onResult(false, msg);
    } finally {
      busy = false;
    }
  }

  function submitTp(e: SubmitEvent) {
    e.preventDefault();
    void invoke("player.tp", { Username: username, X: x, Y: y });
  }
  function submitTpto(e: SubmitEvent) {
    e.preventDefault();
    if (!target.trim()) return;
    void invoke("player.tpto", { Username: username, Target: target.trim() });
  }
  function confirmKick() {
    void invoke("player.kick", { Username: username });
  }
</script>

{#if op === "kick"}
  <ConfirmDialog
    open={true}
    title="Kick {username}"
    confirmLabel={busy ? "Kicking…" : "Kick"}
    danger
    onConfirm={confirmKick}
    onCancel={onClose}
  >
    {#snippet children()}
      <p>Disconnect the player's session. They'll be returned to login.</p>
      {#if error}<p class="mt-2 text-rose-300">{error}</p>{/if}
    {/snippet}
  </ConfirmDialog>
{:else if op === "tp" || op === "tpto"}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
    <div class="w-[420px] bg-[#0d1117] border border-white/10 rounded-lg shadow-2xl">
      <header class="border-b border-white/5 px-4 py-2">
        <h3 class="text-[13px] font-semibold text-slate-100">
          {op === "tp" ? "Teleport" : "Teleport to player"}: {username}
        </h3>
      </header>
      {#if op === "tp"}
        <form onsubmit={submitTp} class="px-4 py-3 space-y-3 text-[12px]">
          <div>
            <label class="block text-[11px] text-slate-500 mb-1" for="tpx">World X</label>
            <input
              id="tpx"
              type="number"
              step="any"
              class="w-full bg-black/40 border border-white/10 rounded px-2 py-1 text-slate-200 focus:outline-none focus:border-accent-300/50"
              bind:value={x}
              required
            />
          </div>
          <div>
            <label class="block text-[11px] text-slate-500 mb-1" for="tpy">World Y</label>
            <input
              id="tpy"
              type="number"
              step="any"
              class="w-full bg-black/40 border border-white/10 rounded px-2 py-1 text-slate-200 focus:outline-none focus:border-accent-300/50"
              bind:value={y}
              required
            />
          </div>
          {#if error}<div class="text-rose-300">{error}</div>{/if}
          <div class="flex justify-end gap-2 pt-1">
            <button type="button" class="px-3 py-1 bg-white/5 border border-white/10 rounded" onclick={onClose}>
              Cancel
            </button>
            <button
              type="submit"
              disabled={busy}
              class="px-3 py-1 bg-accent-400 hover:bg-accent-500 text-slate-950 font-semibold rounded flex items-center gap-1 disabled:opacity-50"
            >
              {#if busy}<Loader2 class="w-3 h-3 animate-spin" />{/if}
              Teleport
            </button>
          </div>
        </form>
      {:else}
        <form onsubmit={submitTpto} class="px-4 py-3 space-y-3 text-[12px]">
          <div>
            <label class="block text-[11px] text-slate-500 mb-1" for="tptarget">Destination player</label>
            <input
              id="tptarget"
              type="text"
              class="w-full bg-black/40 border border-white/10 rounded px-2 py-1 text-slate-200 focus:outline-none focus:border-accent-300/50"
              bind:value={target}
              required
            />
          </div>
          {#if error}<div class="text-rose-300">{error}</div>{/if}
          <div class="flex justify-end gap-2 pt-1">
            <button type="button" class="px-3 py-1 bg-white/5 border border-white/10 rounded" onclick={onClose}>
              Cancel
            </button>
            <button
              type="submit"
              disabled={busy}
              class="px-3 py-1 bg-accent-400 hover:bg-accent-500 text-slate-950 font-semibold rounded flex items-center gap-1 disabled:opacity-50"
            >
              {#if busy}<Loader2 class="w-3 h-3 animate-spin" />{/if}
              Teleport
            </button>
          </div>
        </form>
      {/if}
    </div>
  </div>
{/if}
```

- [ ] **Step 2: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

```bash
cd .
git add web-admin/src/components/PlayerOpsModal.svelte
git commit -m "web-admin: PlayerOpsModal — kick/tp/tpto forms via /admin/api/commands"
```

---

### Task 13: Frontend — `routes/players.svelte`

**Files:**
- Create: `web-admin/src/routes/players.svelte`

- [ ] **Step 1: Implement**

```svelte
<script lang="ts">
  import { onMount } from "svelte";
  import { playersStore } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import type { PlayerInfo } from "$lib/types";
  import DataTable from "../components/DataTable.svelte";
  import PlayerOpsModal from "../components/PlayerOpsModal.svelte";
  import { Search } from "$lib/icons";

  let allPlayers = $derived<PlayerInfo[]>(playersStore.value ?? []);
  let search = $state("");
  let statusFilter = $state<"all" | "online" | "offline">("online");

  let filtered = $derived(() => {
    const q = search.trim().toLowerCase();
    return allPlayers.filter((p) => {
      if (statusFilter !== "all" && p.status !== statusFilter) return false;
      if (q && !p.username.toLowerCase().includes(q)) return false;
      return true;
    });
  });

  type Op = "kick" | "tp" | "tpto" | null;
  let modalOp = $state<Op>(null);
  let modalUser = $state("");
  let toast = $state<{ msg: string; ok: boolean } | null>(null);

  function openOp(op: Op, username: string) {
    modalOp = op;
    modalUser = username;
  }
  function closeOp() {
    modalOp = null;
    modalUser = "";
  }
  function onResult(ok: boolean, msg: string) {
    toast = { ok, msg };
    setTimeout(() => (toast = null), 4000);
  }

  onMount(async () => {
    try {
      const initial = await apiGet<PlayerInfo[]>("/admin/api/players?status=all");
      playersStore.set(initial);
    } catch {
      // Stream takes over.
    }
  });

  $effect(() => {
    const off = stream.subscribe("players", (data) => {
      playersStore.set(data as PlayerInfo[]);
    });
    return off;
  });

  const columns = [
    { key: "username", label: "Username", accessor: (p: PlayerInfo) => p.username, width: "22%" },
    {
      key: "status",
      label: "Status",
      accessor: (p: PlayerInfo) => p.status,
      render: (p: PlayerInfo) =>
        p.status === "online"
          ? "● online"
          : "○ offline",
      width: "100px",
    },
    {
      key: "host",
      label: "Host",
      accessor: (p: PlayerInfo) => p.hostId ?? "",
      render: (p: PlayerInfo) => p.hostId ?? "—",
      width: "20%",
    },
    {
      key: "cell",
      label: "Cell",
      accessor: (p: PlayerInfo) => p.cellId ?? "",
      render: (p: PlayerInfo) => p.cellId ?? "—",
      width: "20%",
    },
    {
      key: "world",
      label: "World",
      render: (p: PlayerInfo) =>
        p.worldX != null && p.worldY != null && (p.worldX !== 0 || p.worldY !== 0)
          ? `(${p.worldX.toFixed(0)}, ${p.worldY.toFixed(0)})`
          : "—",
      width: "120px",
    },
    {
      key: "ops",
      label: "Ops",
      width: "180px",
      // No accessor → not sortable; render only.
      render: (p: PlayerInfo) => p, // see below — actual buttons rendered in a custom cell snippet
    },
  ];
</script>

<main class="p-4 space-y-3">
  <div class="flex items-center justify-between">
    <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Players</h2>
    <div class="flex items-center gap-2 text-[11px]">
      <div class="flex items-center bg-white/5 border border-white/10 rounded">
        <Search class="w-3.5 h-3.5 ml-2 text-slate-500" />
        <input
          type="text"
          placeholder="search…"
          class="bg-transparent px-2 py-1 text-[12px] text-slate-200 placeholder-slate-500 focus:outline-none w-44"
          bind:value={search}
        />
      </div>
      <div class="flex bg-white/5 border border-white/10 rounded overflow-hidden">
        {#each ["online", "all", "offline"] as f (f)}
          <button
            class="px-2 py-0.5 {statusFilter === f ? 'bg-accent-300/20 text-accent-300' : 'text-slate-400 hover:bg-white/5'}"
            onclick={() => (statusFilter = f as typeof statusFilter)}
          >{f}</button>
        {/each}
      </div>
    </div>
  </div>

  <div class="bg-[#0d1117] border border-white/10 rounded-lg p-3">
    <table class="w-full text-[12px] border-collapse">
      <thead>
        <tr class="text-left text-[10.5px] uppercase tracking-wide text-slate-500 border-b border-white/10">
          <th class="py-1.5 px-2 font-medium" style="width:22%">Username</th>
          <th class="py-1.5 px-2 font-medium" style="width:100px">Status</th>
          <th class="py-1.5 px-2 font-medium" style="width:20%">Host</th>
          <th class="py-1.5 px-2 font-medium" style="width:20%">Cell</th>
          <th class="py-1.5 px-2 font-medium" style="width:120px">World</th>
          <th class="py-1.5 px-2 font-medium" style="width:200px">Ops</th>
        </tr>
      </thead>
      <tbody>
        {#each filtered() as p (p.username)}
          <tr class="border-b border-white/5 hover:bg-white/5">
            <td class="py-1.5 px-2 font-mono">{p.username}</td>
            <td class="py-1.5 px-2 {p.status === 'online' ? 'text-emerald-300' : 'text-slate-500'}">
              {p.status === "online" ? "● online" : "○ offline"}
            </td>
            <td class="py-1.5 px-2">{p.hostId ?? "—"}</td>
            <td class="py-1.5 px-2 font-mono">{p.cellId ?? "—"}</td>
            <td class="py-1.5 px-2">
              {p.worldX != null && p.worldY != null && (p.worldX !== 0 || p.worldY !== 0)
                ? `(${p.worldX.toFixed(0)}, ${p.worldY.toFixed(0)})`
                : "—"}
            </td>
            <td class="py-1.5 px-2">
              <div class="flex gap-1.5">
                <button
                  class="px-2 py-0.5 text-[10.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
                  onclick={() => openOp("tp", p.username)}
                  disabled={p.status !== "online"}
                  title={p.status === "online" ? "Teleport" : "Player offline"}
                >
                  tp
                </button>
                <button
                  class="px-2 py-0.5 text-[10.5px] bg-white/5 border border-white/10 rounded hover:bg-white/10 disabled:opacity-50"
                  onclick={() => openOp("tpto", p.username)}
                  disabled={p.status !== "online"}
                >
                  tpto
                </button>
                <button
                  class="px-2 py-0.5 text-[10.5px] bg-rose-500/15 border border-rose-500/30 text-rose-200 rounded hover:bg-rose-500/25 disabled:opacity-50"
                  onclick={() => openOp("kick", p.username)}
                  disabled={p.status !== "online"}
                >
                  kick
                </button>
              </div>
            </td>
          </tr>
        {:else}
          <tr><td colspan="6" class="py-4 text-center text-slate-500">No players match.</td></tr>
        {/each}
      </tbody>
    </table>
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

  <PlayerOpsModal
    op={modalOp}
    username={modalUser}
    onClose={closeOp}
    onResult={onResult}
  />
</main>
```

NOTE: The Players page uses an inline table rather than `DataTable` because the per-row Ops column needs Svelte's `<button onclick={...}>` syntax in the cell — `DataTable`'s `render` prop accepts a value, not arbitrary markup, in this plan's design. If you want, you can extend `DataTable` to accept a `cellSnippet` snippet — out of scope for this task.

The `columns` array in the script is unused now (kept it earlier for symmetry); delete it before committing:

```svelte
// remove the `const columns = [...]` block from the script — the inline
// table renders directly without DataTable.
```

- [ ] **Step 2: Typecheck**

```bash
cd web-admin && bun run typecheck
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
cd .
git add web-admin/src/routes/players.svelte
git commit -m "web-admin: Players route — search/filter + per-row tp/tpto/kick ops"
```

---

### Task 14: Frontend — wire route dispatch in `app.svelte`

**Files:**
- Modify: `web-admin/src/app.svelte`

- [ ] **Step 1: Replace the route switch**

In `app.svelte`, find:

```svelte
{#if path === "/cluster"}
  <Cluster />
{:else}
  <div class="p-8 text-slate-500">
    Panel <code>{path}</code> — not yet implemented.
  </div>
{/if}
```

Replace with:

```svelte
{#if path === "/cluster"}
  <Cluster />
{:else if path === "/hosts"}
  <Hosts />
{:else if path === "/gateways"}
  <Gateways />
{:else if path === "/players"}
  <Players />
{:else}
  <div class="p-8 text-slate-500">
    Panel <code>{path}</code> — not yet implemented.
  </div>
{/if}
```

And add the imports near the top of the script section:

```ts
import Hosts from "./routes/hosts.svelte";
import Gateways from "./routes/gateways.svelte";
import Players from "./routes/players.svelte";
```

- [ ] **Step 2: Typecheck + commit**

```bash
cd web-admin && bun run typecheck
```

```bash
cd .
git add web-admin/src/app.svelte
git commit -m "web-admin: route /hosts /gateways /players in the app shell"
```

---

### Task 15: e2e smoke (manual)

**Files:**
- (No new files; verification step.)

- [ ] **Step 1: Build everything**

```bash
cd web-admin && bun run build
cd . && go build -o /tmp/4node-test ./examples/4node-basic/
```

- [ ] **Step 2: Run with Postgres**

```bash
just db-up   # if not running
/tmp/4node-test --admin-listen=:9101 --postgres-url='postgres://mmo:mmo@localhost:5432/mmo_4node?sslmode=disable'
```

- [ ] **Step 3: Open the dashboard**

Browse to `http://localhost:9101/admin/`, log in `josh` / `localdev`. Click each section:

- **Hosts** — should show 1 row with the in-process host (state="live*" or similar), 4 cells, total entities matching the cluster page, current load.
- **Gateways** — should show 1 row (in-proc / inproc), sessions = 0 until a player connects.
- **Players** — empty until you connect a player. Open the game tab, sign in to the game, return to Players → 1 row with status=online, host, cell, world coords (if PlayerLocation tracks them, otherwise `—`).

- [ ] **Step 4: Test each player op**

With at least one online player:

- Click **tp** → modal opens → enter `(500, 500)` → submit → toast `player.tp ok (...)`. Game client should teleport.
- Click **tpto** → enter another player's username (or an invalid one) → submit. If invalid, error message in modal.
- Click **kick** → confirm dialog → confirm → game client disconnects.

- [ ] **Step 5: Stress sort + filter**

- Click each column header on Hosts/Gateways → sort toggles asc/desc with chevron indicator.
- Search box on Players narrows to matching usernames.
- Status filter switches between online/all/offline. (offline returns nothing in v1 — that's expected; the dropdown is the future-features hint.)

- [ ] **Step 6: No commit**

This task is verification only. If you find bugs, file them as new tasks.

---

### Task 16: CLAUDE.md update

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Append a paragraph after the existing admin dashboard description**

Find the existing block that starts with `**Admin dashboard**` (added by the foundation plan) and the follow-up paragraph (added by the frontend plan). Append after them:

```markdown

The Hosts, Gateways, and Players routes (`/hosts`, `/gateways`, `/players`) consume `Process.HostListEntries` / `GatewayListEntries` / `ActivePlayerSnapshots` — typed accessors that aggregate registry + metrics state. Live updates flow on the `hosts` / `gateways` / `players` SSE topics (one shared 1Hz ticker). Player operations (tp / tpto / kick) POST to `/admin/api/commands/<verb>`, dispatched through the existing cmdsys with the operator's grants. Offline player listing is a placeholder until `PlayerRepository` exposes a search API.
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "CLAUDE.md: document Hosts/Gateways/Players panels"
```

---

## Self-review checklist

- [ ] **Spec coverage:** §8 panels #6 (Hosts) → Task 9, #8 (Gateways) → Task 10, #11 (Players list) → Task 13, #12 (Player ops) → Tasks 12-13. Per-host / per-player detail routes (#7, #9, #13) explicitly deferred — flagged in the orientation section.
- [ ] **Placeholder scan:** All `// TODO` / `// implement later` removed. The Players route's "offline" filter is intentionally a UI placeholder with backend-side comment; that's documented behavior, not a missing implementation.
- [ ] **Type consistency:** `HostInfo`/`GatewayInfo`/`PlayerInfo` shapes match between Go DTOs (`pkg/admin/view.go`), backend mapping (`view_local.go`), TS types (`web-admin/src/lib/types.ts`), and route consumers (Tasks 9, 10, 13). `HostListEntry` / `GatewayListEntry` are universe-side types; admin DTOs convert them — separation is intentional (universe types may grow more fields without breaking the wire).
- [ ] **Backend changes are additive:** No existing `pkg/universe` API was renamed or removed. New methods + new types only.
- [ ] **No new dependencies:** All work uses Go stdlib + existing `pkg/admin`/`pkg/cmdsys`/`pkg/services/auth`, plus existing Svelte/Vite/Tailwind on the frontend.

---

## Execution

Plan complete. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task, review between tasks.
2. **Inline Execution** — execute tasks in this session with checkpoints.

Which approach?
