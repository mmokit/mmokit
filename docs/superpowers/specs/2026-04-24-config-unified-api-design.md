# Config-Unified API Design

**Date:** 2026-04-24
**Branch target:** `config-unified-api` (off main)
**Scope:** Unify the mmokit API surface so games declare everything in a single `Config` literal handed to `mmokit.New(cfg)`. Eliminate the arbitrary split between Config struct fields and Process setter methods.

## Problem

Today the API has two registration surfaces:

- **`Config` struct fields** (set BEFORE `mmokit.New(cfg)`) — static data, hooks (`LoginHandler`, `HTTPRoutes`), registries (`Protocol`, `OpRouter`, `ConnManager`), asset config.
- **`Process` setter methods** (called AFTER `mmokit.New(cfg)`) — `SetWorld`, `OnInit`, `AddSystem`, `SetPlayerRouter`, `SetConsole`, `OnConsoleReady`.

The split is arbitrary. `cfg.LoginHandler` is a callback in Config. `mmo.OnConsoleReady(...)` is also a callback, but lives on Process. `cfg.Protocol` is a registration in Config. `mmo.SetWorld(...)` is also a registration, but on Process. New users have to learn which goes where.

## Design

**Move every one-time registration onto `Config`. Keep `AddSystem` on `Process` (Express-middleware style — natural top-to-bottom = tick-execution-order reading).**

### New Config fields

```go
// pkg/universe/coordinator.go (Config struct, additions)
type Config struct {
    // ... all existing fields preserved ...

    // World (optional) is the per-cell GameWorld factory. If nil and OnInit
    // is also nil, the engine creates a bare *WorldBase per cell. If both
    // World and OnInit are set, Build panics — they're alternative paths.
    World func(base *WorldBase) GameWorld

    // OnInit (optional) runs after the engine constructs a bare *WorldBase
    // for each cell. Use for the simple case where you don't need a custom
    // GameWorld type but still need to spawn entities or register replicators.
    // Mutually exclusive with World.
    OnInit func(base *WorldBase)

    // PlayerRouter resolves a username to its target cell ID at login.
    // Replaces Process.SetPlayerRouter.
    PlayerRouter PlayerRouter

    // Console configures the interactive admin console (optional).
    // Replaces Process.SetConsole.
    Console ConsoleOpts

    // OnConsoleReady fires once the console is constructed. Receives the
    // *Process so admin commands can be wired without closure capture.
    // Replaces Process.OnConsoleReady (signature change: gains *Process arg).
    OnConsoleReady func(p *Process, c *engine.Console)
}
```

### Process methods deleted

- `SetWorld(factory)` — replaced by `Config.World`
- `OnInit(fn)` — replaced by `Config.OnInit`
- `SetPlayerRouter(router)` — replaced by `Config.PlayerRouter`
- `SetConsole(opts)` — replaced by `Config.Console`
- `OnConsoleReady(fn)` — replaced by `Config.OnConsoleReady` (signature gains `*Process`)

### Process methods kept

- `AddSystem(name, factory)` — Express-middleware style; preserves natural reading order matching tick execution
- `Start(ctx)` — runs the loop
- `Build()` — explicit build (still callable for test scenarios)
- `Shutdown()` — graceful stop
- All accessors: `OpRouter()`, `Protocol()`, `Cells`, `ConnManager()`, `ClientRenderMode()`, `AnyInputRouter()`, `CmdRegistry()`, `CmdDispatcher()`, etc.

### `World` / `OnInit` resolution rules (in `Build()`)

Validation runs at the top of `Build()`. Mutual exclusivity catches misconfigurations early.

| `World` | `OnInit` | Behavior |
|---------|----------|----------|
| nil | nil | Engine creates bare `*WorldBase` per cell. No init hook. |
| nil | set | Engine creates bare `*WorldBase` per cell. Runs `OnInit(base)` after construction. |
| set | nil | Engine calls `World(base)` to construct the GameWorld for each cell. |
| set | set | **Panic** at validation — ambiguous configuration. |

