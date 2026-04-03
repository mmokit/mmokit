# MMOKit Networking & Movement API Design

## Context

External game developers using mmokit face two major friction points:

1. **Replication boilerplate.** Every entity type requires ~100-200 lines of manual `EntityReplicator` implementation (Hash, Snapshot, SnapshotLayout, InitialData). The 4node-basic example bypasses the ReplicationSystem entirely with a hand-rolled 250-line custom binary protocol — indicating the current API is too hard to adopt.

2. **No built-in movement.** Click-to-move and WASD movement are universal patterns, but every game reinvents them. The main game has `ShipControlSystem` (game-specific), the 4node-basic has its own `MovementSystem`, and the slither example has angle-based movement. None are reusable.

This spec defines two new APIs for `pkg/`:
- **Auto-Replicator:** Reflection-based entity replication from struct tags
- **Movement Systems:** Generic click-to-move and direction-vector systems

Both are game-agnostic — no assumptions about genre, entity types, or game mechanics.

---

## Part 1: Auto-Replicator API

### Problem

The `EntityReplicator` interface requires four methods per entity type:

```go
type EntityReplicator interface {
    EntityType() uint8
    Hash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry)
    Snapshot(w *quantize.SnapshotWriter, viewer *ViewerInfo, entry spatial.Entry)
    SnapshotLayout() []int
    InitialData(viewer *ViewerInfo, entry spatial.Entry) []byte
}
```

For a simple entity with position, velocity, and health, this is ~80 lines of hand-written code. For 5 entity types, that's ~400 lines of repetitive serialization logic that must stay in sync with component definitions.

### Solution: Struct Tags + Component Bindings

**Step 1: Tag ECS components with `net:"..."` struct tags**

```go
// Game-specific component — tag fields for replication
type ShipVitals struct {
    Health    float32 `net:"qnorm"`    // 0-1 normalized → 1 byte
    MaxHealth float32 `net:"u16"`      // raw uint16 → 2 bytes
    Shield    float32 `net:"qnorm"`    // 0-1 normalized → 1 byte
    MaxShield float32 `net:"u16"`      // raw uint16 → 2 bytes
    Boosting  bool    `net:"bool"`     // boolean → 1 byte
}

type ShipProfile struct {
    Name   string `net:"initial,string"` // sent once on visibility enter
    SkinID uint8  `net:"initial,u8"`     // sent once on visibility enter
}
```

Untagged fields are silently skipped (same convention as `json:"..."` tags).

**Step 2: Register via `AutoReplicator` with component bindings**

```go
registry.Register(mmokit.AutoReplicator(KindShip,
    mmokit.ViewerRelativePos(posMap, cellCoordMap), // 2× float32 viewer-relative
    mmokit.QVelocity(velMap, 2000),                // 2× int16 quantized
    mmokit.QAngle(rotMap),                         // 1× uint16
    mmokit.QSize(colliderMap, 500),                // 1× uint16 (radius only)
    mmokit.Component(vitalsMap),                   // reads struct tags
    mmokit.Component(profileMap),                  // initial-only fields
))
```

**Step 3: Done.** The `AutoReplicator` implements `EntityReplicator`. Hash, Snapshot, SnapshotLayout, and InitialData are auto-generated from tag metadata.

### Binding Types

There are two categories of bindings:

#### Built-in Bindings (for core mmokit components)

These handle cross-component math and known encoding patterns. They don't use struct tags — their behavior is hardcoded.

| Binding | Input | Wire Layout | Description |
|---------|-------|-------------|-------------|
| `ViewerRelativePos(posMap, cellCoordMap)` | Position + CellCoord | 2× float32 (8B) | World-absolute position relative to viewer's cell. Uses entity's Position + CellCoord and viewer's CellCoord to compute world-absolute offset. |
| `EntryPosition()` | spatial.Entry | 2× float32 (8B) | Raw cell-local position from spatial entry |
| `QVelocity(velMap, scale)` | Velocity | 2× int16 (4B) | Quantized velocity pair |
| `QAngle(rotMap)` | Rotation | 1× uint16 (2B) | Quantized angle |
| `QSize(colliderMap, scale)` | Collider.Radius | 1× uint16 (2B) | Quantized radius |

These bindings internally access the entity via the passed `ecs.Map1[T]` references and the spatial entry's entity handle. The viewer's info is available via the `ViewerInfo` parameter passed to Hash/Snapshot.

