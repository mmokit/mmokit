# Coordinator-Routed Console Commands

## Context

Console commands currently capture a single `gw *GameWorld` reference from one node. Helper functions like `resolvePlayer(gw, ...)` only search that node's `PlayerManager` and ECS world. With coordinator-level login routing, players land on different nodes based on their saved position — so commands like `tp xennion 5 5` fail with "entity not found" when the player is on a different node. The `playerdb` command shows online players as "offline" for the same reason.

The Coordinator already tracks `activeUsers[username] → nodeID` and shares a global `PlayerDB`. Console commands should use this coordinator-level knowledge to route commands to the correct node, and read global data without involving any node's game loop.

## Scope

**In scope:**
- Merge `players`/`ps` and `playerdb`/`pdb` into a single `players` command
- Make action commands (`tp`, `damage`, `kill`, `heal`, `give`, `kick`, `spawnnpcs`, `tpto`, `currency`) route to the correct node
- `RegisterCommands` takes `*Coordinator` for global data access
- Remove `resolvePlayer(gw, ...)` pattern in favor of coordinator-routed execution

**Out of scope:**
- Cross-cell teleportation (tp to a different node triggers entity transfer)
- Changes to pkg/ layer (coordinator already has everything needed)
- Modifying `npcs`, `say`, `grid` commands (already work correctly or are node-local by nature)

## Design

### Two Command Categories

**Data commands** — read-only, no node involvement:
- `players` — reads `activeUsers` map + `PlayerDB` directly on the console goroutine
- No `ExecOnGameLoop` needed. Thread-safe via coordinator's `RWMutex` and `PlayerDB`'s mutex.

**Action commands** — modify game state, routed to correct node:
- `tp`, `damage`, `kill`, `heal`, `give`, `kick`, `spawnnpcs`, `tpto`, `currency`
- Coordinator resolves `username → nodeID` via `activeUsers` map
- Sends closure to that node's `PendingAdminCmds` channel
- Waits for result with timeout

### Consolidated `players` Command

Replaces both `players`/`ps` and `playerdb`/`pdb`.

```
players                    — list online players
players --all / players -a — list all players (including offline)
players <username>         — detailed view for one player
players <username> --live  — real-time ECS data from node
```

**Default list view** (no node involvement):
```
  USERNAME        STATUS   NODE          POSITION              CURRENCY  LAST LOGIN
  xennion         online   node_1_1     (1,1):(4090,4096)     500       2026-04-08 03:50
```

**With `--all`:**
```
  USERNAME        STATUS   NODE          POSITION              CURRENCY  LAST LOGIN
  xennion         online   node_1_1     (1,1):(4090,4096)     500       2026-04-08 03:50
  oldplayer       offline  —            (0,2):(100,200)        0         2026-04-07 22:15
```

**Data sources:**
- Status: `coordinator.activeUsers[username]` → "online" with nodeID, else "offline"
- Position, currency, last login: `PlayerDB.Get(username)` (shared, thread-safe)
- Node: from `activeUsers` map value

**Single player detail view** (`players xennion`):
Shows the same data as `playerdb <username>` did — position, currencies, cargo, bank, equipment, created/last login dates. All from `PlayerDB` (no node involvement). Status from `activeUsers`.

**Live flag** (`players xennion --live`):
Reaches into the player's node via `PendingAdminCmds` to show real-time ECS data: current HP/shield, live position, active cargo mass, current velocity. Only works for online players.

### Action Command Routing

#### `execOnPlayerNode` helper

```go
func execOnPlayerNode(
    coord *Coordinator,
    allNodes []NodeInfo,
    username string,
    fn func(gw *GameWorld, sess *PlayerSession) string,
) string
```

1. Look up `nodeID := coord.ActiveUsers()[username]` (new read-locked accessor)
2. If not found: return `"player not found (offline?)"`
3. Find `NodeInfo` in `allNodes` where `node.ID == nodeID`
4. Send closure to `node.World.Engine.PendingAdminCmds`:
   - Look up session via `gw.Players.ByUsername(username)`
   - Verify session is active and entity is alive
   - Call `fn(gw, sess)` and return result
5. Wait for result with 5s timeout

#### `execOnEntityNode` helper

For commands that target entities by netID (not username). Since the coordinator doesn't track entity→node mapping, this falls back to searching all nodes:

