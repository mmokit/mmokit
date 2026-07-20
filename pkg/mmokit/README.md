# MMOKIT

Game-facing facade for the MMO framework. A game can use this package as its
main engine import while MMOKIT supplies per-cell ECS loops, spatial
partitioning, cross-cell handoff, typed messaging, client replication, and
distributed process roles.

Current source and tests are authoritative. For runnable compositions, start
with `examples/simple` and `examples/4node-basic`.

## Minimal process

```go
package main

import "github.com/zenion/mmoserver/pkg/mmokit"

func main() {
    process := mmokit.New(mmokit.Config{
        Name:          "simple",
        AnonymousAuth: true,
    })

    process.AddSystem(mmokit.NewSystem(&MovementSystem{}))
    process.Start()
}
```

`mmokit.New` creates a `*Process`, installs the protocol/schema registry, and
binds universal command-line flags if the program has not parsed flags yet.
`Start` builds the selected process roles and blocks until shutdown. It also
accepts an optional parent context.

Common zero-value defaults are one cell, 8192 world units per cell, 20 Hz,
and a 500-unit area of interest. See `universe.Config` (re-exported as
`mmokit.Config`) for networking, roles, TLS, admin, persistence, partitioning,
and service settings.

## Custom systems and queries

Embed the non-generic `SystemBase`. The pointer passed to `NewSystem` provides
type information only; the process constructs a fresh zero-value instance for
every cell.

```go
type MovementSystem struct {
    mmokit.SystemBase

    moving mmokit.Query[struct {
        Position *mmokit.Position
        Velocity *mmokit.Velocity
        Params   *mmokit.MoveParams `ecs:"optional"`
    }]
}

func (s *MovementSystem) Init() {
    // Optional: add exclusions or replace the default filter.
    s.moving.With(mmokit.Without[mmokit.Dormant]())
}

func (s *MovementSystem) Update(dt float32) {
    for _, bundle := range s.moving.Iter {
        bundle.Position.X += bundle.Velocity.X * dt
        bundle.Position.Y += bundle.Velocity.Y * dt
    }
}
```

Query fields are discovered and built automatically after `Init`. Exported
bundle fields must point to component structs. An `ecs:"optional"` field is
`nil` when absent. By default queries exclude `Ghost` and `Replica`; use
`IncludeAll` to clear those defaults and `Without[T]` to add exclusions.

The bundle pointer is reused between iterations. Do not retain it or its
address after the loop body.

System registration order is tick order. Structural commands queued by one
system are flushed after its `Update`, so the next system sees them.

### Per-cell state

Constructor dependencies and mutable data that should exist once per cell can
be registered explicitly:

```go
mmokit.AddState(process, func(stage *mmokit.Stage) *CombatState {
    return &CombatState{}
})

func (s *CombatSystem) Init() {
    s.state = mmokit.State[CombatState](s.Stage())
}
```

## Spawning and components

`Stage.Spawn` is the canonical entity constructor:

```go
ship := stage.Spawn(
    mmokit.Position{X: 100, Y: 200},
    mmokit.EntityKind{Type: KindShip},
    mmokit.Collider{Radius: 18},
    ShipHealth{Current: 100, Maximum: 100},
)
```

Pass components by value. Exactly one `Position` is required. Do not supply
framework-owned `NetworkID` or `CellCoord`; zero `Velocity` is attached when
omitted. Duplicate component types and pointer components panic.

Use `Stage.SpawnPlayer` in an `OnPlayerJoin` hook. It injects `Position` and
`PlayerConn`, assigns the session entity, and notifies the client.

### Entity kinds

Register every replicated/transferable kind before `Build` or `Start`:

```go
type ShipBundle struct {
    Health    *ShipHealth
    Inventory *Inventory
    Runtime   *ShipRuntime `mmokit:"local"`
    Tag       *PlacedTag   `mmokit:"optional"`
}

mmokit.RegisterKind[ShipBundle](process, KindShip, "Ship")
```

An untagged field is required at spawn and transferred between cells.
`mmokit:"optional"` permits omission while retaining transfer and replication
support. `mmokit:"local"` is reconstructed locally and never serialized;
`mmokit:"-"` is ignored. Framework transfer-core components do not belong in
the bundle.

Use `WithField`, `WithBindingFn`, `WithMarshal`, and related field options for
custom transfer or replication behavior. Use `WithExtraBindingFn` for a
replicated component whose cross-cell transfer is registered separately.

### Access and structural mutation

