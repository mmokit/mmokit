# internal/game

Game-specific logic for the space MMO. This package consumes the generic `pkg/engine` and wires in all game behavior through hooks, systems, and admin commands.

## GameWorld (`world.go`)

The central game state struct. Embeds `*engine.Engine` so all engine fields and methods (ECS, ConnMgr, Grid, Log, Tick, MarkForRemoval, NextNetID, etc.) are accessible directly.

```go
type GameWorld struct {
    *engine.Engine
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

## Constructor & Hooks (`game.go`)

```go
gw := game.NewGameWorld(eng, gameCfg)
hooks := gw.Hooks()
```

`NewGameWorld` initializes all Ark mappers, player tracking maps, and spawns initial asteroids + trade station.

`Hooks()` returns an `engine.Hooks` struct wired to the lifecycle methods in `lifecycle.go`.

## Entity Factories (`spawn.go`)

All entity creation goes through these methods:

| Method | Components Created |
|--------|-------------------|
| `SpawnPlayer(connID)` | Position, Velocity, Rotation, Collider, NetworkID, EntityKind, ShipControl, Health, Shield, Weapon, Inventory, PlayerConn, PlayerInput, MiningLaser |
| `SpawnProjectile(owner, x, y, angle, speed, damage, lifetime)` | Position, Velocity, Rotation, Collider, NetworkID, EntityKind, Projectile, Lifetime, Owner |
| `SpawnStation()` | Position, Velocity, Rotation, Collider, NetworkID, EntityKind, Station |
| `SpawnLootCrate(x, y, resources)` | Position, Velocity, Rotation, Collider, NetworkID, EntityKind, Inventory, Lifetime, LootCrate |
| `spawnAsteroids()` | Position, Velocity, Rotation, Collider, NetworkID, EntityKind, Minable |

`SpawnPlayer` restores saved position/inventory from PlayerDB if the player has logged in before, otherwise random-spawns near the station. It also sends the `PlayerSpawnedMsg` to the client.

## Lifecycle Hooks (`lifecycle.go`)

These methods are called by the engine's game loop at specific points in the tick:

| Hook | What it does |
|------|-------------|
| `onConnect(connID)` | Adds to PendingConnections, logs |
| `onDisconnect(connID)` | Saves player state, removes entity immediately, cleans up all maps |
| `processLogins()` | Drains input from pending connections looking for LoginMsg, validates username uniqueness, spawns player |
| `processDeaths()` | Sends PlayerDiedMsg to each dead player's client, moves them to DeadPlayers set |
| `postFlush()` | Spawns loot crates from PendingLootDrops, processes respawn requests |
| `clearTickState()` | Resets PendingDeaths slice |
| `getNetID(entity) (uint32, bool)` | Returns NetworkID for FlushRemovals callback |

**Important:** `onDisconnect` removes the entity immediately (not through MarkForRemoval) and appends the netID to RemovedNetIDs directly. This ensures disconnected entities are cleaned up in the same tick.

## Admin Commands (`commands.go`)

`RegisterCommands(console, gw)` registers all game-specific console commands. Each command captures `gw` in its closure and uses `console.ExecOnGameLoop()` to safely access ECS state.

Also sets `console.PrintGameHelp` for the help display.

**Helper functions:**

- `resolvePlayer(gw, input)` — finds player by numeric connID or username prefix (case-insensitive)
- `resolveResource(input)` — maps `"ore"`, `"crystal"`, `"gas"`, `"metal"` (prefix match) to resource index

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
