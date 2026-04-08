# MMOKit Architecture Simplification

## Context

Analysis of the mmokit platform reveals ~45% of the space game code (internal/game/) is boilerplate — adapter layers, manual mapper construction, hand-coded network handlers, and interface implementations that WorldBase already handles. The GameWorld interface has 34 methods, but games only meaningfully override ~8 of them. The entity system has two patterns (old EntityDef/Registry and new EntityKindDef) coexisting. This spec consolidates three phases of improvement into one architectural vision.

This is a breaking change. No backward compatibility. All three games (space game, 4node-basic, slither) will be updated.

## Phase 1: Shrink GameWorld Interface + Eliminate Adapter

### Problem

The `GameWorld` interface has 34 methods. 19 are infrastructure that `WorldBase` handles automatically — no game ever overrides them. Games must embed `WorldBase` to get default implementations, but the space game adds an `gameWorldAdapter` wrapper (308 lines) because its `GameWorld` struct embeds `*Engine` instead of `*WorldBase`.

### Solution

**Shrink the interface to ~12 game-relevant methods:**

```go
type GameWorld interface {
    Init()
    Hooks() engine.Hooks
    Shutdown()

    // Transfer — games define how to serialize/deserialize game-specific components
    SerializeEntity(entity ecs.Entity) ([]byte, error)
    SpawnFromTransfer(data []byte) (netID uint32, connID uint32, err error)

    // Cross-node actions — game-specific combat, mining, etc.
    HandleCrossNodeAction(action *CrossNodeAction) *ActionResult
    HandleActionResult(result *ActionResult)

    // Chat — game controls how chat is dispatched
    DispatchChat(username, text string)

    // Bridge wiring
    SetBridge(bridge NodeBridge)

    // Cell bounds update (dynamic partitioning)
    UpdateCellBounds(cell CellID, cellSize float32)

    // Entity lifecycle
    MarkForRemoval(entity ecs.Entity)
}
```

**Methods moved to internal/non-interface (handled by WorldBase, called by bridge):**
- All 5 replica methods
- All 5 proxy methods
- All 6 promotion/dead reckoning methods
- TickGhosts, TickTransferCooldowns, RemoveGhostByNetID
- ECSWorld, GetAoIRadius, WakeDormantEntities

The bridge (`node_bridge_impl.go`) calls these via type assertion or direct WorldBase access instead of through the interface.

**Eliminate the adapter:**

The space game's `GameWorld` struct embeds `*WorldBase` directly (like 4node-basic and slither already do). The `gameWorldAdapter` is deleted. Game-specific overrides (`HandleCrossNodeAction`, `DispatchChat`, `Hooks`, `Shutdown`) become methods on the game's `GameWorld` struct.

### Files Changed (Phase 1)

| File | Change |
|------|--------|
| `pkg/universe/world.go` | Shrink interface from 34 → 12 methods |
| `pkg/universe/world_base.go` | Keep all methods, they're just no longer interface requirements |
| `pkg/universe/node_bridge_impl.go` | Call WorldBase methods directly (type-assert to `*WorldBase` or use concrete field) |
| `pkg/universe/node.go` | Update processMessage to access WorldBase directly for infrastructure methods |
| `internal/game/adapter.go` | **Delete entirely** |
| `internal/game/world.go` | GameWorld embeds `*mmokit.WorldBase`, add game-specific interface methods |
| `internal/game/factory.go` | Factory returns `*GameWorld` directly (no adapter wrapper) |
| `internal/game/game.go` | Move adapter Init() logic into GameWorld.Init() |
| `examples/4node-basic/world.go` | Already embeds WorldBase — verify interface compliance |
| `examples/slither/world.go` | Already embeds WorldBase — verify interface compliance |

---

## Phase 2: EntityKindDef Migration + AutoReplicator

### Problem

The space game has two entity patterns coexisting:
- **Old pattern** (space game): `*Mappers struct` + `initXxxEntity()` + `EntityDef/Registry` + manual `buildReplicationRegistry()` + 6 `nethandler_*.go` files (352 lines of hand-coded Hash/Snapshot)
- **New pattern** (4node-basic): `EntityKindDef` + `KindComponent()` + auto-discovery replication + `AutoReplicator` with struct tags

The old pattern requires ~950 lines of boilerplate. The new pattern achieves the same with ~100 lines.

### Solution

**Migrate all 5 entity types to EntityKindDef:**

```go
func (gw *GameWorld) initEntityKinds() {
    w := gw.ECSWorld()

    // Ship
    shipDef := mmokit.EntityKindDef{
        Kind: gamecomp.TypeShip,
        Name: "Ship",
        EngineBindings: &mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500},
    }
    mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.ShipControl](w))
    mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.Health](w))
    // ... all ship components
    gw.RegisterEntityKind(shipDef)

    // Asteroid, Station, NPC, LootCrate — same pattern
}
```

**Delete:**
- All `*Mappers` structs (5 structs)
- All `initXxxEntity()` functions (5 functions)
- `buildReplicationRegistry()` — auto-discovered from EntityKindDefs
- All `nethandler_*.go` files (6 files, 352 lines) — AutoReplicator handles replication
- `nethandler_shared.go` — base field helpers no longer needed
- `components.go` Components struct — mappers accessed via EntityKindDef registrations