#### `Component(ecsMap)` Binding (for game-specific components)

Reflects on the struct's `net:"..."` tags at init time, builds closure-based field accessors. Zero reflection on the hot path.

```go
mmokit.Component(vitalsMap)  // vitalsMap is *ecs.Map1[ShipVitals]
```

For optional components (entity may or may not have this component):

```go
mmokit.OptionalComponent(healthMap)  // writes zero bytes if absent
```

### Struct Tag Syntax

Format: `net:"encoding[,option=value]"`

| Tag | Wire Size | Go Types | Description |
|-----|-----------|----------|-------------|
| `pos` | 8 bytes | struct with X,Y float32 | Float32 pair, no quantization |
| `f32` | 4 bytes | float32 | Raw IEEE 754 float |
| `qvel,scale=N` | 2 bytes | float32 | Int16 quantized (field × scale) |
| `qangle` | 2 bytes | float32 | Uint16 normalized angle [-π, π] |
| `qnorm` | 1 byte | float32 | Uint8 normalized [0.0, 1.0] |
| `qsize,scale=N` | 2 bytes | float32 | Uint16 quantized size |
| `u8` | 1 byte | uint8/int | Raw uint8 |
| `u16` | 2 bytes | uint16/int | Raw uint16 |
| `u32` | 4 bytes | uint32 | Raw uint32 |
| `i16` | 2 bytes | int16/int | Raw int16 |
| `bool` | 1 byte | bool | 0 or 1 |
| `initial,<enc>` | varies | any | One-time only (InitialData), not per-tick |
| `initial,string` | 1+N bytes | string | Length-prefixed string, initial only |

### How It Works Internally

#### Init Time (once per entity type)

1. `Component(ecsMap)` calls `reflect.TypeOf` on the component struct
2. Iterates fields, parses `net:"..."` tags
3. For each tagged field, creates a `fieldBinding` with:
   - Field index (for `reflect.Value.Field(i)`)
   - Wire size (from encoding table)
   - Hash writer closure: `func(val reflect.Value, h *Hasher)`
   - Snapshot writer closure: `func(val reflect.Value, w *SnapshotWriter)`
   - Whether it's initial-only
4. Caches all bindings — no further reflection

#### Per-Tick (hot path)

1. `Hash()`: For each binding, get component pointer, call hash writer closures
2. `Snapshot()`: For each binding, get component pointer, call snapshot writer closures
3. `SnapshotLayout()`: Pre-computed from wire sizes at init time
4. `InitialData()`: Only runs for initial-tagged fields on visibility enter

#### Performance

Closure dispatch adds ~20ns/entity over hand-written code. At 1000 entities × 20Hz, that's 0.4ms/tick — well within budget. The expensive part (spatial queries, delta encoding) is unchanged.

### The `autoReplicator` Struct

```go
type autoReplicator struct {
    entityType    uint8
    bindings      []componentBinding  // ordered list of all bindings
    layout        []int               // pre-computed from binding wire sizes
    hasInitial    bool                // whether any binding has initial-only fields
}
```

Implements `EntityReplicator` by iterating bindings in order.

### Optional Components

`OptionalComponent(ecsMap)` checks `HasAll(entity)` before reading. If absent, writes zero bytes for all snapshot fields (maintaining fixed layout). The delta encoder sees zeros and sends them efficiently.

### Escape Hatch

For complex cases (variable-length data like snake body segments, viewer-dependent filtering), games implement `EntityReplicator` directly. The two approaches coexist — `AutoReplicator` and manual replicators register the same way.

### File Structure

| File | Contents |
|------|----------|
| `pkg/system/auto_replicator.go` | `AutoReplicator()`, `Component()`, `OptionalComponent()`, built-in bindings, `autoReplicator` struct |
| `pkg/system/field_meta.go` | Tag parser, `fieldMeta` type, encoding→wire-size mapping |
| `pkg/system/field_writers.go` | Hash writer and snapshot writer dispatch tables per encoding |
| `pkg/system/auto_replicator_test.go` | Unit tests |
| `pkg/mmokit/mmokit.go` | Re-export: `AutoReplicator`, `Component`, `OptionalComponent`, all built-in bindings |

No changes to `pkg/system/replication.go` or `pkg/quantize/` — the auto-replicator implements the existing interface.

---

## Part 2: Movement Systems

### Problem

Click-to-move and WASD movement are implemented ad-hoc in every game and example. There are no reusable systems in `pkg/system/`.

