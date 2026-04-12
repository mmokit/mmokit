# internal/game

Game-specific logic for the space MMO. This package consumes the generic `pkg/engine` and wires in all game behavior through hooks, systems, and admin commands.

## GameWorld (`world.go`)

The central game state struct. Embeds `*mmokit.WorldBase` so all engine fields and methods (ECS, ConnMgr, Log, Tick, MarkForRemoval, NextNetID, etc.) are accessible directly. Game-specific state like the spatial grid lives here, not on the engine.

```go
type GameWorld struct {
    *mmokit.WorldBase
    Grid   *spatial.Grid
    Config GameConfig
    // ... all Ark mappers, player tracking maps, event queues
}
```

**Key state:**

| Field | Description |
|-------|-------------|
| `PlayerEntities` | `connID → ecs.Entity` for alive players |
| `NetIDToEntity` | `netID → ecs.Entity` rebuilt each tick by SpatialSystem |
| `ConnToUsername` | `connID → username` for active connections |
| `DeadPlayers` | Set of connIDs awaiting respawn |
| `PendingDeaths` | Death notifications to send this tick |
| `PendingLootDrops` | Cargo to spawn as loot crates after entity removal |
| `PendingChat` | Chat messages to broadcast this tick |
| `PlayerDB` | In-memory persistent player data |

**Key methods:**

- `UsernameInUse(username) bool` — checks for duplicate logins
- `SavePlayerState(connID, entity)` — persists position/inventory to PlayerDB
- `MarkPlayerDeath(entity, killerNetID)` — records death, captures loot, queues removal

## Entity Kinds (`entity_kinds.go`)

`initEntityKinds(gw)` registers all entity kind definitions via `gw.RegisterEntityKind()`. Each kind definition uses `mmokit.KindComponent()` to declare components that are serialized on transfer and replicated over the network, and `mmokit.KindComponentLocalOnly()` for components added locally after transfer that are not serialized. The network system auto-discovers replicators from these definitions via `BuildReplicators()`.

## Constructor (`game.go`)

```go
gw := game.NewGameWorld(base, gameCfg, playerDB)
```

`NewGameWorld` accepts a `*mmokit.WorldBase` (pre-wired by the coordinator), game config, and player database. It initializes all Ark mappers and player tracking maps. When `base.FromSplit()` is false (normal startup), `Init()` spawns initial asteroids and the trade station. When `base.FromSplit()` is true (world created by dynamic cell split), `Init()` skips initial entity spawning since entities are transferred from the parent cell.

## Entity Factories (`entity_*.go`)

Each entity type has its own file containing spawn functions. Spawn functions call `gw.SpawnEntity()` with `mmokit.WithComponents()` to auto-add all components registered on the entity kind, plus any override options. Kind-specific component initialization (e.g., health values, collider radius) is applied after spawning.

| Method                                    | File                  |
|-------------------------------------------|-----------------------|
| `SpawnPlayer(connID)`                     | `entity_ship.go`      |
| `SpawnStation()`                          | `entity_station.go`   |
| `SpawnLootCrate(x, y, resources)`         | `entity_lootcrate.go` |
| `SpawnAsteroid(...)` / `spawnAsteroids()` | `entity_asteroid.go`  |
| `SpawnNPC(...)`                           | `entity_npc.go`       |

`SpawnPlayer` restores saved position/inventory from PlayerDB if the player has logged in before, otherwise random-spawns near the station. It also sends the `PlayerSpawnedMsg` to the client.

## Lifecycle Hooks (`lifecycle.go`)

These methods are called by the engine's game loop at specific points in the tick:

| Hook | What it does |
|------|-------------|
| `onConnect(connID)` | Adds to PendingConnections, logs |
| `onDisconnect(connID)` | Saves player state, removes entity immediately, cleans up all maps |
| `processPendingSessions()` | Processes pending sessions from entity transfers and coordinator-assigned players; transitions to Active |
| `processDeaths()` | Sends PlayerDiedMsg to each dead player's client, moves them to DeadPlayers set |
| `postFlush()` | Spawns loot crates from PendingLootDrops, processes respawn requests |
| `clearTickState()` | Resets PendingDeaths slice |
| `getNetID(entity) (uint32, bool)` | Returns NetworkID for FlushRemovals callback |

**Important:** `onDisconnect` removes the entity immediately (not through MarkForRemoval) and appends the netID to RemovedNetIDs directly. This ensures disconnected entities are cleaned up in the same tick.

## Admin Commands (`commands.go`)

`RegisterCommands(console, coord, playerDB, store, allCells)` registers all game-specific console commands. The coordinator's `ActiveUserCell()` routes player-targeting commands to the correct cell. Data commands (like `players`) read from the shared `PlayerDB` and coordinator's `activeUsers` map without involving any game loop.

**Helper functions:**

- `execOnPlayerCell(coord, allCells, username, fn)` — finds the cell hosting a player via `coord.ActiveUserCell()`, executes `fn` on that cell's game loop
- `execOnEntityCell(allCells, targetArg, fn)` — finds an entity by netID across all cells, executes `fn` on the owning cell
- `resolveEntity(gw, input)` — finds any entity by network ID within a single node (used as fallback inside closures)
- `resolveResource(input)` — maps `"ore"`, `"crystal"`, `"gas"`, `"metal"` (prefix match) to resource index

**`debug` command** toggles the topology debug overlay on all connected clients, sending `SE_CELL_TOPOLOGY` events with cell boundaries, depths, and cell ownership.

**Consolidated `players` command** replaces both `players`/`ps` and `playerdb`/`pdb`:

- `players` — list online players (coordinator data, no game loop)
- `players --all` — include offline players from PlayerDB
- `players <username>` — detailed player info
- `players <username> --live` — real-time ECS data from player's node

## Game Config (`config.go`)

All tunable balance parameters. Separate from engine config (which only has ListenAddr and TickRate).

```go
cfg := game.DefaultGameConfig()
```

Supports reflection-based `GetField`/`SetField` for runtime tweaking via console `config`/`set` commands. Array fields (like SellPrices) are not settable through this interface.

Values are copied into components at spawn time (e.g., ShieldRegenRate), so config changes only affect newly spawned entities.

## PlayerDB (`playerdb.go`)

Simple in-memory key-value store for persistent player data (keyed by lowercase username).

```go
pdata := gw.PlayerDB.GetOrCreate("alice")
pdata.Flux += earned
pdata.HasSave = true
```

Stores: username, flux balance, last position (X, Y), cargo (Resources [4]), and whether the player has a saved position.

Survives disconnect and death. On death, Resources and HasSave are cleared so the player respawns near the station with empty cargo.
