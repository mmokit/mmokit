# Admin / Observability Dashboard — Design

**Status:** Draft for review
**Author:** Josh Stout (with Claude)
**Date:** 2026-05-10
**Related memories:** `feedback_security_best_practices`, `feedback_no_backward_compat`, `feedback_refactor_over_stopgaps`, `feedback_mmokit_facade_only`, `feedback_logging`, `feedback_package_manager`, `project_opensource_ready`
**Related specs:** [2026-05-08-services-event-bus-design.md](2026-05-08-services-event-bus-design.md), [2026-05-02-per-command-help-design.md](2026-05-02-per-command-help-design.md)

## 1. Summary

mmokit's coordinator already exposes a powerful but bare-bones operational surface: `/metrics` (Prometheus text), `/commands` (cmdsys schema introspection), `/events` (commit-log JSON). Operators today drive the system through the interactive console (cell split/merge, host drain, player kick, perf snapshots). What's missing is a **visual, browser-based control plane** that turns that surface into a live dashboard — one that shows cluster topology, per-cell health, host load, and player activity in real time, and exposes every cmdsys verb as a typed form-driven action.

This design adds **`pkg/admin/`** — a new mmokit-shipped package that mounts a Svelte SPA + JSON/SSE API onto the coordinator's existing `AdminListen` mux. It is engine-shipped (not game-specific), session-authenticated, and architected so the entire dashboard can lift out of the coordinator into a standalone `--mode=admin` role in a future phase without any frontend changes.

## 2. Goals & non-goals

### Goals

- **Engine-shipped, game-agnostic.** Every mmokit deployment gets the dashboard for free by setting `Config.Admin.Enabled = true`. No game code required.
- **Built on existing surface.** Reuses `pkg/metrics`, `pkg/cmdsys`, `pkg/universe.Process`, and the commit log. No parallel telemetry or new authoritative state.
- **Visual cell topology as the hero.** Live grid map of cells, color-coded by load (or host or entity density), quadtree-aware (splits render as nested squares), with click-through drill-down.
- **All cmdsys verbs as actions.** Every registered command is invocable from the UI with a form auto-built from its JSON Schema. Cell split/merge/migrate, player tp/kick, perf snapshot, config get/set — all reachable from a `⌘K` palette and from contextual menus in panels.
- **Online + offline player operations.** Player list combines connected sessions with persisted records, supports search/filter, and exposes engine-defined verbs plus any game-defined ones uniformly.
- **Live updates via SSE.** Per-topic streams (`cells`, `hosts`, `players`, `events`, `alerts`) over a single multiplexed connection. Client demuxes by event name; one connection regardless of panel count.
- **Cookie-session auth from day 1.** HttpOnly + Secure + SameSite=Strict cookies, argon2id password hashes, rate-limited login with lockout, audit log for all command invocations. Maps to the existing `cmdsys.Caller` + `Grants` model.
- **Game extensibility via reflection.** Games register `PanelDef` records that declare topics they subscribe to and commands they expose; the SPA renders any registered panel without game-specific JavaScript.
- **Approach-2 ready.** All cluster reads go through a `ClusterView` interface and all live updates go through a `TopicBus`. Both interfaces have one in-process implementation today and accept a remote (MeshControl) implementation tomorrow with no API surface change above them.
- **Phased delivery.** Single design covers the full vision (~30 features); v1 implementation ships 14 panels (the MVP set); v2/v3 panels get their own implementation plans without architectural rework.

### Non-goals (v1)

- **Custom JS plugins from games.** Games can register `PanelDef` metadata (rendered by a generic table/chart/form host); they cannot ship arbitrary Svelte components into the dashboard. Plugin loader is v2.
- **TOTP / MFA.** Cookie sessions only. MFA flagged for v2.
- **SSO / OAuth.** Operators are static config entries with argon2id hashes. No external IdP integration.
- **Password reset flow.** Operator config is edited and the coordinator is restarted.
- **Standalone `--mode=admin` role.** v1 runs embedded in the coordinator. The architecture supports lifting out, but the actual remote impl + new role + MeshControl streams are deferred.
- **Multi-cluster federation.** One dashboard per coordinator.
- **Configurable / saveable widget grids (Grafana-style).** Each panel has a fixed layout per route. Custom dashboards are deferred indefinitely.
- **Light theme.** Dark theme only; admin tooling is internal.
- **History / time-series storage.** Live values + bounded ring buffers (commit log). Long-term metrics belong in Prometheus, which the existing `/metrics` endpoint already feeds.

