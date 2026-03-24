# Engine Extraction Phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete extraction of a generic, open-source-ready 2D game engine in `pkg/` by removing backward-compat shims, extracting the order book matching engine, extracting server meshing infrastructure, and splitting the protobuf schema.

**Architecture:** Four sequential items: (0) remove re-export shims and update all callers to import directly from `pkg/`, (1) extract generic order book matching to `pkg/orderbook/`, (2) extract generic coordinator/node/replica to `pkg/universe/` behind a `GameWorld` interface with byte-serialized transfers, (3) split `proto/game.proto` into `proto/engine/engine.proto` + `proto/game/game.proto`.

**Tech Stack:** Go 1.23+, Ark ECS v0.7.1, protobuf (buf), TypeScript/PixiJS web client

**Spec:** `docs/superpowers/specs/2026-03-24-engine-extraction-phase2-design.md`

---

## Item 0: Remove Backward-Compat Shims

### Task 1: Remove TickQueue shim — update all callers

The shim file `internal/game/tickqueue.go` re-exports `engine.TickQueue`, `engine.Enqueue`, `engine.Drain`, `engine.Peek`. Delete it and update all callers to import `pkg/engine` directly.

**Files:**
- Delete: `internal/game/tickqueue.go`
- Delete or update: `internal/game/tickqueue_test.go` (tests the shim — delete since pkg/engine has its own tests)
- Modify: `internal/game/game.go` (NewTickQueue call, Queue field)
- Modify: `internal/game/lifecycle.go` (Drain calls)
- Modify: `internal/system/input.go` (~17 Enqueue calls)
- Modify: `internal/system/ability.go` (Enqueue)
- Modify: `internal/system/docking.go` (Drain)
- Modify: `internal/system/economy.go` (~6 Drain calls)
- Modify: `internal/system/equipment.go` (Enqueue)
- Modify: `internal/system/network.go` (Drain/Enqueue)
- Modify: `internal/universe/node.go` (Enqueue)

- [ ] **Step 1: Delete the shim files**

Delete `internal/game/tickqueue.go` and `internal/game/tickqueue_test.go` (tests the shim wrappers — underlying logic is tested in `pkg/engine/`).

- [ ] **Step 2: Update `internal/game/game.go`**

Change `Queue: NewTickQueue()` → `Queue: engine.NewTickQueue()`. The `Queue` field type is already `*engine.TickQueue` (same type via alias). Add `"github.com/zenion/mmoserver/pkg/engine"` import if not present.

- [ ] **Step 3: Update `internal/game/lifecycle.go`**

Replace all `Drain[` calls from `game.Drain[` → `engine.Drain[`. Add `engine` import alias if needed (file already imports `game` package for other things — lifecycle.go IS in the game package, so these are unqualified `Drain[...]` calls). Since lifecycle.go is IN the `game` package, the calls are just `Drain[T](gw.Queue)`. After deleting the shim, these won't compile. Change to `engine.Drain[T](gw.Queue)` with import `"github.com/zenion/mmoserver/pkg/engine"`.

- [ ] **Step 4: Update all system files**

For each system file (`input.go`, `ability.go`, `docking.go`, `economy.go`, `equipment.go`, `network.go`): replace `game.Enqueue[` → `engine.Enqueue[` and `game.Drain[` → `engine.Drain[`. Add `"github.com/zenion/mmoserver/pkg/engine"` import.

- [ ] **Step 5: Update `internal/universe/node.go`**

Replace `game.Enqueue[` → `engine.Enqueue[`. Add engine import.

- [ ] **Step 6: Build and verify**

Run: `make build`
Expected: Clean build with no errors.

- [ ] **Step 7: Commit**

```bash
git add -A && git commit -m "refactor: remove TickQueue shim, callers import pkg/engine directly"
```

---

### Task 2: Remove EntityRegistry shim — update callers

The shim file `internal/game/registry.go` re-exports `engine.EntityDef`, `engine.EntityRegistry`, `engine.NewEntityRegistry`. Delete it and update callers.

**Files:**
- Delete: `internal/game/registry.go`
- Modify: `internal/game/game.go:35` (NewEntityRegistry call)
- Modify: `internal/game/world.go:107` (Registry field type)
- Modify: `internal/game/entity_ship.go:34` (EntityDef usage)
- Modify: `internal/game/entity_asteroid.go:24`
- Modify: `internal/game/entity_station.go:22`
- Modify: `internal/game/entity_lootcrate.go:23`
- Modify: `internal/game/entity_npc.go:21`
- Modify: `internal/game/commands.go` (Registry iteration)
- Modify: `internal/game/registry_test.go` (test file)

