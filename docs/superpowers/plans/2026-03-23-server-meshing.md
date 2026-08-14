# Server Meshing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement in-process multi-node server meshing with 9 concurrent nodes (3x3 sector grid), entity handoff across node boundaries, and border replication for seamless cross-sector gameplay.

**Architecture:** Coordinator manages 9 Node instances, each with its own ECS world, spatial grid, and game loop goroutine. Nodes communicate via Go channels for entity transfers and border replication. ConnManager is shared; Coordinator fans out connection events to per-node channels.

**Tech Stack:** Go, Ark ECS v0.7.1 (With/Without filters), protobuf, Go channels for inter-node messaging.

**Spec:** `docs/superpowers/specs/2026-03-23-server-meshing-design.md`

**Important context for this codebase:**
- There are no tests in this project. Skip all TDD steps — implement directly and verify via `make build`.
- Ark ECS `Without()` syntax: `ecs.NewFilter2[A, B](world).Without(ecs.C[Ghost](), ecs.C[Replica]())`
- Ark ECS `Map` types use `NewMap1[T](world)`, access via `map.Get(entity)`, check via `map.HasAll(entity)`
- The game loop ticks at 20Hz (50ms). Systems run sequentially within a tick.
- `pkg/` packages must NOT import `internal/` or `gen/`. The new universe package goes in `internal/universe/`.
- Always use `bun` (not npm) for JS/TS dependency management.
- Always add debug logging via `gw.Log.Log(game.CatXxx, ...)` for significant state changes.

---

## File Structure

### New Files

| File | Responsibility |
|------|---------------|
| `internal/universe/message.go` | NodeMessage types, TransferPayload, ReplicaSnapshot, ArrivalConfirm |
| `internal/universe/topology.go` | 8-connected neighbor computation from sector coords |
| `internal/universe/node.go` | Node struct, lifecycle, inbox drain hook, replication send |
| `internal/universe/coordinator.go` | Coordinator struct, startup, event fan-out, transfer routing, chat relay |
| `internal/universe/transfer.go` | Entity serialization/deserialization for handoff, ghost management |
| `internal/universe/replica.go` | Replica creation/update/expiry, coordinate translation |
| `internal/universe/belts.go` | Deterministic asteroid belt generation from sector coords |

### Modified Files

| File | Change |
|------|--------|
| `internal/component/components.go` | Add Ghost, Replica, TransferCooldown components |
| `pkg/engine/engine.go` | Add netIDBase field, SetNetIDBase method, modify NextNetID |
| `pkg/engine/loop.go` | Accept per-node events channel, configurable processEvents |
| `internal/game/game.go` | Accept sector param in NewGameWorld, conditional station/asteroid spawn |
| `internal/game/world.go` | Add Coordinator reference field to GameWorld |
| `internal/game/entity_asteroid.go` | Replace random scatter with belt-based spawning |
| `internal/game/logcat.go` | Add CatTransfer, CatReplica, CatMap log categories |
| `internal/system/sector_boundary.go` | Add cross-node transfer detection |
| `internal/system/physics.go` | Add Without(Ghost, Replica) to filter |
| `internal/system/shipcontrol.go` | Add Without(Ghost, Replica) to filter |
| `internal/system/input.go` | Add Without(Ghost, Replica) to filter |
| `internal/system/mining.go` | Add Without(Ghost, Replica) to filter |
| `internal/system/ability.go` | Add Without(Ghost, Replica) to filter |
| `internal/system/collision.go` | Add Without(Ghost, Replica) to filter |
| `internal/system/lifetime.go` | Add Without(Ghost, Replica) to filter |
| `internal/system/docking.go` | Add Without(Ghost, Replica) to filter |
| `internal/system/targetlock.go` | Add Without(Ghost, Replica) to filter |
| `internal/system/economy.go` | Add Without(Ghost, Replica) to filter |
| `internal/system/equipment.go` | Add Without(Ghost, Replica) to filter |
| `internal/system/statuseffect.go` | Add Without(Ghost, Replica) to filter |
| `internal/system/shieldregen.go` | Add Without(Ghost, Replica) to filter |
| `internal/system/network.go` | Add Without(Ghost) to playerFilter; include replicas in AoI |
| `cmd/server/main.go` | Refactor startup to use Coordinator + 9 Nodes |

---

## Task 1: Add New ECS Components (Ghost, Replica, TransferCooldown)

