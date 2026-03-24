# Engine Extraction Phase 2: Order Book, Universe Meshing, Proto Split

## Problem

After Phase 1 of engine extraction, `pkg/` is clean (zero `internal/` or `gen/` imports). However:

1. Backward-compat shims (type aliases, re-exports) add indirection — this is a greenfield project, not needed.
2. `internal/marketplace/` contains a generic order book matching engine buried under game-specific bank/currency logic.
3. `internal/universe/` contains the server meshing infrastructure (coordinator, node, replica) tightly coupled to game types — should be the centerpiece of the open-source engine.
4. `proto/game.proto` mixes generic protocol messages with game-specific mechanics in one file.

## Scope

Four work items, executed in order:

- **Item 0:** Remove backward-compat shims, update all callers to import directly
- **Item 1:** Extract generic order book → `pkg/orderbook/`
- **Item 2:** Extract generic server meshing → `pkg/universe/`
- **Item 3:** Split `proto/game.proto` into `engine.proto` + `game.proto`

---

## Item 0: Remove Backward-Compat Shims

### Files to delete

- `internal/game/tickqueue.go` — re-exports `engine.TickQueue`, `engine.Enqueue`, etc.
- `internal/game/registry.go` — re-exports `engine.EntityDef`, `engine.EntityRegistry`
- `internal/universe/topology.go` — re-exports `pkg/universe.ComputeTopology`, `SectorID`

### Files to simplify

- `internal/component/components.go` — remove all 16 type aliases (`Position = pkgcomp.Position`, etc.). Game-specific types remain defined here. Remove `pkg/component` import.
- `internal/universe/message.go` — remove re-exported constants/type aliases. Keep only `NodeMessage` struct with direct `pkg/universe` type references.
- `internal/game/transfer_types.go` — remove `ArrivalConfirmMsg`, `ChatRelay`, `RespawnTransfer` aliases. Callers use `pkg/universe` types directly.

### Caller updates by scope

**Component aliases (~35 files):** Every system file, entity file, and the `Components` struct in `internal/game/components.go` need to import `pkg/component` for generic types. Files that use both generic and game-specific components use aliased imports:
```go
import (
    comp "github.com/zenion/mmoserver/pkg/component"
    gamecomp "github.com/zenion/mmoserver/internal/component"
)
```

**TickQueue (~8 files):** Systems using `game.Enqueue`/`game.Drain` update to `engine.Enqueue`/`engine.Drain` with import `pkg/engine`.

**EntityRegistry (~2 files):** `internal/game/game.go` calls `NewEntityRegistry()` — update to `engine.NewEntityRegistry()`. The `EntityDef` and `EntityRegistry` types are used only within `internal/game/` entity files via the alias, which goes away.

**Topology (~2 files):** `internal/universe/coordinator.go` and test files update to import `pkg/universe` directly for `ComputeTopology` and `SectorID`.

**Transfer types (~5 files):** Files referencing `game.ArrivalConfirmMsg`, `game.ChatRelay`, `game.RespawnTransfer` update to `universe.ArrivalConfirmMsg`, `universe.ChatRelay`, `universe.SpawnTransfer` (renamed).

### GameWorld.Queue type change

`GameWorld.Queue` field type stays `*engine.TickQueue` (already the same type via alias). After deleting the shim, `game.go` imports `pkg/engine` directly.

### Note on Item 2 interaction

`internal/universe/` files will be heavily modified again in Item 2 (full extraction). Import updates here are not wasted — Item 2 restructures the files, but correct imports are needed for build verification between items.

---

## Item 1: Extract Order Book → `pkg/orderbook/`

### New package: `pkg/orderbook/`

Files moved as-is (only package name changes):
- `book.go` — `OrderBook` struct, sorted insert/remove/aggregate
- `types.go` — `Order`, `Trade`, `PlaceResult`, `OrderBookView`, `PriceLevel`, `OrderSide`
- `config.go` — `Config` struct (TaxPct, OrderExpiry, MinPrice, MaxOrders)
- `persist.go` — JSON persistence via `pkg/persist.AsyncWriter`
- `book_test.go` — pure matching tests

