# Connection Proxy Architecture & DefaultNode Removal

## Context

The Coordinator currently routes all new WebSocket connections to a single "DefaultNode" — the node owning `Config.DefaultCell`. This creates a conceptual bottleneck where one node is treated as special. It conflates four independent concerns (login routing, respawn routing, console hosting, game world access) into a single node dependency. With dynamic cell partitioning, this becomes untenable: the default cell can be split, and no single node should be privileged.

Industry-standard MMO architecture separates connection management from game simulation via a proxy/gateway layer. Clients connect to a proxy that handles authentication and routing, then hands off authenticated sessions to the appropriate game server. This is the pattern used by Star Citizen's Replication Layer, SpatialOS's Runtime, and most production MMOs.

This spec removes the DefaultNode concept and introduces a connection proxy layer within the Coordinator, a LoginService for authentication, and a PlayerRouter for node assignment — all designed as in-process services that can be extracted to separate processes later.

## Scope

**In scope:**
- Remove `DefaultCell`, `DefaultNode()`, `findDefaultNode()` from Coordinator
- Move login processing from per-node PlayerManager to Coordinator-level proxy
- Add `LoginService` (in-process, extractable) for login handling
- Add `PlayerRouter` callback for game-driven node assignment
- Replace `RequestSpawnOnNode()` (hardcoded to DefaultNode) with explicit-target spawning
- Decouple Console from DefaultNode's Engine
- Update space game (`internal/game/`) and examples to use new APIs
- Global username deduplication (across all nodes, not just per-node)
- Reconnection routing (find lingering session's node, route there)

**Out of scope:**
- Dynamic cell partitioning integration (follow-up spec)
- External login service / database-backed auth (future work, but API designed for it)
- Load-balanced connection distribution (future — currently all logins route to one destination per player)

## Architecture

### Current Flow
```
WebSocket → ConnManager → Coordinator.routeEvents()
                                ↓
                          findDefaultNode() → DefaultNode
                                ↓
                          DefaultNode.PlayerManager.processLogins()
                                ↓
                          Parse login message, validate username
                                ↓
                          Transition(StateActive) → SpawnPlayer()
```

### New Flow
```
WebSocket → ConnManager → Coordinator (proxy layer)
                                ↓
                          Buffer connection in pendingConns
                                ↓
                          Coordinator.processLogins() each tick:
                            - Drain input messages for pending conns
                            - Call LoginService.TryLogin(connID, msgs)
                            - On success: get username
                                ↓
                          Check reconnection:
                            - Scan all nodes for lingering session by username
                            - If found: route to that node (reconnect path)
                                ↓
                          Check duplicate username:
                            - Scan all nodes for active session by username
                            - If found: reject login
                                ↓
                          Call PlayerRouter(username) → nodeID
                                ↓
                          Route player to target node:
                            - setPlayerNode(connID, nodeID)
                            - Send connect event + username to node
                            - Node.PlayerManager creates session in StateActive
```

### Key Difference
The node's PlayerManager no longer parses login messages. It receives pre-authenticated sessions with a username already set. The `ProcessLogins` hook is replaced with a simpler `OnPlayerAssigned` hook that creates a session and transitions to Active.

## Components

### 1. LoginService

Lives in `pkg/universe/login.go`. Handles login message parsing, separated from session management.

```go
// LoginHandler parses login messages and returns the username.
// Returns ErrLoginPending if no valid login message found yet.
// Returns other errors for rejected logins.
type LoginHandler func(connID uint32, messages [][]byte) (username string, err error)

// LoginService manages pre-node login processing.
type LoginService struct {
    handler        LoginHandler
    onRejected     func(connID uint32, reason string)
    pendingConns   map[uint32]*pendingConn  // connID -> buffered state
    loginTimeout   time.Duration            // max time before auto-disconnect
}

type pendingConn struct {
    connID    uint32
    createdAt time.Time
}
```

The game provides `LoginHandler` — same closure as today's `SetLoginHandler`, just operating on raw messages instead of draining from ConnManager directly.

**Why a separate struct:** This isolates login logic so it can later move to a separate service (e.g., HTTP-based auth with session tokens). The interface is `(connID, messages) -> (username, error)` — protocol-agnostic enough for future extraction.

### 2. PlayerRouter

Callback on Coordinator. Called after successful login to determine target node.

```go
// PlayerRouter determines which node should host a player.
// Called after successful login. Returns a nodeID.
type PlayerRouter func(username string) string
```

The Coordinator provides helpers for the game to use in its router:

```go
// NodeAtPosition returns the nodeID that owns the given world position.
// Handles dynamic cells — always finds the correct subcell.
func (c *Coordinator) NodeAtPosition(worldX, worldY float32) string
```

**Space game implementation:**
```go
coord.SetPlayerRouter(func(username string) string {
    if pdata := playerDB.Get(username); pdata != nil {
        worldX := float32(pdata.CellX)*coords.CellSize + pdata.X
        worldY := float32(pdata.CellY)*coords.CellSize + pdata.Y
        return coord.NodeAtPosition(worldX, worldY)
    }
    // New player — spawn at station position
    stationWorldX := float32(gameCfg.StationCell.CellX)*coords.CellSize + coords.CellSize/2
    stationWorldY := float32(gameCfg.StationCell.CellY)*coords.CellSize + coords.CellSize/2
    return coord.NodeAtPosition(stationWorldX, stationWorldY)
})
```

### 3. Reconnection Logic

Moved from per-node PlayerManager to Coordinator. When a player logs in:

1. Coordinator checks all nodes for a `StateDisconnected` session with matching username
2. If found: route new connection to that node (reconnect to lingering session)
3. If not found: proceed with PlayerRouter for fresh assignment

This fixes the existing bug where reconnection only works if the player happens to reconnect to DefaultNode (the same node that owns the lingering session).

**Implementation:** Coordinator needs a way to query sessions across all nodes. Options:

- **Option A (rejected):** Each node's PlayerManager exposes `FindDisconnectedByUsername(username) *PlayerSession`. Coordinator iterates all nodes on the coordinator goroutine, calling each via `ExecOnGameLoop`.
  - Problem: ExecOnGameLoop is async with timeout. Too slow for login flow.
- **Option B (chosen):** Maintain a coordinator-level `disconnectedSessions map[string]string` (username -> nodeID). Updated when PlayerManager transitions to/from StateDisconnected via a callback.
  - Fast O(1) lookup. Callback keeps it in sync.
  - This is the approach.

```go
// In Coordinator:
disconnected map[string]string // username -> nodeID (lingering sessions)
activeUsers  map[string]string // username -> nodeID (active sessions, for dupe detection)

// Callback wired into each node's PlayerManager:
onSessionActive: func(username, nodeID string) {
    c.mu.Lock()
    c.activeUsers[username] = nodeID
    delete(c.disconnected, username)
    c.mu.Unlock()
}
onSessionDisconnected: func(username, nodeID string) {
    c.mu.Lock()
    c.disconnected[username] = nodeID
    delete(c.activeUsers, username)
    c.mu.Unlock()
}
onSessionRemoved: func(username string) {
    c.mu.Lock()
    delete(c.disconnected, username)
    delete(c.activeUsers, username)
    c.mu.Unlock()
}
```

Duplicate username detection becomes a simple `activeUsers[username]` check — O(1), global across all nodes, no need to scan each node.

### 4. Coordinator Changes

#### Removed
- `Config.DefaultCell` field
- `Coordinator.defaultCell` field
- `Coordinator.DefaultNode()` method
- `Coordinator.DefaultCell()` method
- `Coordinator.findDefaultNode()` method

#### Added
- `Config.LoginHandler LoginHandler` — required, replaces per-node SetLoginHandler
- `Config.LoginRejectedHandler func(connID uint32, reason string)` — optional
- `Config.LoginTimeout time.Duration` — default 30s
- `Coordinator.SetPlayerRouter(PlayerRouter)` — required before Start()
- `Coordinator.NodeAtPosition(worldX, worldY float32) string` — helper
- `Coordinator.loginService *LoginService` — internal
- `Coordinator.disconnected map[string]string` — reconnection tracking
- `Coordinator.processLogins()` — called on coordinator goroutine each tick

#### Modified: `routeEvents()`
```go
// Before: route to DefaultNode
if evt.Connected {
    defaultNode := c.findDefaultNode()
    c.setPlayerNode(evt.ConnID, defaultNode.ID)
    defaultNode.Events <- evt
}

// After: buffer connection, let processLogins handle routing
if evt.Connected {
    c.loginService.AddPending(evt.ConnID)
    // sendServerConfig (tick rate, etc.) moves from PlayerManager to Coordinator.
    // It only needs ConnManager + Config, no Engine dependency.
    c.sendServerConfig(evt.ConnID)
} else {
    // Disconnect routing unchanged — uses playerNode map
    nodeID := c.getPlayerNode(evt.ConnID)
    if nodeID != "" {
        if node, ok := c.getNode(nodeID); ok {
            node.Events <- evt
        }
        c.removePlayerNode(evt.ConnID)
    } else {
        // Player was still in pending login — just remove
        c.loginService.RemovePending(evt.ConnID)
    }
}
```

#### New: `processLogins()` on Coordinator goroutine
Runs on a ticker (same rate as game loop, e.g., 20Hz). For each pending connection:
1. Drain messages from ConnManager
2. Call `LoginHandler(connID, msgs)` → `(username, err)`
3. If `ErrLoginPending`: continue (try next tick)
4. If error: call `LoginRejectedHandler`, disconnect
5. If success:
   a. Check `disconnected[username]` — if found, route to that nodeID (reconnect)
   b. Check all nodes for active session with same username — if found, reject (duplicate)
   c. Call `PlayerRouter(username)` → nodeID
   d. `setPlayerNode(connID, nodeID)`
   e. Send `PlayerAssignment{ConnID, Username, IsReconnect}` to target node's Events channel
   f. Node's `OnPlayerAssigned` hook creates session and transitions to Active (or reconnects)

The coordinator goroutine is single-threaded (no lock contention for login processing). The `disconnected` map is only written from node callbacks (need lock) and read from coordinator goroutine (need lock).

### 5. PlayerManager Changes

#### Removed
- `SetLoginHandler()` — login parsing moves to Coordinator
- `processLogins()` — no longer needed per-node
- `ProcessLogins` hook — removed from engine hooks

#### Modified
- `OnConnect` hook now receives username alongside connID
- Session created in `StatePending` briefly, then immediately transitioned to `StateActive`
- Reconnection logic for disconnected sessions stays in PlayerManager (node receives reconnect event with connID + username, matches to lingering session)

#### New hook: `OnPlayerAssigned`
Called when the Coordinator assigns an authenticated player to this node.
```go
type PlayerAssignment struct {
    ConnID   uint32
    Username string
    IsReconnect bool
}
```

When `IsReconnect` is true, PlayerManager finds the lingering `StateDisconnected` session, assigns the new connID, and transitions back to `PriorState`. When false, creates a new session and transitions to `StateActive`.

### 6. RequestSpawnOnNode Replacement

`RequestSpawnOnNode()` currently hardcodes routing to DefaultNode. Replace with:

```go
// RequestRespawn asks the coordinator to respawn a player.
// The coordinator calls PlayerRouter to determine the target node.
func (b *nodeBridge) RequestRespawn(connID uint32, username string)
```

The bridge sends a `MsgRespawnRequest` to the coordinator's inbox (a new channel on the Coordinator struct, drained in `routeEvents`). The coordinator calls `PlayerRouter(username)` to determine the destination node, then sends `MsgSpawnTransfer` to that node and updates `playerNode`. This keeps the game's spawn logic in the router callback where it belongs.

**Alternative:** `RequestSpawnAt(connID, username, nodeID string)` — game determines target node itself. Simpler, but game needs access to coordinator to resolve nodeID. The bridge already has `coord` reference, so game could call `bridge.Coordinator().NodeAtPosition(x, y)`.

Going with `RequestRespawn` — cleaner separation. The router centralizes all "where does this player go?" logic.

### 7. Console Decoupling

Console currently takes `*Engine` for:
- `ExecOnGameLoop()` — runs closures on game loop (for thread-safe ECS access)
- `Perf` — performance profiling
- `Metrics` — node metrics

**Change:** Console takes an `ExecFunc` instead of `*Engine`. The Coordinator provides a default that picks a node contextually.

```go
// Before:
c.console = engine.NewConsole(defaultNode.Engine, c.Log)

// After:
c.console = engine.NewConsole(c.Log)
```

Console commands that need ECS access (e.g., `spawn`, `entity list`) already use `ExecOnGameLoop()` from specific nodes via the node builtins. The console doesn't need a default engine — each command knows which node to target.

For commands that currently run on "the" engine (like `perf`): these become node-scoped. `perf` shows perf for a specific node, defaulting to... well, we need to handle this. Options:
- `perf` shows aggregated stats across all nodes
- `perf` requires a node argument: `perf node_0_0`
- `perf` shows the first node (arbitrary but functional)

Going with: `perf` shows aggregated/all-nodes view by default, `perf <nodeID>` shows a specific node. Same for `metrics`.

**Console constructor change:**
```go
func NewConsole(log *logger.Logger) *Console
```

Remove `engine *Engine` field. Commands that need an engine get it from their closure (node-specific builtins already work this way).

## Files Changed

### pkg/ layer
| File | Change |
|------|--------|
| `pkg/universe/coordinator.go` | Remove DefaultCell/DefaultNode, add LoginService/PlayerRouter/processLogins, modify routeEvents, modify startConsole |
| `pkg/universe/login.go` | **New file** — LoginService, LoginHandler type, pendingConn management |
| `pkg/universe/bridge.go` | Replace `RequestSpawnOnNode` with `RequestRespawn` in interface |
| `pkg/universe/node_bridge_impl.go` | Implement `RequestRespawn` — sends to coordinator, coordinator calls router |
| `pkg/engine/player_manager.go` | Remove processLogins/SetLoginHandler, add OnPlayerAssigned hook |
| `pkg/engine/console.go` | Remove `engine *Engine` field, update NewConsole signature |
| `pkg/engine/builtins.go` | Update perf/metrics to work without default engine |
| `pkg/mmokit/mmokit.go` | Update exported types/facades |
| `pkg/universe/universe_test.go` | Update tests for new APIs |

### Game layer
| File | Change |
|------|--------|
| `cmd/server/main.go` | Remove DefaultCell from config, add LoginHandler + PlayerRouter, update console setup |
| `internal/game/game.go` | Remove SetLoginHandler call (moves to main.go as LoginHandler) |
| `internal/game/lifecycle.go` | Replace `RequestSpawnOnNode` with `RequestRespawn` |
| `internal/game/factory.go` | No longer sets up login handler per-node |
| `internal/game/commands.go` | Update RegisterCommands for lazy node access |

### Examples
| File | Change |
|------|--------|
| `examples/4node-basic/main.go` | Remove DefaultCell, add LoginHandler + PlayerRouter |
| `examples/4node-basic/world.go` | Remove login handler setup from world Init |
| `examples/slither/main.go` | Remove DefaultNode() access, add LoginHandler + PlayerRouter |

## Migration Path

### For game developers using mmokit:

**Before:**
```go
coord := mmokit.NewCoordinator(mmokit.Config{
    DefaultCell: mmokit.CellID{X: 1, Y: 1},
    // ...
})

// Login handler set per-world in factory:
gw.Players.SetLoginHandler(func(s, pm) error { ... })
```

**After:**
```go
coord := mmokit.NewCoordinator(mmokit.Config{
    LoginHandler: func(connID uint32, msgs [][]byte) (string, error) {
        // Parse login message, return username
    },
    // ...
})

coord.SetPlayerRouter(func(username string) string {
    return coord.NodeAtPosition(spawnX, spawnY)
})
```

### Breaking changes:
- `Config.DefaultCell` removed — callers must remove this field
- `DefaultNode()` removed — callers must use coordinator-level APIs
- `SetLoginHandler()` removed from PlayerManager — must move to Config.LoginHandler
- `RequestSpawnOnNode()` renamed to `RequestRespawn()` on NodeBridge interface
- `NewConsole()` signature changed — no longer takes `*Engine`

## Verification

1. **Single-node:** Start space game with 1x1 grid. Player logs in, spawns correctly. Disconnect + reconnect within grace period. Verify reconnection works.
2. **Multi-node:** Start space game with 3x3 grid. Player logs in, routed to station cell node. Cross cell boundary, disconnect, reconnect — should route to node with lingering session.
3. **Duplicate username:** Two connections try same username — second rejected globally (not just per-node).
4. **Login timeout:** Connection that never sends login is disconnected after LoginTimeout.
5. **Console:** `perf`, `node list`, `cell list`, `spawn` all work without DefaultNode. `perf` shows all-nodes view.
6. **Respawn:** Player dies on non-station node. `RequestRespawn` routes to station cell via PlayerRouter.
7. **Examples:** 4node-basic and slither work with new API.

## Sources

Architecture research:
- [MMO Architecture: client connections, sockets, threads](https://prdeving.wordpress.com/2023/10/13/mmo-architecture-client-connections-sockets-threads-and-connection-oriented-servers/)
- [Server-Side MMO Architecture - IT Hare](http://ithare.com/chapter-via-server-side-mmo-architecture-naive-and-classical-deployment-architectures/)
- [Star Citizen Server Meshing Wiki](https://starcitizen.tools/Server_meshing)
- [SC Live Tech Talk: Server Meshing 2026](https://www.starshipdealers.com/blog/sc-live-tech-talk-server-meshing-2026/)
- [SpatialOS Worker Design](https://docs.improbable.io/reference/13.0/shared/design/design-workers)
- [Server Architecture: A Noobs Guide](https://www.gamedeveloper.com/programming/server-architecture-a-noobs-guide)
