# `pkg/engine`

Low-level ECS runtime shared by every cell. It owns the Ark world, player
sessions, fixed-timestep loop, deferred entity removal, loop-job queue,
profiling, and interactive console foundations.

Game code should normally use the `pkg/mmokit` facade. The universe layer
constructs and wires engines for cells.

## Engine state

```go
cfg := engine.DefaultConfig()
eng := engine.New(cfg, connSender, gameLog)
```

`New` requires the narrow `net.ConnSender` interface, so a cell can use either
the gateway's `*net.ConnManager` or a mesh-backed virtual connection manager.
The engine exposes its ECS world, connection sender, logger, configuration,
tick counter, player manager, profiler, and optional per-cell metrics.

`DefaultConfig` currently uses HTTP `:8080`, UDP `:9000`, and a 20 Hz tick
rate.

### Network IDs and removal

- `SetNetIDBase` installs the range base granted to the cell.
- `NextNetID` atomically returns `base + next`.
- `MarkForRemoval` defers removal until the loop's removal phase.
- `FlushRemovals` records IDs through `GetNetID`, invokes
  `OnEntityRemoved`, and then removes still-live entities from Ark.

The universe `Stage` wires the callbacks used to keep its spatial and network
ID indexes synchronized. Game code should therefore use `Stage`/MMOKIT spawn
and despawn APIs instead of calling `ECS.RemoveEntity` directly.

## Game loop

```go
loop := engine.NewGameLoop(eng, systems, systemNames, hooks)
loop.Run(ctx) // blocks until ctx is cancelled
```

Each tick runs in this order:

1. Increment `Engine.Tick` and call `ClearTickState`.
2. Drain connection events and queued loop jobs.
3. Process pending player sessions and typed client input.
4. Run systems in registration order, calling `AfterSystem` after each one.
5. Call `PreFlush`, clear `RemovedNetIDs`, and flush entity removals.
6. Call `PostFlush` and `PostTick`.
7. Record tick, per-system, and optional cell metrics.

`AfterSystem` is how the universe layer flushes deferred structural commands,
making changes queued by system N visible to system N+1 in the same tick.

`AddSystemLive`, `RemoveSystemLive`, `ReplaceSystemLive`, and `SystemByName`
must be called on the owning loop goroutine. They are primarily used by the
WASM hot-swap path.

## Systems

The runtime-facing interface is intentionally small:

```go
type System interface {
    Update(dt float32)
}
```

`engine.SystemBase` supplies dependency injection for framework systems:
`ECSWorld`, `Engine`, `Init`, query discovery, and query construction.
Game systems should embed the non-generic `mmokit.SystemBase`, which also
provides `Stage` and the deferred command buffer.

`SystemDef` pairs a display name with a factory. Its optional `Configure`
hook runs once when the definition is added to a process; the factory then
creates a fresh system instance for every cell.

## Scheduling work on the loop

Ark state belongs to the cell-loop goroutine. Off-loop callers should use:

```go
err := eng.RunOnLoop(ctx, func() error {
    // Read or mutate this cell's ECS state.
    return nil
})
```

`RunOnLoop` executes inline when called reentrantly from the loop. Otherwise
it queues the closure and waits for completion or context cancellation. Pass a
deadline and call it only while `HasLoopRunning` is true.

`SubmitLoopJob` is the non-blocking, fire-and-forget variant. It returns
`false` when the bounded queue is full. The loop uses an 8 ms soft drain
budget per tick and logs jobs taking more than 5 ms; one slow job can exceed
that budget before the loop stops draining.

## Console

The console is backed by `pkg/cmdsys`; legacy untyped command registration
and `ExecOnGameLoop` are no longer part of the API.

```go
console := engine.NewConsole(gameLog)
err := console.RegisterTyped(cmdsys.Command{
    Verb:        "players.list",
    Capability:  "players.list",
    Description: "list players",
    Route:       cmdsys.RouteLocal,
    Handler:     handler,
})
console.Run(ctx)
```

Use `NewConsoleWithDispatcher` when the console must share a process-owned
registry and distributed dispatcher. Built-ins include contextual `help`,
`quit`, and the `log status/on/off/toggle/only/filter` command group. Commands
that touch a cell route their work through `Engine.RunOnLoop`.

## Related packages

- `pkg/mmokit`: game-facing facade and system helpers
- `pkg/universe`: process, cell, stage, mesh, and engine wiring
- `pkg/query`: bundle-based Ark queries
- `pkg/cmdsys`: typed commands, routing, RBAC, and audit plumbing