Note: `StationID` fields in `Order` and API become `LocationID` — more generic than game-specific "station" vocabulary.

### New file: `pkg/orderbook/service.go`

Generic matching engine extracted from current `internal/marketplace/service.go`:

```go
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

// PlaceSellOrder matches against existing buys, returns fills and resting order ID (0 if fully filled).
func (s *Service) PlaceSellOrder(sellerID string, locationID, itemID uint32, price int64, qty int32) ([]MatchEvent, uint64, error)

// PlaceBuyOrder matches against existing sells.
func (s *Service) PlaceBuyOrder(buyerID string, locationID, itemID uint32, price int64, qty int32) ([]MatchEvent, uint64, error)

// InstantSell fills against existing buys at market price.
func (s *Service) InstantSell(sellerID string, locationID, itemID uint32, qty int32) ([]MatchEvent, error)

// InstantBuy fills against existing sells at market price.
func (s *Service) InstantBuy(buyerID string, locationID, itemID uint32, qty int32) ([]MatchEvent, error)

// CancelOrder removes an order and returns it for caller to handle refund.
func (s *Service) CancelOrder(playerID string, orderID uint64) (*Order, error)

// ExpireOrders removes expired orders and returns them for caller to handle refunds.
func (s *Service) ExpireOrders() []*Order

// Browse returns aggregated order book view.
func (s *Service) Browse(locationID, itemID uint32) OrderBookView

// PlayerOrders returns all orders for a player.
func (s *Service) PlayerOrders(playerID string) []*Order
```

Key design: matching functions return `[]MatchEvent` — the caller (game layer) decides what to do with fills (transfer items, apply tax, send notifications).

### Splitting PlaceSellOrder — detailed walkthrough

Current `PlaceSellOrder` (service.go lines 135-256) does these steps interleaved:

