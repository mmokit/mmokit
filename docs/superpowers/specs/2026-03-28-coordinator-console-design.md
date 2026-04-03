# Coordinator-Managed Console Design

## Context

The interactive server console (`pkg/engine/console.go`) currently requires manual setup in every game's `main.go`: create Console, register builtins with game-specific callbacks, register game commands, call `console.Run(ctx)`. This is ~15 lines of boilerplate that every game repeats. The Coordinator already has everything needed to wire the console automatically — node list, metrics, logger.

## Design

### Console is default, opt-out for headless

The Coordinator **always** creates a Console unless `WithHeadless()` is passed. Games provide game-specific callbacks via `WithConsole(opts)` for the config/entity builtins. If `WithConsole` is not provided, the console still runs with node/log/perf builtins only.

### New types

```go
// ConsoleOpts provides game-specific console configuration.
// All fields are optional — omit what your game doesn't need.
type ConsoleOpts struct {
    Config        Configurable     // enables "config list/get/set"
    ConfigSave    func() error     // enables "config save"
    ConfigReset   func()           // enables "config reset"
    Entities      *EntityOpts      // enables "entity summary/list/get/remove"
    Registry      *EntityRegistry  // enables "entity add"
    Commands      []Command        // game-specific commands
    CommandGroups []CommandGroup   // game-specific command groups
}
```

### New CoordinatorOptions

```go
// WithConsole provides game-specific console configuration.
// The console is created by default — this adds game builtins and commands.
func WithConsole(opts ConsoleOpts) CoordinatorOption

// WithHeadless disables the interactive console entirely.
// Use for tests, containers, or headless deployments.
// Takes precedence over WithConsole if both are provided.
func WithHeadless() CoordinatorOption
```

### Coordinator.Start(ctx) becomes blocking

When the console is enabled (default), `Start(ctx)`:
1. Starts all node game loops (goroutines)
2. Creates Console from default node's Engine + Logger
3. Auto-registers **node** builtins (built from Coordinator's node map — zero game input)
4. Registers game-provided builtins from ConsoleOpts (config, entities)
5. Registers game-provided commands and command groups
6. Installs SIGINT/SIGTERM handler that cancels ctx
7. Calls `console.Run(ctx)` — **blocks** on main goroutine
8. On quit/signal: cancels context, shuts down all nodes, returns

When headless (`WithHeadless()`), `Start(ctx)`:
1. Starts all node game loops
2. Installs SIGINT/SIGTERM handler
3. **Blocks** waiting for ctx.Done() or signal
4. Shuts down all nodes, returns

Key change: `Start` always blocks now. This eliminates the separate `coord.Shutdown()` call — shutdown happens inside Start.

### Console accessor

```go
// Console returns the Coordinator's console, or nil if headless.
// Call after Start has been invoked (it creates the console).
// Use to register additional commands from game setup code that
// runs between NewCoordinator and Start.
func (c *Coordinator) Console() *Console
```

Since Start blocks, games that need to register commands after construction but before the console starts running can do so via a callback:

```go
// WithOnConsoleReady registers a callback invoked after the console
// is created and builtins are registered, but before console.Run().
func WithOnConsoleReady(fn func(c *Console)) CoordinatorOption
```

### Node builtins auto-wired

The Coordinator builds `[]NodeRef` from its node map:

```go
for _, node := range c.Nodes {
    refs = append(refs, NodeRef{
        ID:   node.ID,
        Cell: node.Cell,
        Exec: func(fn func() string) string {
            // route through node.Engine.PendingAdminCmds
        },
        Metrics: func() LoadSnapshot {
            return node.Engine.Metrics.Snapshot()
        },
    })
}
```

This is passed to `RegisterBuiltins(BuiltinOpts{Nodes: refs})` automatically.

### What games look like after

**Simple example (4node-basic):**
```go
coord := mmokit.NewCoordinator(grid, cfg, factory,
    mmokit.WithConnManager(cm),
    mmokit.WithLogger(logger),
    mmokit.WithAoIRadius(AoIRadius),
)
go httpServer()
coord.Start(ctx) // blocks, console + signals + shutdown
```

**Game with config and custom commands:**
```go
coord := mmokit.NewCoordinator(grid, cfg, factory,
    mmokit.WithConnManager(cm),
    mmokit.WithLogger(logger),
    mmokit.WithConsole(mmokit.ConsoleOpts{
        Config:     mmokit.NewReflectConfig(&gameCfg),
        ConfigSave: func() error { return store.SaveConfig(gameCfg) },
        Entities:   buildEntityOpts(),
    }),
    mmokit.WithOnConsoleReady(func(c *mmokit.Console) {
        c.Register(myGameCommand)
        c.RegisterGroup(myGameGroup)
    }),
)
go httpServer()
coord.Start(ctx)
```

**Headless/test:**
```go
coord := mmokit.NewCoordinator(grid, cfg, factory,
    mmokit.WithConnManager(cm),
    mmokit.WithHeadless(),
)
coord.Start(ctx) // blocks until ctx cancelled, no console
```

### Backward compatibility

- `coord.Shutdown()` still exists and works (cancels internal context)
- Existing games that call `Start` then do their own signal handling will need updating since Start now blocks. This is a breaking change but aligns with the "Coordinator owns the lifecycle" direction.
- The `internal/game/` main.go is the only existing caller and will be updated.

### Files changed

| File | Change |
|------|--------|
| `pkg/universe/coordinator.go` | Add Console field, ConsoleOpts, WithConsole, WithHeadless, WithOnConsoleReady options, update Start to block |
| `pkg/engine/builtins.go` | No changes (BuiltinOpts already supports partial registration) |
| `pkg/engine/console.go` | No changes |
| `pkg/mmokit/mmokit.go` | Export new types: ConsoleOpts, WithConsole, WithHeadless, WithOnConsoleReady |
| `cmd/server/main.go` | Simplify: remove manual console setup, use WithConsole + WithOnConsoleReady |
| `examples/slither/main.go` | Simplify: remove signal handler, let Start block |
| `examples/4node-basic/main.go` | Simplify: remove signal handler, let Start block |

## Verification

1. `go vet ./...` passes
2. Main game server starts with console, builtins work (help, node list, perf, config)
3. Slither example starts with console (node/log/perf only, no config/entity)
4. 4node-basic example starts with console
5. Game-specific commands (players, damage, etc.) still work in main game
6. SIGINT cleanly shuts down all examples
7. `WithHeadless()` starts without console, shuts down on ctx cancel
