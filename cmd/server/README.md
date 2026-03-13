# cmd/server

Entry point for the game server. Wires together the engine, game world, systems, and console.

## Startup Sequence

```
1. Create platform config (ListenAddr, TickRate)
2. Create game config (all balance parameters)
3. Create ConnManager (WebSocket connection hub)
4. Create Logger (with default categories enabled)
5. Register game log categories on Logger
6. Create spatial Grid (using game config's GridCellSize)
7. Create Engine (ECS world, ConnMgr, Logger) — no spatial dependency
8. Create GameWorld (Engine, config, playerDB, Grid — initializes mappers, spawns asteroids + station)
9. Register all systems in execution order
10. Create GameLoop with systems and game hooks
11. Create operation router with injected parser + frame builder
12. Start game loop on goroutine
13. Start WebSocket + UDP servers on goroutines
14. Set up console with game commands
15. Run console on main goroutine (blocks)
```

## Goroutine Layout

```
Main goroutine          → Console.Run() (stdin readline loop)
Game loop goroutine     → GameLoop.Run() (20Hz fixed timestep)
HTTP server goroutine   → ConnManager.ListenAndServe()
UDP server goroutine    → UDPServer.Run()
Ops router goroutines   → Router.Run() + worker pool
Per-connection          → Conn.readPump() + Conn.writePump()
```

The console communicates with the game loop through `PendingAdminCmds` channel. The network layer communicates through the `Events()` channel and per-connection input buffers. The operation router polls channel-0x01 messages and dispatches to handlers on its worker pool.

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

Categories are defined in `internal/game/logcat.go` and registered on the logger at startup. Enabled by default: `connect`, `spawn`, `combat`, `kill`, `mining`, `economy`, `dock`, `loot`, `market`. Toggle at runtime via the console.