```go
health := mmokit.Get[ShipHealth](ship)
exists := mmokit.Has[ShipHealth](ship)
mmokit.Set(ship, ShipHealth{Current: 75, Maximum: 100})
mmokit.Despawn(ship)
```

`Get`, `Has`, and `Set` are safe on dead or zero entities. Call `Prime[T]` in
`Init` before dynamically looking up a component type from inside a locked
query when no registered kind/query has already introduced that type.

Do not make structural Ark changes while iterating a query. Queue them through
the system's command buffer:

```go
mmokit.AddComponent(s.Commands(), entity, Burning{Seconds: 3})
mmokit.RemoveComponent[Shielded](s.Commands(), entity)
s.Commands().Despawn(entity.Handle())
s.Commands().Defer(func() { /* broader stage operation */ })
```

## Tick callbacks and nearby entities

For small behaviors that do not need a custom system struct:

```go
type HealthBundle struct {
    Health *ShipHealth
}

mmokit.OnTickEachAll[HealthBundle](process, func(
    entity mmokit.Entity,
    b *HealthBundle,
    dt float32,
) {
    // Runs on every current and future stage.
})
```

`OnWorldTickAll`, `OnTickAll`, and `OnTickEachAll` replay onto stages created
by splits or migrations. Their non-`All` forms apply only to one stage.

`Nearby` and `NearbyWith[T]` iterate entities from the stage's spatial grid,
including local live entities and replicas:

```go
for target := range mmokit.NearbyWith[ShipHealth](stage, x, y, radius) {
    // ...
}
```

## Typed messaging

### Entity messages

`Entity.Send` routes locally or to the entity's authoritative cell. Prefer a
pointer so a local handler can mutate response fields without an extra boxing
allocation.

```go
type Damage struct {
    Amount float32
    Source mmokit.Entity
}

mmokit.HandleAll(process, func(target mmokit.Entity, msg *Damage) {
    health := mmokit.Get[ShipHealth](target)
    if health != nil {
        health.Current -= msg.Amount
    }
})

target.Send(&Damage{Amount: 25, Source: attacker})
```

`HandleAll` installs the handler on current and future stages and broadcasts
the handled message to area-of-interest viewers. Use `HandleAllInternal` for
server-only messages. The stage-scoped `Handle` is for deliberately local
registration.

### Client input and server events

```go
mmokit.HandleClient(process, func(player mmokit.Entity, msg *SetMoveTarget) {
    if mmokit.PlayerStateOf(player) != mmokit.StateActive {
        return
    }
    // Validate values and rate limits, then mutate the owned player.
})

mmokit.RegisterEvent[InventoryChanged]()
mmokit.SendEvent(stage, connID, &InventoryChanged{ /* ... */ })
```

The framework proves that a `HandleClient` target belongs to the sending
connection; the handler remains responsible for player-state, value, and rate
validation. `SendEventToAll` efficiently sends one encoded event to every
active session.

### Typed operations

Use correlated request/response operations for RPC-shaped client actions:

```go
mmokit.RegisterOp[BrowseRequest, BrowseResponse](
    mmokit.RoutePlayerCell,
    func(ctx *mmokit.OpContext, req *BrowseRequest) (*BrowseResponse, error) {
        stage := mmokit.OpContextStage(ctx)
        _ = stage
        return &BrowseResponse{}, nil
    },
)
```

`RouteGatewayLocal` runs without cell state. `RoutePlayerCell` runs on the
authoritative cell for the requesting player.

## Wire contract and SDK generation

Typed input/events and entity messages use channel `0x00`; operations use
channel `0x01`. The client protocol is the reflection-based binary codec, not
protobuf. Protobuf is reserved for server-internal mesh traffic.

Wire type IDs derive from package-qualified Go type names. Renaming or moving
a registered message type is a protocol break, as are replicated field-layout
and stable enum changes.

`mmokit.New` installs a protocol assembled from `RegisterKind`, `HandleAll`,
`HandleClient`, `RegisterEvent`, and `RegisterOp` registrations. Generate a
client from that schema with the repository recipe, for example:

```bash
just client-sdk examples/4node-basic
```

Review generated diffs whenever the wire contract changes.

## Where to look next

- `examples/simple`: smallest process and custom system
- `examples/4node-basic`: entity kinds, players, meshing, services, admin, and
  a generated TypeScript client
- `pkg/mmokit/doc.go`: facade primitives and lifecycle notes
- `pkg/universe`: process, stage, handoff, and distributed implementation
