# Phase 1: Shrink GameWorld Interface + Eliminate Adapter

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce the GameWorld interface from 34 methods to ~12, and eliminate the space game's adapter pattern by embedding WorldBase directly in GameWorld.

**Architecture:** The bridge (`node_bridge_impl.go`) currently calls infrastructure methods (replica/proxy/ghost lifecycle) through the GameWorld interface. These are moved to direct `*WorldBase` access via `node.Base` (a new field on Node). The GameWorld interface keeps only methods that games actually override. The space game's GameWorld struct changes from embedding `*Engine` to embedding `*WorldBase`, eliminating the adapter wrapper entirely.

**Tech Stack:** Go, ECS (Ark)

---

### Task 1: Add `Base` field to Node struct

The Node currently only has `World GameWorld`. We need direct access to the embedded `*WorldBase` for infrastructure methods that are being removed from the interface.

**Files:**
- Modify: `pkg/universe/node.go`
- Modify: `pkg/universe/coordinator.go` (createNode — set node.Base)

- [ ] **Step 1: Add `Base` field to Node**

In `pkg/universe/node.go`, add a `Base` field to the `Node` struct:

```go
type Node struct {
    ID        string
    Cell      CellID
    Engine    *engine.Engine
    World     GameWorld
    Base      *WorldBase // direct access for infrastructure methods
    Inbox     chan NodeMessage
    Events    chan net.PlayerEvent
    Neighbors map[string]*Node
    Log       *logger.Logger
    // ...
}
```

- [ ] **Step 2: Set `node.Base` in createNode**

In `pkg/universe/coordinator.go`, in `createNode`, after the node is created (where `World` is assigned), set `Base`:

```go
node.Base = base
```