The `World` factory's signature is `func(base *WorldBase) GameWorld`. Returning `base` directly (cast to `GameWorld` since `*WorldBase` implements the interface) is a valid trivial implementation.

## End-state user code

```go
// examples/4node-basic/main.go
func main() {
    mmo := mmokit.New(mmokit.Config{
        // World shape
        CellsX:    CellsX,
        CellsY:    CellsY,
        CellSize:  CellSize,
        TickRate:  TickRate,
        AoIRadius: AoIRadius,

        // Static assets
        StaticFS:       webDist,
        StaticFSPrefix: "web/dist",

        // Spawn + login
        DefaultSpawn: mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85},
        LoginHandler: mmokit.HandleLogin(...),

        // Protocol
        Protocol: mmokit.NewProtocol("basic").ClientEvents(func(e *mmokit.ClientEvents) {
            mmokit.RegisterClientEvent[basicpb.LoginMsg](e, basicpb.ClientEventCode_BCE_LOGIN)
        }),

        // World factory
        World: NewWorld,

        // Console hook receives Process; no closure capture needed
        OnConsoleReady: func(p *mmokit.Process, c *mmokit.Console) {
            registerBotCommands(p, c.Registry())
        },

        // Dev hardening
        InvariantMode:    universe.InvariantPanic,
        StrictNetIDIndex: true,
    })

    mmo.AddSystem("Input",       mmokit.NewInputSystem(setupHandlers))
    mmo.AddSystem("ClickToMove", mmokit.NewClickToMoveSystem())
    mmo.AddSystem("Physics",     mmokit.NewPhysicsSystem())
    mmo.AddSystem("Spatial",     mmokit.NewSpatialSystem())
    mmo.AddSystem("Network",     mmokit.NewNetworkSystem())

    mmo.Start(context.Background())
}
```

## Migration impact

Three games + one example to update:

- `examples/4node-basic/main.go` — moves `SetWorld`, `OnConsoleReady` into Config
- `examples/slither/main.go` — moves `SetWorld` into Config
- `examples/simple/main.go` — likely uses `OnInit` pattern; moves to Config
- `cmd/server/main.go` — moves `SetWorld`, `OnConsoleReady`, possibly others into Config

Plus tests that exercise these methods directly (`pkg/universe/*_test.go`, `pkg/mmokit/*_test.go`, integration suites).

## Backward compatibility

None per project policy (no backward compat). The Process methods are deleted in the same commit as the Config fields are added; every caller migrates atomically.

## Validation

Add tests for:

- `Build()` panics when both `World` and `OnInit` are set
- `Build()` creates bare `*WorldBase` when both are nil
- `OnInit` runs once per cell after construction
- `Config.PlayerRouter`, `Config.Console`, `Config.OnConsoleReady` round-trip through Process behavior unchanged
- `OnConsoleReady` callback receives the same `*Process` returned by `New(cfg)`

End-to-end smoke: each example game runs cleanly after migration; SDK regen is unaffected (this is a Go-side API change, not a wire-format change).

## Out of scope

- Renaming `Config` itself (e.g. to `Options`) — keep the established name
- Changing the `Build`/`Start` lifecycle semantics
- Touching `pkg/ops`, `pkg/engine`, `pkg/mmokit/protocol.go` server/client event registries — Phase 1-4 work is already complete
- Refactoring `WorldBase`, `Cell`, or any per-cell internals

## Future direction

Once the CLI tool (`mmokit dev`, `mmokit build` — see protocol-unification spec) lands, the Config-driven model dovetails: the CLI parses a Go config file, calls `mmokit.New`, and orchestrates the pipeline. Express-middleware-style `AddSystem` calls remain the imperative seam for runtime-extensible games (mods, plugins).
