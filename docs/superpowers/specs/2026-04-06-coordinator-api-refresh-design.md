# Coordinator API Refresh

## Context

The current `WorldFactory` API in mmokit is awkward. It's a function field buried in Config that mixes world construction with initialization, forces simple games to create unnecessary custom world structs, and provides no clean hook for post-wiring setup (leading to hacks like `PendingAdminCmds` for initial entity spawning in slither).

This redesign makes the coordinator API cohesive: data-only Config, explicit setup methods, pointer-embed WorldBase, and a required Init lifecycle hook.

## Design

### 1. Data-Only Config

Strip all function fields from Config. It becomes pure configuration:

```go
type Config struct {
    CellsX            uint32  // default 1
    CellsY            uint32  // default 1
    CellSize          float32 // default 8192
    SpatialBucketSize float32 // default CellSize/10
    TickRate           int     // default 20
    AoIRadius          float32 // default 500
    DefaultCell        CellID
    Headless           bool
    ProxiesEnabled     bool
    DebugTopology      bool
    DynamicPartitioning *PartitionConfig
    ConnManager        *net.ConnManager
    Logger             *logger.Logger
    LogCategories      string
}
```

**Removed:** `WorldFactory`, `Console`, `OnConsoleReady` — these become methods on Coordinator.

### 2. Coordinator Setup Methods

```go
func NewCoordinator(cfg Config) *Coordinator

// World creation (pick one, required before Build)
func (c *Coordinator) SetWorld(factory func(base *WorldBase) GameWorld)
func (c *Coordinator) OnInit(fn func(w *WorldBase))

// Systems (unchanged)
func (c *Coordinator) AddSystem(name string, factory func() System)

// Console (replaces Config.Console and Config.OnConsoleReady)
func (c *Coordinator) SetConsole(opts ConsoleOpts)
func (c *Coordinator) OnConsoleReady(fn func(c *Console))

// Lifecycle
func (c *Coordinator) Build()
func (c *Coordinator) Start(ctx context.Context)
```

`SetWorld` and `OnInit` are mutually exclusive. `Build()` panics if neither was called.

### 3. Pointer-Embed WorldBase

Games embed `*WorldBase` (pointer) instead of `WorldBase` (value):

```go
// Before
type MyWorld struct {
    mmokit.WorldBase
}
gw := &MyWorld{WorldBase: *base}  // copies large struct

// After
type MyWorld struct {
    *mmokit.WorldBase
}
gw := &MyWorld{WorldBase: base}   // just a pointer
```

All WorldBase fields are unexported with accessor methods — pointer promotion works identically.

### 4. GameWorld Init() Lifecycle

Add `Init()` to the GameWorld interface:

```go
type GameWorld interface {
    Init()
    // ... all existing methods unchanged
}
```

`WorldBase` provides a default no-op `Init()`.

**Build() lifecycle:**
1. Create nodes (one per cell)
2. For each node: create WorldBase, call SetWorld factory (or create default world for OnInit path)
3. Wire bridges and topology between all nodes
4. For each node: call `GameWorld.Init()` (or OnInit callback)
5. Register console builtins

Init() runs after bridges are wired but before game loops start. ECS is safe to access directly — no concurrent goroutines yet. This eliminates the `PendingAdminCmds` hack.

### 5. SetWorld Factory Signature

```go
func (c *Coordinator) SetWorld(factory func(base *WorldBase) GameWorld)
```

Dropped the `*Coordinator` parameter. Games access the coordinator via `base.Coordinator()` (already exists on WorldBase).

### 6. OnInit for Simple Cases

For games that don't need a custom world struct:

```go
func (c *Coordinator) OnInit(fn func(w *WorldBase))
```

The coordinator creates a default WorldBase-only world per node and calls `fn` on each after wiring. No custom struct needed.

## Example Rewrites

### Simple (no custom world needed)

```go
func main() {
    coord := mmokit.NewCoordinator(mmokit.Config{
        CellSize: 8192,
        TickRate: 20,
    })
    coord.OnInit(func(w *mmokit.WorldBase) {
        w.SpawnEntity(mmokit.Position{X: 4096, Y: 4096}, mmokit.WithCollider(20))
    })
    coord.AddSystem("Oscillate", func() mmokit.System { return &OscillateSystem{} })
    coord.Start(context.Background())
}
```

`MySimpleWorld` struct disappears entirely.

### 4node-basic