- [ ] **Step 1: Delete the shim file**

Delete `internal/game/registry.go`.

- [ ] **Step 2: Update all callers in `internal/game/`**

All files are within the `game` package, so they used unqualified `EntityDef`, `EntityRegistry`, `NewEntityRegistry`. Replace with `engine.EntityDef`, `engine.EntityRegistry`, `engine.NewEntityRegistry`. Add `"github.com/zenion/mmoserver/pkg/engine"` import to each file.

- [ ] **Step 3: Update registry_test.go**

Same pattern — add engine import, qualify types.

- [ ] **Step 4: Build and test**

Run: `make build && go test ./internal/game/`
Expected: Build succeeds, all tests pass.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "refactor: remove EntityRegistry shim, callers import pkg/engine directly"
```

---

### Task 3: Remove component type aliases — update all 37 files

Remove the 16 type aliases from `internal/component/components.go` and update all 37 importing files. Files that use ONLY generic types switch to `pkg/component`. Files that use BOTH generic and game-specific types use aliased imports.

**Files:**
- Modify: `internal/component/components.go` — remove aliases and `pkgcomp` import
- Modify: 37 files listed in exploration (all files importing `internal/component`)

- [ ] **Step 1: Analyze each file's component usage**

For each of the 37 files, determine which components it uses:
- **Generic only** (Position, Velocity, Rotation, Collider, NetworkID, EntityKind, Health, Shield, Lifetime, PlayerConn, SectorCoord, Ghost, Replica, TransferCooldown, MoveTarget, TargetLock) → switch import to `pkg/component`
- **Game-specific only** (ShipControl, Minable, MiningLaser, Inventory, PlayerInput, LootCrate, Station, Equipment, AbilitySet, StatusEffects, StatusType, StatusEffect, etc.) → keep `internal/component`
- **Both** → use aliased imports: `comp "pkg/component"` and `gamecomp "internal/component"`

- [ ] **Step 2: Update `internal/component/components.go`**

Remove all 16 type alias lines and the `pkgcomp` import. Keep game-specific type definitions. The file still imports `"github.com/mlange-42/ark/ecs"` (for MiningLaser.Target field), `gamepb` (for entity/resource type constants), and `"github.com/zenion/mmoserver/internal/item"` (for Inventory.TotalMass).

- [ ] **Step 3: Update `internal/game/components.go`**

The `Components` struct has mappers for both generic and game-specific components. It needs both imports:
```go
import (
    "github.com/mlange-42/ark/ecs"
    comp "github.com/zenion/mmoserver/pkg/component"
    gamecomp "github.com/zenion/mmoserver/internal/component"
)
```
Update each mapper's type parameter: `component.Position` → `comp.Position` for generic, `component.ShipControl` → `gamecomp.ShipControl` for game-specific.

- [ ] **Step 4: Update system files (batch)**

Most system files use both generic and game-specific components. Add dual imports with aliases. Prefix generic component references with `comp.` and game-specific with `gamecomp.`.

Files: `physics.go`, `spatial.go`, `collision.go`, `lifetime.go`, `network.go`, `sector_boundary.go`, `shipcontrol.go`, `targetlock.go`, `ability.go`, `mining.go`, `docking.go`, `economy.go`, `equipment.go`, `shieldregen.go`, `statuseffect.go`, `input.go`, `nethandler_*.go` (6 files).

- [ ] **Step 5: Update `internal/game/` files (batch)**

Files: `world.go`, `game.go`, `entity_ship.go`, `entity_asteroid.go`, `entity_station.go`, `entity_lootcrate.go`, `entity_npc.go`, `belts.go`, `combat_helpers.go`, `commands.go`, `droptable.go`, `transfer.go`, `transfer_types.go`, `transfer_test.go`.

Same dual-import pattern where needed.

- [ ] **Step 6: Update `internal/universe/` files**

Files: `node.go`, `replica.go`, `node_test.go`, `replica_test.go`.

- [ ] **Step 7: Build and test**

Run: `make build && go test ./...`
Expected: Clean build, all tests pass.

- [ ] **Step 8: Commit**

```bash
git add -A && git commit -m "refactor: remove component type aliases, callers import pkg/component directly"
```

---

### Task 4: Remove topology and message shims — update callers

Delete the topology shim in `internal/universe/topology.go`. Clean up message re-exports in `internal/universe/message.go`. Update transfer_types.go aliases.

**Files:**
- Delete: `internal/universe/topology.go` (shim re-exporting from pkg/universe)
- Modify: `internal/universe/message.go` — remove re-exported constants/aliases, keep NodeMessage
- Modify: `internal/universe/coordinator.go` — import `pkg/universe` for ComputeTopology, SectorID
- Modify: `internal/universe/node.go` — import `pkg/universe` for SectorID
- Modify: `internal/universe/bridge.go` — use `pkguniverse` types
- Modify: `internal/game/transfer_types.go` — remove ArrivalConfirmMsg, ChatRelay, RespawnTransfer aliases
- Modify: `internal/game/nodebridge.go` — update interface to use `pkguniverse` types for ArrivalConfirmMsg; rename RespawnTransfer → SpawnTransfer
- Modify: `internal/game/lifecycle.go` — update RespawnTransfer calls
- Modify: `internal/system/input.go` — update ChatRelay bridge calls
- Modify: test files: `topology_test.go`, `coordinator_test.go`, `replica_test.go`, `node_test.go`

- [ ] **Step 1: Delete topology shim**

Delete `internal/universe/topology.go`.

- [ ] **Step 2: Update `internal/universe/message.go`**

Remove all re-exported type aliases and constants. Keep only:
```go
package universe

