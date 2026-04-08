# Phase 2: EntityKindDef Migration + AutoReplicator

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate all 5 entity types from the old `*Mappers struct + EntityDef/Registry + hand-coded nethandlers` pattern to the modern `EntityKindDef + AutoReplicator` pattern. Delete ~700 lines of boilerplate.

**Architecture:** Each entity type gets an `EntityKindDef` with `KindComponent()` registrations. The `AutoReplicator` (already used by 4node-basic) auto-discovers replication from these definitions. A new `LocalOnly()` option marks components that are always added locally but never serialized for transfer. The old `buildReplicationRegistry()`, all `nethandler_*.go` files, and all `*Mappers` structs are deleted.

**Tech Stack:** Go, ECS (Ark), mmokit AutoReplicator

**Prerequisites:** Phase 1 must be complete (GameWorld embeds WorldBase, adapter eliminated).

**Important:** Keep the `Components` struct (`gw.C.Health`, `gw.C.Position`, etc.) — it's a game-wide convenience accessor used by ~193 references across 25 system files. Only entity definition/replication patterns change.

---

### Task 1: Add LocalOnly option to KindComponent

**Files:**
- Modify: `pkg/mmokit/kind.go` (or wherever `KindComponent` is defined)
- Modify: `pkg/universe/world_base.go` (`EnsureEntityKindComponents`)

- [ ] **Step 1: Find KindComponent definition**

Search for `func KindComponent` in `pkg/mmokit/` to find the exact file and signature. Read it to understand the current API.

- [ ] **Step 2: Add LocalOnly option**

Add a variadic option parameter to `KindComponent`:

```go
type KindComponentOption int

const (
    LocalOnly KindComponentOption = 1 // component added locally after transfer, never serialized
)

func KindComponent[T any](def *EntityKindDef, m *ecs.Map1[T], opts ...KindComponentOption) {
    isLocalOnly := false
    for _, o := range opts {
        if o == LocalOnly {
            isLocalOnly = true
        }
    }
    // ... existing registration logic
    // If LocalOnly, register as ensureExists but NOT as transfer component
}
```

The exact implementation depends on how `KindComponent` currently works — read the code first. The key behavior: LocalOnly components get an `ensureExists` callback (added after transfer if missing) but are NOT registered with the `ReplicationRegistry` for serialization.

- [ ] **Step 3: Update EnsureEntityKindComponents if needed**

In `pkg/universe/world_base.go`, `EnsureEntityKindComponents` already adds zero-value components after transfer. Verify it handles LocalOnly components correctly — they should always be added, not just when missing from the transfer frame.

- [ ] **Step 4: Verify compilation**

Run: `go vet ./pkg/mmokit/ && go vet ./pkg/universe/`

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/ pkg/universe/world_base.go
git commit -m "feat: add LocalOnly option to KindComponent for transfer-excluded components"
```

---

### Task 2: Create EntityKindDef for Ship

The ship is the most complex entity type. Migrate it first as the reference pattern.

**Files:**
- Modify: `internal/game/entity_ship.go`
- Modify: `internal/game/game.go` (Init — register entity kinds)

- [ ] **Step 1: Read current ship entity setup**

Read `internal/game/entity_ship.go` to understand:
- What components the `shipMappers` struct contains
- What `initShipEntity` does
- What `SpawnPlayer` does (this stays, just uses new mappers)

Also read `internal/game/nethandler_ship.go` to understand what components are replicated (Hash/Snapshot methods reveal which components the network sends).

- [ ] **Step 2: Create ship EntityKindDef**

In `internal/game/game.go` (or a new `internal/game/entity_kinds.go` file), create:

```go
func (gw *GameWorld) initEntityKinds() {
    w := gw.ECSWorld()

    // Ship — player-controlled vessel
    shipDef := mmokit.EntityKindDef{
        Kind: gamecomp.TypeShip,
        Name: "Ship",
        EngineBindings: &mmokit.EngineBindingsConfig{
            VelQuantScale:  2000,
            SizeQuantScale: 500,
        },
    }
    // Replicated components (sent to other clients via AutoReplicator)
    mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.Health](w))
    mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.Shield](w))
    mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.ShipControl](w))
    mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.Equipment](w))
    mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.Inventory](w))
    mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.TargetLock](w))
    mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.AbilitySet](w))
    mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.StatusEffects](w))
    mmokit.KindComponent(&shipDef, ecs.NewMap1[mmokit.MoveTarget](w))
    // Local-only components (added after transfer, not serialized)
    mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.PlayerInput](w), mmokit.LocalOnly())
    mmokit.KindComponent(&shipDef, ecs.NewMap1[gamecomp.MiningLaser](w), mmokit.LocalOnly())
    gw.RegisterEntityKind(shipDef)
}
```

**Important:** Check which components the current nethandler Hash/Snapshot methods include. These are the replicated ones. Components NOT in the nethandler but added in `initShipEntity` or `FinishTransferSpawn` are LocalOnly.

- [ ] **Step 3: Remove old shipMappers struct and initShipEntity function**

Delete the `shipMappers` struct and `initShipEntity()` function from `entity_ship.go`. Keep `SpawnPlayer`, `reconnectPlayer`, and other ship-specific logic.

- [ ] **Step 4: Update SpawnPlayer to use new entity creation pattern**

`SpawnPlayer` currently uses `m.base.NewEntity(...)`. With EntityKindDef, use `gw.SpawnEntity()` or create the entity using the registered kind's mappers. Read how 4node-basic's `spawnPlayer` works for reference.

- [ ] **Step 5: Verify compilation**

Run: `go vet ./internal/game/`

- [ ] **Step 6: Commit**

```bash
git add internal/game/
git commit -m "feat: migrate ship entity to EntityKindDef pattern"
```

---

### Task 3: Create EntityKindDefs for remaining entity types

Migrate Asteroid, Station, NPC, and LootCrate following the same pattern as Ship.

**Files:**
- Modify: `internal/game/entity_asteroid.go`
- Modify: `internal/game/entity_station.go`
- Modify: `internal/game/entity_npc.go`
- Modify: `internal/game/entity_lootcrate.go`

- [ ] **Step 1: Read each entity file and its nethandler**

For each entity type, read:
- `entity_*.go` — mappers struct, init function, spawn function
- `nethandler_*.go` — which components are replicated

- [ ] **Step 2: Add EntityKindDefs to initEntityKinds()**

For each entity type, add a definition block in `initEntityKinds()`:

**Asteroid:**
```go
    asteroidDef := mmokit.EntityKindDef{
        Kind: gamecomp.TypeAsteroid,
        Name: "Asteroid",
        EngineBindings: &mmokit.EngineBindingsConfig{SizeQuantScale: 500},
    }
    mmokit.KindComponent(&asteroidDef, ecs.NewMap1[gamecomp.Minable](w))
    gw.RegisterEntityKind(asteroidDef)
