# Custom Admin Panel API — v1 Design

## 1. Goal

Let games register dashboard panels from Go alone. A registered panel appears in the sidebar with live data and operator actions, with zero per-game Svelte files committed into the engine's SPA. The Go `PanelDef` is the only contract — the TS side is a single dynamic renderer that interprets it.

This is the final missing piece of admin dashboard Phase 1 (§8 #28 "Custom panel API"). Backend wiring (`PanelRegistry`, `mmokit.RegisterAdminPanel`, `/admin/api/panels`) already exists; this spec adds the runtime data plane (a way for game code to publish to admin topics) and the SPA-side dynamic renderer.

## 2. Non-goals

- **Per-game Svelte components.** No `web-admin/src/components/game-panels/` directory; no static component registry; no dynamic JS loading. Every panel ships through the single generic renderer. `PanelDef.Component` stays in the struct (it's the v2 plugin-loader hook) but is ignored by v1.
- **Multi-topic composition.** Each panel binds to exactly one topic. If a game needs two data sources, it publishes a unified payload to one topic.
- **Chart / scalar visualizations.** `PanelDef.Visualization` stays in the struct and defaults to `"table"`. Other modes (`"scalar"`, `"chart"`) are explicit v2 work; the renderer rejects unknown values back to `"table"` for forward compatibility.
- **Frontend unit tests.** The renderer is exercised end-to-end by the 4node-basic Bots panel in the manual smoke. Backend has Go tests as usual.

## 3. Architecture

```
Game code (examples/4node-basic/)
    │
    ├─ main.go ──── mmokit.RegisterAdminPanel(coord, AdminPanelDef{
    │                  ID:"bots", Topics:["bots"], Commands:["bot.spawn","bot.clear"]})
    │
    └─ admin_bots_publisher.go ─── 1Hz: mmokit.PublishAdminTopic(coord, "bots", rows)
                                            │
                                            ▼
                            pkg/mmokit ─── adminBus(coord) ──► pkg/admin.TopicBus
                                                                       │
                                                                       ▼
                                                  /admin/api/stream (SSE topic="bots")
                                                                       │
                                                                       ▼
                                            web-admin/src/components/PanelHost.svelte
                                                  ├─ subscribes to panel.topics[0]
                                                  ├─ renders DataTable from rows
                                                  └─ toolbar: panel.commands
                                                         └─ if argsSchema empty → POST
                                                            else → ArgsModal → POST
```

Two new pieces of wiring:

1. **Publish surface (Go).** Game code needs a way to push payloads into the admin `TopicBus` from outside `pkg/admin`. We add `mmokit.PublishAdminTopic(coord, topic, payload)` backed by a per-`Process` bus map that mirrors the existing `adminPanelMap` pattern. The bus is created in `mmokit` and passed into `admin.NewServer` via a new `ServerOpts.Bus` field; if absent, `NewServer` creates one itself (preserves existing tests).
2. **Dynamic renderer (TS).** `PanelHost.svelte` is the only new top-level component. It reads a `PanelDef`, subscribes to `panel.topics[0]` via the existing `stream.subscribe`, renders the latest payload as a DataTable (or scalar dump if non-array), and shows one toolbar button per command. Button click POSTs the verb — with or without an `ArgsModal` in front of it depending on the verb's argsSchema.

## 4. Backend

### 4.1 `pkg/admin/admin.go`

Add `Bus *TopicBus` to `ServerOpts`. In `NewServer`:

```go
bus := opts.Bus
if bus == nil {
    bus = NewTopicBus(0)
}
```

Existing test fixtures and stand-alone uses of `NewServer` keep working.

### 4.2 `pkg/mmokit/admin.go`

Add a parallel map for buses:

```go
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
// SSE multiplexer. No-op if no subscribers are listening. Safe to call
// from any goroutine.
func PublishAdminTopic(coord *universe.Process, topic string, payload any) {
    adminBus(coord).Publish(topic, payload)
}
```

`DefaultAdminServerFactory` passes the same bus via `ServerOpts.Bus`:

```go
return admin.NewServer(admin.ServerOpts{
    ...,
    Bus: adminBus(c),
})
```

### 4.3 Tests

`pkg/mmokit/admin_test.go` adds one test:

```go
func TestPublishAdminTopic_RoutesToBus(t *testing.T) {
    // Construct a Process via the mmokit test harness (or a stub)
    // Subscribe a fake Subscriber to topic "T"
    // mmokit.PublishAdminTopic(proc, "T", payload)
    // Assert subscriber received payload within a tight deadline
}
```

If a stub `*universe.Process` isn't trivial to construct in tests, fall back to testing `adminBus` directly with a non-nil pointer key.

## 5. Frontend

### 5.1 `web-admin/src/components/PanelHost.svelte` — single renderer

Props:

```ts
type Props = { panel: PanelDef };
```

Behavior:

1. **Hydrate verb schemas.** On mount, for each `verb` in `panel.commands ?? []`, fetch `/admin/api/commands/<verb>` once and stash the returned `argsSchema` in a local `Map<string, Schema>`. Render the toolbar buttons before the fetches complete; buttons are clickable but show a spinner if schema isn't loaded yet.
2. **Subscribe to topic.** If `panel.topics?.[0]` exists, call `stream.subscribe(panel.topics[0], handler)`. Handler stores the latest payload in `$state`.
3. **Render body:**
   - If latest payload is `Array<Record<string, unknown>>`: derive columns from `Object.keys` of `payload[0]` (union with `payload[1..4]` to catch sparse rows) and render via `DataTable.svelte`. Sortable, searchable for free.
   - Else if payload is a plain object: render as a 2-column key/value strip (cheap scalar mode).
   - Else (null/undefined/primitive): show `"no data"` placeholder.
4. **Toolbar.** One `<button>` per command. On click:
   - If the cached schema has no fields → direct `apiPost("/admin/api/commands/<verb>", {})`. Toast result.
   - Else → open `ArgsModal` with the verb name. Modal POSTs on submit and emits a result event.

### 5.2 `web-admin/src/components/ArgsModal.svelte`

Props:

```ts
type Props = {
  verb: string;
  schema: Schema;       // already fetched by PanelHost
  onClose: () => void;
  onResult: (ok: boolean, msg: string) => void;
};
```

Renders one input per `FieldSchema`:

| Kind | Input | Coercion |
|---|---|---|
| `string` | `<input type="text">` | identity |
| `int32` / `int64` | `<input type="number" step="1">` | `parseInt(v, 10)` |
| `float32` / `float64` | `<input type="number" step="any">` | `parseFloat(v)` |
| `bool` | `<input type="checkbox">` | identity |
| `[]<elem>` or `{...}` | `<input type="text">` with `(JSON)` hint | `JSON.parse(v)` |

Required fields show a red asterisk; submit is disabled until all are filled. `field.Default` populates initial value; `field.Help` renders as a small description under the input.

Submit POSTs `{ [field.name]: coerced(v), ... }` to `/admin/api/commands/<verb>`. On success: `onResult(true, "ok")`, close. On failure: render the error inline; keep the modal open.

### 5.3 `web-admin/src/components/Sidebar.svelte`

Currently renders a hard-coded items array. Modify to also iterate `panelsStore.value ?? []` and append game-registered panels under their declared `group` (case-insensitive match against the existing groups; unknown groups appear as their own section). Each rendered entry links to `/panel/<panel.id>`.

Icon lookup: small map `Record<string, Component>` keyed on lucide icon name strings the game might use. We pre-import a curated set (`Bot`, `Server`, `Database`, `Layers`, `Zap`, `Gauge`, plus the icons already in the file). Unknown icons fall back to a default (`Boxes`).

### 5.4 `web-admin/src/app.svelte`

- Add a single `onMount` fetch of `/admin/api/panels` → `panelsStore.set(panels)` (right after the existing session fetch).
- Add route branch:
  ```svelte
  {:else if path.startsWith("/panel/")}
    {@const id = path.slice("/panel/".length)}
    {@const def = (panelsStore.value ?? []).find((p) => p.id === id)}
    {#if def}
      <PanelHost panel={def} />
    {:else}
      <div class="p-8 text-slate-500">Panel <code>{id}</code> not registered.</div>
    {/if}
  ```

## 6. 4node-basic example

### 6.1 Panel registration (main.go)

Before `coord.Start()`:

```go
err := mmokit.RegisterAdminPanel(coord, mmokit.AdminPanelDef{
    ID:       "bots",
    Label:    "Bots",
    Icon:     "Bot",
    Group:    "Game",
    Topics:   []string{"bots"},
    Commands: []string{"bot.spawn", "bot.clear"},
})
if err != nil {
    log.Fatalf("register bots panel: %v", err)
}
```

### 6.2 Publisher (examples/4node-basic/admin_bots_publisher.go)

```go
type BotRow struct {
    CellID string `json:"cellId"`
    Count  int    `json:"count"`
    HostID string `json:"hostId"`
}

func startBotsPublisher(ctx context.Context, coord *mmokit.Process) {
    go func() {
        t := time.NewTicker(time.Second)
        defer t.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-t.C:
                rows := collectBotRows(coord)
                mmokit.PublishAdminTopic(coord, "bots", rows)
            }
        }
    }()
}

func collectBotRows(coord *mmokit.Process) []BotRow {
    // Walk coord.ClusterCells(); for each cell, ask its host (if local)
    // for the bot count. Multi-host visibility is best-effort — remote
    // cells report 0 in v1 since this example uses single-process
    // metrics. Real games would publish via per-host cmdsys verbs.
}
```

Hook from `main.go`:

```go
botsCtx, botsCancel := context.WithCancel(context.Background())
defer botsCancel()
startBotsPublisher(botsCtx, coord)
```

The example deliberately uses local-only entity counts. Cross-host bot enumeration belongs to a future spec — it requires a `bot.count` cmdsys verb that fans out via `RouteAllHosts`.

### 6.3 Smoke

After `just dev`:

- Browse to `/admin/`, log in, find "Bots" in the sidebar under the new "Game" group.
- Click → panel page. Empty table until bots are spawned.
- Hit `bot spawn 10 0_0` from the console; within ~1s the table shows `{cellId:"cell_0_0", count:10, hostId:...}`.
- Click the **bot.spawn** toolbar button → modal opens asking `count: int`, `cellID: string`. Submit. Toast: ok. Table row updates.
- Click **bot.clear** → no modal (zero-arg). Toast: ok. Table empties.

## 7. Open questions / explicit decisions

- **Per-host bot enumeration.** Punted; see 6.2. The Bots panel demonstrates the API, not a production-grade telemetry shape.
- **Multi-topic panels.** Not in v1. If a panel needs multiple data sources, the game publishes a fused payload.
- **Custom argsModal rendering per verb.** Out of scope. ArgsModal's typed-input matrix is enough for `bot.spawn` and the common case. Games needing rich forms can ship their own routes — but that's a v2 capability that intentionally isn't on the v1 surface.
- **Sidebar groups.** Games declare `group` as a free-form string. Sidebar groups them under headings of the same name with no special-casing. The 4node example uses `"Game"`; the space game can use `"Combat"`, etc.
- **`PanelDef.Component`** stays in the struct (would be silly to remove and re-add); v1 ignores it.

## 8. File structure

**New files:**

```
pkg/mmokit/admin_test.go                                  # TestPublishAdminTopic_RoutesToBus
web-admin/src/components/PanelHost.svelte                 # generic renderer
web-admin/src/components/ArgsModal.svelte                 # typed args form
examples/4node-basic/admin_bots_publisher.go              # 1Hz publisher + collector
```

**Modified files:**

```
pkg/admin/admin.go                                        # ServerOpts.Bus field
pkg/mmokit/admin.go                                       # adminBus(), PublishAdminTopic()
web-admin/src/components/Sidebar.svelte                   # iterate panelsStore + icon map
web-admin/src/app.svelte                                  # hydrate panelsStore + /panel/<id> route
web-admin/src/lib/types.ts                                # FieldSchema + Schema types
examples/4node-basic/main.go                              # RegisterAdminPanel + startBotsPublisher
CLAUDE.md                                                 # one paragraph on the custom panel surface
```

## 9. Test plan

- **`pkg/mmokit/admin_test.go::TestPublishAdminTopic_RoutesToBus`** — subscribe a fake `admin.Subscriber`, publish, assert delivery.
- **Existing `pkg/admin/*` tests** keep passing unchanged (`ServerOpts.Bus` is optional).
- **Frontend typecheck** clean.
- **Manual smoke** per §6.3.

## 10. Phasing

Single plan. ~6 backend tasks (1 wiring, 1 test, 1 universe-side check) + ~4 frontend tasks (PanelHost, ArgsModal, Sidebar, app.svelte+types) + ~2 game-side tasks (publisher, registration) + 1 CLAUDE.md update. Estimated 12-13 tasks per the writing-plans skill.