```go
func execOnEntityNode(
    allNodes []NodeInfo,
    netID uint32,
    fn func(gw *GameWorld, entity ecs.Entity) string,
) string
```

Sends the closure to each node's `PendingAdminCmds` until one finds the entity. This is acceptable for admin commands (infrequent, debugging-only).

### Command Changes

| Command | Before | After |
|---------|--------|-------|
| `players`/`ps` | Iterates `allNodes` from game loop | Reads `activeUsers` + `PlayerDB` directly, no game loop |
| `playerdb`/`pdb` | Reads `PlayerDB` + checks local node for status | **Removed** — merged into `players` |
| `damage <target> <amt>` | `resolvePlayer(gw, target)` on local node | `execOnPlayerNode` or `execOnEntityNode` |
| `kill <target>` | Same | Same routing |
| `tp <target> <x> <y>` | Same | `execOnPlayerNode` / `execOnEntityNode` |
| `tpto <player> <target>` | Same | `execOnPlayerNode` (both players must be on same node for now) |
| `heal <target>` | Same | Same routing |
| `give <player> <res> <amt>` | Same | `execOnPlayerNode` |
| `currency <player> <amt>` | Same | `execOnPlayerNode` |
| `kick <player>` | Same | `execOnPlayerNode` |
| `spawnnpcs <n> <player>` | Same | `execOnPlayerNode` |
| `npcs` | Local node ECS query | Unchanged (node-local, shows NPCs on console's exec node) |
| `say <msg>` | Enqueues chat on local node | Unchanged (chat relays to other nodes via bridge) |
| `grid` | Propagates to all nodes | Unchanged (already cross-node) |

### Coordinator Accessor

Add a read-locked accessor to `Coordinator` so game code can query `activeUsers` without accessing the field directly:

```go
// ActiveUsers returns a snapshot of active usernames and their node IDs.
func (c *Coordinator) ActiveUsers() map[string]string {
    c.mu.RLock()
    defer c.mu.RUnlock()
    result := make(map[string]string, len(c.activeUsers))
    for k, v := range c.activeUsers {
        result[k] = v
    }
    return result
}

// ActiveUserNode returns the nodeID for an active username, or "" if offline.
func (c *Coordinator) ActiveUserNode(username string) string {
    c.mu.RLock()
    defer c.mu.RUnlock()
    return c.activeUsers[username]
}
```

### `RegisterCommands` Signature Change

```go
// Before:
func RegisterCommands(console *mmokit.Console, gw *GameWorld, store mmokit.Store, allNodes []NodeInfo)

// After:
func RegisterCommands(console *mmokit.Console, coord *mmokit.Coordinator, playerDB *PlayerRepo, store mmokit.Store, allNodes []NodeInfo)
```

The `gw` parameter is removed. Commands that need a game world use `execOnPlayerNode` or pick a node from `allNodes`. The `coord` parameter provides `ActiveUserNode()` for routing and `activeUsers` for listing.

`playerDB` is passed explicitly since it's shared and commands read from it directly without going through a game world.

### `allNodes` Staleness

The `allNodes` slice is built once in `OnConsoleReady`. With dynamic cells, nodes can be added/removed. For now this is acceptable — console commands work with nodes that existed at startup. Dynamic cell support is a follow-up concern (the `allNodes` slice would need to be rebuilt on topology changes, or replaced with a live query to `coord.Nodes`).

## Files Changed

| File | Change |
|------|--------|
| `pkg/universe/coordinator.go` | Add `ActiveUsers()` and `ActiveUserNode()` accessors |
| `pkg/mmokit/mmokit.go` | Re-export new accessor methods if needed |
| `internal/game/commands.go` | Rewrite: new signature, `execOnPlayerNode`/`execOnEntityNode` helpers, consolidated `players` command, route all action commands |
| `cmd/server/main.go` | Update `RegisterCommands` call in `OnConsoleReady` |

## Verification

1. Start server with 3x3 grid. Login as player. Verify `players` shows online status and correct node.
2. `tp xennion 100 100` — works even though console runs on a different node.
3. `players --all` — shows offline players from PlayerDB.
4. `players xennion` — shows detailed player info.
5. `damage xennion 10` — routes to correct node, damage applied.
6. `spawnnpcs 5 xennion` — NPCs spawn around player on correct node.