### Solution: Two Minimal Generic Systems

Both are dead simple — move at speed, stop when done. No deceleration, no drag, no steering model. Games layer their own physics on top.

#### ClickToMoveSystem

**Behavior:**
- Query entities with `Position`, `Velocity`, `MoveTarget`, `CellCoord`
- If `MoveTarget.Active`: set `Velocity` toward target at `MoveParams.MaxSpeed`
- If within arrival epsilon (~1 unit): set `Velocity` to zero, `MoveTarget.Active = false`
- If not active: do nothing (velocity unchanged — other systems can control it)

**Developer code:**
```go
// Register system
coord.AddSystem("ClickToMove", mmokit.NewClickToMoveSystem())

// Input handler — game provides its own protobuf message type
mmokit.Handle(router, MyMoveEventCode, mmokit.States(mmokit.StateActive),
    func(ctx *mmokit.InputContext, msg *mypb.MoveTargetMsg) {
        mt := moveTargetMap.Get(ctx.Entity)
        mmokit.SetMoveTarget(mt, msg.TargetX, msg.TargetY)
    })
```

#### DirectionMoveSystem

**Behavior:**
- Query entities with `Position`, `Velocity`, `DirectionInput`
- If `DirectionInput.Active`: set `Velocity` = normalize(DirX, DirY) × `MoveParams.MaxSpeed`
- If not active: set `Velocity` to zero

**Developer code:**
```go
// Register system
coord.AddSystem("DirectionMove", mmokit.NewDirectionMoveSystem())

// Input handler
mmokit.Handle(router, MyInputCode, mmokit.States(mmokit.StateActive),
    func(ctx *mmokit.InputContext, msg *mypb.DirectionMsg) {
        di := dirInputMap.Get(ctx.Entity)
        di.X, di.Y = msg.DirX, msg.DirY
        di.Active = msg.Active
    })
```

### New Components

Added to `pkg/component/core.go`:

```go
// MoveParams holds per-entity movement configuration.
// Optional — systems use defaults if absent.
type MoveParams struct {
    MaxSpeed float32 // units/sec; 0 means use system default (300)
}

// DirectionInput holds WASD/joystick direction state.
// Set by the game's input handler each tick.
type DirectionInput struct {
    X, Y   float32 // direction vector (normalized by client)
    Active bool    // currently holding a direction key
}
```

`MoveTarget` already exists in `pkg/component/core.go`.

### Helper Functions

```go
// SetMoveTarget converts world-absolute coordinates to cell-local and activates.
// Uses math.Floor for correct negative coordinate handling.
func SetMoveTarget(mt *MoveTarget, worldX, worldY float32)

// CancelMoveTarget deactivates the move target.
func CancelMoveTarget(mt *MoveTarget)
```

These live in `pkg/system/` and are re-exported via `pkg/mmokit/`.

### System Internals

Both systems use the same pattern:

```go
type ClickToMoveSystem struct {
    engine.SystemBase
    filter    *ecs.Filter4[Position, Velocity, MoveTarget, CellCoord]
    paramsMap *ecs.Map1[MoveParams]   // optional lookup
    defaults  MoveParams              // configurable at construction
}

type DirectionMoveSystem struct {
    engine.SystemBase
    filter    *ecs.Filter3[Position, Velocity, DirectionInput]
    paramsMap *ecs.Map1[MoveParams]   // optional lookup
    defaults  MoveParams              // configurable at construction
}
```

Both skip `Ghost` and `Replica` entities (read-only, not simulated locally).

### Composability

- Systems only write `Velocity`. The existing `PhysicsSystem` integrates velocity → position.
- Games can register systems before/after in the pipeline for custom behavior.
- Games that need deceleration, drag, steering, or any other physics write their own system that also writes `Velocity` — standard ECS composition.

### File Structure

| File | Contents |
|------|----------|
| `pkg/component/core.go` | Add `MoveParams`, `DirectionInput` |
| `pkg/system/click_to_move.go` | `ClickToMoveSystem`, `SetMoveTarget`, `CancelMoveTarget` |
| `pkg/system/direction_move.go` | `DirectionMoveSystem` |
| `pkg/system/click_to_move_test.go` | Tests |
| `pkg/system/direction_move_test.go` | Tests |
| `pkg/mmokit/mmokit.go` | Re-export: `NewClickToMoveSystem`, `NewDirectionMoveSystem`, `SetMoveTarget`, `CancelMoveTarget`, `MoveParams`, `DirectionInput` |

