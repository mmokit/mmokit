# `pkg/engine`

Low-level ECS runtime shared by every cell. It owns the Ark world, player
sessions, fixed-timestep loop, deferred entity removal, loop-job queue,
profiling, and interactive console foundations.

Game code should normally use the `mmokit` facade. The universe layer
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

### Tick rates quantize to whole milliseconds

The scheduled tick period, the timestep handed to systems, and the
cluster-clock quantum are one number, derived once from `Config.TickRate`.
It is a whole number of milliseconds because `ClusterClock.ClusterTick`
divides a millisecond wall clock by it and `TickTime` multiplies it back to
stamp outbound replication — the tick stream and the stamp grid have to be the
same grid, or an authority handoff shows up on the client as interpolation
jitter.

So only rates that divide 1000 are exact. Others round to the nearer
achievable period and the startup log names the requested and effective rates
separately: 60 Hz schedules 17 ms and runs at 58.8 Hz. Rounding closes that
error rather than eliminating it — 120 Hz still runs at 125 Hz, because 8 ms
is the only period near it. A rate at or below zero falls back to 50 ms, and a
rate above 1000 clamps to 1 ms.

### Network IDs and removal

- `SetNetIDBase` installs the range base granted to the cell.
- `NextNetID` atomically returns `base + next`.
- `MarkForRemoval` defers authoritative destruction until the loop's removal
  phase; local replica/ghost teardown uses `RemovalLocalOnly`.
- Every engine-mediated removal runs the Stage's mandatory spatial/netID
  cleanup before removing the Ark row. `OnEntityRemoved` is an optional
  observer, not the cleanup mechanism.
- `SampleRemovedNetIDs` freezes the current tombstone batch for replication;
  later-system removals roll into the next tick instead of being cleared.

The universe `Stage` wires the callbacks used to keep its spatial and network
ID indexes synchronized. Game code should therefore use `Stage`/MMOKIT spawn
and despawn APIs instead of calling `ECS.RemoveEntity` directly.

## Game loop

```go
loop := engine.NewGameLoop(eng, systems, systemNames, hooks)
loop.Run(ctx) // blocks until ctx is cancelled
```

Each tick runs in this order:

1. Increment `Engine.Tick`, advance the removal-publication phase, and call
   `ClearTickState`.
2. Drain connection events and queued loop jobs.
3. Process pending player sessions and typed client input.
4. Run systems in registration order, calling `AfterSystem` after each one.
5. Call `PreFlush(dt)` and flush queued entity removals.
6. Call `PostFlush` and `PostTick`.
7. Record tick, per-system, and optional cell metrics.

`PreFlush` receives the loop's own `dt`, which is how the universe layer fires
`mmokit.OnWorldTick` and friends on the same timestep the systems integrated.

When `ctx` is cancelled the loop drains the loop-job queue before returning,
failing everything still queued with `ErrLoopStopped`.

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
it queues the closure and waits. The contract:

- A loop that has **not started yet** still accepts jobs and runs them once it
  does. Cells schedule work into that window deliberately.
- A loop that has **exited** returns `ErrLoopStopped` immediately, and so does
  every job still queued when it exits. `HasLoopRunning` cannot tell those two
  states apart — it reads false for both — so do not gate on it to decide
  whether a job will run.
- If the caller's context expires while the job is still queued, the job is
  abandoned and **never runs**.
- If the loop has already started the job, the caller waits for it to finish
  even past its own deadline. The deadline is soft by at most one job's
  runtime, so that a caller cannot tear down state a running closure is still
  writing.

`SubmitLoopJob` is the non-blocking, fire-and-forget variant. It returns `nil`
when queued or run inline, `ErrLoopQueueFull` when the bounded queue is full,
and `ErrLoopStopped` when the loop has exited.

The drain budget is derived from the tick period rather than fixed: a quarter
of the frame, capped at 8 ms and floored at 1 ms, with a slow-job warning at
half of that. One slow job can exceed the budget before the loop stops
draining, because the budget is checked after a job runs — so at least one job
always makes progress per tick.

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

- `mmokit`: game-facing facade and system helpers
- `pkg/universe`: process, cell, stage, mesh, and engine wiring
- `pkg/query`: bundle-based Ark queries
- `pkg/cmdsys`: typed commands, routing, RBAC, and audit plumbing