## 3. Architecture

### 3.1 High-level shape

```text
┌─────────────────────────────────────────────────────────────────────────┐
│  coordinator process (mode includes "coordinator")                      │
│                                                                          │
│   ┌──────────── pkg/admin (NEW) ────────────┐                            │
│   │                                          │                           │
│   │  HTTP mux  /admin/*                      │      ┌─ pkg/cmdsys ──┐    │
│   │   ├─ static SPA (embed.FS)               │      │  Dispatcher  │    │
│   │   ├─ /api/cluster   ──────┐              │      │  Registry    │    │
│   │   ├─ /api/stream?topics=  │              │      └──────────────┘    │
│   │   ├─ /api/commands/<verb> │              │              ▲           │
│   │   ├─ /api/auth/*          │              │              │           │
│   │                           ▼              │              │           │
│   │                     ┌──────────────┐     │              │           │
│   │   panel registry──▶ │ ClusterView  │ ◀── pluggable      │           │
│   │   session store     └──────────────┘     │              │           │
│   │                           │              │              │           │
│   │                           ▼              │              │           │
│   │                     ┌──────────────┐     │              │           │
│   │                     │  TopicBus    │     │              │           │
│   │                     │ (SSE today)  │     │              │           │
│   │                     └──────────────┘     │              │           │
│   └────────────────────────│─────────────────┘              │           │
│              ┌─────────────┘                                │           │
│              ▼                                              │           │
│   ┌──────────────────────────────────────┐                  │           │
│   │ LocalClusterView (impl)              │──────────────────┘           │
│   │   reads Process, commitLog, cells,   │                              │
│   │   metrics, sessions in-memory        │                              │
│   └──────────────────────────────────────┘                              │
└─────────────────────────────────────────────────────────────────────────┘
                                   ▲
                                   │  Phase 2: swap LocalClusterView for
                                   │  RemoteClusterView backed by a new
                                   │  MeshControl AdminStream RPC
```

### 3.2 Key abstractions

**`ClusterView` interface** is the only way `pkg/admin` reads cluster state. It exposes typed snapshots: `Cells() []CellInfo`, `Hosts() []HostInfo`, `Gateways() []GatewayInfo`, `Players(filter PlayerFilter) []PlayerInfo`, `Player(username string) (PlayerInfo, error)`, `CommitLog(query CommitQuery) []CommitEvent`, `Perf(cellID string) TickStats`. Methods return typed errors (`view.ErrCellNotFound`, `view.ErrUnavailable`) so the remote impl can map gRPC errors to the same set without changes upstream.

`LocalClusterView` is the v1 implementation — reads `*universe.Process` directly. `RemoteClusterView` is a future v2 implementation that calls a new `MeshControl.AdminQuery` RPC. The dashboard handlers, store, and panel registry never branch on which is in use.

**`TopicBus`** is the transport-neutral pub/sub for live updates. Publishers (the metrics ticker, the commit-log appender, the session presence hook) call `bus.Publish(topic string, payload any)`. Subscribers register a `Subscriber` interface. The SSE writer is one concrete subscriber; in a v2 admin-mode process, a MeshControl streamer becomes the subscriber on the coordinator side and re-publishes locally inside the admin process. All wire choices live below `TopicBus`.

**Dispatcher reuse — already approach-2-ready.** `cmdsys.Dispatcher.Invoke()` already routes by `RouteKind` and supports remote routing over MeshControl. The `/admin/api/commands/:verb` HTTP handler builds a `cmdsys.Caller` from the session and calls `Dispatcher.Invoke` — nothing changes between phases except a new `Caller.Source` variant (`SourceAdminHTTP`).

**`PanelRegistry`** holds `PanelDef` records. The SPA fetches `/admin/api/panels` once at boot and renders the sidebar from this metadata. Games register additional panels via `mmokit.RegisterAdminPanel(coord, PanelDef{...})`. v1 builtins (Cluster, Hosts, Gateways, Players, Performance, Events, Logs, Settings) are registered by `pkg/admin` at startup.