---

## Part 3: Refactor 4node-basic Example

Refactor the 4node-basic example to use the new APIs, serving as both validation and reference implementation for external developers.

### Current State (to be replaced)

| File | Lines | What it does |
|------|-------|-------------|
| `system_network.go` | ~250 | Hand-rolled binary protocol: manual spatial queries, visibility tracking, ghost/replica dedup, frame building |
| `system_movement.go` | ~40 | Custom seek-and-arrive movement |
| `system_input.go` | ~50 | Manual world→cell-local coordinate conversion, MoveTarget setting |

### Target State

**Networking:** Replace `system_network.go` with `ReplicationSystem` + `AutoReplicator`.

```go
// replication.go — the entire networking setup
func setupReplication(gw *BasicWorld, registry *mmokit.ReplicatorRegistry) {
    posMap := ecs.NewMap1[mmokit.Position](gw.ECSWorld())
    cellMap := ecs.NewMap1[mmokit.CellCoord](gw.ECSWorld())
    velMap := ecs.NewMap1[mmokit.Velocity](gw.ECSWorld())
    colliderMap := ecs.NewMap1[mmokit.Collider](gw.ECSWorld())
    nameMap := ecs.NewMap1[Name](gw.ECSWorld())

    registry.Register(mmokit.AutoReplicator(KindPlayer,
        mmokit.ViewerRelativePos(posMap, cellMap),
        mmokit.QVelocity(velMap, 2000),
        mmokit.QSize(colliderMap, 500),
        mmokit.Component(nameMap),  // Name.Name tagged `net:"initial,string"`
    ))
}
```

**Movement:** Replace `system_movement.go` with `ClickToMoveSystem`.

```go
coord.AddSystem("ClickToMove", mmokit.NewClickToMoveSystem())
```

**Input:** Replace manual coordinate conversion with `SetMoveTarget` helper.

```go
mmokit.Handle(router, uint32(basicpb.BasicClientEventCode_BCE_MOVE_TARGET),
    mmokit.States(mmokit.StateActive),
    func(ctx *mmokit.InputContext, msg *basicpb.BasicMoveTargetMsg) {
        mt := gw.MoveTargetMap.Get(ctx.Entity)
        mmokit.SetMoveTarget(mt, msg.TargetX, msg.TargetY)
    })
```

### Files Changed

| File | Action |
|------|--------|
| `examples/4node-basic/system_network.go` | Delete — replaced by ReplicationSystem |
| `examples/4node-basic/system_movement.go` | Delete — replaced by ClickToMoveSystem |
| `examples/4node-basic/system_input.go` | Simplify — use SetMoveTarget helper |
| `examples/4node-basic/replication.go` | New — AutoReplicator setup (~20 lines) |
| `examples/4node-basic/world.go` | Update system registration to use new systems |
| `examples/4node-basic/components.go` | Add `net:"initial,string"` tag to Name component |

### Client Impact

The web client (`examples/4node-basic/web/`) currently parses the custom binary format. After this change, it must parse the standard `BinaryFrameWriter` wire format (the same format the slither client already parses). The client JS will need updating to match the new frame layout — this is expected and desirable since it means the client now uses the standard mmokit wire protocol.

---

## Verification Plan

### Auto-Replicator
1. Unit tests: create mock components with struct tags, verify Hash/Snapshot/Layout/InitialData output matches hand-written equivalents
2. Test optional components: verify zero-byte fallback when component absent
3. Test initial-only fields: verify they appear in InitialData but not Snapshot
4. Integration: register an AutoReplicator with ReplicationSystem, verify frames are sent correctly
5. Compare wire output against existing slither `snakeReplicator` for equivalent field definitions

### Movement Systems
1. Unit tests: create entity with MoveTarget, run ClickToMoveSystem, verify velocity direction and arrival stop
2. Unit tests: create entity with DirectionInput, run DirectionMoveSystem, verify velocity
3. Test SetMoveTarget with negative world coordinates (math.Floor correctness)
4. Test MoveParams override vs system defaults
5. Test Ghost/Replica skip behavior
6. Integration: wire into 4node-basic example, verify click-to-move works end-to-end

### Client Compatibility
- Wire format changes are acceptable — no legacy compatibility required
- All clients (main game, slither, 4node-basic web) update to match the new format
- If the wire format needs changes to better support auto-replication (e.g. consistent field ordering, improved header), make those changes
