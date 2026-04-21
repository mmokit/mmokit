# pkg/engine

Generic MMO server engine. Knows nothing about any specific game — provides the scaffolding that any real-time multiplayer game needs.

## Engine (`engine.go`)

Central platform state. Holds the ECS world, connection manager, logger, tick counter, and entity removal queue. The engine has no spatial dependency — spatial indexing is game-layer concern (e.g. `GameWorld.Grid`).

```go
eng := engine.New(cfg, connMgr, gameLog)
```

**Key methods:**

| Method | Description |
|--------|-------------|
| `NextNetID() uint32` | Allocates a unique network entity ID (atomic, safe from any goroutine) |
| `MarkForRemoval(entity)` | Queues an entity for deletion at end of tick |
| `FlushRemovals(getNetID)` | Deletes all queued entities. The callback lets the game provide NetworkID lookup without the engine importing game types. Fires `OnEntityRemoved(entity)` for each one — WorldBase uses this to clear the per-cell netID index |
| `RunOnLoop(ctx, fn)` | Posts a closure to run on the game-loop goroutine. Detects on-loop reentrance (goroutine-ID check) and runs inline when the caller is already on the loop. Off-loop callers queue into a bounded channel drained each tick with an 8ms budget. Replaces the old `PendingAdminCmds` channel for admin + cross-goroutine ECS access |

**Fields the game accesses directly (via embedding):**

- `ECS *ecs.World` — the Ark ECS world
- `ConnMgr *net.ConnManager` — WebSocket connection manager
- `Log *logger.Logger` — category-based debug logger
- `Tick uint32` — current tick number
- `RemovedNetIDs []uint32` — network IDs removed this tick (for client notifications)
- `OnEntityRemoved func(ecs.Entity)` — optional hook invoked during `FlushRemovals` for each removed entity. Used by WorldBase to Deregister from the spatial grid and Exit from the netID index

## Game Loop (`loop.go`)

Fixed-timestep tick loop. The game injects behavior via a `Hooks` struct rather than the engine knowing about game types.

```go
loop := engine.NewGameLoop(eng, systems, hooks)
go loop.Run(ctx)
```

**Tick order (every 50ms at 20Hz):**

1. `ClearTickState()` — reset per-tick queues
2. Process connect/disconnect events → `OnConnect` / `OnDisconnect`
3. Drain admin commands from console
4. Engine-internal login processing (`PlayerManager.processPendingSessions`)
5. Run all systems in registration order
6. `PreFlush()` — pre-removal notifications
7. `FlushRemovals(GetNetID)` — delete entities, capture removed IDs
8. `PostFlush()` — post-removal work (spawns, state changes)

**Hooks struct:**

```go
type Hooks struct {
    OnConnect      func(connID uint32)
    OnDisconnect   func(connID uint32)
    PreFlush       func()
    PostFlush      func()
    ClearTickState func()
    PostTick       func()
}
```

The game implements these as methods on its world struct and returns them from a `Hooks()` method.

## System Interface (`system.go`)

```go
type System interface {
    Update(dt float32)
}
```

Systems capture their game world pointer at construction time. The engine calls `Update(dt)` without passing any world reference, keeping the engine type-agnostic.

## Console (`console.go`)

Interactive CLI framework. Handles stdin reading, command dispatch, and logger toggle shortcuts.

```go
console := engine.NewConsole(eng, gameLog)
game.RegisterCommands(console, gw)  // game adds its commands
console.Run(ctx)                     // blocks on main goroutine
```

The console reads available log categories dynamically from the logger via `gameLog.Categories()` — no hardcoded category list.

**Built-in platform commands:** `help`, `status`, `on`, `off`, `toggle`, `only`, `quit`

**Game command registration:**

```go
console.Register(engine.Command{
    Name:    "players",
    Aliases: []string{"ps"},
    Fn: func(args []string) {
        result := console.ExecOnGameLoop(func() string {
            // safely access game state on the game loop goroutine
            return "..."
        })
        fmt.Print(result)
    },
})
```

`ExecOnGameLoop` sends a closure through `PendingAdminCmds` and blocks until the game loop executes it — this is how console commands safely read/write ECS state.

The game sets `console.PrintGameHelp` to a function that prints its help sections.

Any unrecognized command is tried as a logger category toggle shortcut (prefix match).

## Config (`config.go`)

Platform-level configuration. Only holds values the engine itself needs.

```go
type Config struct {
    ListenAddr string  // default ":8080"
    TickRate   int     // default 20
}
```

Game-specific balance parameters live in the game's own config struct.

## StateWriter (`persist.go`)

Interface for future async persistence. Defined but not yet wired in.

```go
type StateWriter interface {
    Write(key string, data []byte) error
    Flush(ctx context.Context) error
    Close() error
}
```

## AdminCmd

```go
type AdminCmd struct {
    Fn func()
}
```

A closure to execute on the game loop goroutine. The game captures its own world pointer in the closure — the engine never sees game types.
