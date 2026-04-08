# Phase 3: Platform Polish

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Clean up remaining accidental complexity: consolidate coordinator player maps, simplify engine hooks, reduce public API surface, and adopt SpawnEntity pattern in the space game.

**Architecture:** Five independent improvements that can be done in any order. Each produces a self-contained commit. None depend on each other.

**Tech Stack:** Go, ECS (Ark)

**Prerequisites:** Phase 1 and Phase 2 should be complete, but these tasks are mostly independent of them.

---

### Task 1: Consolidate Coordinator Player Maps (3 → 1)

The Coordinator tracks player locations in 3 separate maps: `playerNode` (connID → nodeID), `activeUsers` (username → nodeID), `disconnected` (username → nodeID). This causes sync bugs (we hit one during the connection proxy work where `disconnected` held a stale nodeID after merge). Consolidate into one.

**Files:**
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Define PlayerLocation struct**

```go
type PlayerLocation struct {
    NodeID   string
    ConnID   uint32
    Active   bool // false = disconnected (grace period)
}
```

Replace the 3 maps with:
```go
    players   map[string]*PlayerLocation // username → location
    connIndex map[uint32]string          // connID → username (reverse lookup)
```

- [ ] **Step 2: Update NewCoordinator**

Replace initialization of `playerNode`, `activeUsers`, `disconnected` with:
```go
    players:   make(map[string]*PlayerLocation),
    connIndex: make(map[uint32]string),
```

- [ ] **Step 3: Update notification methods**

Replace `notifySessionActive`, `notifySessionDisconnected`, `notifySessionRemoved`:

```go
func (c *Coordinator) notifySessionActive(username, nodeID string) {
    c.mu.Lock()
    loc := c.players[username]
    if loc == nil {
        loc = &PlayerLocation{}
        c.players[username] = loc
    }
    loc.NodeID = nodeID
    loc.Active = true
    c.mu.Unlock()
}

func (c *Coordinator) notifySessionDisconnected(username, nodeID string) {
    c.mu.Lock()
    if loc := c.players[username]; loc != nil {
        loc.Active = false
        loc.NodeID = nodeID
    }
    c.mu.Unlock()
}

func (c *Coordinator) notifySessionRemoved(username string) {
    c.mu.Lock()
    if loc := c.players[username]; loc != nil {
        if loc.ConnID != 0 {
            delete(c.connIndex, loc.ConnID)
        }
        delete(c.players, username)
    }
    c.mu.Unlock()
}
```

- [ ] **Step 4: Update accessor methods**

Update `ActiveUserNode`, `ActiveUsers`, `getPlayerNode`, `setPlayerNode`, `removePlayerNode` to use the new maps:

```go
func (c *Coordinator) ActiveUserNode(username string) string {
    c.mu.RLock()
    defer c.mu.RUnlock()
    if loc := c.players[username]; loc != nil && loc.Active {
        return loc.NodeID
    }
    return ""
}

func (c *Coordinator) ActiveUsers() map[string]string {
    c.mu.RLock()
    defer c.mu.RUnlock()
    result := make(map[string]string)
    for username, loc := range c.players {
        if loc.Active {
            result[username] = loc.NodeID
        }
    }
    return result
}

func (c *Coordinator) getPlayerNode(connID uint32) string {
    c.mu.RLock()
    defer c.mu.RUnlock()
    username := c.connIndex[connID]
    if loc := c.players[username]; loc != nil {
        return loc.NodeID
    }
    return ""
}

func (c *Coordinator) setPlayerNode(connID uint32, nodeID string) {
    c.mu.Lock()
    // Find username for this connID if we have it
    // For now, just track connID → nodeID via connIndex
    c.connIndex[connID] = "" // will be filled when session becomes active
    c.mu.Unlock()
}
```

Actually — `setPlayerNode` is called BEFORE the player's username is known (during connection routing). The `connIndex` needs to map connID → nodeID directly until the username is resolved. This means `connIndex` might need to be `map[uint32]string` mapping connID → nodeID (not username), and separate from the username-based `players` map.

**Revised approach:** Keep `connIndex map[uint32]string` as connID → nodeID (same as old `playerNode`). The `players` map replaces `activeUsers` + `disconnected`:

