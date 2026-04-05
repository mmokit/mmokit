# mmokit Boilerplate Reduction — Design Spec

## Context

The `examples/4node-basic/` example serves as both a user reference and a feature overview for mmokit. Currently ~226 lines in `world.go` alone, with significant boilerplate: duplicate replicator declarations, manual transfer hook wiring, manual spatial registration, topology messaging defined in game code, and a circular closure hack for coordinator access. This work makes mmokit's API surface production-quality for open source by eliminating repeated patterns and moving generic infrastructure into the framework.

## Changes

### 1. Pass Coordinator into WorldFactory

**Files:** `pkg/universe/coordinator.go`, `pkg/mmokit/mmokit.go`

Change signature:
```go
// Before
WorldFactory func(base *WorldBase) GameWorld

// After
WorldFactory func(base *WorldBase, coord *Coordinator) GameWorld
```

The coordinator passes `self` at line 220 of `coordinator.go`. Also update `SetWorldFactory`. This eliminates the `var coord` closure hack in every example's `main.go` and removes the `Coord` field from game worlds.

### 2. EntityKindDef — unified entity kind registration

**Files:** `pkg/universe/entity_kind.go` (new), `pkg/universe/world_base.go`, `pkg/mmokit/mmokit.go`

New types:
```go
// EntityKindDef describes an entity kind's components for transfer, replication, and schema.
type EntityKindDef struct {
    Kind       uint8
    Name       string // for schema export (e.g. "Player")
    components []registeredComponent
}

// registeredComponent holds closures for one component's registration.
type registeredComponent struct {
    registerTransfer func(reg *ReplicationRegistry)
    binding          func(w *ecs.World) ComponentBinding  // for AutoReplicator
    ensureExists     func(entity ecs.Entity)              // add zero-value if missing
}
```

