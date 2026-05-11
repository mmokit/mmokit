# Admin Log Tail — v1 Design

## 1. Goal

Stream every host's logs to the admin dashboard in real time so operators can tail, filter, and pause the cluster's log output from a browser. The existing `/logs` page only toggles which categories are enabled — this spec adds the actual log delivery channel, cluster-wide.

## 2. Non-goals

- **Persistent log storage.** Logs live in an in-memory ring on the coordinator. Restart → empty tail. A future spec can layer Postgres or a flat-file sink onto the same Hook interface.
- **Log levels.** `pkg/logger.Logger` is binary on/off per category. Levels (debug/info/warn/error) are a separate refactor.
- **Server-side regex filters on the tail.** `Logger.SetFilter(cat, pattern)` already exists but isn't exposed to the dashboard. Adding that surface is a follow-up.
- **Forwarder on `RoleGateway` / `RoleService`-only processes.** Only `RoleHost` processes forward logs in v1. Gateway-only logs stay local to the gateway process. Easy to extend later by hanging the same forwarder off `RoleGateway`, but the typical gateway runs alongside the coordinator and already shares its logger via the in-process Hook path.
- **Per-category fan-out subscriptions.** The dashboard subscribes to the single `"logs"` SSE topic and filters client-side. Server-side category filtering on the SSE stream is unnecessary at expected volumes (~50-500 entries/sec aggregate cluster-wide).

## 3. Architecture

```
Host process A (RoleHost, --coordinator-addr=…)
  process.Log.Log("mesh:cell", ...)
       │
       ▼
  Hook fan-out (logger.Logger.AddHook)
       │
       ├─► [other in-process hooks…]
       │
       └─► meshLogForwarder.Emit(cat, msg, t)
              │ non-blocking send to bounded chan
              ▼
         drain goroutine (100ms / N=64 entries, whichever first)
              │ pack LogBatch + send over MeshControl
              ▼
         HostMessage.log_batch → CoordMessage stream

Coordinator process (RoleCoordinator)
  controlServer receives HostMessage.LogBatch
       │ stamp each entry with sender host_id
       ▼
  admin.LogRing.Append(entries)
       │
       ▼ pump
  TopicBus.Publish("logs", entry)
       │
       ▼
  /admin/api/stream subscribers (SPA LogTail.svelte)

Coordinator's own logs:
  process.Log.Log(...)
       │
       ▼
  in-process Hook → LogRing.Append + pump → TopicBus.Publish
  (host_id = "local")
```

## 4. Backend

### 4.1 `pkg/logger.Logger` — Hook interface

Append to `pkg/logger/logger.go`:

```go
// Hook receives every log line that survives the category-enabled check
// and per-category filter. Implementations MUST be O(1) — Log() blocks
// the calling goroutine while hooks fire. Use a bounded channel + pump
// pattern if any real work is involved.
type Hook interface {
    Emit(cat, msg string, t time.Time)
}

func (l *Logger) AddHook(h Hook) {
    l.mu.Lock()
    l.hooks = append(l.hooks, h)
    l.mu.Unlock()
}

func (l *Logger) RemoveHook(h Hook) {
    l.mu.Lock()
    for i, x := range l.hooks {
        if x == h {
            l.hooks = append(l.hooks[:i], l.hooks[i+1:]...)
            break
        }
    }
    l.mu.Unlock()
}
```

`Logger.Log` calls hooks after the `log.Printf`:

```go
func (l *Logger) Log(cat string, format string, args ...any) {
    // existing enabled + filter check unchanged
    ...
    log.Printf("[%s] %s", cat, msg)
    l.mu.RLock()
    hooks := l.hooks
    l.mu.RUnlock()
    if len(hooks) == 0 {
        return
    }
    now := time.Now()
    for _, h := range hooks {
        h.Emit(cat, msg, now)
    }
}
```

Add a `time` import. No new test files — the hook contract is covered by the `pkg/admin` ring tests.

### 4.2 `pkg/admin.LogRing`

New file `pkg/admin/log_ring.go`:

```go
type LogEntry struct {
    Host string    `json:"host"`
    Cat  string    `json:"cat"`
    Msg  string    `json:"msg"`
    T    time.Time `json:"t"`
}

type LogRing struct {
    mu   sync.Mutex
    cap  int
    buf  []LogEntry
    next int  // ring index
    full bool
}

func NewLogRing(cap int) *LogRing { ... }
func (r *LogRing) Append(e LogEntry) { ... }
func (r *LogRing) Recent(n int) []LogEntry { ... }  // newest-first
```

Pump + Hook in `pkg/admin/log_pump.go`:

```go
type logPump struct {
    ring *LogRing
    bus  *TopicBus
    ch   chan LogEntry
}

// Emit implements logger.Hook. Always non-blocking — drops on full
// channel rather than stalling the game loop.
func (p *logPump) Emit(cat, msg string, t time.Time) {
    e := LogEntry{Host: "local", Cat: cat, Msg: msg, T: t}
    p.ring.Append(e)
    select {
    case p.ch <- e:
    default:
        // drop — pump is behind; ring still has the entry.
    }
}

// Run drains the channel and publishes to the bus until ctx cancels.
func (p *logPump) Run(ctx context.Context) { ... }
```