```go
    players   map[string]*PlayerLocation // username → location (replaces activeUsers + disconnected)
    connIndex map[uint32]string          // connID → nodeID (same as old playerNode)
```

This reduces 3 maps to 2, eliminates the sync issue between `activeUsers` and `disconnected`, and keeps `connIndex` for pre-login routing.

- [ ] **Step 5: Update routeAuthenticatedPlayer**

The reconnection check currently reads `c.disconnected[username]`. Change to check `c.players[username]` where `!loc.Active`:

```go
    if loc := c.players[username]; loc != nil && !loc.Active {
        reconnectNodeID = loc.NodeID
    }
    if loc := c.players[username]; loc != nil && loc.Active {
        existingNodeID = loc.NodeID
    }
```

- [ ] **Step 6: Update partition.go**

SplitCell and MergeCell reference `c.playerNode`. Update to `c.connIndex`.

- [ ] **Step 7: Verify and test**

Run: `go vet ./pkg/universe/ && go test ./pkg/universe/ -count=1`

- [ ] **Step 8: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/partition.go
git commit -m "refactor: consolidate coordinator player maps (3 → 2), single source of truth"
```

---

### Task 2: Simplify Engine Hooks (7 → 5)

Remove `ClearTickState` and `ProcessLogins` from the Hooks struct — they're internal engine concerns, not game concerns.

**Files:**
- Modify: `pkg/engine/loop.go` (Hooks struct, tick function, hook merging)
- Modify: `pkg/engine/player_manager.go` (hooks method)
- Modify: `internal/game/game.go` (remove ClearTickState hook)
- Modify: `examples/slither/world.go` (remove ClearTickState hook)

- [ ] **Step 1: Remove ClearTickState from Hooks struct**

In `pkg/engine/loop.go`, remove `ClearTickState func()` from the `Hooks` struct. In `tick()`, call `TickQueue.ClearAll()` directly (or whatever the game's queue clearing was) at the start of the tick — this is engine-internal, not a game hook.

Actually — the engine doesn't own the TickQueue. Games create their own. So ClearTickState IS needed as a hook for games that use TickQueue. But we can make it automatic: if the engine has a `TickQueue` field, auto-clear it. Otherwise keep the hook.

**Revised approach:** Keep `ClearTickState` for now (games need it). Instead, just remove `ProcessLogins` since it's already handled internally by PlayerManager:

Remove `ProcessLogins func()` from Hooks. Remove it from the hook merging in `NewGameLoop`. Remove the call in `tick()`.

- [ ] **Step 2: Remove ProcessLogins from PlayerManager.hooks()**

In `pkg/engine/player_manager.go`, the `hooks()` method returns a `ProcessLogins` function. Remove it — `processPendingSessions` is called directly in the tick.

Wait — check how this is currently wired. The `ProcessLogins` hook is merged in `NewGameLoop` at line 58. If PlayerManager's `hooks()` provides it, and the game's hooks don't, removing from PlayerManager means it never runs.

Read the tick loop to see if `processPendingSessions` is called via the hook or directly:

The hook is the only way it runs. So we need to keep calling it — just not as a game-exposed hook. Move the call into the engine tick directly:

In `tick()`, after `processEvents()` and `processAdminCmds()`, call `eng.Players.processPendingSessions()` directly (make it exported or call through a private method).

- [ ] **Step 3: Update game code**

Remove `ProcessLogins` from any game Hooks (it's probably not set in game code since PlayerManager handles it).

- [ ] **Step 4: Verify and test**

Run: `go vet ./pkg/engine/ && go test ./pkg/engine/ -count=1`

- [ ] **Step 5: Commit**

```bash
git add pkg/engine/loop.go pkg/engine/player_manager.go
git commit -m "refactor: remove ProcessLogins from Hooks, call internally in tick"
```

---

### Task 3: Adopt SpawnEntity in Space Game Spawn Functions

The space game has 5 spawn functions that manually construct entities with `m.base.NewEntity(...)`. Migrate to `gw.SpawnEntity()` with option functions for a consistent pattern.

**Files:**
- Modify: `internal/game/entity_ship.go` (SpawnPlayer)
- Modify: `internal/game/entity_asteroid.go` (spawnAsteroidWithItem)
- Modify: `internal/game/entity_station.go` (SpawnStation)
- Modify: `internal/game/entity_npc.go` (SpawnNPC)
- Modify: `internal/game/entity_lootcrate.go` (SpawnLootCrate)

- [ ] **Step 1: Read SpawnEntity API**

Read `pkg/universe/world_base.go` to understand `SpawnEntity` and its option functions (`WithCollider`, `WithEntityKind`, `WithVelocity`, `WithRotation`, `WithComponents`). Understand what `WithComponents()` does — it auto-adds all components registered on the entity's kind.

- [ ] **Step 2: Update each spawn function**

For each entity type, replace manual mapper-based entity creation with `gw.SpawnEntity()`. Example for asteroid:

```go
// Before:
entity := m.base.NewEntity(
    &mmokit.Position{X: x, Y: y},
    &mmokit.Velocity{},
    &mmokit.Rotation{Angle: angle},
    &mmokit.Collider{Radius: radius, Layer: layer},
    &mmokit.NetworkID{ID: netID},
    &mmokit.EntityKind{Type: gamecomp.TypeAsteroid},
)