1. **Validate** (lines 135-155): check `fluxItemID` rejection, price/qty bounds, max orders → **moves to Settlement** (game-specific: Flux rejection, bank balance check)
2. **Match loop** (lines 157-210): iterate buy orders, compute fill qty, create Trade → **moves to pkg/orderbook/** (pure matching)
3. **Per-fill bank ops** (within match loop): `ModifyBank` buyer/seller, `ModifyFlux` with tax → **moves to Settlement** (game-specific)
4. **Resting order** (lines 212-252): insert unfilled remainder into book, escrow items → **split**: insert → `pkg/orderbook/`, escrow → Settlement
5. **Persist + notify** (lines 253-256): persist order, send notifications → **Settlement**

The split: `pkg/orderbook/Service.PlaceSellOrder` does steps 2 and the insert half of step 4, returning `[]MatchEvent` and resting order ID. `Settlement.PlaceSellOrder` does steps 1, 3, escrow half of 4, and 5.

Same pattern applies to `PlaceBuyOrder`, `InstantSell`, `InstantBuy`.

For `CancelOrder`: current code (lines 537-570) finds order, removes from book, refunds items/Flux to bank. After split: `pkg/orderbook/Service.CancelOrder` removes from book and returns the `*Order`. `Settlement.CancelOrder` processes the refund.

For `ExpireOrders`: current code (lines 572-600) iterates orders, removes expired, refunds. After split: `pkg/orderbook/Service.ExpireOrders` removes and returns `[]*Order`. Settlement refunds.

### Remaining in `internal/marketplace/`

**`settlement.go`** (new) — game-specific settlement layer:
```go
type Settlement struct {
    orderbook *orderbook.Service
    bank      BankOps
    log       *logger.Logger
    cfg       orderbook.Config
    notify    func(username string, code uint32, payload []byte)
}

func (s *Settlement) PlaceSellOrder(seller string, locationID, itemID uint32, price int64, qty int32) (*PlaceResult, error)
// 1. Validate seller has items in bank (game-specific: BankOps.GetBankBalance)
// 2. Validate not selling Flux (game-specific: fluxItemID check)
// 3. Call orderbook.PlaceSellOrder() -> get matches + resting order ID
// 4. For each match: transfer items to buyer bank, transfer Flux with tax to seller
// 5. If resting order: escrow items from seller bank
// 6. Send trade notifications, log, mark dirty

func (s *Settlement) CancelOrder(player string, orderID uint64) error
// 1. Call orderbook.CancelOrder() -> get cancelled order
// 2. Refund escrowed items/Flux to player bank

func (s *Settlement) ExpireOrders()
// 1. Call orderbook.ExpireOrders() -> get expired orders
// 2. Refund escrowed items/Flux for each
```

**`handler.go`** — unchanged (protobuf marshaling, calls Settlement instead of old Service)

### Test split

- `pkg/orderbook/service_test.go` — new tests for pure matching (no bank mock needed, simpler): place orders, verify MatchEvents returned, verify order book state
- `internal/marketplace/settlement_test.go` — existing `service_test.go` adapted: uses mockBank, verifies bank state after settlements, notification sending

---

## Item 2: Extract Server Meshing → `pkg/universe/`

### Package dependency: `pkg/universe/` imports `pkg/component/`

The generic Node needs to query ECS for Ghost and TransferCooldown components (both in `pkg/component/`). This is an explicit, intentional dependency — `pkg/universe/` depends on `pkg/component/` for core engine component types.

### New/updated files in `pkg/universe/`

**`topology.go`** — already exists, no changes.

**`message.go`** — updated with fully generic types:
```go
type MsgType uint8

const (
    MsgTransfer       MsgType = 1
    MsgArrivalConfirm MsgType = 2
    MsgReplica        MsgType = 3
    MsgChat           MsgType = 4
    MsgSpawnTransfer  MsgType = 5  // renamed from MsgRespawnTransfer
)

type ArrivalConfirmMsg struct {
    NetworkID uint32
    ConnID    uint32
}

type ChatRelay struct {
    Username string
    Text     string
}

type SpawnTransfer struct {  // renamed from RespawnTransfer
    ConnID   uint32
    Username string
}

type ReplicaSnapshot struct {
    NetworkID  uint32
    EntityType uint8
    X, Y       float32
    VX, VY     float32
    Rotation   float32
    Radius     float32
    SectorSX   int32
    SectorSY   int32
    Extra      []byte  // game-specific component data (health, shield, etc.)
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

**`world.go`** (new) — interface contract for game worlds:
```go
type GameWorld interface {
    // Serialization for cross-node transfers
    SerializeEntity(entity ecs.Entity) ([]byte, error)
    SpawnFromTransfer(data []byte) (ecs.Entity, error)

    // Replica support
    ScanBorderEntities(sectorSize, aoiRadius float32) map[string][]ReplicaSnapshot
    ApplyReplicas(snapshots []ReplicaSnapshot, sourceNodeID string)
    ExpireReplicas()

    // Entity lifecycle
    MarkForRemoval(entity ecs.Entity)
    ECSWorld() *ecs.World
    GetAoIRadius() float32

    // Ghost/transfer cooldown management (game implements with its own mappers)
    TickGhosts()
    TickTransferCooldowns()
    RemoveGhostByNetID(netID uint32)

    // Chat dispatch
    DispatchChat(username, text string)

    // Player login/spawn support
    RegisterPendingLogin(connID uint32, username string)

    // Shutdown
    Shutdown()
}
```

Ghost/transfer-cooldown tick-down and ghost removal by netID are delegated to the `GameWorld` implementation rather than having the generic Node access game-specific component mappers directly. The game adapter implements these using its own ECS mappers.

**`bridge.go`** (new) — generic NodeBridge interface:
```go
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
// ... all no-op methods
```

Note: `RelayChatToOtherNodes` and `RequestSpawnOnNode` renamed from `ChatRelay`/`SpawnTransfer` to avoid collision with the message struct names.

`internal/game/nodebridge.go` is deleted — `NoopNodeBridge` moves to `pkg/universe/`. The game's `GameWorld` struct holds `Bridge universe.NodeBridge` directly.

**`node.go`** (new) — generic node with inbox/message dispatch:
```go
type Node struct {
    ID            string
    Sector        coords.SectorCoord
    Engine        *engine.Engine
    World         GameWorld
    Loop          *engine.GameLoop
    Inbox         chan NodeMessage
    Events        chan net.PlayerEvent
    Neighbors     map[string]*Node
    ReplicaNetIDs map[uint32]ecs.Entity
    Log           *logger.Logger
}

func (n *Node) Run(ctx context.Context)
func (n *Node) Shutdown()
func (n *Node) DrainInbox()
```

`DrainInbox` dispatches messages:
- `MsgTransfer` → remove pre-existing replica for same netID from `ReplicaNetIDs`, then `n.World.SpawnFromTransfer(msg.Transfer)`
- `MsgArrivalConfirm` → `n.World.RemoveGhostByNetID(msg.Confirm.NetworkID)`
- `MsgReplica` → `n.World.ApplyReplicas(msg.Replicas, msg.FromNodeID)` (adapter manages its own replica entity tracking, not `Node.ReplicaNetIDs` — see below)
- `MsgChat` → `n.World.DispatchChat(msg.Chat.Username, msg.Chat.Text)`
- `MsgSpawnTransfer` → `n.World.RegisterPendingLogin(msg.Spawn.ConnID, msg.Spawn.Username)`

After message dispatch: `n.World.TickGhosts()`, `n.World.TickTransferCooldowns()`.

**Replica ID tracking:** `ReplicaNetIDs` moves to the game adapter (it needs to map netID → ecs.Entity, which is game-side concern). The adapter's `ApplyReplicas` manages this map internally. The Node's `DrainInbox` for `MsgTransfer` calls `n.World.RemoveReplicaByNetID(netID)` (new method on GameWorld) before spawning.

Updated `GameWorld` interface addition:
```go
RemoveReplicaByNetID(netID uint32)
```

**`coordinator.go`** (new) — generic coordinator:
```go
type NodeFactory func(sector coords.SectorCoord, eng *engine.Engine, log *logger.Logger) (GameWorld, *engine.GameLoop)

type GridConfig struct {
    MinSX, MaxSX int32  // sector X range (e.g. -1 to 1 for 3x3)
    MinSY, MaxSY int32  // sector Y range
}

type Coordinator struct {
    Nodes       map[string]*Node
    SectorOwner map[coords.SectorCoord]string
    Topology    Topology
    ConnMgr     *net.ConnManager
    Log         *logger.Logger

    mu         sync.RWMutex
    playerNode map[uint32]string
}

func NewCoordinator(grid GridConfig, platformCfg engine.Config,
    connMgr *net.ConnManager, log *logger.Logger,
    factory NodeFactory) *Coordinator

func (c *Coordinator) Start(ctx context.Context)
func (c *Coordinator) Shutdown()
func (c *Coordinator) DefaultNode() *Node
func (c *Coordinator) NodeForSector(sector coords.SectorCoord) *Node
```

`NodeFactory` returns both `GameWorld` and `*engine.GameLoop`. The Coordinator creates the `Engine` per node (for netID base assignment), calls factory to get the game world and loop, then assembles the `Node`. This keeps engine/loop creation in the coordinator while game system registration happens in the factory.

**`node_bridge_impl.go`** (new) — concrete bridge that routes messages between nodes:
```go
type nodeBridge struct {
    node  *Node
    coord *Coordinator
}
// Implements NodeBridge by sending NodeMessages to neighbor node Inbox channels
```

### What stays in `internal/universe/`

**`factory.go`** (new) — game-specific NodeFactory:
```go
func GameNodeFactory(gameCfg game.GameConfig, connMgr *net.ConnManager,
    playerDB *game.PlayerRepo) universe.NodeFactory {
    return func(sector coords.SectorCoord, eng *engine.Engine, log *logger.Logger) (universe.GameWorld, *engine.GameLoop) {
        grid := spatial.NewGrid(gameCfg.GridCellSize)
        gw := game.NewGameWorld(eng, gameCfg, playerDB, grid, ...)
        // register all 16 game systems
        loop := engine.NewGameLoop(eng, systems, sysNames, gw.Hooks())
        adapter := &gameWorldAdapter{gw: gw}
        return adapter, loop
    }
}
```

**`adapter.go`** (new) — implements `universe.GameWorld` for this specific game:
```go
type gameWorldAdapter struct {
    gw          *game.GameWorld
    replicaNetIDs map[uint32]ecs.Entity  // managed by adapter
}

func (a *gameWorldAdapter) SerializeEntity(entity ecs.Entity) ([]byte, error) {
    // Build TransferPayload from ECS components, JSON marshal to bytes
}

func (a *gameWorldAdapter) SpawnFromTransfer(data []byte) (ecs.Entity, error) {
    // Unmarshal bytes → TransferPayload, call gw.SpawnFromTransfer(payload)
}

func (a *gameWorldAdapter) ScanBorderEntities(sectorSize, aoiRadius float32) map[string][]universe.ReplicaSnapshot {
    // Query ECS for border entities, serialize Health/Shield/Minable into Extra bytes
}

func (a *gameWorldAdapter) ApplyReplicas(snapshots []universe.ReplicaSnapshot, sourceNodeID string) {
    // Deserialize Extra bytes, create/update replica entities with game components
    // Manages a.replicaNetIDs map
}

func (a *gameWorldAdapter) RemoveReplicaByNetID(netID uint32) {
    // Remove from a.replicaNetIDs and ECS
}

func (a *gameWorldAdapter) TickGhosts() {
    // Decrement ghost TTLs, remove expired (uses gw.C.Ghost mapper)
}

func (a *gameWorldAdapter) TickTransferCooldowns() {
    // Decrement transfer cooldown timers (uses gw.C.TransferCooldown mapper)
}

func (a *gameWorldAdapter) RemoveGhostByNetID(netID uint32) {
    // Find ghost by netID, mark for removal
}

func (a *gameWorldAdapter) DispatchChat(username, text string) {
    // engine.Enqueue(gw.Queue, &gamepb.ChatMsg{...})
}

func (a *gameWorldAdapter) RegisterPendingLogin(connID uint32, username string) {
    // gw.Players.Usernames[connID] = username; gw.Players.PendingLogins[connID] = username
}
```

### Transfer serialization format

`TransferPayload` stays as a Go struct in `internal/game/transfer_types.go`. The adapter serializes it to `[]byte` using `encoding/json` (simple, debuggable). Can be upgraded to protobuf or custom binary later for performance.

### Transfer flow (end-to-end)

1. `SectorBoundarySystem` detects entity crossing boundary
2. Game code calls `adapter.SerializeEntity(entity)` → gets `[]byte`
3. Game code calls `gw.Bridge.SendTransfer(destNodeID, bytes)` (bridge is `universe.NodeBridge`)
4. Bridge implementation puts `NodeMessage{Type: MsgTransfer, Transfer: bytes}` on dest node's inbox
5. Dest node's `DrainInbox` calls `n.World.RemoveReplicaByNetID(netID)` then `n.World.SpawnFromTransfer(bytes)`
6. Adapter deserializes bytes → `TransferPayload`, spawns entity with all game components
7. Adapter calls back to bridge: `SendArrivalConfirm(sourceNodeID, confirm)`

---

## Item 3: Proto Split

### Wire-breaking change notice

This is a wire-breaking change — all clients (Unity, web) must be rebuilt after the split. This is acceptable for a greenfield project with no deployed clients.

### Proto3 zero-value handling

All enums include an explicit `_UNKNOWN = 0` sentinel value to satisfy proto3's zero-value requirement:
```proto
enum ClientEventCode {
    CE_UNKNOWN = 0;
    CE_PLAYER_INPUT = 1;
    // ...
}
```

### New file: `proto/engine/engine.proto`

Package `enginepb`. Contains:

**Envelopes:**
- `ClientEvent` { code, data }
- `ServerEvent` { code, data }
- `OperationRequest` { code, request_id, data }
- `OperationResponse` { code, request_id, return_code, error_msg, data }

**Event codes (engine subset, 1-99 reserved):**
- `ClientEventCode`: CE_UNKNOWN=0, CE_PLAYER_INPUT=1, CE_PING=2, CE_LOGIN=3, CE_SPAWN_REQUEST=4, CE_CHAT=5
- `ServerEventCode`: SE_UNKNOWN=0, SE_WORLD_UPDATE=1, SE_PLAYER_SPAWNED=2, SE_PONG=3, SE_PLAYER_DIED=4, SE_LOGIN_REJECTED=5, SE_PLAYER_OWN_STATE=6, SE_SECTOR_CHANGE=7, SE_CHAT=8

**Core messages:**
- `PlayerInputMsg` { sequence, thrust, turn, x, y } (base movement only — game-specific fields like ability_cast, lock_target_id live in game.proto's extended input)
- `PingMsg`, `PongMsg`
- `LoginMsg` { username }
- `LoginRejectedMsg` { reason }
- `SpawnRequestMsg` {} (renamed from RespawnRequestMsg)
- `ChatMsg` { username, text }
- `PlayerSpawnedMsg` { network_id, x, y, sector_x, sector_y }
- `PlayerDiedMsg` { killed_by_id }
- `SectorChangeMsg` { sector_x, sector_y }

**World state:**
- `WorldUpdateMsg` { tick, entities[], killed_ids[], removed_ids[], chats[] }
- `EntityState` { id, type, x, y, vx, vy, rotation, radius, width, height, extra_data bytes }
  - No health/shield/combat fields — those are game-specific
  - `extra_data` is an opaque bytes field for game-specific state
- `PlayerOwnStateMsg` { sequence_ack, x, y, sector_x, sector_y, extra_data bytes }

### Updated file: `proto/game/game.proto`

Package `gamepb`. Imports `engine/engine.proto`.

Contains all game-specific messages:
- Game client event codes (100+): CE_INVENTORY_TRANSFER=100, CE_BANK_REQUEST=101, CE_SELL_BANK_ITEM=102, CE_EQUIP=103, CE_DOCK=104, CE_UNDOCK=105, CE_LOOT_ITEM=106, CE_LOOT_ALL=107, CE_SHOP_BUY=108
- Game server event codes (100+): SE_BANK_CONTENTS=100, SE_TRANSFER_RESULT=101, SE_EQUIP_RESULT=102, SE_DOCKING_STATE=103, SE_DOCKED=104, SE_MAP_DATA=105, SE_DEBUG_FLAGS=106
- Game-specific enums: `EntityType`, `ResourceType`, `EquipSlot`, `StatusEffectType`
- `OperationCode` for marketplace ops
- `GamePlayerInputMsg` extending base input with ability_cast, lock_target_id, jettison_item_id, mine fields
- `GameEntityState` with health_frac, shield_frac, locked_by_id, locked_by_progress + oneof type_data { ShipState, NpcState, AsteroidState, LootCrateState, StationState }
- All mining, equipment, ability, docking, loot, banking, marketplace messages

### Buf module configuration

Single buf module at `proto/` level with both subdirectories:
```yaml
# proto/buf.yaml
version: v2
modules:
  - path: engine
  - path: game
```

`game.proto` imports engine types via:
```proto
import "engine/engine.proto";
```

### Codegen output

- `gen/go/engine/` — package `enginepb`
- `gen/go/game/` — package `gamepb` (imports enginepb)
- `gen/es/engine_pb.js` + `gen/es/game_pb.js`
- `gen/csharp/Engine.cs` + `gen/csharp/Game.cs`

### Web client impact

~15 files in `web-pixi/src/` import from `@gen/game_pb.js`. After split:
- Files using only engine types → import from `@gen/engine_pb.js`
- Files using game types → import both
- `network.ts` needs both (sends engine events, processes game-specific responses)
- **Vite config update needed:** add `@gen/engine_pb.js` alias alongside existing `@gen/game_pb.js`

---

## Execution Order

1. **Item 0** — Remove backward-compat shims (~50 file import updates)
2. **Item 1** — Extract `pkg/orderbook/` (split service.go matching from settlement)
3. **Item 2** — Extract `pkg/universe/` (GameWorld interface, generic coordinator/node)
4. **Item 3** — Split proto (engine.proto + game.proto, regen all languages)

Build and test after each item.

## Verification

After all items:
- `make build` succeeds
- `go test ./...` passes
- `grep -r 'internal/' pkg/` returns nothing
- `grep -r 'gen/' pkg/` returns nothing
- `make proto` regenerates cleanly
- `make dev` — server starts, web client connects and works
- Each `pkg/` package can be understood independently without game knowledge