**Files:**
- Modify: `internal/component/components.go`
- Modify: `internal/game/game.go` (add mappers)
- Modify: `internal/game/world.go` (add mapper fields)

- [ ] **Step 1: Add three new components to components.go**

After the `SectorCoord` struct definition, add:

```go
// Ghost marks an entity mid-transfer. Visible in AoI but not mutated by game systems.
type Ghost struct {
	TTL        int    // ticks remaining before auto-removal (starts at 10)
	DestNodeID string // which node the entity transferred to
}

// Replica is a read-only copy of an entity from a neighboring node.
// Participates in spatial grid and AoI queries but is never mutated.
type Replica struct {
	SourceNodeID string
	SourceNetID  uint32
	TTL          int // ticks remaining before expiry (reset to 30 on refresh)
}

// TransferCooldown prevents rapid re-transfers after arriving on a new node.
type TransferCooldown struct {
	Remaining int // ticks remaining (starts at 10)
}
```

- [ ] **Step 2: Add mappers to GameWorld**

In `internal/game/world.go`, add to the GameWorld struct:

```go
GhostMap           *ecs.Map1[component.Ghost]
ReplicaMap         *ecs.Map1[component.Replica]
TransferCooldownMap *ecs.Map1[component.TransferCooldown]
```

- [ ] **Step 3: Initialize mappers in NewGameWorld**

In `internal/game/game.go`, after the `SectorCoordMap` initialization, add:

```go
gw.GhostMap = ecs.NewMap1[component.Ghost](ecsWorld)
gw.ReplicaMap = ecs.NewMap1[component.Replica](ecsWorld)
gw.TransferCooldownMap = ecs.NewMap1[component.TransferCooldown](ecsWorld)
```

- [ ] **Step 4: Build and verify**

Run: `make build`
Expected: Compiles with no errors.

- [ ] **Step 5: Commit**

```bash
git add internal/component/components.go internal/game/game.go internal/game/world.go
git commit -m "feat(ecs): add Ghost, Replica, TransferCooldown components"
```

---

## Task 2: Add System Filter Exclusions for Ghost/Replica

**Files:**
- Modify: All systems in `internal/system/` that mutate entity state

Every system that mutates entities must exclude Ghost and Replica entities using Ark's `Without()` filter. Systems that only READ (Spatial, Network) keep including them.

- [ ] **Step 1: Add Without exclusions to all mutation systems**

For each system file, find the lazy filter initialization (the `if s.filter == nil` block) and chain `.Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())`.

Files and their filter locations:
- `physics.go`: `ecs.NewFilter2[component.Position, component.Velocity](s.gw.ECS)`
- `shipcontrol.go`: `ecs.NewFilter4[component.MoveTarget, component.ShipControl, component.Velocity, component.Rotation](gw.ECS)`
- `input.go`: No filter — uses `gw.ConnMgr.DrainInput()` loop over `PlayerEntities`. Add a check: `if gw.GhostMap.HasAll(entity) { continue }` before processing.
- `mining.go`: Find the filter and add Without.
- `ability.go`: Find the filter and add Without.
- `collision.go`: Find the filter and add Without.
- `lifetime.go`: Find the filter and add Without.
- `docking.go`: Find the filter and add Without.
- `targetlock.go`: Find the filter and add Without.
- `economy.go`: Find the filter and add Without.
- `equipment.go`: Find the filter and add Without.
- `statuseffect.go`: Find the filter and add Without.
- `shieldregen.go`: Find the filter and add Without.
- `sector_boundary.go`: Add Without for Ghost and Replica (and also TransferCooldown to skip entities in cooldown).