// After:
entity := gw.SpawnEntity(
    mmokit.Position{X: x, Y: y},
    mmokit.WithRotation(angle),
    mmokit.WithCollider(radius),
    mmokit.WithEntityKind(gamecomp.TypeAsteroid),
    mmokit.WithComponents(), // auto-adds Minable etc. from EntityKindDef
)
```

Note: `SpawnEntity` auto-assigns NetworkID, so `NextNetID()` calls can be removed. Check the SpawnEntity implementation to confirm.

- [ ] **Step 3: Verify compilation**

Run: `go vet ./internal/game/`

- [ ] **Step 4: Commit**

```bash
git add internal/game/entity_*.go
git commit -m "refactor: adopt SpawnEntity pattern in all spawn functions"
```

---

### Task 4: Reduce Coordinator Public API

Make internal methods that are implementation details, not public API.

**Files:**
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Identify methods to make private**

These are currently exported but are implementation details:
- `getPlayerNode` → already private (lowercase)
- `setPlayerNode` → already private
- `removePlayerNode` → already private
- `buildNodeRefs` → make private: `buildNodeRefs`
- `defaultEntityOpts` → make private: already lowercase

Check which ones are actually exported (uppercase) vs already private:

```bash
grep -n "^func (c \*Coordinator) [A-Z]" pkg/universe/coordinator.go
```

Review the list. Methods that are only called internally or by the console (which has access to the coordinator) should be private. Keep public only what external callers (games, main.go) need.

- [ ] **Step 2: Make internal methods private**

For each method that should be private, rename from `BuildNodeRefs` → `buildNodeRefs`, etc. Update all callers.

- [ ] **Step 3: Verify compilation**

Run: `go vet ./pkg/universe/ && go vet ./cmd/server/`

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "refactor: reduce Coordinator public API — internalize implementation details"
```

---

### Task 5: Documentation Update

**Files:**
- Modify: `CLAUDE.md`
- Modify: `pkg/mmokit/README.md`
- Modify: `internal/game/README.md`

- [ ] **Step 1: Update CLAUDE.md**

- Update GameWorld interface method count
- Update entity pattern documentation (EntityKindDef is now the only pattern)
- Update hook list if ProcessLogins was removed
- Note game event codes at 100+

- [ ] **Step 2: Update READMEs**

- `pkg/mmokit/README.md`: EntityKindDef pattern, LocalOnly option, SpawnEntity pattern
- `internal/game/README.md`: No nethandlers, no adapter, no mappers structs. EntityKindDef + SpawnEntity + Components struct

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md pkg/mmokit/README.md internal/game/README.md
git commit -m "docs: update for Phase 2 + Phase 3 architecture changes"
```

---

### Task 6: Final Verification

- [ ] **Step 1: Full test suite**

Run: `go test ./pkg/... -count=1 -timeout=60s`

- [ ] **Step 2: Full vet**

Run: `go vet ./pkg/... ./internal/game/ ./cmd/server/`

- [ ] **Step 3: Build examples**

Run: `cd examples/4node-basic && go build . && cd ../slither && go build .`

- [ ] **Step 4: Manual smoke test**

Start server: `make dev`
- Full gameplay test: login, combat, mining, docking, transfers, dynamic cells
- Verify no regressions from all 3 phases