Append for **remote** entries goes directly to `LogRing.Append` from the controlServer (no Hook → goes straight to ring + bus).

Default ring capacity: **4096 entries** (~hours of typical traffic). Configurable via `Config.LogRingCap`.

### 4.3 Server wiring (`pkg/admin/admin.go`)

- New field `Server.logRing *LogRing`.
- New `Config.LogRingCap int` (default 4096).
- `NewServer` constructs `logRing := NewLogRing(cap)`, starts a `logPump` goroutine (lifecycle tied to `s.cancel`).
- If `opts.Logger != nil`, `opts.Logger.AddHook(s.logPump)` — wires the coord's own logs into the ring.
- New HTTP route `GET /admin/api/logs/recent?n=N` returns `s.logRing.Recent(n)` (n defaults to 200, capped at ring size). Same auth as `/admin/api/audit`.

### 4.4 Proto additions (`proto/meshpb/mesh.proto`)

```proto
message LogBatch {
  repeated LogEntry entries = 1;
}
message LogEntry {
  string cat        = 1;
  string msg        = 2;
  int64  ts_unix_ms = 3;
}
```

Add to `HostMessage.oneof msg`:

```proto
LogBatch log_batch = 21;
```

Regenerate via `just proto`.

### 4.5 Host forwarder (`pkg/universe`)

New file `pkg/universe/log_forwarder.go`:

```go
type meshLogForwarder struct {
    hostID string
    out    chan logger.Hook // for clarity — actually buffers (cat,msg,t) tuples
    // ...
}
```

The forwarder:
- Buffers (cat, msg, t) tuples into a bounded channel (capacity 1024).
- Non-blocking send on `Emit` — drop on full.
- Drain goroutine assembles `LogBatch{Entries: [...]}` every **100ms** or when batch size hits **64 entries**, whichever first.
- Sends batch as `HostMessage_LogBatch` over the existing MeshControl stream (the same one carrying Heartbeat).
- On stream reconnect, the forwarder keeps running — the in-flight batch is dropped, new batches use the new stream.

