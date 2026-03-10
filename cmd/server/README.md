# cmd/server

Entry point for the game server. Wires together the engine, game world, systems, and console.

## Startup Sequence

```
1. Create platform config (ListenAddr, TickRate)
2. Create game config (all balance parameters)
3. Create ConnManager (WebSocket connection hub)
4. Create Logger (with default categories enabled)
5. Create spatial Grid (using game config's GridCellSize)
6. Create Engine (ECS world, ConnMgr, Grid, Logger)
7. Create GameWorld (initializes mappers, spawns asteroids + station)
8. Register all 10 systems in execution order
9. Create GameLoop with systems and game hooks
10. Start game loop on goroutine
11. Start WebSocket server on goroutine
12. Set up console with game commands
13. Run console on main goroutine (blocks)
```

## Goroutine Layout

```
Main goroutine          → Console.Run() (stdin readline loop)
Game loop goroutine     → GameLoop.Run() (20Hz fixed timestep)
HTTP server goroutine   → ConnManager.ListenAndServe()
Per-connection          → Conn.readPump() + Conn.writePump()
```

The console communicates with the game loop through `PendingAdminCmds` channel. The network layer communicates through the `Events()` channel and per-connection input buffers.

## Shutdown

Signal handler catches SIGINT/SIGTERM, cancels the context. The game loop, HTTP server, and console all respect context cancellation and shut down cleanly.

## System Registration Order

```go
systems := []engine.System{
    system.NewInputSystem(gw),
    system.NewShipControlSystem(gw),
    system.NewMiningSystem(gw),
    system.NewEconomySystem(gw),
    system.NewCombatSystem(gw),
    system.NewPhysicsSystem(gw),
    system.NewLifetimeSystem(gw),
    system.NewSpatialSystem(gw),
    system.NewDamageSystem(gw),
    system.NewNetworkSystem(gw),
}
```

This order is load-bearing. See `internal/system/README.md` for details on why.

## Default Logger Categories

Enabled at startup: `connect`, `spawn`, `combat`, `kill`, `mining`, `economy`. Toggle at runtime via the console.