`base` is the `*WorldBase` created earlier in `createNode` (it's already a local variable).

- [ ] **Step 3: Verify compilation**

Run: `go vet ./pkg/universe/`

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/node.go pkg/universe/coordinator.go
git commit -m "feat: add Base field to Node for direct WorldBase access"
```

---

### Task 2: Move infrastructure methods from GameWorld interface to bridge direct calls

The bridge currently calls 19 methods through the `GameWorld` interface that are purely `WorldBase` infrastructure. Change the bridge to call `node.Base` directly.

**Files:**
- Modify: `pkg/universe/node_bridge_impl.go` (PreTick, PostSystems)
- Modify: `pkg/universe/node.go` (processMessage for ghost/replica removal)

- [ ] **Step 1: Update PreTick to use node.Base**

In `pkg/universe/node_bridge_impl.go`, replace `b.node.World.*` calls with `b.node.Base.*`:

```go
func (b *nodeBridge) PreTick() {
    b.node.Base.ClearReplicaUpdateFlags()
    b.node.Base.ClearProxyUpdateFlags()
    b.node.DrainInbox()
    b.node.Base.TickReplicaDeadReckoning(0.05)
    if b.coord.cfg.ProxiesEnabled {
        b.node.Base.TickProxyDeadReckoning(0.05)
        b.node.Base.WakeDormantEntities(b.coord.cfg.AoIRadius)
    }
}
```

- [ ] **Step 2: Update PostSystems to use node.Base**

```go
func (b *nodeBridge) PostSystems() {
    if b.coord.cfg.ProxiesEnabled {
        b.sendProxies()
        b.node.Base.ExpireProxies()
    } else {
        b.sendReplicas()
    }
    b.node.Base.ExpireReplicas()
}
```

- [ ] **Step 3: Update sendReplicas/sendProxies**

Read `node_bridge_impl.go` for `sendReplicas` and `sendProxies`. These call `b.node.World.ScanBorderEntities(...)` and `b.node.World.ScanBorderProxies(...)`. Change to `b.node.Base.ScanBorderEntities(...)` and `b.node.Base.ScanBorderProxies(...)`.

- [ ] **Step 4: Update processMessage in node.go**

In `node.go`, `processMessage` calls `n.World.RemoveGhostByNetID(...)`, `n.World.RemoveReplicaByNetID(...)`, `n.World.RemoveProxyByNetID(...)`, `n.World.ApplyReplicas(...)`, `n.World.ApplyProxySummaries(...)`, `n.World.BuildDetailResponse(...)`, `n.World.PromoteProxy(...)`, `n.World.TickGhosts()`, `n.World.TickTransferCooldowns()`. Change all to `n.Base.*`.

Read the file, find each `n.World.` call, and determine if it's an infrastructure method (listed above) or a game method (HandleCrossNodeAction, DispatchChat, SpawnFromTransfer, SerializeEntity). Infrastructure → `n.Base.`, game → keep `n.World.`.

- [ ] **Step 5: Verify compilation**

Run: `go vet ./pkg/universe/`

- [ ] **Step 6: Run tests**

Run: `go test ./pkg/universe/ -count=1`

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/node_bridge_impl.go pkg/universe/node.go
git commit -m "refactor: bridge calls WorldBase directly for infrastructure methods"
```

---

### Task 3: Shrink the GameWorld interface

Remove all infrastructure methods from the interface. Only game-overridable methods remain.

**Files:**
- Modify: `pkg/universe/world.go`

- [ ] **Step 1: Replace the GameWorld interface**

Replace the entire interface with:

```go
type GameWorld interface {
    Init()
    Hooks() engine.Hooks
    Shutdown()

    SerializeEntity(entity ecs.Entity) ([]byte, error)
    SpawnFromTransfer(data []byte) (netID uint32, connID uint32, err error)

    HandleCrossNodeAction(action *CrossNodeAction) *ActionResult
    HandleActionResult(result *ActionResult)

    DispatchChat(username, text string)

    SetBridge(bridge NodeBridge)
    UpdateCellBounds(cell CellID, cellSize float32)
    MarkForRemoval(entity ecs.Entity)
}
```

- [ ] **Step 2: Fix compilation errors**

Some callers may use methods through the interface that were removed. The bridge was already updated (Task 2). Check for any other callers:
- `partition.go` — calls `World.SerializeEntity()` (kept), and infrastructure methods via `World.*` that need to change to accessing `Base` directly (if SplitCell/MergeCell reference infrastructure methods)
- `coordinator.go` — `createNode` assigns `World` (kept)

Fix any remaining `World.` calls to infrastructure methods by using `Base.` or type assertions.

- [ ] **Step 3: Verify compilation**

Run: `go vet ./pkg/universe/ && go vet ./pkg/engine/`

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/universe/ ./pkg/engine/ -count=1`

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/world.go pkg/universe/partition.go
git commit -m "refactor: shrink GameWorld interface from 34 to 12 methods"
```

---

### Task 4: Restructure space game GameWorld to embed WorldBase

The space game's `GameWorld` currently embeds `*mmokit.Engine`. Change it to embed `*mmokit.WorldBase` and implement the (now smaller) `GameWorld` interface directly. Delete the adapter.

**Files:**
- Modify: `internal/game/world.go` — change embedded type
- Modify: `internal/game/factory.go` — remove adapter wrapping
- Modify: `internal/game/game.go` — move adapter.Init() logic into GameWorld.Init()
- Delete: `internal/game/adapter.go`

- [ ] **Step 1: Change GameWorld struct to embed WorldBase**

In `internal/game/world.go`, change:

```go
type GameWorld struct {
    *mmokit.Engine
    // ...
}
```

to:

```go
type GameWorld struct {
    *mmokit.WorldBase
    // ...
}
```

Remove fields that WorldBase already provides: `Bridge` (WorldBase has it), `NodeID` (use `WorldBase.NodeID()`), `Cell` (use `WorldBase.Cell()` with conversion).

Keep fields that are game-specific: `Spatial`, `Config`, `Registry`, `C`, `Queue`, `Players`, `NetIDToEntity`, `PlayerDB`, `console`, `PlayerSessions`, `OnPostSpawn`, `SideEffects`, `flushTicks`, `FullRefreshInterval`.

Note: `gw.Engine` was previously accessed directly. Now use `gw.Engine()` (WorldBase accessor). `gw.ECS` becomes `gw.Engine().ECS`. `gw.ConnMgr` becomes `gw.Engine().ConnMgr`. `gw.Log` becomes `gw.Engine().Log`. This touches MANY files in internal/game/ — any file that references `gw.Engine`, `gw.ECS`, `gw.ConnMgr`, `gw.Log`, `gw.Tick`, `gw.Perf`, `gw.RemovedNetIDs`, `gw.Players` (if from Engine).

Actually — the cleaner approach: add convenience accessors to GameWorld for the most-used fields:

```go
func (gw *GameWorld) ECS() *ecs.World  { return gw.Engine().ECS }
func (gw *GameWorld) Log() *logger.Logger { return gw.Engine().Log }
```

Wait — this creates naming conflicts with `WorldBase.ECSWorld()`. Let's just use `gw.Engine().ECS` everywhere, or keep local variables.

The simplest approach for this task: change the embed, fix all compilation errors by updating references. This will be a large find-and-replace task.

- [ ] **Step 2: Update factory.go**

Change the world factory to return `*GameWorld` directly (no adapter):

```go
coord.SetWorld(func(base *mmokit.WorldBase) mmokit.GameWorld {
    // Use root cell for CellCoord
    cell := base.Cell()
    rootCell := cell
    for rootCell.Depth > 0 {
        rootCell = rootCell.Parent()
    }

    gw := NewGameWorld(base, gameCfg, playerDB, mmokit.CellCoord{
        CellX: rootCell.X,
        CellY: rootCell.Y,
    }, base.FromSplit())
    gw.PlayerSessions = playerSessions
    return gw
})
```

`NewGameWorld` now takes `*WorldBase` instead of `*Engine`.

- [ ] **Step 3: Move adapter.Init() logic into GameWorld.Init()**

The adapter's `Init()` method does:
1. Build replication registry → `gw.SetReplicationRegistry(reg)`
2. Set transfer received callback
3. Set player transfer received callback
4. Wire OnPostSpawn

Move all this into `GameWorld.Init()` (in `game.go`). Since `GameWorld` now embeds `*WorldBase`, it can call `gw.SetReplicationRegistry()` etc. directly.

- [ ] **Step 4: Move adapter game-specific overrides to GameWorld methods**

Add these methods to `GameWorld`:

```go
func (gw *GameWorld) SetBridge(bridge mmokit.NodeBridge) {
    gw.WorldBase.SetBridge(bridge)
    // any game-specific bridge wiring
}

func (gw *GameWorld) DispatchChat(username, text string) {
    gw.Log.Log(CatPlayerChat, "inbox: relayed chat <%s> %s", username, text)
    mmokit.Enqueue(gw.Queue, &enginepb.ChatMsg{Username: username, Text: text})
}

func (gw *GameWorld) HandleCrossNodeAction(action *mmokit.CrossNodeAction) *mmokit.ActionResult {
    // Move from adapter.go — all the switch cases
}

func (gw *GameWorld) HandleActionResult(result *mmokit.ActionResult) {
    // Move from adapter.go
}

func (gw *GameWorld) Shutdown() {
    // Move from adapter.go / existing game.go
}

func (gw *GameWorld) Hooks() mmokit.Hooks {
    // Already exists in game.go
}
```

- [ ] **Step 5: Update UnwrapGameWorld**

```go
func UnwrapGameWorld(w mmokit.GameWorld) *GameWorld {
    return w.(*GameWorld)
}
```

- [ ] **Step 6: Delete adapter.go**

Remove `internal/game/adapter.go` entirely.

- [ ] **Step 7: Fix all compilation errors**

This is the big one. Every file in `internal/game/` that references:
- `gw.Engine` (as embedded field) → `gw.Engine()` (WorldBase accessor)
- `gw.ECS` → `gw.Engine().ECS`  
- `gw.ConnMgr` → `gw.Engine().ConnMgr`
- `gw.Log` → `gw.Engine().Log`
- `gw.Tick` → `gw.Engine().Tick`
- `gw.RemovedNetIDs` → `gw.Engine().RemovedNetIDs`
- `gw.Perf` → `gw.Engine().Perf`
- `gw.NodeID` → `gw.WorldBase.NodeID()` or just use CellID
- `gw.Cell` → keep as game-level field (CellCoord, not CellID)
- `gw.Bridge` → `gw.WorldBase.Bridge()` or keep as convenience field

Also update:
- `gw.NextNetID()` → `gw.Engine().NextNetID()` 
- `gw.MarkForRemoval(e)` → `gw.Engine().MarkForRemoval(e)` (already on WorldBase too)
- `gw.Players` → if it was from Engine, use `gw.Engine().Players`

Note: `WorldBase` already has methods like `MarkForRemoval`, `SpatialGrid()`, `Engine()`, `Cell()`, `NodeID()`, etc. Many of these resolve the name collision.

Alternatively, add a helper field in GameWorld for frequently used references:

```go
type GameWorld struct {
    *mmokit.WorldBase
    eng     *mmokit.Engine // cached from WorldBase.Engine() for convenience
    // ...
}
```

Then in NewGameWorld: `gw.eng = base.Engine()`. This avoids touching every file.

- [ ] **Step 8: Verify compilation**

Run: `go vet ./internal/game/ && go vet ./cmd/server/`

- [ ] **Step 9: Run tests**

Run: `go test ./pkg/universe/ ./pkg/engine/ -count=1`

- [ ] **Step 10: Commit**

```bash
git add -A
git commit -m "refactor: eliminate adapter, GameWorld embeds WorldBase directly"
```

---

### Task 5: Update examples

Both 4node-basic and slither already embed WorldBase. They just need to verify they still implement the (now smaller) GameWorld interface.

**Files:**
- Modify: `examples/4node-basic/world.go` — verify interface compliance
- Modify: `examples/slither/world.go` — verify interface compliance

- [ ] **Step 1: Check 4node-basic compiles**

Run: `go vet ./examples/4node-basic/`

If it fails, the missing interface methods were likely ones that 4node-basic didn't implement but WorldBase provided defaults for. Since the interface shrunk, this should just work. Fix any issues.

- [ ] **Step 2: Check slither compiles**

Run: `go vet ./examples/slither/`

Slither implements `DispatchChat` and `HandleCrossNodeAction` on its world struct — verify these still match the interface signatures.

- [ ] **Step 3: Commit if changes needed**

```bash
git add examples/
git commit -m "fix: update examples for shrunk GameWorld interface"
```

---

### Task 6: Update tests and verify

**Files:**
- Modify: `pkg/universe/universe_test.go` — mockWorld may need methods removed
- Modify: `internal/game/*_test.go` — may reference adapter or old patterns

- [ ] **Step 1: Update mockWorld in universe_test.go**

The `mockWorld` struct implements `GameWorld`. Remove methods that were dropped from the interface. Keep only the ~12 interface methods.

- [ ] **Step 2: Fix internal/game tests**

Tests that use `UnwrapGameWorld` or reference the adapter need updating. The `testutil_test.go` creates a `GameWorld` directly — ensure it still works with WorldBase embedding.

- [ ] **Step 3: Run full test suite**

Run: `go test ./pkg/... -count=1 -timeout=60s`
Run: `go vet ./internal/game/ ./cmd/server/`

- [ ] **Step 4: Manual smoke test**

Start server: `make dev`
- Login, verify spawn works
- Move, shoot, mine — verify gameplay
- Cross cell boundary — verify transfer
- `cell split 1 1` — verify dynamic cells
- `debug` — verify topology overlay

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "test: update tests for GameWorld interface shrink + adapter removal"
```

---

### Task 7: Documentation update

**Files:**
- Modify: `CLAUDE.md`
- Modify: `pkg/mmokit/README.md`
- Modify: `internal/game/README.md`

- [ ] **Step 1: Update CLAUDE.md**

- GameWorld interface section: update method count, remove mention of adapter
- Coordinator setup pattern: no adapter needed
- Architecture description: games embed WorldBase directly

- [ ] **Step 2: Update READMEs**

- `pkg/mmokit/README.md`: Update GameWorld section
- `internal/game/README.md`: Remove adapter references, update architecture

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md pkg/mmokit/README.md internal/game/README.md
git commit -m "docs: update for GameWorld interface shrink + adapter elimination"
```