Forwarder registration:
- In `pkg/universe/bootstrap.go` (or wherever the host's `controlClient` is built), after MeshControl dials, attach the forwarder: `process.Log.AddHook(forwarder)`.
- Only when `RoleHost` is in the role set AND `CoordinatorAddr != ""` (i.e. remote host). In-process hosts (`RoleHost` + `RoleCoordinator`) share the coord's logger directly via the in-process pump path.

### 4.6 Coord receiver

In `pkg/universe`'s `controlServer` (or the equivalent file demuxing `HostMessage`):

```go
case *meshpb.HostMessage_LogBatch:
    senderHostID := /* looked up from stream identity */
    for _, e := range m.LogBatch.Entries {
        entry := admin.LogEntry{
            Host: senderHostID,
            Cat:  e.Cat,
            Msg:  e.Msg,
            T:    time.UnixMilli(e.TsUnixMs),
        }
        coord.adminLogRing.Append(entry)
        coord.adminLogBus.Publish("logs", entry)
    }
```

**Import-direction note:** `pkg/universe` can't import `pkg/admin` (cycle). The controlServer can't construct an `admin.LogEntry` directly. Solution: `pkg/universe` exposes a primitive callback hook:

```go
// In pkg/universe/coordinator.go
type RemoteLogEntry struct {
    HostID string
    Cat    string
    Msg    string
    TimeMs int64
}
func (c *Process) OnRemoteLogBatch(fn func([]RemoteLogEntry)) { ... }
```

`pkg/mmokit.DefaultAdminServerFactory` sets the callback to a closure that converts `[]RemoteLogEntry` → `[]admin.LogEntry`, appends to the per-process `adminLogRing`, and publishes to the bus. The per-process bus map (`adminBusMap`) gains a parallel `adminLogRingMap` for the ring instances.

### 4.7 Cross-host category toggle (`log.set` cmdsys verb)

New verb registered by `pkg/admin` (so it's available even when no game registers it):

```go
type logSetArgs struct {
    Category string `cmd:"required"`
    Enabled  bool
}
type logSetResult struct {
    Category string
    Enabled  bool
}
```

- `Route: RouteAllHosts` — fans out to every host (and the coordinator).
- Each host's handler calls `logger.Enable(cat)` or `logger.Disable(cat)`.
- Returns the post-call `IsEnabled` for confirmation.

The Logs page's existing `POST /admin/api/logs/categories/<cat>` endpoint stays for back-compat but is no longer called by the SPA — the SPA switches to `POST /admin/api/commands/log.set`. (We can remove the old endpoint when nothing else uses it.)

## 5. Frontend

### 5.1 `LogEntry` TS type

Append to `web-admin/src/lib/types.ts`:

```ts
export type LogEntry = {
  host: string;  // "local" for coord, host_id for remote
  cat: string;
  msg: string;
  t: string;     // ISO timestamp
};
```

### 5.2 `LogTail.svelte`

New component `web-admin/src/components/LogTail.svelte`. Behavior:

- **Hydrate** on mount: `apiGet<LogEntry[]>("/admin/api/logs/recent?n=200")` → seed `entries` (oldest first for natural top-to-bottom reading).
- **Stream:** `stream.subscribe("logs", (entry) => entries = capTail([...entries, entry]))`. Cap client-side ring at **1000** entries (drop oldest).
- **Filters:**
  - Category substring (text input)
  - Host substring (text input)
  - Both are AND'd, case-insensitive, applied to a `$derived` view.
- **Pause toggle** — when paused, new entries still accumulate into the buffer (so unpause shows what was missed), but the rendered view stays frozen on the last unpaused snapshot.
- **Auto-scroll** — sticks to bottom unless the user has scrolled up >50px from the bottom. Detected via `scrollTop + clientHeight` vs `scrollHeight`.
- **Host badges** — `[host]` prefix per line, color tinted by FNV-1a hash of host_id (reuse the existing host-color helper from CellMap).
- **Timestamp prefix** — `HH:MM:SS.mmm` in `text-[var(--text-dim)]`.
- **Layout** — monospace pre block with `font-mono text-[11px]`, scrollable, fills available height.

### 5.3 `routes/logs.svelte` — split-pane

Restructure into a flex row:
- **Left ~280px:** existing category toggles converted to a compact vertical list (one row per category, group headers, `[all] [none]` per group). Same data and behavior; the wide grid becomes a narrow sidebar.
- **Right grow:** `<LogTail />`.

Replace the existing per-cat `apiPost("/admin/api/logs/categories/<cat>", {enabled})` with `apiPost("/admin/api/commands/log.set", {Category: cat, Enabled: enabled})`. Optimistic-update the local toggle state same as before; the cmdsys response confirms.

## 6. Open questions / explicit decisions

- **Host_id stamping for remote entries:** the controlServer already tracks the sender's host_id per stream connection (used by RegisterHost). Re-use that identity.
- **Reconnect behavior:** on host's MeshControl reconnect, the forwarder's batch goroutine retargets to the new stream silently. No replay of lost entries — visible as a gap in the tail.
- **Throughput cap:** the forwarder drops entries when its in-channel is full. We don't try to back-pressure the game loop — log loss on a hot host is acceptable. If we ever see this in practice we can revisit (e.g. log-the-drop with `logs:dropped` category, or expose a counter).
- **Process role for "local" tag:** the coord's own logs use `host = "local"`. We could instead use the coord's host_id (typically "coordinator" or the chosen --host-id) — but "local" is unambiguous on the UI side and avoids special-casing in tests.
- **In-process hosts:** when running `--mode=all` (single process), there's no remote forwarder. The single in-process Hook covers everything. The `meshLogForwarder` registration check (`RoleHost && CoordinatorAddr != ""`) skips this case.

## 7. File structure

**New files:**

```
pkg/admin/log_ring.go                     # ring buffer + tests
pkg/admin/log_ring_test.go
pkg/admin/log_pump.go                     # in-process Hook impl + pump goroutine
pkg/admin/api_logs_tail.go                # GET /admin/api/logs/recent
pkg/universe/log_forwarder.go             # cross-host forwarder
pkg/admin/log_cmdsys.go                   # log.set verb registration
web-admin/src/components/LogTail.svelte
```

**Modified files:**

```
pkg/logger/logger.go                      # Hook interface + AddHook + time import
pkg/admin/admin.go                        # LogRing field + LogRingCap config + wiring
proto/meshpb/mesh.proto                   # LogBatch + LogEntry + HostMessage.log_batch
gen/go/meshpb/mesh.pb.go                  # regenerated
pkg/universe/coordinator.go               # OnRemoteLogBatch callback + meshLogForwarder hookup
pkg/universe/control_server.go (or eq.)   # HostMessage.LogBatch demux
pkg/mmokit/admin.go                       # adminLogRingMap, OnRemoteLogBatch wiring
web-admin/src/lib/types.ts                # LogEntry
web-admin/src/routes/logs.svelte          # split-pane restructure
CLAUDE.md                                 # one paragraph
```

## 8. Test plan

- **`pkg/logger.Logger.AddHook`**: hook receives Emit calls for enabled categories, doesn't for disabled.
- **`pkg/admin.LogRing`**: append/Recent round-trip; wraparound at capacity; concurrent appends safe.
- **`pkg/admin.logPump`**: drop-on-full doesn't block Emit; ctx cancel exits cleanly.
- **Proto round-trip**: `LogBatch` serializes + deserializes.
- **Cross-host integration smoke**: 2-process test (coord + host) where the host's log line shows up on the coord's bus within a fixed deadline. Reuses the existing 4node-basic distributed harness pattern.
- **Frontend** typecheck + manual smoke per the LogTail spec; no Vitest tests added (same pattern as PanelHost).

## 9. Phasing

Single plan. Estimated **~12 tasks**: 4 backend (Hook+ring+pump+route), 3 cross-host (proto+forwarder+coord receiver), 1 cmdsys (log.set), 3 frontend (types+LogTail+logs.svelte), 1 CLAUDE.md.