**Session middleware** wraps every `/admin/api/*` route. It validates the session cookie, resolves it to a `cmdsys.Caller` (with the operator's grants), and attaches the caller to the request context. Logout clears the cookie + audits the action.

### 3.3 Data flow examples

- **Live cell map.** Cell metrics → `Coordinator.snapshotMetrics()` (every 250ms) → `bus.Publish("cells", snap)` → SSE subscribers fan out → Svelte `cellsStore` updates → `CellMap.svelte` redraws (throttled to 4 Hz).
- **Cell split via UI.** Click → `POST /admin/api/commands/cell.split` `{CellID:"0_0"}` → session middleware → `dispatcher.Invoke(caller, "cell.split", args)` → existing transfer protocol → audit log → response. Topology change fires `bus.Publish("topology", …)` for everyone watching.
- **Player kick.** Same flow; cmdsys already routes `player.kick` to the player's owner host (`RoutePlayerOwner`).

## 4. Backend layout (`pkg/admin/`)

```text
pkg/admin/
├── admin.go              # Server struct, Mount(mux), config
├── view.go               # ClusterView interface + types (CellInfo, HostInfo, PlayerInfo, …)
├── view_local.go         # LocalClusterView — in-process impl reading from *universe.Process
├── view_local_test.go
├── topicbus.go           # TopicBus, Subscriber interface
├── topicbus_test.go
├── publishers.go         # tick-rate publisher, commit-log publisher, session presence publisher
├── sse.go                # SSE Subscriber implementation (single multiplexed connection)
├── api_cluster.go        # GET /api/cluster (one-shot snapshot)
├── api_stream.go         # GET /api/stream/<topic>
├── api_commands.go       # POST /api/commands/<verb>, GET /api/commands (catalogue)
├── api_panels.go         # GET /api/panels
├── api_auth.go           # POST /api/auth/login, /logout, GET /api/auth/session
├── auth.go               # SessionStore interface, cookie helpers
├── auth_memory.go        # in-memory SessionStore (default)
├── auth_postgres.go      # Postgres-backed SessionStore (uses existing pgx pool)
├── auth_test.go
├── lockout.go            # rate-limit / lockout per username + IP
├── panel.go              # PanelDef, PanelRegistry, builtin panel registrations
├── rbac.go               # operator identities, grant resolution from config
├── audit.go              # admin-action audit log adapter
├── admin_e2e_test.go
└── static/               # embed.FS pointing at the built Svelte SPA
    └── dist.go           # //go:embed dist
```

### 4.1 Wiring into the coordinator

`pkg/universe/bootstrap.go` adds (gated on `Config.Admin.Enabled`, after `startAdminHTTPListener` builds the mux but before the server is started):

```go
if c.cfg.Admin.Enabled {
    srv := admin.NewServer(admin.Config{
        View:          admin.NewLocalClusterView(c),
        Registry:      c.registry,
        Dispatcher:    c.dispatcher,
        SessionStore:  admin.NewMemorySessionStore(),  // or PostgresSessionStore
        Operators:     c.cfg.Admin.Operators,
        PanelRegistry: c.panelRegistry,
        Logger:        c.Log,
        Commit:        c.commitLog,
    })
    srv.Mount(mux)  // registers /admin/* routes
}
```

`Config.Admin` is a new struct on `universe.Config`:

```go
type AdminConfig struct {
    Enabled            bool
    SessionStore       string         // "memory" (default) or "postgres"
    SessionTTL         time.Duration  // default 8h
    LockoutMaxAttempts int            // default 5
    LockoutWindow      time.Duration  // default 15m
    Operators          []OperatorConfig
}

type OperatorConfig struct {
    Username     string
    PasswordHash string   // argon2id encoded ($argon2id$v=19$…)
    Grants       []string // cmdsys grant patterns, e.g. ["cell.*", "player.*"]
}
```

### 4.2 Game-side public API on `mmokit`

```go
mmokit.RegisterAdminPanel(coord, mmokit.PanelDef{
    ID:           "marketplace",
    Label:        "Marketplace",
    Icon:         "shopping-cart",
    Group:        "Game",
    Topics:       []string{"marketplace.orders"},
    Commands:     []string{"market.cancel", "market.refund"},
    InitialFetch: "/admin/api/game/marketplace/snapshot",
})

// Optional custom HTTP handler under /admin/api/game/<your-prefix>/*
mmokit.RegisterAdminAPI(coord, "/marketplace", marketplaceHandler)
```

This is the entire game extensibility surface. The Svelte SPA reads `/api/panels` at boot, slots new panels into the sidebar under their declared `Group`, mounts the generic panel renderer (`PanelHost.svelte`) which subscribes to declared topics and exposes declared commands. Games requiring richer rendering than table/chart/form fall back to the generic renderer in v1; custom Svelte components are a v2 feature.

### 4.3 Approach-2 swap point

When `--mode=admin` becomes a goal, only `view_local.go` and `publishers.go` get remote siblings. `view_remote.go` calls a new `MeshControl.AdminQuery` RPC; `publishers_remote.go` consumes a new `MeshControl.AdminStream` bidi stream. Everything above the `ClusterView` and `TopicBus` interfaces — the Svelte frontend, all `/admin/api/*` handlers, panel registry, and session/auth — runs identically in admin processes. The new `--mode=admin` role registers with the coordinator over MeshControl, receives `PeerList` broadcasts, and proxies commands via the existing remote dispatcher path.

## 5. Frontend layout (`web-admin/`)

```text
web-admin/
├── package.json          # Svelte 5, Vite, TypeScript, Bun
├── vite.config.ts        # outputs to dist/, embedded by Go
├── tsconfig.json
├── index.html
├── src/
│   ├── main.ts           # mount, router init
│   ├── app.svelte        # shell: sidebar + topbar + workspace slot
│   ├── lib/
│   │   ├── api.ts        # typed fetch wrappers
│   │   ├── stream.ts     # SSE client: subscribe(topic, handler), auto-reconnect
│   │   ├── auth.ts       # session check, login, logout
│   │   ├── stores.ts     # Svelte stores per topic
│   │   ├── cmdsys.ts     # /commands schema → form builder
│   │   ├── format.ts     # bytes / duration / load formatters
│   │   ├── theme.ts      # dark theme tokens (CSS vars)
│   │   ├── icons.ts      # SVG icon set (lucide subset)
│   │   └── types.ts      # mirror of Go API types (hand-maintained for v1)
│   ├── routes/
│   │   ├── login.svelte
│   │   ├── cluster.svelte
│   │   ├── hosts.svelte
│   │   ├── gateways.svelte
│   │   ├── players.svelte
│   │   ├── performance.svelte
│   │   ├── events.svelte
│   │   ├── logs.svelte
│   │   └── settings.svelte
│   └── components/
│       ├── Sidebar.svelte
│       ├── TopBar.svelte
│       ├── CellMap.svelte         # canvas-rendered, quadtree-aware
│       ├── CellDrawer.svelte      # right-side drilldown
│       ├── HostRoster.svelte
│       ├── PlayerTable.svelte
│       ├── Sparkline.svelte
│       ├── PerfBars.svelte
│       ├── CommandPalette.svelte  # ⌘K
│       ├── CommandForm.svelte     # JSON Schema → form
│       ├── AlertBanner.svelte
│       ├── EventStream.svelte
│       └── PanelHost.svelte       # generic renderer for game-registered PanelDef
└── dist/                          # build output, embedded into pkg/admin/static
```

### 5.1 Build pipeline

`bun install` once; `bun run build` produces `web-admin/dist/`, which `pkg/admin/static/dist.go` embeds via `//go:embed dist`. `just admin-dev` runs `vite dev` with a proxy to `:9101` for `/api/*` and `/auth/*` so HMR works against a live coordinator. `just admin-build` runs `bun run build` and is wired into `just build` so the embedded SPA is always fresh in the binary. Vite output is content-hashed; `pkg/admin` serves `index.html` with `Cache-Control: no-store` and assets with long-cache headers.

### 5.2 Reactivity model

Svelte 5 runes (`$state`, `$derived`). One global SSE connection multiplexed by topic; `lib/stream.ts` exposes `subscribe(topic, handler)` and dedupes — if `cluster.svelte` and the alert banner both subscribe to `topology`, one SSE stream feeds both. Per-topic stores: `cellsStore`, `hostsStore`, `playersStore`, `eventsStore`, `alertsStore`. Components consume stores directly; no manual diff'ing.

### 5.3 Cell map rendering (the hero)

`CellMap.svelte` is **canvas-rendered**, not DOM, for performance — a 4×4 grid at depth 3 has 256 sub-cells per quadrant. Layout: each depth-0 cell is a square; split children render as nested 2×2 inside the parent (recursive). Color modes via toggle:

- **Load** (default): green → yellow → red gradient on `cell.load`
- **Host**: categorical hue per host with a legend in the corner
- **Entity density**: blue → white on `cell.entities.real`

Hover → tooltip with `cellID / entities / players / load`. Click → opens `CellDrawer` with full details and action buttons (`split`, `merge`, `migrate`). Re-renders are throttled to 4 Hz; the underlying snapshot updates continuously.

### 5.4 Generic panel rendering for game extensibility

`PanelHost.svelte` takes a `PanelDef` and renders without game-specific code:

- Subscribes to declared `Topics` and renders payloads as a table (default) or chart (if `Visualization == "chart"`)
- Wires declared `Commands` as toolbar buttons; clicking opens `CommandForm` auto-built from the verb's JSON Schema
- An optional `InitialFetch` URL is fetched at mount and shown above the live data
- Games requiring richer rendering can ship a custom Svelte component name in `PanelDef.Component` — but for v1 we don't load custom JS; the panel falls back to the generic renderer if the named component isn't built into the SPA

## 6. Wire API

### 6.1 HTTP routes (all under `/admin/`)

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| GET | `/admin/` | none | serves `index.html` from embedded SPA |
| GET | `/admin/assets/*` | none | static SPA assets |
| POST | `/admin/api/auth/login` | none + lockout | `{username, password}` → sets HttpOnly cookie, returns `{user, grants}` |
| POST | `/admin/api/auth/logout` | session | clears cookie + invalidates session row |
| GET | `/admin/api/auth/session` | session | returns current `{user, grants, expiresAt}` (or 401) |
| GET | `/admin/api/cluster` | session | one-shot snapshot: hosts, gateways, cells, sessions, recent events |
| GET | `/admin/api/cells` | session | full cell list with details |
| GET | `/admin/api/cells/:id` | session | single cell drill-down |
| GET | `/admin/api/hosts` | session | host roster |
| GET | `/admin/api/gateways` | session | gateway roster |
| GET | `/admin/api/players?filter=...` | session | online + offline (paginated) |
| GET | `/admin/api/players/:username` | session | single player drill-down |
| GET | `/admin/api/events?n=&since=&cell=&commit=` | session | commit-log query (existing `/events` shape, wrapped under `/admin/api`) |
| GET | `/admin/api/perf/:cellID` | session | per-system tick stats |
| GET | `/admin/api/commands` | session | full cmdsys catalogue |
| GET | `/admin/api/commands/:verb` | session | one command schema |
| POST | `/admin/api/commands/:verb` | session + RBAC | invoke a cmdsys verb, body = JSON args |
| GET | `/admin/api/panels` | session | registered panel metadata |
| GET | `/admin/api/stream?topics=a,b,c` | session | SSE multiplexed stream |
| GET | `/admin/api/audit?n=&since=` | session + grant `admin.audit` | admin action audit log |

The existing `/metrics`, `/commands`, `/events` endpoints stay where they are (Prometheus and tooling consumers), unauthenticated, on the same AdminListen mux. The dashboard's `/admin/api/*` are session-gated separately.

### 6.2 SSE topics

```text
topology      — cell ownership / quadtree depth changes (publish on commit)
cells         — per-cell metric snapshots, batched (publish at 4Hz)
hosts         — host load / heartbeat (publish at 1Hz)
sessions      — gateway session add/remove (publish on event)
players       — player online/offline / cell change (publish on event)
events        — commit-log appends (publish on every CommitEvent)
alerts        — invariant violations + custom thresholds (publish on event)
log:<cat>     — log lines for a category (publish on every log call, opt-in)
```

Each SSE message is `event: <topic>\ndata: <json>\n\n`. The client uses `EventSource` and demultiplexes by `event:`. A single `/admin/api/stream` connection subscribes to many topics via `?topics=cells,hosts,events`; the server only sends events for subscribed topics. One connection per dashboard regardless of panel count.

### 6.3 Command invocation contract

```http
POST /admin/api/commands/cell.split
Cookie: admin_session=...
Content-Type: application/json

{ "CellID": "0_0", "Bypass": false }
```

Response (success):

```json
{
  "ok": true,
  "result": { "ChildIDs": ["0_0:1", "0_0:2", "0_0:3", "0_0:4"] },
  "traceId": "abc123"
}
```

Errors:

| Status | Cause |
| --- | --- |
| 401 | no session or expired |
| 403 | RBAC denied (no grant matched) |
| 400 | schema validation failed (with field-level errors) |
| 409 | cmdsys returned an error (e.g. cell already split, cooldown active) |
| 500 | handler panic / dispatcher failure |

### 6.4 Snapshot payload shapes

```typescript
type CellInfo = {
  id: string;            // "0_0", "0_0:1", etc
  depth: number;
  parent?: string;
  hostId: string;
  load: number;
  tickP99Us: number;
  entities: { real: number; replica: number; ghost: number; connected: number };
  bytes: { sentPerSec: number; recvPerSec: number };
  neighbors: string[];
};

type HostInfo = {
  id: string;
  roles: string[];
  state: "live" | "draining" | "dead";
  isLocal: boolean;
  heartbeatAgeMs: number;
  cells: string[];
  load: number;
  totalEntities: number;
};

type PlayerInfo = {
  username: string;
  status: "online" | "offline";
  hostId?: string;
  cellId?: string;
  worldX?: number;
  worldY?: number;
  lastLogin?: string;
};

type CommitEvent = {
  commitId: string;
  scenario: "Split" | "Merge" | "Migrate";
  step: string;
  stepIndex: number;
  success: boolean;
  durationMs: number;
  affected: string[];
  hostIds: string[];
  error?: string;
  timestamp: string;
};
```

Frontend types live in `web-admin/src/lib/types.ts`, hand-maintained for v1 (~6 types, low churn). If drift becomes a problem we'll add a small codegen step that mirrors the Go types.

## 7. Auth + session model

### 7.1 Login flow

1. `POST /admin/api/auth/login` with `{username, password}` (TLS-only in prod; loopback OK for dev with `Secure` flag relaxed)
2. Lockout middleware checks `username` and remote IP against the in-memory rate-limiter (`pkg/admin/lockout.go`): up to `Config.Admin.Lockout.MaxAttempts` (default 5) failed attempts per `Config.Admin.Lockout.Window` (default 15m); on exceed, returns 429 with `Retry-After`
3. Operator config is consulted: `Operators[username]` → `{passwordHash, grants[]}`. Hash is **argon2id** (`golang.org/x/crypto/argon2`) with per-operator random salt encoded into the hash string
4. On match, `SessionStore.Create(SessionRecord{user, grants, ip, ua, expiresAt})` returns a 256-bit cryptographically-random session ID
5. Server sets cookie: `admin_session=<sid>; Path=/admin; HttpOnly; Secure; SameSite=Strict; Max-Age=<TTL>`
6. Response body: `{user, grants, expiresAt}`

### 7.2 Session storage

```go
type SessionStore interface {
    Create(rec SessionRecord) (sid string, err error)
    Lookup(sid string) (SessionRecord, bool)
    Touch(sid string) error      // bump LastSeen
    Delete(sid string) error
    DeleteExpired() error          // periodic sweep
}
```

Two implementations in v1:

- **`MemorySessionStore`** — `map[sid]SessionRecord` + RWMutex, sweep goroutine. Default. Sessions die with the process (acceptable for dev and small ops).
- **`PostgresSessionStore`** — table `admin_session(sid TEXT PK, username, grants JSONB, ip, ua, created_at, expires_at, last_seen_at)` + index on `expires_at`. Survives coordinator restarts. Migration goes in `pkg/persist/postgres/migrations/`.

Choice via `Config.Admin.SessionStore`.

### 7.3 Cookie security

- `HttpOnly` (no JS access) + `Secure` (HTTPS only — relaxed when bind addr is `127.0.0.1` or `localhost` for dev) + `SameSite=Strict`
- Cookie value is an opaque random `sid`; all session data is server-side. No JWT, no AEAD-encoded claims — keeps revocation O(1).
- Session TTL default 8 hours, sliding via `Touch` on every authenticated request (rate-limited to one Touch per minute per session to avoid pgx churn).

### 7.4 Operator config

```yaml
admin:
  enabled: true
  listen: ":9101"
  session_store: postgres
  session_ttl: 8h
  lockout:
    max_attempts: 5
    window: 15m
  operators:
    - username: josh
      password_hash: "$argon2id$v=19$m=64MB,t=3,p=1$..."
      grants: ["*.*"]
    - username: oncall
      password_hash: "$argon2id$..."
      grants: ["cell.list", "cell.info", "host.list", "player.list", "player.info", "commit.log"]
```

A `mmokit admin hash-password` CLI subcommand generates argon2id hashes for the config file (no plaintext in config; interactive entry prompts then prints the hash).

### 7.5 From session to `cmdsys.Caller`

The session middleware constructs:

```go
caller := cmdsys.Caller{
    ID:     sess.Username,
    Source: cmdsys.SourceAdminHTTP,    // new variant on CallerSource
    Grants: parseGrants(sess.Grants),
}
ctx := cmdsys.WithCaller(r.Context(), caller)
```

This drops into the existing cmdsys RBAC check on every command invocation through `Dispatcher.Invoke`. No new authz code in `pkg/admin` — we reuse what already gates the console.

### 7.6 Audit

Every command invocation through `/admin/api/commands/:verb` writes to `admin_audit(trace_id, username, ip, verb, args_json, ok, error, started_at, finished_at)`. Login, logout, and failed-auth events also write. `GET /admin/api/audit` exposes the table to operators with grant `admin.audit`. Audit uses Postgres if available; otherwise a bounded in-memory ring (default 4096 entries).

### 7.7 Explicitly out of scope for v1

- Password reset flow — operator config edit + restart for now
- TOTP / MFA — flagged as `Config.Admin.MFA` for v2
- Per-grant capability discovery in the UI — the sidebar shows all panels; clicking a denied action returns a clean 403 toast. UI hiding is a v2 polish item once a grant catalogue export from cmdsys is stable.
- SSO/OAuth — explicit non-goal

## 8. Phase 1 panels (the 14 MVP panels)

| # | Panel | Route | Topics | Commands surfaced | Notes |
| --- | --- | --- | --- | --- | --- |
| 1 | Cell map (hero) | `/cluster` | `cells`, `topology` | `cell.split`, `cell.merge`, `cell.migrate` | Canvas-rendered, quadtree-aware, color-mode toggle |
| 2 | Cell drawer | `/cluster` (drawer) | `cells`, `events` | same as #1 | Slides in from right when a cell is clicked |
| 4 | Host ownership view | `/cluster` (mode) | `topology`, `hosts` | — | Same map widget, "color by host" mode + legend |
| 6 | Host roster | `/hosts` | `hosts` | `host.info` | Table: id, roles, state, hb-age, cells owned, load |
| 8 | Gateway roster | `/gateways` | `sessions` | `gateway.info`, `session.list` | Table: id, sessions, bytes/sec, mode |
| 10 | Cluster ops panel | `/cluster` (header) | — | `cell.split/merge/migrate`, `host.drain` | Quick actions with confirm modal |
| 11 | Player list | `/players` | `players` | `player.list`, `player.info` | Server-side filter (online/offline/all), pagination, search |
| 12 | Player ops | `/players` (toolbar) | `players` | `player.tp`, `player.tpto`, `player.kick`, + game verbs | Per-row menu; `CommandForm` from schema |
| 16 | Live metric charts | `/performance` | `cells`, `hosts` | — | Sparklines: tick µs, load EWMA, entity count, bytes/sec |
| 17 | Per-system tick profile | `/performance` (drilldown) | (on-demand `/perf/:cellID`) | `perf.snapshot` | Bar chart per system from `TickStats` |
| 20 | Commit-log tail | `/events` | `events` | — | Live event stream, filter by scenario/cell/step |
| 21 | Invariant alert banner | global | `alerts` | — | Persists in topbar across all routes until acknowledged |
| 25 | Command palette | global (`⌘K`) | — | any cmdsys verb | Fuzzy-search `/api/commands`, schema-derived form |
| 28 | Custom panel API | per-panel | game-registered | game-registered | `PanelHost.svelte` renders any registered `PanelDef` |

For each panel the implementation plan will record route, primary store, refresh cadence, schema dependencies, and RBAC capability.

## 9. Phase 2+ roadmap (deferred)

Each phase-2 feature has its own implementation plan; the architecture supports it without rework:

- **Quadtree depth visualizer (#3)** — extension of `CellMap`, no new endpoints
- **Topology timeline / replay (#5)** — new `/admin/api/topology/snapshots` endpoint backed by a ring buffer
- **Per-host / per-gateway / per-player detail pages (#7, #9, #13)** — new routes, existing endpoints
- **Session/connection inspector + login feed (#14, #15)** — new `sessions:detail` topic
- **Border replication traffic + bot controls (#18, #19)** — new endpoint + hooks into existing bot test harness
- **Service event-bus inspector + log tail + config editor (#22, #23, #24)** — HTTP wrappers around existing console builtins
- **Audit log UI + RBAC editor (#26, #27)** — table viewer + form over `Operators` config
- **Custom entity-kind columns + domain panels (#29, #30)** — game-registered metadata; `PanelDef` already supports it
- **`--mode=admin` role** — flip-of-a-switch once `RemoteClusterView` + `MeshControl.AdminStream` land

## 10. Error handling

**Frontend.** Every API call goes through `lib/api.ts`, which normalizes errors to `{kind: 'http'|'network'|'rbac'|'validation'|'cmdsys', status, message, fieldErrors?}`. `kind: 'rbac'` shows "You don't have permission" toast; `'validation'` highlights form fields; `'cmdsys'` shows the verb's error body. Network errors trigger the global "reconnecting…" indicator. SSE auto-reconnects in `lib/stream.ts` with exponential backoff (200 ms → 5 s capped, jittered).

**Backend.** Every handler wraps panics into 500 + audit entry. Dispatcher errors map to specific HTTP codes per the contract in §6.3. SSE writers detect closed clients (`http.ResponseWriter` flush error) and unsubscribe themselves — no goroutine leaks. Lockout state is held only in memory; on coordinator restart, lockout counters reset (acceptable; protected by argon2id hash strength).

**Approach-2 readiness.** `ClusterView` methods return typed errors (`view.ErrCellNotFound`, `view.ErrUnavailable`); the future remote impl maps gRPC errors to the same set without changing handlers.

## 11. Testing strategy

- **`pkg/admin/` Go tests.** `view_local_test.go` verifies the in-process view against a fixture cluster; `topicbus_test.go` covers fanout, slow-subscriber dropping, and unsubscribe; `auth_test.go` covers cookie roundtrip, lockout, session expiry; `api_*_test.go` use `httptest.Server` + a fake `Dispatcher` to assert request/response contracts.
- **Frontend.** Vitest for `lib/*` (api error normalization, SSE demuxer, schema-to-form builder); Playwright smoke for login → cluster page → click cell → invoke split. Headless against a 4node-basic instance with `--admin-listen=:9101` + a known `josh/dev-password` operator.
- **Integration smoke.** A new `pkg/admin/admin_e2e_test.go` boots a coordinator with `Config.Admin.Enabled` + `MemorySessionStore`, drives a real `cell.split` from HTTP, and asserts the SSE `topology` event fires on the subscribed connection.
- **No hooks into game logic.** Every test runs against `pkg/admin` + `pkg/universe` only; no `internal/game` imports.

## 12. Phasing summary

- **Phase 1 (this implementation plan).** `pkg/admin/` skeleton, `ClusterView`/`TopicBus` interfaces + local impls, SSE infrastructure, session auth (memory + postgres), `web-admin/` SPA with the 14 MVP panels (§8), embedded-FS build pipeline, e2e smoke against 4node-basic. Ships behind `Config.Admin.Enabled = true`.
- **Phase 2.** Detail pages, replay, log tail, config editor, audit UI (§9 — most items).
- **Phase 3.** `--mode=admin` standalone role, custom Svelte plugin loader for game panels, MFA, password reset.

## 13. Logging

Per `feedback_logging`, every state-changing path logs through the `Logger` passed in `Config`:

- Login success: `cat=admin "login user=%s ip=%s"`
- Login failure: `cat=admin "login-fail user=%s ip=%s reason=%s"`
- Lockout trigger: `cat=admin "lockout user=%s ip=%s window=%s"`
- Command invocation: `cat=admin "cmd verb=%s user=%s args=%s ok=%v dur=%s"`
- Audit ring overflow (memory store only): `cat=admin "audit-overflow dropped=%d"`

Category `admin` is auto-registered by `pkg/admin` at startup via the dynamic logger registration that already exists in `pkg/logger`.