For `network.go`: Add `Without(ecs.C[component.Ghost]())` to the **playerFilter** only (ghost players shouldn't broadcast). The AoI query via spatial grid already naturally includes replicas.

All systems need `"github.com/zenion/mmokit/internal/component"` imported (most already have it).

- [ ] **Step 2: Build and verify**

Run: `make build`
Expected: Compiles with no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/system/
git commit -m "feat(ecs): exclude Ghost/Replica entities from mutation systems"
```

---

## Task 3: Engine Modifications (NetID Base, Per-Node Events)

**Files:**
- Modify: `pkg/engine/engine.go`
- Modify: `pkg/engine/loop.go`

- [ ] **Step 1: Add netID base offset to Engine**

In `pkg/engine/engine.go`, add to Engine struct:

```go
netIDBase uint32
```

Add method:

```go
// SetNetIDBase sets the base offset for NetworkID allocation.
// Each node should have a unique base to prevent ID collisions.
func (e *Engine) SetNetIDBase(base uint32) {
	e.netIDBase = base
}
```

Modify `NextNetID()`:

```go
func (e *Engine) NextNetID() uint32 {
	return e.netIDBase + e.nextNetID.Add(1)
}
```

- [ ] **Step 2: Add per-node events channel to GameLoop**

In `pkg/engine/loop.go`, add to GameLoop struct:

```go
eventsCh <-chan net.PlayerEvent // per-node events channel (nil = use ConnMgr.Events())
```

Add a new constructor variant or parameter. Simplest: add `SetEventsCh` method:

```go
// SetEventsCh sets a per-node events channel. When set, processEvents
// drains from this channel instead of ConnMgr.Events().
func (gl *GameLoop) SetEventsCh(ch <-chan net.PlayerEvent) {
	gl.eventsCh = ch
}
```

Modify `processEvents()` to drain from `eventsCh` if set:

```go
func (gl *GameLoop) processEvents() {
	var ch <-chan net.PlayerEvent
	if gl.eventsCh != nil {
		ch = gl.eventsCh
	} else {
		ch = gl.engine.ConnMgr.Events()
	}
	for {
		select {
		case evt := <-ch:
			if evt.Connected {
				gl.hooks.OnConnect(evt.ConnID)
			} else {
				gl.hooks.OnDisconnect(evt.ConnID)
			}
		default:
			return
		}
	}
}
```

- [ ] **Step 3: Build and verify**

Run: `make build`
Expected: Compiles. Existing single-node behavior unchanged (eventsCh is nil, falls back to ConnMgr.Events()).

- [ ] **Step 4: Commit**

```bash
git add pkg/engine/
git commit -m "feat(engine): add netID base offset and per-node events channel"
```

---

## Task 4: Add Log Categories and Coordinator Reference to GameWorld

**Files:**
- Modify: `internal/game/logcat.go`
- Modify: `internal/game/world.go`
- Modify: `internal/game/game.go`

- [ ] **Step 1: Add new log categories**

In `internal/game/logcat.go`, add:

```go
var CatTransfer = logger.NewCategory("transfer")
var CatReplica  = logger.NewCategory("replica")
```

Add them to `GameCategories` slice.

- [ ] **Step 2: Add universe-related fields to GameWorld**

In `internal/game/world.go`, add to GameWorld struct:

```go
// Universe (set by Coordinator for multi-node; nil for single-node)
NodeID string // this node's ID (empty for single-node)
Sector component.SectorCoord // which sector this node owns
```

- [ ] **Step 3: Add sector parameter to NewGameWorld**

In `internal/game/game.go`, modify `NewGameWorld` signature to accept sector:

```go
func NewGameWorld(eng *engine.Engine, cfg GameConfig, playerDB *PlayerRepo, grid *spatial.Grid, sector component.SectorCoord) *GameWorld {
```

Set `gw.Sector = sector` in the constructor. Conditionally spawn station only if sector is (0,0):

```go
gw.Sector = sector

// Spawn initial content for this sector
gw.spawnAsteroids()
if sector.SX == 0 && sector.SY == 0 {
	gw.SpawnStation()
}
```

- [ ] **Step 4: Update cmd/server/main.go to pass sector**

The current call `game.NewGameWorld(eng, gameCfg, playerDB, grid)` needs sector added: `game.NewGameWorld(eng, gameCfg, playerDB, grid, component.SectorCoord{0, 0})` for single-node compatibility.

- [ ] **Step 5: Build and verify**

Run: `make build`

- [ ] **Step 6: Commit**

```bash
git add internal/game/ cmd/server/main.go
git commit -m "feat(game): add sector param to NewGameWorld, log categories"
```

---

## Task 5: Topology and Message Types

**Files:**
- Create: `internal/universe/message.go`
- Create: `internal/universe/topology.go`

- [ ] **Step 1: Create message types**

Create `internal/universe/message.go` with all inter-node message types:

```go
package universe

import "github.com/zenion/mmokit/internal/component"

// MsgType identifies the kind of inter-node message.
type MsgType uint8

const (
	MsgTransfer      MsgType = 1 // entity transfer payload
	MsgArrivalConfirm MsgType = 2 // transfer confirmed by destination
	MsgReplica       MsgType = 3 // border entity replication batch
	MsgChat          MsgType = 4 // chat relay
	MsgRespawnTransfer MsgType = 5 // player respawn on another node
)

// NodeMessage is the envelope for all inter-node communication.
type NodeMessage struct {
	Type       MsgType
	FromNodeID string
	// Payload — exactly one of these is set based on Type
	Transfer      *TransferPayload
	ArrivalConfirm *ArrivalConfirmMsg
	Replicas      []ReplicaSnapshot
	Chat          *ChatRelay
	Respawn       *RespawnTransfer
}

// TransferPayload contains all component data for an entity handoff.
type TransferPayload struct {
	NetworkID  uint32
	EntityType uint8
	ConnID     uint32 // 0 for non-player entities
	Username   string // "" for non-player entities
	SourceTick uint32 // source node's tick counter for dead reckoning

	// Core components (always present)
	Position component.Position
	Sector   component.SectorCoord
	Velocity component.Velocity
	Rotation component.Rotation
	Collider component.Collider

	// Optional components (nil if not present)
	Health      *component.Health
	Shield      *component.Shield
	ShipControl *component.ShipControl
	Equipment   *component.Equipment
	MoveTarget  *component.MoveTarget
	AbilitySet  *component.AbilitySet
	Minable     *component.Minable
	Lifetime    *component.Lifetime
	// Deep-copied inventory
	CargoItems map[uint32]int32
	MaxCargo   float32
	// StatusEffects with Source cleared
	StatusEffects *component.StatusEffects
}

// ArrivalConfirmMsg confirms entity arrived on destination node.
type ArrivalConfirmMsg struct {
	NetworkID uint32
	ConnID    uint32 // non-zero for player entities
}

// ReplicaSnapshot is a lightweight entity snapshot for border replication.
type ReplicaSnapshot struct {
	NetworkID  uint32
	EntityType uint8
	Position   component.Position
	Sector     component.SectorCoord
	Velocity   component.Velocity
	Rotation   component.Rotation
	Collider   component.Collider
	// Type-specific (nil if not applicable)
	Health *component.Health
	Shield *component.Shield
	Minable *component.Minable
}

// ChatRelay relays chat messages across nodes.
type ChatRelay struct {
	Username string
	Text     string
}

// RespawnTransfer requests a player respawn on another node (e.g. at station).
type RespawnTransfer struct {
	ConnID   uint32
	Username string
}
```

- [ ] **Step 2: Create topology computation**

Create `internal/universe/topology.go`:

```go
package universe

import "github.com/zenion/mmokit/pkg/coords"

// Topology holds the neighbor relationships between sectors.
type Topology struct {
	Neighbors map[coords.SectorCoord][]coords.SectorCoord
}

// ComputeTopology builds 8-connected neighbor relationships for a set of sectors.
func ComputeTopology(sectors []coords.SectorCoord) Topology {
	sectorSet := make(map[coords.SectorCoord]bool, len(sectors))
	for _, s := range sectors {
		sectorSet[s] = true
	}

	neighbors := make(map[coords.SectorCoord][]coords.SectorCoord, len(sectors))
	for _, s := range sectors {
		var adj []coords.SectorCoord
		for dx := int32(-1); dx <= 1; dx++ {
			for dy := int32(-1); dy <= 1; dy++ {
				if dx == 0 && dy == 0 {
					continue
				}
				neighbor := coords.SectorCoord{SX: s.SX + dx, SY: s.SY + dy}
				if sectorSet[neighbor] {
					adj = append(adj, neighbor)
				}
			}
		}
		neighbors[s] = adj
	}
	return Topology{Neighbors: neighbors}
}

// SectorID returns a string ID for a sector coordinate (used as node ID).
func SectorID(s coords.SectorCoord) string {
	return fmt.Sprintf("node_%d_%d", s.SX, s.SY)
}
```

(Add `"fmt"` import.)

- [ ] **Step 3: Build and verify**

Run: `make build`

- [ ] **Step 4: Commit**

```bash
git add internal/universe/
git commit -m "feat(universe): add message types and topology computation"
```

---

## Task 6: Asteroid Belt Generation

**Files:**
- Create: `internal/universe/belts.go`
- Modify: `internal/game/entity_asteroid.go`
- Modify: `internal/game/game.go`

- [ ] **Step 1: Create belt generation**

Create `internal/universe/belts.go` with deterministic belt generation from sector coords:

```go
package universe

import (
	"hash/fnv"
	"math/rand/v2"

	"github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/pkg/coords"
)

// AsteroidBelt defines a cluster of asteroids within a sector.
type AsteroidBelt struct {
	CenterX, CenterY float32
	Radius           float32
	ResourceTypes    []uint8 // 1-2 dominant types
	Count            int
}

// GenerateBelts creates deterministic asteroid belts for a sector.
func GenerateBelts(sector component.SectorCoord) []AsteroidBelt {
	h := fnv.New64a()
	h.Write([]byte{byte(sector.SX), byte(sector.SX >> 8), byte(sector.SY), byte(sector.SY >> 8)})
	rng := rand.New(rand.NewPCG(h.Sum64(), 0))

	// Number of belts: 1-3
	numBelts := 1 + rng.IntN(3)
	isStation := sector.SX == 0 && sector.SY == 0

	// Station sector gets fewer, smaller belts
	if isStation {
		numBelts = 1 + rng.IntN(2)
	}

	margin := float32(200) // avoid sector edges
	usable := coords.SectorSize - margin*2

	belts := make([]AsteroidBelt, 0, numBelts)
	for i := 0; i < numBelts; i++ {
		cx := margin + rng.Float32()*usable
		cy := margin + rng.Float32()*usable

		// In station sector, avoid the center where the station is
		if isStation {
			stationCenter := coords.SectorSize / 2
			for {
				dx := cx - stationCenter
				dy := cy - stationCenter
				if dx*dx+dy*dy > 400 { // 20-unit exclusion radius
					break
				}
				cx = margin + rng.Float32()*usable
				cy = margin + rng.Float32()*usable
			}
		}

		// Pick 1-2 dominant resource types
		numTypes := 1 + rng.IntN(2)
		types := make([]uint8, numTypes)
		for t := range types {
			types[t] = uint8(rng.IntN(4)) // 0=ore, 1=crystal, 2=gas, 3=metal
		}

		radius := float32(30 + rng.IntN(50)) // 30-80 world units
		count := 20 + rng.IntN(40)           // 20-60 asteroids per belt

		if isStation {
			radius *= 0.6
			count = count * 2 / 3
		}

		belts = append(belts, AsteroidBelt{
			CenterX:       cx,
			CenterY:       cy,
			Radius:        radius,
			ResourceTypes: types,
			Count:         count,
		})
	}
	return belts
}
```

- [ ] **Step 2: Modify spawnAsteroids to use belts**

In `internal/game/entity_asteroid.go`, replace `spawnAsteroids()`:

```go
func (gw *GameWorld) spawnAsteroids() {
	belts := universe.GenerateBelts(component.SectorCoord{SX: gw.Sector.SX, SY: gw.Sector.SY})
	total := 0
	for _, belt := range belts {
		for i := 0; i < belt.Count; i++ {
			angle := rand.Float32() * 2 * math.Pi
			dist := rand.Float32() * belt.Radius
			x := belt.CenterX + float32(math.Cos(float64(angle)))*dist
			y := belt.CenterY + float32(math.Sin(float64(angle)))*dist
			// Clamp within sector bounds
			if x < 0 { x = 0 }
			if y < 0 { y = 0 }
			if x >= coords.SectorSize { x = coords.SectorSize - 1 }
			if y >= coords.SectorSize { y = coords.SectorSize - 1 }
			// Pick resource type — 75% dominant, 25% random
			var resType uint8
			if rand.Float32() < 0.75 {
				resType = belt.ResourceTypes[rand.IntN(len(belt.ResourceTypes))]
			} else {
				resType = uint8(rand.IntN(4))
			}
			gw.spawnAsteroidWithType(x, y, resType)
		}
		total += belt.Count
	}
	gw.Log.Log(CatSpawn, "spawned %d asteroids in %d belts for sector (%d,%d)",
		total, len(belts), gw.Sector.SX, gw.Sector.SY)
}
```

Add a new `spawnAsteroidWithType` method that takes the resource type as a parameter (extracted from the current `spawnAsteroid` which picks a random type).

- [ ] **Step 3: Remove old AsteroidCount config usage**

The belt generation determines counts internally. The old `Config.AsteroidCount` is no longer used by `spawnAsteroids`.

- [ ] **Step 4: Build and verify**

Run: `make build`

- [ ] **Step 5: Commit**

```bash
git add internal/universe/belts.go internal/game/entity_asteroid.go internal/game/game.go
git commit -m "feat(universe): asteroid belt generation per sector"
```

---

## Task 7: Node Structure and Lifecycle

**Files:**
- Create: `internal/universe/node.go`

- [ ] **Step 1: Create Node struct and lifecycle**

Create `internal/universe/node.go`:

```go
package universe

import (
	"context"

	"github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/internal/game"
	"github.com/zenion/mmokit/internal/system"
	"github.com/zenion/mmokit/pkg/coords"
	"github.com/zenion/mmokit/pkg/engine"
	"github.com/zenion/mmokit/pkg/net"
	"github.com/zenion/mmokit/pkg/spatial"
)

// Node is a self-contained game simulation owning one sector.
type Node struct {
	ID        string
	Sector    coords.SectorCoord
	Engine    *engine.Engine
	World     *game.GameWorld
	Systems   []engine.System
	SysNames  []string
	Loop      *engine.GameLoop

	Inbox     chan NodeMessage
	Events    chan net.PlayerEvent // per-node events from Coordinator
	Neighbors map[string]*Node
}

// NewNode creates a node for the given sector.
func NewNode(
	id string,
	sector coords.SectorCoord,
	cfg engine.Config,
	gameCfg game.GameConfig,
	connMgr *net.ConnManager,
	playerDB *game.PlayerRepo,
	log *logger.Logger,
) *Node {
	eng := engine.New(cfg, connMgr, log)
	grid := spatial.NewGrid(gameCfg.GridCellSize)
	gw := game.NewGameWorld(eng, gameCfg, playerDB, grid, component.SectorCoord{SX: sector.SX, SY: sector.SY})
	gw.NodeID = id

	systems := []engine.System{
		system.NewInputSystem(gw),
		system.NewDockingSystem(gw),
		system.NewTargetLockSystem(gw),
		system.NewShipControlSystem(gw),
		system.NewMiningSystem(gw),
		system.NewEconomySystem(gw),
		system.NewEquipmentSystem(gw),
		system.NewAbilitySystem(gw),
		system.NewStatusEffectSystem(gw),
		system.NewPhysicsSystem(gw),
		system.NewSectorBoundarySystem(gw),
		system.NewLifetimeSystem(gw),
		system.NewSpatialSystem(gw),
		system.NewCollisionSystem(gw),
		system.NewShieldRegenSystem(gw),
		system.NewNetworkSystem(gw),
	}
	sysNames := []string{
		"Input", "Docking", "TargetLock", "ShipControl", "Mining",
		"Economy", "Equipment", "Ability", "StatusEffect", "Physics",
		"SectorBoundary", "Lifetime", "Spatial", "Collision", "ShieldRegen", "Network",
	}

	gameLoop := engine.NewGameLoop(eng, systems, sysNames, gw.Hooks())

	events := make(chan net.PlayerEvent, 64)
	gameLoop.SetEventsCh(events)

	return &Node{
		ID:        id,
		Sector:    sector,
		Engine:    eng,
		World:     gw,
		Systems:   systems,
		SysNames:  sysNames,
		Loop:      gameLoop,
		Inbox:     make(chan NodeMessage, 256),
		Events:    events,
		Neighbors: make(map[string]*Node),
	}
}

// Run starts the node's game loop. Blocks until context is cancelled.
func (n *Node) Run(ctx context.Context) {
	n.Loop.Run(ctx)
}
```

Note: imports may need adjusting based on actual package paths. The `logger` import will come from `pkg/logger`.

- [ ] **Step 2: Build and verify**

Run: `make build`

- [ ] **Step 3: Commit**

```bash
git add internal/universe/node.go
git commit -m "feat(universe): Node struct and lifecycle"
```

---

## Task 8: Coordinator (Event Routing, Startup)

**Files:**
- Create: `internal/universe/coordinator.go`

- [ ] **Step 1: Create Coordinator**

Create `internal/universe/coordinator.go` with:
- Struct definition with Nodes map, SectorOwner map, PlayerNode map, shared resources
- `NewCoordinator()` constructor that creates 9 nodes in a 3x3 grid
- `Start(ctx)` that starts all node goroutines + event routing goroutine
- `routeEvents(ctx)` goroutine that drains ConnMgr.Events() and fans out to per-node Events channels
- `NodeForSector(sector)` lookup
- `getPlayerNode(connID)` / `setPlayerNode(connID, nodeID)` with mutex

Key details:
- 3x3 grid: sectors (-1,-1) through (1,1)
- Each node gets netID base: `nodeIndex * 10_000_000`
- Topology computed via `ComputeTopology()`
- Neighbors wired: for each node, set `node.Neighbors[neighborID] = neighborNode`
- Default node for new connections: sector (0,0)

- [ ] **Step 2: Add Shutdown method**

Coordinator.Shutdown() calls each node's `World.Shutdown()`, flushes PlayerDB.

- [ ] **Step 3: Build and verify**

Run: `make build`

- [ ] **Step 4: Commit**

```bash
git add internal/universe/coordinator.go
git commit -m "feat(universe): Coordinator with event routing and 3x3 grid"
```

---

## Task 9: Refactor cmd/server/main.go to Use Coordinator

**Files:**
- Modify: `cmd/server/main.go`

This is the integration point. Replace the single Engine+GameWorld+GameLoop with a Coordinator that creates 9 nodes.

- [ ] **Step 1: Replace single-node startup with Coordinator**

The key changes:
1. Remove single `eng`, `gw`, `systems`, `gameLoop` creation
2. Create `coordinator := universe.NewCoordinator(platformCfg, gameCfg, connMgr, playerDB, gameLog)`
3. Call `go coordinator.Start(ctx)` instead of `go gameLoop.Run(ctx)`
4. OpRouter stays shared — pass `connMgr` and shared `playerSessions`
5. Console needs adaptation — for now, target node (0,0) for commands
6. Shutdown calls `coordinator.Shutdown()`

- [ ] **Step 2: Wire OpRouter to shared PlayerSessions**

The PlayerSessions must be updated when players login/transfer. Each node's `GameWorld.PlayerSessions` should reference the shared instance.

- [ ] **Step 3: Wire Console**

For now, point the console at node (0,0)'s GameWorld. Multi-node console commands come later.

- [ ] **Step 4: Build and verify**

Run: `make build`
Run: `make dev` — verify the game starts, 9 nodes log their startup, player can connect and play in sector (0,0).

- [ ] **Step 5: Commit**

```bash
git add cmd/server/main.go
git commit -m "feat: refactor startup to use Coordinator with 9 nodes"
```

---

## Task 10: Entity Transfer (Serialization + Handoff)

**Files:**
- Create: `internal/universe/transfer.go`
- Modify: `internal/system/sector_boundary.go`
- Modify: `internal/universe/node.go` (inbox drain hook)

- [ ] **Step 1: Create transfer serialization/deserialization**

Create `internal/universe/transfer.go` with:
- `SerializeEntity(gw *game.GameWorld, entity ecs.Entity) *TransferPayload` — reads all components, deep-copies inventory, clears ecs.Entity references in TargetLock/MiningLaser/StatusEffects
- `DeserializeEntity(gw *game.GameWorld, payload *TransferPayload) ecs.Entity` — creates new entity with all components, applies dead reckoning (advance position by `(destTick - sourceTick) * 0.05 * velocity`)
- Ghost management: `AddGhost(gw, entity, destNodeID)`, `ProcessGhosts(gw)` (TTL decrement + removal)
- TransferCooldown management: `ProcessCooldowns(gw)` (decrement + removal)

- [ ] **Step 2: Modify SectorBoundarySystem for cross-node detection**

In `sector_boundary.go`, after detecting a sector change, check if the new sector belongs to a different node. If so, enqueue a transfer to the destination node's Inbox instead of normalizing.

The system needs access to the Coordinator's `SectorOwner` map. Pass this via GameWorld (add a `SectorOwnerFunc func(coords.SectorCoord) string` field, or a more direct reference).

- [ ] **Step 3: Add inbox drain to node lifecycle**

In `node.go`, add a method `DrainInbox()` that processes all pending messages from the Inbox channel. This runs at the start of each tick via a hook (`ClearTickState` or a new hook).

Process messages:
- `MsgTransfer`: Call `DeserializeEntity()` to create the entity, send `ArrivalConfirm` back
- `MsgArrivalConfirm`: Find ghost entity by NetworkID, remove it
- `MsgReplica`: Process replica snapshots (Task 11)
- `MsgChat`: Add to `PendingChat`
- `MsgRespawnTransfer`: Spawn player at station

- [ ] **Step 4: Update Coordinator with PlayerNode routing on transfer**

When a player entity transfers, update `PlayerNode[connID]` to the destination node.

- [ ] **Step 5: Build and verify**

Run: `make build`
Run: `make dev` — fly to a sector boundary, verify entity transfers to the neighboring node. Check server logs for transfer/arrival messages.

- [ ] **Step 6: Commit**

```bash
git add internal/universe/transfer.go internal/system/sector_boundary.go internal/universe/node.go
git commit -m "feat(universe): entity transfer with ghost mechanics"
```

---

## Task 11: Border Replication

**Files:**
- Create: `internal/universe/replica.go`
- Modify: `internal/universe/node.go` (add replication send + replica receive)

- [ ] **Step 1: Create replica logic**

Create `internal/universe/replica.go` with:
- `ScanBorderEntities(gw *game.GameWorld, sector coords.SectorCoord, margin float32) map[coords.SectorCoord][]ReplicaSnapshot` — scans entities near sector edges, groups by which neighbor they should be sent to
- `ApplyReplicas(gw *game.GameWorld, snapshots []ReplicaSnapshot, fromSector coords.SectorCoord)` — creates/updates replica entities with coordinate translation (using `coords.RelativeOffset`)
- `ExpireReplicas(gw *game.GameWorld)` — decrements TTL, removes expired replicas
- Dirty tracking: `lastReplicatedPos map[uint32][2]float32`

Coordinate translation for replicas: when inserting a replica from a neighbor, translate its position to the receiver's local coordinate space using sector offsets.

- [ ] **Step 2: Wire replication into node tick**

After all systems run (via a PostTick hook or at the end of the system list):
1. Call `ScanBorderEntities()` to find entities near edges
2. Send `MsgReplica` to each neighbor's Inbox
3. In `DrainInbox()`, process `MsgReplica` via `ApplyReplicas()`
4. Call `ExpireReplicas()` each tick

- [ ] **Step 3: Build and verify**

Run: `make build`
Run: `make dev` — position near a sector boundary, verify entities from the neighboring sector appear via replication. Check logs for replica creation/expiry.

- [ ] **Step 4: Commit**

```bash
git add internal/universe/replica.go internal/universe/node.go
git commit -m "feat(universe): border replication with coordinate translation"
```

---

## Task 12: Chat Relay and Respawn Routing

**Files:**
- Modify: `internal/universe/coordinator.go`
- Modify: `internal/universe/node.go`

- [ ] **Step 1: Chat relay**

Add a shared chat channel to Coordinator. Each node sends PendingChat messages to this channel after its tick. Coordinator fans out to all nodes' Inboxes. Each node processes MsgChat in DrainInbox and adds to its PendingChat for NetworkSystem broadcast.

- [ ] **Step 2: Respawn routing**

When a player dies on a non-(0,0) node and requests respawn:
1. The owning node detects respawn request
2. Sends MsgRespawnTransfer to node (0,0)'s Inbox
3. Node (0,0) spawns the player at the station
4. Coordinator updates PlayerNode routing
5. Source node cleans up DeadPlayers, PlayerEntities, ConnToUsername

- [ ] **Step 3: Build and verify**

Run: `make build`
Run: `make dev` — test chat across sectors, test death/respawn in a remote sector.

- [ ] **Step 4: Commit**

```bash
git add internal/universe/
git commit -m "feat(universe): cross-node chat relay and respawn routing"
```

---

## Task 13: Integration Testing and Polish

- [ ] **Step 1: Full gameplay test**

Run `make dev` and verify:
1. Player spawns at station in sector (0,0)
2. Asteroids appear as belts in sector (0,0)
3. Fly to adjacent sector — entity transfers seamlessly
4. Asteroids visible in the new sector (different belt layout)
5. Return to station — seamless transfer back
6. Near sector boundary — see entities from both sides via replication
7. Combat lock clears on sector transfer
8. Disconnect in remote sector, reconnect — spawn at correct location
9. Chat visible across all sectors
10. Die in remote sector — respawn at station

- [ ] **Step 2: Fix any issues found**

- [ ] **Step 3: Final commit**

```bash
git add -A
git commit -m "feat(universe): complete server meshing with 9 concurrent nodes"
```