**Keep:**
- Spawn functions (`SpawnPlayer`, `SpawnAsteroid`, etc.) — game-specific spawn logic stays
- `entity_ship.go` — but much smaller (spawn logic only, no mappers/init)

### Auto Transfer Restoration

Extend `EntityKindDef` with a `LocalOnly` option for components that are always added locally but never serialized:

```go
mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.PlayerInput](w), mmokit.LocalOnly())
mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.MiningLaser](w), mmokit.LocalOnly())
mmokit.KindComponent(&stationDef, ecs.NewMap1[gamecomp.Station](w), mmokit.LocalOnly())
```

`WorldBase.EnsureEntityKindComponents()` (already exists) handles adding these after transfer. `FinishTransferSpawn` in `transfer.go` becomes mostly unnecessary — only entity-type-specific config overrides remain (e.g., collider sizes from config).

### Files Changed (Phase 2)

| File | Change |
|------|--------|
| `pkg/universe/world_base.go` | Extend EnsureEntityKindComponents for LocalOnly |
| `pkg/mmokit/kind.go` | Add LocalOnly option to KindComponent |
| `internal/game/entity_ship.go` | Rewrite: EntityKindDef + spawn function only |
| `internal/game/entity_asteroid.go` | Same |
| `internal/game/entity_station.go` | Same |
| `internal/game/entity_npc.go` | Same |
| `internal/game/entity_lootcrate.go` | Same |
| `internal/game/nethandler_*.go` | **Delete all 6 files** |
| `internal/game/replicators.go` | **Delete** (auto-discovery) |
| `internal/game/components.go` | **Delete or simplify** — mappers from EntityKindDef |
| `internal/game/transfer.go` | Simplify — only config-based overrides remain |
| `internal/game/game.go` | Call `initEntityKinds()` instead of 5 separate init functions |

---

## Phase 3: Platform Polish

### 3a. Consolidate Coordinator Player Maps (3 → 1)

Replace `playerNode`, `activeUsers`, `disconnected` with a single `PlayerLocation` map:

```go
type PlayerLocation struct {
    NodeID string
    ConnID uint32
    Active bool // false = disconnected (grace period)
}

players    map[string]*PlayerLocation // username → location
connIndex  map[uint32]string          // connID → username (reverse lookup)
```

**Files:** `pkg/universe/coordinator.go`

### 3b. Simplify Engine Hooks (7 → 5)

- Remove `ClearTickState` — TickQueue auto-clears at tick start
- Remove `ProcessLogins` — already internal to PlayerManager
- Keep: `PreFlush`, `PostFlush`, `PostTick` (game-essential)
- `OnConnect`/`OnDisconnect` stay on PlayerManager (not hooks)

**Files:** `pkg/engine/loop.go`, `pkg/engine/player_manager.go`

### 3c. TickQueue as Framework Utility

Move `TickQueue` from game code to `pkg/engine/tick_queue.go`. Add `AutoClear()` so games don't need ClearTickState hooks.

**Files:** Create `pkg/engine/tick_queue.go`, remove from game package

### 3d. Composable SpawnEntity for All Games

Extend `WorldBase.SpawnEntity()` with option functions so games use a consistent pattern:

```go
entity := gw.SpawnEntity(
    mmokit.Position{X: x, Y: y},
    mmokit.WithCollider(radius),
    mmokit.WithEntityKind(gamecomp.TypeShip),
    mmokit.WithComponents(), // auto-adds all registered kind components
)
```

Already exists — space game just needs to adopt it in spawn functions.

### 3e. Reduce Coordinator Public Methods

Make internal: `getPlayerNode`, `setPlayerNode`, `removePlayerNode`, `getNode`, `getNodeOwner`, `buildNodeRefs`, `defaultEntityOpts`. These are implementation details, not API.

**Files:** `pkg/universe/coordinator.go`

---

## Implementation Order

1. Phase 1 first (interface + adapter) — foundation for everything else
2. Phase 2 next (EntityKindDef) — biggest code reduction, requires Phase 1
3. Phase 3 last (polish) — independent improvements, any order

## Estimated Impact

| Metric | Before | After |
|--------|--------|-------|
| internal/game LOC | ~7,056 | ~4,200 (-40%) |
| GameWorld interface | 34 methods | 12 methods (-65%) |
| Entity files (total) | 5 files, 592 LOC | 5 files, ~250 LOC (-58%) |
| Network handler files | 6 files, 352 LOC | 0 files (-100%) |
| Adapter | 308 LOC | 0 LOC (-100%) |
| Replication registry | 40 LOC manual | 0 LOC (auto) |
| Components struct | 74 LOC | ~10 LOC or eliminated |

## Verification

After each phase:
1. `go vet ./...` — all packages compile
2. `go test ./pkg/... -count=1` — all engine tests pass
3. `make dev` — space game runs, login works, combat works, transfers work
4. Examples: `cd examples/4node-basic && go build .` and `cd examples/slither && go build .`
5. Dynamic cells: `--dynamic-cells` + `cell split 1 1` + `cell merge` — no regressions