import (
    "github.com/zenion/mmoserver/internal/game"
    pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

type NodeMessage struct {
    Type       pkguniverse.MsgType
    FromNodeID string
    Transfer       *game.TransferPayload
    ArrivalConfirm *pkguniverse.ArrivalConfirmMsg
    Replicas       []game.ReplicaSnapshot
    Chat           *pkguniverse.ChatRelay
    Spawn          *pkguniverse.SpawnTransfer
}
```

Note: rename `Respawn` field → `Spawn`, `RespawnTransfer` → `SpawnTransfer`.

- [ ] **Step 3: Update `internal/game/transfer_types.go`**

Remove the three type aliases. Keep only `TransferPayload` and `ReplicaSnapshot`.

- [ ] **Step 4: Rename RespawnTransfer → SpawnTransfer everywhere**

Update `pkg/universe/message.go`: rename `RespawnTransfer` → `SpawnTransfer`, `MsgRespawnTransfer` → `MsgSpawnTransfer`.

Update `internal/game/nodebridge.go`: rename `RespawnTransfer` method → `RequestSpawnOnNode`, rename `ChatRelay` method → `RelayChatToOtherNodes`. Update parameter types to use `pkguniverse.ArrivalConfirmMsg`. Update NoopNodeBridge accordingly.

- [ ] **Step 5: Update all callers of renamed methods**

- `internal/universe/bridge.go` — update method names and types
- `internal/universe/node.go` — update message dispatch (MsgRespawnTransfer → MsgSpawnTransfer, Respawn → Spawn)
- `internal/game/lifecycle.go` — update bridge calls
- `internal/system/input.go` — update ChatRelay → RelayChatToOtherNodes calls
- `internal/universe/coordinator.go` — add `pkguniverse "github.com/zenion/mmoserver/pkg/universe"` import, use for ComputeTopology, SectorID
- Test files: update accordingly

- [ ] **Step 6: Build and test**

Run: `make build && go test ./...`
Expected: Clean build, all tests pass.

- [ ] **Step 7: Commit**

```bash
git add -A && git commit -m "refactor: remove topology/message shims, rename RespawnTransfer to SpawnTransfer"
```

---

## Item 1: Extract Order Book

### Task 5: Move generic order book files to `pkg/orderbook/`

Move the pure-generic files from `internal/marketplace/` to `pkg/orderbook/`.

**Files:**
- Create: `pkg/orderbook/book.go` (from `internal/marketplace/book.go`)
- Create: `pkg/orderbook/types.go` (from `internal/marketplace/types.go`)
- Create: `pkg/orderbook/config.go` (from `internal/marketplace/config.go`)
- Create: `pkg/orderbook/persist.go` (from `internal/marketplace/persist.go`)
- Create: `pkg/orderbook/book_test.go` (from `internal/marketplace/book_test.go`)
- Modify: `internal/marketplace/service.go` — update imports to `pkg/orderbook`
- Modify: `internal/marketplace/handler.go` — update imports
- Modify: `cmd/server/main.go` — update imports

- [ ] **Step 1: Create `pkg/orderbook/` directory and copy files**

Copy `book.go`, `types.go`, `config.go`, `persist.go`, `book_test.go` to `pkg/orderbook/`. Change package declaration from `marketplace` to `orderbook`.

- [ ] **Step 2: Rename StationID → LocationID in types.go**

In `pkg/orderbook/types.go`: rename `StationID` field to `LocationID` in `Order` struct and `bookKey` struct. In `pkg/orderbook/book.go`: no changes needed (doesn't reference StationID).

- [ ] **Step 3: Update `persist.go` import paths**

Change self-references from `marketplace` types to `orderbook` types. The file uses `Order`, `Trade`, `bookKey` — all now in same package. Update `pkg/persist` import path (should be unchanged).

- [ ] **Step 4: Delete moved files from `internal/marketplace/`**

Delete `book.go`, `types.go`, `config.go`, `persist.go`, `book_test.go` from `internal/marketplace/`.

- [ ] **Step 5: Update `internal/marketplace/service.go` imports**

Add `"github.com/zenion/mmoserver/pkg/orderbook"` import. Types like `Order`, `OrderBook`, `Config`, `PlaceResult`, `bookKey` now come from `orderbook` package. Prefix references: `orderbook.Order`, `orderbook.OrderBook`, etc.

- [ ] **Step 6: Update `internal/marketplace/handler.go` imports**

Add orderbook import for types like `PlaceResult`, `SideBuy`.

- [ ] **Step 7: Update `cmd/server/main.go`**

Change `marketplace.Config{...}` → `orderbook.Config{...}`. Add `"github.com/zenion/mmoserver/pkg/orderbook"` import.

- [ ] **Step 8: Build and test**

Run: `make build && go test ./internal/marketplace/ && go test ./pkg/orderbook/`
Expected: Build succeeds, all tests pass.

- [ ] **Step 9: Commit**

```bash
git add -A && git commit -m "refactor: move generic order book files to pkg/orderbook/"
```

---

### Task 6: Split service.go — extract matching engine

Extract the pure matching logic from `internal/marketplace/service.go` into `pkg/orderbook/service.go`. Create `internal/marketplace/settlement.go` for game-specific bank/currency operations.

**Files:**
- Create: `pkg/orderbook/service.go` — generic matching engine
- Create: `internal/marketplace/settlement.go` — game-specific settlement
- Delete: `internal/marketplace/service.go` (replaced by settlement.go)
- Modify: `internal/marketplace/handler.go` — calls Settlement instead of Service
- Modify: `cmd/server/main.go` — creates Settlement wrapping orderbook.Service

- [ ] **Step 1: Create `pkg/orderbook/service.go`**

Extract the core matching engine. Key methods return `[]MatchEvent` instead of directly mutating banks:

```go
package orderbook

type MatchEvent struct {
    BuyOrderID  uint64
    SellOrderID uint64
    BuyerID     string
    SellerID    string
    ItemID      uint32
    LocationID  uint32
    Quantity    int32
    Price       int64
}

type Service struct {
    mu     sync.Mutex
    books  map[bookKey]*OrderBook
    orders map[uint64]*Order
    cfg    Config
    nextID uint64
    writer *persist.AsyncWriter
}
```

Methods:
- `PlaceSellOrder(sellerID string, locationID, itemID uint32, price int64, qty int32) ([]MatchEvent, uint64, error)` — matching loop only, returns fills + resting order ID
- `PlaceBuyOrder(buyerID string, locationID, itemID uint32, price int64, qty int32) ([]MatchEvent, uint64, error)`
- `InstantSell(sellerID string, locationID, itemID uint32, qty int32) ([]MatchEvent, error)`
- `InstantBuy(buyerID string, locationID, itemID uint32, qty int32) ([]MatchEvent, error)`
- `CancelOrder(playerID string, orderID uint64) (*Order, error)` — removes from book, returns order for refund
- `ExpireOrders() []*Order` — removes expired, returns for refund
- `Browse(locationID, itemID uint32) OrderBookView`
- `PlayerOrders(playerID string) []*Order`
- `NewService(cfg Config, writer *persist.AsyncWriter) *Service`
- `LoadAll(store persist.Store) error`

The matching loop from current `PlaceSellOrder` (lines ~157-210) moves here. It creates `MatchEvent` entries instead of calling `bank.ModifyBank`/`bank.ModifyFlux`.

- [ ] **Step 2: Create `internal/marketplace/settlement.go`**

Game-specific settlement wrapping `orderbook.Service`:

```go
package marketplace

type Settlement struct {
    ob     *orderbook.Service
    bank   BankOps
    log    *logger.Logger
    cfg    orderbook.Config
    notify func(username string, code uint32, payload []byte)
}

func NewSettlement(ob *orderbook.Service, bank BankOps, cfg orderbook.Config,
    log *logger.Logger, notify func(string, uint32, []byte)) *Settlement

func (s *Settlement) PlaceSellOrder(seller string, locationID, itemID uint32, price int64, qty int32) (*PlaceResult, error)
func (s *Settlement) PlaceBuyOrder(buyer string, locationID, itemID uint32, price int64, qty int32) (*PlaceResult, error)
func (s *Settlement) InstantSell(seller string, locationID, itemID uint32, qty int32) (*PlaceResult, error)
func (s *Settlement) InstantBuy(buyer string, locationID, itemID uint32, qty int32) (*PlaceResult, error)
func (s *Settlement) CancelOrder(player string, orderID uint64) error
func (s *Settlement) ExpireOrders()
func (s *Settlement) Browse(locationID, itemID uint32) orderbook.OrderBookView
func (s *Settlement) PlayerOrders(player string) []*orderbook.Order
```

Each method: validates bank state → calls `ob.Method()` → processes MatchEvents (Flux transfers with tax, item transfers) → sends notifications → logs.

Move `BankOps` struct, `fluxItemID` constant, `logCatMarket` constant, `sendTradeNotification` from old service.go.

- [ ] **Step 3: Delete `internal/marketplace/service.go`**

All logic is now in `pkg/orderbook/service.go` (matching) and `internal/marketplace/settlement.go` (bank ops).

- [ ] **Step 4: Update `internal/marketplace/handler.go`**

Change handler functions to call `Settlement` methods instead of `Service`. The function signatures are compatible — `Settlement.Browse` returns `orderbook.OrderBookView`, etc.

- [ ] **Step 5: Update `cmd/server/main.go`**

Create `orderbook.Service` and `marketplace.Settlement`:
```go
obSvc := orderbook.NewService(marketCfg, marketWriter)
if err := obSvc.LoadAll(marketStore); err != nil { ... }
settlement := marketplace.NewSettlement(obSvc, bankOps, marketCfg, gameLog, notifyFn)
marketplace.RegisterHandlers(opRouter, settlement, 1)
```

- [ ] **Step 6: Split test files**

Create `pkg/orderbook/service_test.go` with pure matching tests (no bank mock).
Adapt `internal/marketplace/service_test.go` → `internal/marketplace/settlement_test.go` for settlement tests with mockBank.

- [ ] **Step 7: Build and test**

Run: `make build && go test ./pkg/orderbook/ && go test ./internal/marketplace/`
Expected: Build succeeds, all tests pass.

- [ ] **Step 8: Commit**

```bash
git add -A && git commit -m "feat: extract generic order book matching engine to pkg/orderbook/"
```

---

## Item 2: Extract Server Meshing

> **Critical note:** Tasks 7-9 from the original plan are merged into a single Task 7 because `SendTransfer` changes from `*TransferPayload` to `[]byte`, which requires the adapter, all callers, and the generic Node/Coordinator to exist simultaneously. These cannot be committed separately without build breaks.
>
> **Design deviation:** `pkg/universe/` does NOT import `pkg/component/`. All component-specific operations (ghost ticking, transfer cooldowns, replica management) are delegated to the `GameWorld` interface, which the game adapter implements. This is a cleaner boundary than the spec's original suggestion.

### Task 7: Extract full server meshing to pkg/universe/ (atomic)

Create the generic `GameWorld` interface, `NodeBridge`, `Node`, `Coordinator`, and bridge implementation in `pkg/universe/`. Simultaneously create the game-specific adapter in `internal/universe/`. Delete old implementation files. This is one atomic commit because the `SendTransfer([]byte)` signature change requires all pieces to exist at once.

**Files to create in `pkg/universe/`:**
- `world.go` — GameWorld interface
- `bridge.go` — NodeBridge interface + NoopNodeBridge
- `node.go` — generic Node struct with DrainInbox
- `coordinator.go` — generic Coordinator with GridConfig, NodeFactory
- `node_bridge_impl.go` — concrete nodeBridge routing messages between nodes

**Files to create in `internal/universe/`:**
- `factory.go` — game-specific NodeFactory
- `adapter.go` — implements `universe.GameWorld` for this game (serialization, replica scanning, ghost/cooldown ticking, chat dispatch)

**Files to delete from `internal/universe/`:**
- `coordinator.go` — replaced by `pkg/universe/coordinator.go`
- `bridge.go` — replaced by `pkg/universe/node_bridge_impl.go`
- `node.go` — replaced by `pkg/universe/node.go`
- `replica.go` — logic moves into `adapter.go`
- `message.go` — `NodeMessage` moves to `pkg/universe/message.go`

**Files to delete from `internal/game/`:**
- `nodebridge.go` — `NodeBridge` and `NoopNodeBridge` move to `pkg/universe/bridge.go`

**Files to modify:**
- `internal/game/world.go` — Bridge field type becomes `universe.NodeBridge`
- `internal/game/lifecycle.go` — bridge method calls use new interface
- `internal/system/input.go` — bridge method calls
- `internal/system/sector_boundary.go` — must serialize entity BEFORE calling `Bridge.SendTransfer([]byte)`. Current code mutates `payload.Position`/`payload.Sector` after creating TransferPayload — this must change to: modify entity components in-place → call `adapter.SerializeEntity(entity)` → call `Bridge.SendTransfer(bytes)`.
- `cmd/server/main.go` — use new Coordinator API with GameNodeFactory
- Test files: `topology_test.go`, `coordinator_test.go`, `replica_test.go`, `node_test.go`

- [ ] **Step 1: Create `pkg/universe/world.go`**

```go
package universe

import "github.com/mlange-42/ark/ecs"

type GameWorld interface {
    SerializeEntity(entity ecs.Entity) ([]byte, error)
    SpawnFromTransfer(data []byte) (ecs.Entity, error)
    ScanBorderEntities(sectorSize, aoiRadius float32) map[string][]ReplicaSnapshot
    ApplyReplicas(snapshots []ReplicaSnapshot, sourceNodeID string)
    ExpireReplicas()
    RemoveReplicaByNetID(netID uint32)
    MarkForRemoval(entity ecs.Entity)
    ECSWorld() *ecs.World
    GetAoIRadius() float32
    TickGhosts()
    TickTransferCooldowns()
    RemoveGhostByNetID(netID uint32)
    DispatchChat(username, text string)
    RegisterPendingLogin(connID uint32, username string)
    Shutdown()
}
```

- [ ] **Step 2: Create `pkg/universe/bridge.go`**

```go
package universe

import "github.com/zenion/mmoserver/pkg/coords"

type NodeBridge interface {
    PreTick()
    PostSystems()
    SectorOwner(sector coords.SectorCoord) string
    SendTransfer(destNodeID string, data []byte)
    SendArrivalConfirm(destNodeID string, confirm *ArrivalConfirmMsg)
    OnPlayerTransfer(connID uint32, destNodeID string)
    RelayChatToOtherNodes(username, text string)
    RequestSpawnOnNode(connID uint32, username string)
}

type NoopNodeBridge struct{}
// all no-op methods
```

- [ ] **Step 3: Update `pkg/universe/message.go`**

Add `ReplicaSnapshot` struct (with `Extra []byte` field) and `NodeMessage` struct to the existing file. The `ReplicaSnapshot` type is referenced by `GameWorld` interface methods and `NodeMessage.Replicas`:

```go
type ReplicaSnapshot struct {
    NetworkID  uint32
    EntityType uint8
    X, Y       float32
    VX, VY     float32
    Rotation   float32
    Radius     float32
    SectorSX   int32
    SectorSY   int32
    Extra      []byte  // game-specific data (health, shield, minable, etc.)
}

type NodeMessage struct {
    Type       MsgType
    FromNodeID string
    Transfer   []byte              // game-serialized entity data
    Confirm    *ArrivalConfirmMsg
    Replicas   []ReplicaSnapshot
    Chat       *ChatRelay
    Spawn      *SpawnTransfer
}
```

- [ ] **Step 4: Create `pkg/universe/node.go`**

Generic Node struct. `DrainInbox` dispatches via `GameWorld` interface — no direct ECS access:
- `MsgTransfer` → `n.World.RemoveReplicaByNetID(netID)` then `n.World.SpawnFromTransfer(msg.Transfer)`, then `SendArrivalConfirm` back
- `MsgArrivalConfirm` → `n.World.RemoveGhostByNetID(msg.Confirm.NetworkID)`
- `MsgReplica` → `n.World.ApplyReplicas(msg.Replicas, msg.FromNodeID)`
- `MsgChat` → `n.World.DispatchChat(msg.Chat.Username, msg.Chat.Text)`
- `MsgSpawnTransfer` → `n.World.RegisterPendingLogin(msg.Spawn.ConnID, msg.Spawn.Username)`

After dispatch: `n.World.TickGhosts()`, `n.World.TickTransferCooldowns()`.

Note: for `MsgTransfer`, the netID must be extracted from the transfer bytes. Add a small helper or have `SpawnFromTransfer` return the netID, or pass the netID in `NodeMessage` as a separate field. Simplest: add `TransferNetID uint32` to `NodeMessage` so the generic node can remove the pre-existing replica without deserializing the full payload.

- [ ] **Step 5: Create `pkg/universe/coordinator.go`**

```go
type NodeFactory func(sector coords.SectorCoord, eng *engine.Engine, log *logger.Logger) (GameWorld, *engine.GameLoop)

type GridConfig struct {
    MinSX, MaxSX int32
    MinSY, MaxSY int32
}
```

Coordinator creates Engine per node (with unique netID base), calls factory for GameWorld + GameLoop, assembles Node, computes topology, wires neighbors and bridges.

- [ ] **Step 6: Create `pkg/universe/node_bridge_impl.go`**

Concrete `nodeBridge` implementing `NodeBridge`. Routes messages to neighbor node Inbox channels.

- [ ] **Step 7: Create `internal/universe/adapter.go`**

Implements `universe.GameWorld` for this game:
- `SerializeEntity` — builds `TransferPayload` from ECS components, JSON marshals to `[]byte`
- `SpawnFromTransfer` — JSON unmarshals `[]byte` → `TransferPayload`, calls `gw.SpawnFromTransfer(payload)`
- `ScanBorderEntities` — queries ECS for border entities, serializes Health/Shield/Minable into `Extra` bytes
- `ApplyReplicas` — deserializes `Extra`, creates/updates replica entities, manages replicaNetIDs map
- `RemoveReplicaByNetID` — removes from replicaNetIDs map and ECS
- `ExpireReplicas` — decrements replica TTLs, removes expired
- `TickGhosts` — decrements ghost TTLs, removes expired (using gw.C.Ghost mapper)
- `TickTransferCooldowns` — decrements cooldowns (using gw.C.TransferCooldown mapper)
- `RemoveGhostByNetID` — finds ghost by netID, marks for removal
- `DispatchChat` — `engine.Enqueue(gw.Queue, &gamepb.ChatMsg{...})`
- `RegisterPendingLogin` — sets up pending login state on gw.Players

- [ ] **Step 8: Create `internal/universe/factory.go`**

Game-specific `NodeFactory` that creates `game.GameWorld`, registers all 16 systems, returns `gameWorldAdapter` + `engine.GameLoop`.

- [ ] **Step 9: Update `internal/system/sector_boundary.go`**

Current code pattern:
```go
payload := gw.SerializeEntity(entity) // returns *TransferPayload
payload.Position = newPos              // mutates after serialization
payload.Sector = newSector
gw.Bridge.SendTransfer(destNodeID, payload)
```

New pattern — modify components in-place BEFORE serialization:
```go
pos.X = newLocalX    // modify position component directly
pos.Y = newLocalY
sector.SX = newSX    // modify sector component directly
sector.SY = newSY
bytes, _ := gw.Bridge.World.SerializeEntity(entity) // OR have adapter access
gw.Bridge.SendTransfer(destNodeID, bytes)
```

Actually, the adapter serializes the current ECS state, so modifying components first then serializing is the correct approach. But the entity gets marked as Ghost right after, so the sequence is: update position/sector components → serialize → mark as Ghost → send transfer.

- [ ] **Step 10: Delete old `internal/universe/` files**

Delete: `coordinator.go`, `bridge.go`, `node.go`, `replica.go`, `message.go`.
Delete: `internal/game/nodebridge.go`.

- [ ] **Step 11: Update `internal/game/world.go`**

Change Bridge field type from `game.NodeBridge` to `universe.NodeBridge`. Add `pkguniverse "github.com/zenion/mmoserver/pkg/universe"` import.

- [ ] **Step 12: Update `cmd/server/main.go`**

```go
factory := internaluniverse.GameNodeFactory(gameCfg, connMgr, playerDB)
grid := universe.GridConfig{MinSX: -1, MaxSX: 1, MinSY: -1, MaxSY: 1}
coordinator := universe.NewCoordinator(grid, platformCfg, connMgr, gameLog, factory)
```

- [ ] **Step 13: Update test files**

Update `topology_test.go`, `coordinator_test.go`, `replica_test.go`, `node_test.go` to use new API. Tests that create nodes now use the factory pattern.

- [ ] **Step 14: Build and test**

Run: `make build && go test ./...`
Expected: Build succeeds, all tests pass.

- [ ] **Step 15: Commit**

```bash
git add -A && git commit -m "feat: extract generic server meshing to pkg/universe/"
```

---

## Item 3: Proto Split

### Task 8: Split proto/game.proto into engine.proto + game.proto

Create the two-file proto structure with proper buf configuration.

**Files:**
- Create: `proto/engine/engine.proto` — generic engine protocol
- Modify: `proto/game.proto` → `proto/game/game.proto` — game-specific, imports engine
- Modify: `buf.yaml` — multi-module config
- Modify: `buf.gen.yaml` — multi-input config

- [ ] **Step 1: Create directory structure**

```bash
mkdir -p proto/engine proto/game
```

- [ ] **Step 2: Create `proto/engine/engine.proto`**

Package `enginepb`. Contains envelope types, base event codes (1-99, with `_UNKNOWN = 0` sentinels), core messages (PlayerInputMsg base fields, PingMsg, PongMsg, LoginMsg, etc.), WorldUpdateMsg, EntityState (base fields + `extra_data bytes`), PlayerOwnStateMsg, SectorChangeMsg.

- [ ] **Step 3: Create `proto/game/game.proto`**

Move `proto/game.proto` → `proto/game/game.proto`. Import engine.proto. Game event codes start at 100+. Move game-specific enums (EntityType, ResourceType, EquipSlot, StatusEffectType), all game messages, GameEntityState with oneof type_data, marketplace operations.

- [ ] **Step 4: Delete old `proto/game.proto`**

- [ ] **Step 5: Update `buf.yaml`**

```yaml
version: v2
modules:
  - path: proto/engine
  - path: proto/game
lint:
  use:
    - STANDARD
breaking:
  use:
    - FILE
```

- [ ] **Step 6: Update `buf.gen.yaml`**

```yaml
version: v2
inputs:
  - directory: proto/engine
  - directory: proto/game
plugins:
  - remote: buf.build/protocolbuffers/go
    out: gen/go
    opt: paths=source_relative
  - remote: buf.build/protocolbuffers/csharp
    out: gen/csharp
  - remote: buf.build/bufbuild/es
    out: gen/es
```

- [ ] **Step 7: Run `make proto`**

Expected: Generates `gen/go/engine/`, `gen/go/game/`, `gen/es/engine_pb.js`, `gen/es/game_pb.js`, `gen/csharp/Engine.cs`, `gen/csharp/Game.cs`.

- [ ] **Step 8: Commit proto files**

```bash
git add -A && git commit -m "feat: split proto into engine.proto + game.proto"
```

---

### Task 9: Update Go server for new proto packages

Update all Go imports from `gamepb` to split between `enginepb` and `gamepb`.

**Files:**
- Modify: All Go files importing `gen/go` (~20+ files)
- The import path changes from `gamepb "github.com/zenion/mmoserver/gen/go"` to:
  - `enginepb "github.com/zenion/mmoserver/gen/go/engine"`
  - `gamepb "github.com/zenion/mmoserver/gen/go/game"` (for game-specific types)

- [ ] **Step 1: Update imports in all Go files**

For each file currently importing `gen/go`:
- If it uses only engine types (ClientEvent, ServerEvent, PingMsg, etc.) → use `enginepb`
- If it uses only game types → use `gamepb` (new path)
- If it uses both → import both

Key files: `cmd/server/main.go`, `internal/netutil/event.go`, `internal/system/network.go`, `internal/system/input.go`, `internal/marketplace/handler.go`, `internal/marketplace/service.go`, `internal/bot/bot.go`, `internal/bot/actions.go`, `internal/bot/world.go`, `internal/component/components.go`.

- [ ] **Step 2: Build and test**

Run: `make build && go test ./...`

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "refactor: update Go imports for split proto packages"
```

---

### Task 10: Update web client for new proto packages

Update TypeScript imports in `web-pixi/src/` for the split proto.

**Files:**
- Modify: 15 files in `web-pixi/src/` that import from `@gen/game_pb.js`
- Modify: `web-pixi/vite.config.ts` — add alias for engine_pb

- [ ] **Step 1: Update Vite config**

Add `@gen/engine_pb.js` alias alongside existing `@gen/game_pb.js`.

- [ ] **Step 2: Update TypeScript imports**

For each of the 15 files: move engine types to `@gen/engine_pb.js` import, keep game types in `@gen/game_pb.js`.

- [ ] **Step 3: Build web client**

Run: `cd web-pixi && bun run build`
Expected: Clean build.

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "refactor: update web client imports for split proto packages"
```

---

## Final Verification

### Task 11: Verify clean pkg/ boundary and full integration

- [ ] **Step 1: Verify no internal/gen imports in pkg/**

```bash
grep -r '"github.com/zenion/mmoserver/internal/' pkg/ && echo "FAIL: internal imports found" || echo "PASS"
grep -r '"github.com/zenion/mmoserver/gen/' pkg/ && echo "FAIL: gen imports found" || echo "PASS"
```

- [ ] **Step 2: Full build and test**

```bash
make build && go test ./...
```

- [ ] **Step 3: Integration test**

```bash
make dev
```

Open `http://localhost:8080`, verify web client connects, player can move, cross sector boundaries, and interact.

- [ ] **Step 4: Final commit (if any fixups needed)**

```bash
git add -A && git commit -m "chore: final verification and fixups"
```