Package-level generic builder (Go can't have generic methods):
```go
func KindComponent[T any](def *EntityKindDef, m *ecs.Map1[T], opts ...ComponentOption[T])
```

This appends to `def.components` with closures capturing the typed `*ecs.Map1[T]`:
- `registerTransfer`: calls `RegisterComponent(reg, m, opts...)`
- `binding`: returns `Component(m)` (for AutoReplicator)
- `ensureExists`: `if !m.HasAll(e) { m.Add(e, new(T)) }`

WorldBase method:
```go
func (b *WorldBase) RegisterEntityKind(def EntityKindDef)
```

This:
1. Registers all components with the transfer `ReplicationRegistry`
2. Stores `ensureExists` callbacks (used in change #3)
3. Stores the def for `NewNetworkSystem` to build replicators automatically

### 3. Auto-fill transfer components

**Files:** `pkg/universe/world_base.go` (in `SpawnFromTransferCore`)

After applying `TransferFrame.Components`, iterate all `ensureExists` callbacks from registered entity kinds and call them on the entity. This ensures any component registered via `RegisterEntityKind` exists on the transferred entity with at least a zero value.

`SetOnTransferReceived` remains available for custom logic but is no longer needed for the common "ensure components exist" pattern.

### 4. Default player transfer received

**Files:** `pkg/universe/world_base.go`

Move the standard `onPlayerTransferReceived` logic into WorldBase as a default:
```go
// Default behavior (games can override via SetOnPlayerTransferReceived):
// 1. Reassign session entity: s.Entity = entity
// 2. Send framework SpawnedMsg (change #6)
```

This eliminates the identical `SetOnPlayerTransferReceived` callback from both examples.

### 5. Auto spatial registration in SpawnEntity

**Files:** `pkg/universe/world_base.go` (in `SpawnEntity`)

After creating the entity, if `b.spatialGrid != nil` and the entity has a collider:
```go
b.spatialGrid.Register(spatial.Entry{
    Entity: entity,
    X:      pos.X,
    Y:      pos.Y,
    Radius: collider.Radius,
})
```

Add `WithoutSpatial()` spawn option for entities that shouldn't be in the grid.

### 6. Framework-level SpawnedMsg and CellTopologyMsg

**Files:** `proto/enginepb/engine.proto`, `pkg/universe/coordinator.go`, `pkg/universe/world_base.go`

Move to `enginepb`:
```protobuf
message SpawnedMsg {
    uint32 entity_net_id = 1;
    int32  cell_x        = 2;
    int32  cell_y        = 3;
    float  cell_size     = 4;
    int32  grid_w        = 5;
    int32  grid_h        = 6;
}

message CellTopologyMsg {
    int32 grid_w         = 1;
    int32 grid_h         = 2;
    float base_cell_size = 3;
    repeated CellInfo cells = 4;
}

message CellInfo {
    int32  cell_x   = 1;
    int32  cell_y   = 2;
    uint32 depth    = 3;
    float  size     = 4;
    float  origin_x = 5;
    float  origin_y = 6;
    string node_id  = 7;
}
```

New framework methods:

**WorldBase:**
```go
func (b *WorldBase) SendSpawnedMsg(connID uint32, entity ecs.Entity)
```
Builds and sends the `SpawnedMsg` using the entity's NetworkID, the node's root cell, and coordinator grid dimensions. Games no longer implement this.

**Coordinator:**
```go
func (c *Coordinator) SendCellTopology(connID uint32)
func (c *Coordinator) BroadcastCellTopology()
```

When `DynamicPartitioning` is enabled and `OnTopologyChanged` is nil, the coordinator auto-sets it to `BroadcastCellTopology()`.

**Default `OnCellBoundsChanged`:** WorldBase auto-sends `SpawnedMsg` to affected players (the current example behavior). No game-side hook needed.

### 7. WithComponents() spawn option

**Files:** `pkg/universe/world_base.go`

```go
func WithComponents() SpawnOption
```

Looks up the entity's kind (from `WithEntityKind`) in registered `EntityKindDef`s and calls `ensureExists` for each component. This zero-fills all registered components so the game can immediately `map.Get(entity).Field = value`.

### 8. NewNetworkSystem auto-discovers replicators

**Files:** `pkg/mmokit/mmokit.go` (or wherever `NewNetworkSystem` is defined)

When the `ReplicationConfig.Replicators` is nil after the setup callback, `NewNetworkSystem` builds replicators from the world's registered `EntityKindDef`s automatically. The setup callback only needs to set `cfg.AoIRadius` and any non-default options.

The `EngineBindings` config (VelQuantScale, SizeQuantScale) moves into `EntityKindDef` as an optional field:
```go
type EntityKindDef struct {
    Kind            uint8
    Name            string
    EngineBindings  *EngineBindingsConfig // nil = use defaults
    components      []registeredComponent
}
```

Note: `EntityKindDef` stores the *config* only. The actual `EngineBindings(w, coord, cfg)` call that produces a `ComponentBinding` happens inside `NewNetworkSystem` at runtime, where the `*ecs.World` and `*Coordinator` are available. The `EntityKindDef` is a declarative description, not a live binding.

## Affected Examples

### 4node-basic

Delete from example:
- `buildCellTopologyMsg`, `sendCellTopology`, `broadcastCellTopology` (→ framework)
- `sendSpawnedMsg` (→ framework)
- `SetOnTransferReceived` hook (→ auto-fill)
- `SetOnPlayerTransferReceived` hook (→ framework default)
- `SetOnCellBoundsChanged` hook (→ framework default)
- `World.Coord` field (→ passed via factory)
- Duplicate `AutoReplicator` in `main.go` Network system (→ auto-discovered)

Keep in example (game-specific):
- `spawnPlayer` — game decides spawn position and which components get non-zero values
- Login handler — intentionally simple placeholder
- `OnEnter`/`OnExit` state callbacks — game controls spawn + topology send flow
- `DebugInfoSystem` — game-specific system
- Entity kind constants, component types

Estimated `world.go`: ~100-120 lines (from ~226).

### slither

Same pattern of removals. Slither has additional complexity (custom border scanning, snake body replication) that stays game-side. The `SendSpawnedMsg` and transfer hooks simplify identically.

### schema.go

Games define a shared function for entity kind setup:
```go
func playerKindDef(w *ecs.World) mmokit.EntityKindDef {
    def := mmokit.EntityKindDef{
        Kind: KindPlayer, Name: "Player",
        EngineBindings: &mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500},
    }
    mmokit.KindComponent(&def, ecs.NewMap1[DebugInfo](w))
    mmokit.KindComponent(&def, ecs.NewMap1[PlayerName](w))
    mmokit.KindComponent(&def, ecs.NewMap1[mmokit.MoveTarget](w))
    return def
}
```

Both `NewWorld` and `dumpProtocolSchema` call this function. Single source of truth.

## Verification

1. `go vet ./...` — no compilation errors
2. `make proto` — regenerate after enginepb changes
3. `make client-sdk GAME=examples/4node-basic` — verify schema dump still works
4. Run `examples/4node-basic` with `make dev` — verify:
   - Players spawn and receive SpawnedMsg
   - Click-to-move works
   - Entity replication works (see other players)
   - Dynamic cells: `--dynamic-cells` flag, verify topology broadcasts
   - Multi-node: entities transfer between cells correctly
5. Run `examples/slither` — verify same behaviors
6. `go test ./pkg/universe/...` — existing tests pass
7. `go test ./pkg/universe/... -run TestComponentRegistry` — transfer registry tests