```

**Station:**
```go
    stationDef := mmokit.EntityKindDef{
        Kind: gamecomp.TypeStation,
        Name: "Station",
        EngineBindings: &mmokit.EngineBindingsConfig{SizeQuantScale: 500},
    }
    mmokit.KindComponent(&stationDef, ecs.NewMap1[gamecomp.Station](w), mmokit.LocalOnly())
    gw.RegisterEntityKind(stationDef)
```

**NPC:**
```go
    npcDef := mmokit.EntityKindDef{
        Kind: gamecomp.TypeNPC,
        Name: "NPC",
        EngineBindings: &mmokit.EngineBindingsConfig{VelQuantScale: 2000, SizeQuantScale: 500},
    }
    mmokit.KindComponent(&npcDef, ecs.NewMap1[gamecomp.Health](w))
    mmokit.KindComponent(&npcDef, ecs.NewMap1[gamecomp.Shield](w))
    mmokit.KindComponent(&npcDef, ecs.NewMap1[gamecomp.StatusEffects](w))
    gw.RegisterEntityKind(npcDef)
```

**LootCrate:**
```go
    lootDef := mmokit.EntityKindDef{
        Kind: gamecomp.TypeLootCrate,
        Name: "LootCrate",
        EngineBindings: &mmokit.EngineBindingsConfig{},
    }
    mmokit.KindComponent(&lootDef, ecs.NewMap1[gamecomp.Inventory](w))
    mmokit.KindComponent(&lootDef, ecs.NewMap1[mmokit.Lifetime](w))
    mmokit.KindComponent(&lootDef, ecs.NewMap1[gamecomp.LootCrate](w), mmokit.LocalOnly())
    gw.RegisterEntityKind(lootDef)
```

- [ ] **Step 3: Remove old mappers structs and init functions**

Delete `asteroidMappers`, `stationMappers`, `npcMappers`, `lootCrateMappers` structs and their `init*Entity()` functions. Keep spawn functions.

- [ ] **Step 4: Update spawn functions**

Update `SpawnAsteroid`, `SpawnStation`, `SpawnNPC`, `SpawnLootCrate` to use `gw.SpawnEntity()` or equivalent. Follow the pattern established in Task 2.

- [ ] **Step 5: Verify compilation**

Run: `go vet ./internal/game/`

- [ ] **Step 6: Commit**

```bash
git add internal/game/
git commit -m "feat: migrate all entity types to EntityKindDef pattern"
```

---

### Task 4: Switch to AutoReplicator — delete nethandlers

**Files:**
- Delete: `internal/game/nethandler_ship.go`
- Delete: `internal/game/nethandler_asteroid.go`
- Delete: `internal/game/nethandler_station.go`
- Delete: `internal/game/nethandler_npc.go`
- Delete: `internal/game/nethandler_lootcrate.go`
- Delete: `internal/game/nethandler_shared.go`
- Delete: `internal/game/replicators.go`
- Modify: `internal/game/game.go` (Init — use auto-discovered replication)

- [ ] **Step 1: Update Init() to use auto-discovered replication**

In `GameWorld.Init()`, remove the call to `buildReplicationRegistry()` and `SetReplicationRegistry()`. Instead, the NetworkSystem should auto-discover replicators from registered EntityKindDefs.

Check how 4node-basic does it — it uses `mmokit.NewNetworkSystem()` which auto-discovers replicators. The space game currently uses a custom `NetworkSystem` struct. Update it to use auto-discovery, or if it already does, just remove the manual registry building.

- [ ] **Step 2: Delete all nethandler files**

Delete all 6 nethandler files:
```bash
rm internal/game/nethandler_ship.go
rm internal/game/nethandler_asteroid.go
rm internal/game/nethandler_station.go
rm internal/game/nethandler_npc.go
rm internal/game/nethandler_lootcrate.go
rm internal/game/nethandler_shared.go
```

- [ ] **Step 3: Delete replicators.go**

Delete `internal/game/replicators.go` — `buildReplicationRegistry()` is no longer needed.

- [ ] **Step 4: Fix compilation errors**

Remove any imports or references to deleted functions/files. Update the `NetworkSystem` if it referenced the old replication registry.

- [ ] **Step 5: Verify compilation**

Run: `go vet ./internal/game/`

- [ ] **Step 6: Run tests**

Run: `go test ./pkg/... -count=1`

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "refactor: delete nethandlers and replication registry — AutoReplicator handles all"
```