```go
func main() {
    coord := mmokit.NewCoordinator(mmokit.Config{
        CellsX: 2, CellsY: 2, CellSize: 8192,
        TickRate: 20, AoIRadius: 500, DebugTopology: true,
    })
    coord.SetWorld(NewWorld)
    coord.AddSystem("Input", mmokit.NewInputSystem(setupInputHandlers))
    coord.AddSystem("ClickToMove", mmokit.NewClickToMoveSystem())
    coord.AddSystem("Physics", mmokit.NewPhysicsSystem())
    coord.AddSystem("DeadReckoning", mmokit.NewDeadReckoningSystem())
    coord.AddSystem("Spatial", mmokit.NewSpatialSystem())
    coord.AddSystem("Network", mmokit.NewNetworkSystem())
    coord.Build()
    // HTTP setup with coord.ConnManager()...
    coord.Start(ctx)
}

func NewWorld(base *mmokit.WorldBase) mmokit.GameWorld {
    w := base.ECSWorld()
    return &World{
        WorldBase:     base,
        NameMap:       ecs.NewMap1[PlayerName](w),
        DebugInfoMap:  ecs.NewMap1[DebugInfo](w),
        MoveTargetMap: ecs.NewMap1[mmokit.MoveTarget](w),
    }
}

func (gw *World) Init() {
    gw.registerEntityKinds()
    gw.setupLoginHandler()
}
```

### Slither (no more PendingAdminCmds hack)

```go
func main() {
    cfg := DefaultConfig()
    coord := mmokit.NewCoordinator(mmokit.Config{
        CellsX: 2, CellsY: 2, TickRate: 20,
        AoIRadius: cfg.AoIRadius,
        ConnManager: cm, Logger: logger,
    })
    coord.SetWorld(func(base *mmokit.WorldBase) mmokit.GameWorld {
        return NewSlitherWorld(base, cfg)
    })
    coord.OnConsoleReady(func(console *mmokit.Console) {
        gw := coord.DefaultNode().World.(*SlitherWorld)
        console.RegisterBuiltins(mmokit.BuiltinOpts{...})
    })
    coord.AddSystem("Input", mmokit.NewInputSystem(setupInputHandlers))
    // ... more systems
    coord.Build()
    // HTTP setup...
    coord.Start(ctx)
}

func (gw *SlitherWorld) Init() {
    gw.registerReplicators()
    gw.setupLogin()
    gw.SpawnInitialFood()
    for i := 0; i < gw.Cfg.BotsPerNode; i++ {
        gw.SpawnBotSnake(...)
    }
}
```

### internal/universe (game adapter)

```go
func GameSetup(coord *mmokit.Coordinator, gameCfg game.GameConfig, ...) {
    coord.SetWorld(func(base *mmokit.WorldBase) mmokit.GameWorld {
        gw := game.NewGameWorld(base.Engine(), gameCfg, ...)
        seRegistry := buildSideEffectRegistry(gw)
        return newGameWorldAdapter(base, gw, seRegistry)
    })
    coord.AddSystem("Input", mmokit.NewInputSystem(...))
    // ...
}

func (a *gameWorldAdapter) Init() {
    reg := buildReplicationRegistry(a.gw)
    a.SetReplicationRegistry(reg)
    a.SetOnTransferReceived(...)
    a.SetOnPlayerTransferReceived(...)
}
```

## Files to Modify

### Core engine
- `pkg/universe/coordinator.go` — Config struct (remove function fields), add SetWorld/OnInit/OnConsoleReady methods, update Build() lifecycle to call Init()
- `pkg/universe/world.go` — Add Init() to GameWorld interface
- `pkg/universe/world_base.go` — Default no-op Init(), review struct for pointer-embed compatibility
- `pkg/mmokit/mmokit.go` — Update re-exports, remove WorldFactory alias, add SetWorld/OnInit/OnConsoleReady aliases

### Examples
- `examples/simple/main.go` — Rewrite to use OnInit, remove MySimpleWorld struct
- `examples/4node-basic/main.go` + `world.go` — Pointer-embed WorldBase, split Init from constructor
- `examples/slither/main.go` + `world.go` — Pointer-embed, move spawn logic to Init(), remove PendingAdminCmds

### Game adapter
- `internal/universe/factory.go` — Use SetWorld method, move hook setup to Init()
- `internal/universe/adapter.go` — Pointer-embed WorldBase, add Init()

### Callers of WorldBase
- Any file that does `WorldBase: *base` changes to `WorldBase: base`
- Any file that embeds `mmokit.WorldBase` (value) changes to `*mmokit.WorldBase` (pointer)

## Verification

1. `go vet ./...` — compilation check
2. Run `examples/simple` — entity oscillates, connects via WebSocket
3. Run `examples/4node-basic` — 2x2 grid, players connect, click-to-move, cross-node transfers work
4. Run `examples/slither` — snakes, food, bots spawn on Init without PendingAdminCmds
5. Run `make build` for the main game server
6. Verify `--dump-schema` still works for SDK codegen