---

### Task 5: Simplify transfer.go

**Files:**
- Modify: `internal/game/transfer.go`

- [ ] **Step 1: Review what FinishTransferSpawn still needs**

With LocalOnly components auto-added by `EnsureEntityKindComponents`, most of `FinishTransferSpawn`'s per-entity-type logic is redundant. Read `transfer.go` and identify what remains essential:
- Config-based overrides (e.g., collider radius from `gw.Config.ShipWidth`)
- Equipment stat application (`gw.ApplyEquipmentStats(entity)`)
- Any logic that can't be expressed as a zero-value LocalOnly component

- [ ] **Step 2: Simplify FinishTransferSpawn**

Remove component additions that are now handled by LocalOnly. Keep only config-based overrides:

```go
func (gw *GameWorld) FinishTransferSpawn(entity ecs.Entity, frame *mmokit.TransferFrame) {
    switch frame.EntityType {
    case gamecomp.TypeShip:
        // Override collider with config values (can't be a zero-value default)
        if gw.C.Collider.HasAll(entity) {
            col := gw.C.Collider.Get(entity)
            br := boundingRadius(gw.Config.ShipWidth, gw.Config.ShipHeight)
            col.Radius = br
            col.Width = gw.Config.ShipWidth
            col.Height = gw.Config.ShipHeight
            col.Layer = gamecomp.LayerPlayer
            col.Shape = mmokit.ShapeRect
        }
        gw.ApplyEquipmentStats(entity)

    case gamecomp.TypeNPC:
        if gw.C.Collider.HasAll(entity) {
            col := gw.C.Collider.Get(entity)
            col.Radius = boundingRadius(gw.Config.NpcWidth, gw.Config.NpcHeight)
            col.Width = gw.Config.NpcWidth
            col.Height = gw.Config.NpcHeight
            col.Layer = gamecomp.LayerPlayer
            col.Shape = mmokit.ShapeRect
        }
    }
}
```

- [ ] **Step 3: Verify compilation**

Run: `go vet ./internal/game/`

- [ ] **Step 4: Commit**

```bash
git add internal/game/transfer.go
git commit -m "refactor: simplify FinishTransferSpawn — LocalOnly handles component restoration"
```

---

### Task 6: Update game.go Init to call initEntityKinds

**Files:**
- Modify: `internal/game/game.go`

- [ ] **Step 1: Replace individual init calls with initEntityKinds**

In `NewGameWorld` (or `Init`), replace:
```go
initShipEntity(gw)
initAsteroidEntity(gw)
initStationEntity(gw)
initLootCrateEntity(gw)
initNpcEntity(gw)
```

with:
```go
gw.initEntityKinds()
```

- [ ] **Step 2: Verify compilation and tests**

Run: `go vet ./internal/game/ && go test ./pkg/... -count=1`

- [ ] **Step 3: Commit**

```bash
git add internal/game/game.go
git commit -m "refactor: replace individual entity init with initEntityKinds()"
```

---

### Task 7: Verify and smoke test

- [ ] **Step 1: Full compilation check**

Run: `go vet ./...` (or `go vet ./pkg/... ./internal/game/ ./cmd/server/`)

- [ ] **Step 2: Run tests**

Run: `go test ./pkg/... -count=1 -timeout=60s`

- [ ] **Step 3: Manual smoke test**

Start server: `make dev`
- Login, verify ship spawns with all components (HP, shield, equipment, mining laser)
- Shoot another entity — verify combat works (cross-node if multi-node)
- Mine an asteroid — verify mining works
- Pick up loot crate — verify inventory
- Dock at station — verify station is dockable
- Cross cell boundary — verify entity transfer preserves all components
- `cell split 1 1` — verify dynamic cells work
- Verify replication: other players/entities visible and updating smoothly

- [ ] **Step 4: Commit any fixes**

```bash
git add -A
git commit -m "fix: Phase 2 smoke test fixes"
```
