# Coordinator API Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Redesign the mmokit Coordinator API — data-only Config, SetWorld/OnInit methods, pointer-embed WorldBase, Init() lifecycle hook on GameWorld.

**Architecture:** Split world creation from initialization. Config becomes pure data. Coordinator gains SetWorld/OnInit/SetConsole/OnConsoleReady methods. GameWorld interface gains Init() called by Build() after all nodes and bridges are wired. WorldBase switches from value to pointer embedding.

**Tech Stack:** Go, ECS (mlange-42/ark), mmokit engine

---

### Task 1: Add Init() to GameWorld interface and WorldBase

**Files:**
- Modify: `pkg/universe/world.go:10-68`
- Modify: `pkg/universe/world_base.go:163-215`

- [ ] **Step 1: Add Init() to GameWorld interface**

In `pkg/universe/world.go`, add `Init()` as the first method in the interface:

```go
type GameWorld interface {
	// Init is called by the Coordinator after all nodes are created and bridges
	// are wired, but before game loops start. Use it for entity spawning,
	// login handler setup, replicator registration, and any initialization that
	// needs the full node context (bridge, topology, coordinator).
	Init()

	// Transfer serialization
	SerializeEntity(entity ecs.Entity) ([]byte, error)
	// ... rest unchanged
```

- [ ] **Step 2: Add default no-op Init() to WorldBase**

In `pkg/universe/world_base.go`, add after the existing `Shutdown()` method:

```go
// Init is a no-op default. Override in your game world for custom initialization.
func (b *WorldBase) Init() {}
```

- [ ] **Step 3: Verify compilation**

Run: `go vet ./...`
Expected: PASS — WorldBase already satisfies GameWorld via other default methods, and Init() is now provided.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/world.go pkg/universe/world_base.go
git commit -m "feat: add Init() to GameWorld interface with WorldBase default"
```

---

### Task 2: Switch NewWorldBase to return pointer

**Files:**
- Modify: `pkg/universe/world_base.go:163` (NewWorldBase return type)
- Modify: `pkg/universe/coordinator.go:223` (createNode call site)

- [ ] **Step 1: Change NewWorldBase return type from WorldBase to *WorldBase**

In `pkg/universe/world_base.go`, change the function signature at line 163:

```go
// Before:
func NewWorldBase(eng *engine.Engine, cell CellID, aoiRadius float32, replRegistry *ReplicationRegistry) WorldBase {

// After:
func NewWorldBase(eng *engine.Engine, cell CellID, aoiRadius float32, replRegistry *ReplicationRegistry) *WorldBase {
```

And change the return statement (currently `return base`) — the local var `base` is a `WorldBase` value. Change the construction to return a pointer:

```go
// Before (at end of function):
	return base

// After:
	return &base
```

- [ ] **Step 2: Update createNode to work with *WorldBase**

In `pkg/universe/coordinator.go`, the `createNode` method at line 223:

```go
// Before:
	base := NewWorldBase(eng, cell, cfg.AoIRadius, nil)
	base.spatialGrid = spatial.NewHashGrid(spatialBucketSize)
	base.coord = c
	// ... hook setup ...
	var world GameWorld
	if cfg.WorldFactory != nil {
		world = cfg.WorldFactory(&base, c)
	} else {
		world = &base
	}

// After:
	base := NewWorldBase(eng, cell, cfg.AoIRadius, nil)
	base.spatialGrid = spatial.NewHashGrid(spatialBucketSize)
	base.coord = c
	// ... hook setup unchanged (base is now *WorldBase, field access is the same) ...
	var world GameWorld
	if cfg.WorldFactory != nil {
		world = cfg.WorldFactory(base, c)
	} else {
		world = base
	}
```

The key change: `&base` becomes `base` (already a pointer), and the fallback `&base` becomes `base`.

Also update the `eng.GetNetID` closure inside NewWorldBase — it references `base.netIDMap` which works identically via pointer.

- [ ] **Step 3: Verify compilation**

Run: `go vet ./...`
Expected: PASS — all callers still receive `*WorldBase`. The WorldFactory signature still expects `*WorldBase` so this is compatible.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/world_base.go pkg/universe/coordinator.go
git commit -m "refactor: NewWorldBase returns *WorldBase instead of value"
```

---

### Task 3: Add Coordinator setup methods (SetWorld, OnInit, SetConsole, OnConsoleReady)

**Files:**
- Modify: `pkg/universe/coordinator.go` (Config struct, new methods, new fields on Coordinator)

- [ ] **Step 1: Add fields to Coordinator struct**

In `pkg/universe/coordinator.go`, add to the Coordinator struct (after the existing unexported fields):

```go
type Coordinator struct {
	Nodes     map[string]*Node
	NodeOwner map[CellID]string
	Topology  Topology
	ConnMgr   *net.ConnManager
	Log       *logger.Logger

	defaultCell CellID
	playerNode  map[uint32]string
	cfg         Config
	built       bool
	systemDefs  []systemDef
	netIDAlloc  *NetIDAllocator
	console     *engine.Console
	partState   *partitionState

	// New fields for setup methods
	worldFactory   func(base *WorldBase) GameWorld
	onInit         func(w *WorldBase)
	consoleOpts    *ConsoleOpts
	onConsoleReady func(c *engine.Console)
}
```

- [ ] **Step 2: Add SetWorld method**

```go
// SetWorld sets the factory function that creates a GameWorld for each node.
// The factory receives a fully constructed *WorldBase and should return a game
// world struct that embeds it. Use Init() on your GameWorld for post-wiring setup.
// Mutually exclusive with OnInit. Must be called before Build().
func (c *Coordinator) SetWorld(factory func(base *WorldBase) GameWorld) {
	c.worldFactory = factory
}
```

- [ ] **Step 3: Add OnInit method**

```go
// OnInit sets an initialization function called on each node's WorldBase after
// all nodes are created and bridges are wired. Use this for simple games that
// don't need a custom world struct. Mutually exclusive with SetWorld.
// Must be called before Build().
func (c *Coordinator) OnInit(fn func(w *WorldBase)) {
	c.onInit = fn
}
```

- [ ] **Step 4: Add SetConsole method**

```go
// SetConsole configures game-specific console options (config, entity commands).
// Replaces the Console field that was previously on Config.
func (c *Coordinator) SetConsole(opts ConsoleOpts) {
	c.consoleOpts = &opts
}
```

- [ ] **Step 5: Update OnConsoleReady to be a method (rename existing if needed)**

The existing `cfg.OnConsoleReady` needs to be replaced. Add:

```go
// OnConsoleReady registers a callback invoked after the console is created and
// builtins are registered. Use it to register custom commands.
func (c *Coordinator) OnConsoleReady(fn func(c *engine.Console)) {
	c.onConsoleReady = fn
}
```

- [ ] **Step 6: Verify compilation**

Run: `go vet ./...`
Expected: PASS — new methods added, nothing removed yet.

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "feat: add SetWorld, OnInit, SetConsole, OnConsoleReady methods to Coordinator"
```

---

### Task 4: Wire new methods into Build() and createNode()

**Files:**
- Modify: `pkg/universe/coordinator.go` (Build, createNode, startConsole)

- [ ] **Step 1: Update createNode to use new worldFactory field**

In `createNode`, replace the factory call block (lines 243-248):

```go
// Before:
	var world GameWorld
	if cfg.WorldFactory != nil {
		world = cfg.WorldFactory(base, c)
	} else {
		world = base
	}

// After:
	var world GameWorld
	if c.worldFactory != nil {
		world = c.worldFactory(base)
	} else if cfg.WorldFactory != nil {
		// Legacy path: support old Config.WorldFactory during migration
		world = cfg.WorldFactory(base, c)
	} else {
		world = base
	}
```

Note: The legacy path is temporary — it will be removed in Task 7 when we remove Config.WorldFactory.

- [ ] **Step 2: Add Init() call loop to Build()**

At the end of Build(), after topology wiring (after line 206), add:

```go
	// Call Init() on each node's world now that bridges and topology are wired.
	for _, node := range c.Nodes {
		node.World.Init()
	}
```

- [ ] **Step 3: Add validation to Build()**

At the start of Build(), after the `if c.built` check (after line 148), add:

```go
	if c.worldFactory == nil && c.onInit == nil && c.cfg.WorldFactory == nil {
		panic("mmokit: coordinator requires SetWorld or OnInit before Build")
	}
```

- [ ] **Step 4: Handle OnInit path in createNode**

When `OnInit` is used instead of `SetWorld`, the world is just a bare WorldBase. The OnInit callback is called in Init(). We need a wrapper world that calls the OnInit callback:

Add a small wrapper type in coordinator.go:

```go
// onInitWorld wraps a bare WorldBase and calls the OnInit callback during Init().
type onInitWorld struct {
	*WorldBase
	initFn func(w *WorldBase)
}

func (w *onInitWorld) Init() {
	if w.initFn != nil {
		w.initFn(w.WorldBase)
	}
}
```

Then update the world creation fallback in createNode:

```go
	var world GameWorld
	if c.worldFactory != nil {
		world = c.worldFactory(base)
	} else if cfg.WorldFactory != nil {
		world = cfg.WorldFactory(base, c)
	} else if c.onInit != nil {
		world = &onInitWorld{WorldBase: base, initFn: c.onInit}
	} else {
		world = base
	}
```

- [ ] **Step 5: Update startConsole to use new fields**

In `startConsole` (around lines 414-438), update to check both old Config fields and new method fields:

```go
// Before:
	if c.cfg.Console != nil {
		co := c.cfg.Console
		// ...
	}
	// ...
	if c.cfg.OnConsoleReady != nil {
		c.cfg.OnConsoleReady(c.console)
	}

// After:
	co := c.consoleOpts
	if co == nil {
		co = c.cfg.Console  // legacy fallback
	}
	if co != nil {
		builtinOpts.Config = co.Config
		builtinOpts.ConfigSave = co.ConfigSave
		builtinOpts.ConfigReset = co.ConfigReset
		builtinOpts.Entities = co.Entities
		builtinOpts.Registry = co.Registry
	}
	// ...
	onReady := c.onConsoleReady
	if onReady == nil {
		onReady = c.cfg.OnConsoleReady  // legacy fallback
	}
	if onReady != nil {
		onReady(c.console)
	}
```

- [ ] **Step 6: Add Init() call in partition.go SplitCell path**

In `pkg/universe/partition.go`, after `c.rewireNeighbors()` (line 246) and before starting nodes (line 258), add:

```go
	// Call Init() on newly created worlds now that bridges and neighbors are wired.
	for _, child := range children {
		childNode := c.Nodes[MeshNodeID(child)]
		childNode.World.Init()
	}
```

- [ ] **Step 7: Verify compilation**

Run: `go vet ./...`
Expected: PASS — both old and new paths work.

- [ ] **Step 8: Commit**

```bash
git add pkg/universe/coordinator.go pkg/universe/partition.go
git commit -m "feat: wire SetWorld/OnInit into Build() with Init() lifecycle"
```

---

### Task 5: Update mmokit facade re-exports

**Files:**
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Add NewCoordinator wrapper or verify alias works**

The `Coordinator` type is already aliased as `type Coordinator = universe.Coordinator`, so `SetWorld`, `OnInit`, `SetConsole`, `OnConsoleReady` are automatically available. No new re-exports needed for the methods.

Verify the `Config` alias still works — it's `type Config = universe.Config`, which includes the old fields AND the new methods work via the Coordinator alias.

- [ ] **Step 2: Verify compilation**

Run: `go vet ./...`
Expected: PASS — type aliases pass through all methods.

- [ ] **Step 3: Commit (skip if no changes)**

No commit needed if no code changed.

---

### Task 6: Migrate examples/simple to OnInit

**Files:**
- Modify: `examples/simple/main.go`

- [ ] **Step 1: Rewrite main.go**

Replace the entire `main()` function and remove the `MySimpleWorld` struct:

```go
package main

import (
	"context"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

// OscillateSystem moves all entities left and right.
type OscillateSystem struct {
	mmokit.SystemBase
	filter  *ecs.Filter1[mmokit.Position]
	elapsed float32
	dir     float32
}

func (s *OscillateSystem) Init() {
	s.filter = ecs.NewFilter1[mmokit.Position](s.ECSWorld())
	s.dir = 1
}

func (s *OscillateSystem) Update(dt float32) {
	s.elapsed += dt
	if s.elapsed >= 5.0 {
		s.elapsed = 0
		s.dir = -s.dir
	}
	query := s.filter.Query()
	for query.Next() {
		pos := query.Get()
		pos.X += 100 * s.dir * dt
	}
}

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

Key changes:
- Removed `MySimpleWorld` struct entirely
- Removed `WorldFactory` from Config
- Added `coord.OnInit(...)` with spawn logic
- Removed `CellsX: 1, CellsY: 1` (defaults to 1)

- [ ] **Step 2: Verify compilation**

Run: `cd examples/simple && go vet ./...`
Expected: PASS

- [ ] **Step 3: Run the example**

Run: `cd examples/simple && go run .`
Expected: Server starts, entity oscillates. Verify with `entity list` in console.

- [ ] **Step 4: Commit**

```bash
git add examples/simple/main.go
git commit -m "refactor: examples/simple uses OnInit, removes MySimpleWorld struct"
```

---

### Task 7: Migrate examples/4node-basic to SetWorld + Init() + pointer embed

**Files:**
- Modify: `examples/4node-basic/main.go`
- Modify: `examples/4node-basic/world.go`

- [ ] **Step 1: Update World struct to pointer-embed WorldBase**

In `examples/4node-basic/world.go`, change line 18:

```go
// Before:
type World struct {
	mmokit.WorldBase

// After:
type World struct {
	*mmokit.WorldBase
```

- [ ] **Step 2: Update NewWorld signature and construction**

Change `NewWorld` to accept `*WorldBase` only (drop coord param) and use pointer embedding:

```go
// Before:
func NewWorld(base *mmokit.WorldBase, coord *mmokit.Coordinator) *World {
	w := base.ECSWorld()
	gw := &World{
		WorldBase:     *base,
		Spatial:       base.SpatialGrid(),
		ConnMap:       ecs.NewMap1[mmokit.PlayerConn](w),
		NameMap:       ecs.NewMap1[PlayerName](w),
		DebugInfoMap:  ecs.NewMap1[DebugInfo](w),
		MoveTargetMap: ecs.NewMap1[mmokit.MoveTarget](w),
	}

	// Register entity kinds
	gw.RegisterEntityKind(playerKindDef(w))

	// Login handler ...
	// State callbacks ...

	return gw
}

// After:
func NewWorld(base *mmokit.WorldBase) mmokit.GameWorld {
	w := base.ECSWorld()
	return &World{
		WorldBase:     base,
		Spatial:       base.SpatialGrid(),
		ConnMap:       ecs.NewMap1[mmokit.PlayerConn](w),
		NameMap:       ecs.NewMap1[PlayerName](w),
		DebugInfoMap:  ecs.NewMap1[DebugInfo](w),
		MoveTargetMap: ecs.NewMap1[mmokit.MoveTarget](w),
	}
}
```

- [ ] **Step 3: Add Init() method to World**

Move entity kind registration, login handler, and state callbacks from NewWorld into Init():

```go
func (gw *World) Init() {
	w := gw.ECSWorld()

	// Register entity kinds
	gw.RegisterEntityKind(playerKindDef(w))

	// Login handler
	pm := gw.Engine().Players
	pm.SetLoginHandler(func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) error {
		msgs := pm.Engine().ConnMgr.DrainInput(s.ConnID)
		for _, data := range msgs {
			var evt enginepb.ClientEvent
			if err := proto.Unmarshal(data, &evt); err != nil {
				log.Printf("login: bad envelope from conn %d: %v", s.ConnID, err)
				continue
			}
			if evt.Code == uint32(basicpb.ClientEventCode_BCE_LOGIN) {
				var login basicpb.LoginMsg
				if err := proto.Unmarshal(evt.Data, &login); err != nil {
					log.Printf("login: bad LoginMsg from conn %d: %v", s.ConnID, err)
					continue
				}
				name := strings.ToLower(strings.TrimSpace(login.Name))
				if name == "" || len(name) > 20 {
					continue
				}
				s.Username = name
				return nil
			}
		}
		return mmokit.ErrLoginPending
	})

	// State callbacks
	pm.OnState(mmokit.StateActive, mmokit.StateCallbacks{
		OnEnter: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			s.Entity = gw.spawnPlayer(s.ConnID, s.Username)
			gw.SendSpawnedMsg(s.ConnID, s.Entity)
			gw.SendCellTopology(s.ConnID)
		},
		OnExit: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			if s.Entity != (ecs.Entity{}) && gw.ECSWorld().Alive(s.Entity) {
				if gw.GhostMap().HasAll(s.Entity) {
					s.Entity = ecs.Entity{}
					return
				}
				gw.MarkForRemoval(s.Entity)
				s.Entity = ecs.Entity{}
			}
		},
	})
}
```

- [ ] **Step 4: Update main.go**

```go
// Before:
	cfg := mmokit.Config{
		// ...
		WorldFactory: func(base *mmokit.WorldBase, coord *mmokit.Coordinator) mmokit.GameWorld {
			return NewWorld(base, coord)
		},
	}
	// ...
	coord := mmokit.NewCoordinator(cfg)

// After:
	coord := mmokit.NewCoordinator(mmokit.Config{
		CellsX:        MeshCellsX,
		CellsY:        MeshCellsY,
		CellSize:      CellSize,
		TickRate:       TickRate,
		AoIRadius:     AoIRadius,
		DebugTopology: true,
		LogCategories: *logFlag,
	})
	if *dynamicCells {
		cfg.DynamicPartitioning = mmokit.DefaultPartitionConfig()
	}
	coord.SetWorld(NewWorld)
```

Note: The dynamic cells config needs to be set before NewCoordinator. Adjust to:

```go
	cfg := mmokit.Config{
		CellsX:        MeshCellsX,
		CellsY:        MeshCellsY,
		CellSize:      CellSize,
		TickRate:       TickRate,
		AoIRadius:     AoIRadius,
		DebugTopology: true,
		LogCategories: *logFlag,
	}
	if *dynamicCells {
		cfg.DynamicPartitioning = mmokit.DefaultPartitionConfig()
		log.Println("dynamic cell partitioning enabled")
	}
	coord := mmokit.NewCoordinator(cfg)
	coord.SetWorld(NewWorld)
```

- [ ] **Step 5: Verify compilation**

Run: `cd examples/4node-basic && go vet ./...`
Expected: PASS

- [ ] **Step 6: Run the example**

Run: `cd examples/4node-basic && go run .`
Expected: Server starts, players can connect, click-to-move works, cross-node transfers work.

- [ ] **Step 7: Commit**

```bash
git add examples/4node-basic/main.go examples/4node-basic/world.go
git commit -m "refactor: examples/4node-basic uses SetWorld + Init(), pointer-embed WorldBase"
```

---

### Task 8: Migrate examples/slither to SetWorld + Init() + pointer embed

**Files:**
- Modify: `examples/slither/main.go`
- Modify: `examples/slither/world.go`

- [ ] **Step 1: Update SlitherWorld struct to pointer-embed WorldBase**

In `examples/slither/world.go`, change line 25:

```go
// Before:
type SlitherWorld struct {
	mmokit.WorldBase

// After:
type SlitherWorld struct {
	*mmokit.WorldBase
```

- [ ] **Step 2: Split NewSlitherWorld into construction + Init()**

Change NewSlitherWorld to only construct the struct:

```go
func NewSlitherWorld(base *mmokit.WorldBase, cfg SlitherConfig) *SlitherWorld {
	w := base.ECSWorld()
	return &SlitherWorld{
		WorldBase:     base,
		Cfg:           cfg,
		SnakeBodyMap:  ecs.NewMap1[SnakeBody](w),
		SnakeStateMap: ecs.NewMap1[SnakeState](w),
		SnakeInputMap: ecs.NewMap1[SnakeInput](w),
		FoodMap:       ecs.NewMap1[Food](w),
		BotMap:        ecs.NewMap1[Bot](w),
		RotationMap:   ecs.NewMap1[mmokit.Rotation](w),
		snakeMappers:  newSnakeMappers(w),
		foodMappers:   newFoodMappers(w),
		Spatial:       base.SpatialGrid(),
		Queue:         mmokit.NewTickQueue(),
	}
}
```

- [ ] **Step 3: Add Init() method to SlitherWorld**

Move login handler, state callbacks, replication registry, transfer hooks, AND initial spawns into Init():

```go
func (gw *SlitherWorld) Init() {
	// Login handler
	pm := gw.Engine().Players
	pm.SetLoginHandler(func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) error {
		eng := pm.Engine()
		msgs := eng.ConnMgr.DrainInput(s.ConnID)
		for _, data := range msgs {
			var evt enginepb.ClientEvent
			if err := proto.Unmarshal(data, &evt); err != nil {
				continue
			}
			if evt.Code == uint32(slitherpb.SlitherClientEventCode_SCE_SKIN_SELECT) {
				var m slitherpb.SkinSelectMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				name := strings.ToLower(strings.TrimSpace(m.Name))
				if name == "" || len(name) > 20 {
					continue
				}
				s.Username = name
				s.Data = &SlitherSessionData{SkinID: uint8(m.SkinId)}
				eng.Log.Log(CatGameNetwork, "login: connID=%d name=%s skinID=%d", s.ConnID, name, m.SkinId)
				return nil
			}
		}
		return mmokit.ErrLoginPending
	})

	pm.OnState(mmokit.StateActive, mmokit.StateCallbacks{
		OnEnter: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			data, _ := s.Data.(*SlitherSessionData)
			skinID := uint8(0)
			if data != nil {
				skinID = data.SkinID
			}
			s.Entity = gw.SpawnPlayerSnake(s.ConnID, s.Username, skinID)
		},
		OnExit: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
			if s.Entity != (ecs.Entity{}) {
				gw.MarkForRemoval(s.Entity)
				s.Entity = ecs.Entity{}
			}
		},
	})

	// Replication registry
	reg := mmokit.NewReplicationRegistry()
	reg.Register(mmokit.ComponentReplicator{
		Scan: func(e ecs.Entity) []byte {
			if !gw.SnakeBodyMap.HasAll(e) || !gw.PositionMap().HasAll(e) {
				return nil
			}
			pos := gw.PositionMap().Get(e)
			body := gw.SnakeBodyMap.Get(e)
			return marshalSnakeBodyRelative(body, pos.X, pos.Y)
		},
		Apply: func(e ecs.Entity, data []byte) {
			if !gw.SnakeBodyMap.HasAll(e) || !gw.PositionMap().HasAll(e) {
				return
			}
			pos := gw.PositionMap().Get(e)
			unmarshalSnakeBodyRelativeInto(data, gw.SnakeBodyMap.Get(e), pos.X, pos.Y)
		},
		Add: func(e ecs.Entity, data []byte) {
			var body SnakeBody
			var ox, oy float32
			if gw.PositionMap().HasAll(e) {
				p := gw.PositionMap().Get(e)
				ox, oy = p.X, p.Y
			}
			unmarshalSnakeBodyRelativeInto(data, &body, ox, oy)
			gw.SnakeBodyMap.Add(e, &body)
		},
	})
	universe.RegisterComponent(reg, gw.SnakeStateMap)
	universe.RegisterComponent(reg, gw.BotMap)
	universe.RegisterComponent(reg, gw.FoodMap)
	gw.SetReplicationRegistry(reg)

	// Transfer hooks
	shiftBody := func(entity ecs.Entity, dx, dy float32) {
		if !gw.SnakeBodyMap.HasAll(entity) {
			return
		}
		body := gw.SnakeBodyMap.Get(entity)
		for i := 0; i < body.Length; i++ {
			idx := (body.Head - i + MaxSegments) % MaxSegments
			body.Segments[idx].X += dx
			body.Segments[idx].Y += dy
		}
	}
	gw.SetPreSerialize(shiftBody)
	gw.SetPostSerialize(shiftBody)

	gw.SetOnTransferReceived(func(entity ecs.Entity, frame *mmokit.TransferFrame) {
		if gw.SnakeStateMap.HasAll(entity) && !gw.SnakeInputMap.HasAll(entity) {
			gw.SnakeInputMap.Add(entity, &SnakeInput{})
		}
	})

	// Initial spawns — no more PendingAdminCmds hack!
	gw.SpawnInitialFood()
	cellSize := mmokit.CellSize()
	for i := 0; i < gw.Cfg.BotsPerNode; i++ {
		x := rand.Float32()*cellSize*0.6 + cellSize*0.2
		y := rand.Float32()*cellSize*0.6 + cellSize*0.2
		gw.SpawnBotSnake(x, y)
	}
}
```

- [ ] **Step 4: Update main.go**

Replace the WorldFactory in Config and the PendingAdminCmds block:

```go
// Before:
	var coord *mmokit.Coordinator
	coord = mmokit.NewCoordinator(mmokit.Config{
		// ...
		WorldFactory: func(base *mmokit.WorldBase, _ *mmokit.Coordinator) mmokit.GameWorld {
			return NewSlitherWorld(base, cfg)
		},
		OnConsoleReady: func(console *mmokit.Console) {
			// ...
		},
	})
	// ... systems ...
	coord.Build()
	// PendingAdminCmds hack for initial spawns
	for _, node := range coord.Nodes {
		n := node
		n.Engine.PendingAdminCmds <- func() {
			gw := n.World.(*SlitherWorld)
			gw.SpawnInitialFood()
			// ... bot spawns ...
		}
	}

// After:
	coord := mmokit.NewCoordinator(mmokit.Config{
		CellsX:        uint32(*gridSize),
		CellsY:        uint32(*gridSize),
		TickRate:       20,
		AoIRadius:     cfg.AoIRadius,
		Headless:       *headless,
		ConnManager:    cm,
		Logger:         logger,
		LogCategories:  *logFlag,
	})
	coord.SetWorld(func(base *mmokit.WorldBase) mmokit.GameWorld {
		return NewSlitherWorld(base, cfg)
	})
	coord.OnConsoleReady(func(console *mmokit.Console) {
		gw := coord.DefaultNode().World.(*SlitherWorld)
		registry := buildEntityRegistry(gw)
		console.RegisterBuiltins(mmokit.BuiltinOpts{
			Registry: registry,
			Entities: buildEntityOpts(gw, registry),
		})
	})
	// ... systems unchanged ...
	coord.Build()
	// PendingAdminCmds block REMOVED — spawns happen in Init()
```

- [ ] **Step 5: Verify compilation**

Run: `cd examples/slither && go vet ./...`
Expected: PASS

- [ ] **Step 6: Run the example**

Run: `cd examples/slither && go run .`
Expected: Server starts, food and bots spawn, players can connect and play.

- [ ] **Step 7: Commit**

```bash
git add examples/slither/main.go examples/slither/world.go
git commit -m "refactor: examples/slither uses SetWorld + Init(), removes PendingAdminCmds hack"
```

---

### Task 9: Migrate internal/universe adapter to pointer embed + Init()

**Files:**
- Modify: `internal/universe/adapter.go`
- Modify: `internal/universe/factory.go`

- [ ] **Step 1: Update gameWorldAdapter to pointer-embed WorldBase**

In `internal/universe/adapter.go`:

```go
// Before:
type gameWorldAdapter struct {
	mmokit.WorldBase
	gw                 *game.GameWorld
	sideEffectRegistry *mmokit.SideEffectRegistry
}

func newGameWorldAdapter(base *mmokit.WorldBase, gw *game.GameWorld, seRegistry *mmokit.SideEffectRegistry) *gameWorldAdapter {
	return &gameWorldAdapter{
		WorldBase:          *base,
		gw:                 gw,
		sideEffectRegistry: seRegistry,
	}
}

// After:
type gameWorldAdapter struct {
	*mmokit.WorldBase
	gw                 *game.GameWorld
	sideEffectRegistry *mmokit.SideEffectRegistry
}

func newGameWorldAdapter(base *mmokit.WorldBase, gw *game.GameWorld, seRegistry *mmokit.SideEffectRegistry) *gameWorldAdapter {
	return &gameWorldAdapter{
		WorldBase:          base,
		gw:                 gw,
		sideEffectRegistry: seRegistry,
	}
}
```

- [ ] **Step 2: Add Init() to gameWorldAdapter**

Move replication registry and transfer hook setup from the factory closure into Init():

```go
func (a *gameWorldAdapter) Init() {
	replRegistry := buildReplicationRegistry(a.gw)
	a.SetReplicationRegistry(replRegistry)

	a.SetOnTransferReceived(func(entity mmokit.Entity, frame *mmokit.TransferFrame) {
		a.gw.FinishTransferSpawn(entity, frame)
	})

	a.SetOnPlayerTransferReceived(func(entity mmokit.Entity, frame *mmokit.TransferFrame) {
		if s := a.Engine().Players.ByConnID(frame.ConnID); s != nil {
			a.gw.WireTransferPlayer(entity, s)
		}
		if a.gw.PlayerSessions != nil {
			a.gw.PlayerSessions.Set(frame.ConnID, frame.Username)
		}

		secFrame := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_CELL_CHANGE), &enginepb.CellChangeMsg{
			CellX: frame.CellX,
			CellY: frame.CellY,
		})
		if secFrame != nil {
			a.gw.ConnMgr.SendReliable(frame.ConnID, secFrame)
		}
		mapFrame := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
			Stations: a.gw.CollectStationMapData(),
		})
		if mapFrame != nil {
			a.gw.ConnMgr.SendReliable(frame.ConnID, mapFrame)
		}
	})
}
```

- [ ] **Step 3: Update factory.go to use SetWorld and slim down the factory closure**

```go
// Before:
func GameSetup(...) {
	coord.SetWorldFactory(func(base *mmokit.WorldBase, _ *mmokit.Coordinator) mmokit.GameWorld {
		eng := base.Engine()
		cell := base.Cell()
		id := base.NodeID()
		gw := game.NewGameWorld(eng, gameCfg, playerDB, base.SpatialGrid(), ...)
		gw.NodeID = id
		gw.PlayerSessions = playerSessions
		replRegistry := buildReplicationRegistry(gw)
		base.SetReplicationRegistry(replRegistry)
		base.SetOnTransferReceived(...)
		base.SetOnPlayerTransferReceived(...)
		seRegistry := buildSideEffectRegistry(gw)
		return newGameWorldAdapter(base, gw, seRegistry)
	})

// After:
func GameSetup(...) {
	coord.SetWorld(func(base *mmokit.WorldBase) mmokit.GameWorld {
		eng := base.Engine()
		cell := base.Cell()
		id := base.NodeID()
		gw := game.NewGameWorld(eng, gameCfg, playerDB, base.SpatialGrid(), mmokit.CellCoord{
			CellX: cell.X,
			CellY: cell.Y,
		})
		gw.NodeID = id
		gw.PlayerSessions = playerSessions
		seRegistry := buildSideEffectRegistry(gw)
		return newGameWorldAdapter(base, gw, seRegistry)
	})
```

The replication registry and transfer hooks are now in `gameWorldAdapter.Init()`.

- [ ] **Step 4: Verify compilation**

Run: `go vet ./...`
Expected: PASS

- [ ] **Step 5: Run `make build`**

Run: `make build`
Expected: Builds successfully to bin/server.

- [ ] **Step 6: Commit**

```bash
git add internal/universe/adapter.go internal/universe/factory.go
git commit -m "refactor: internal adapter uses pointer-embed WorldBase + Init()"
```

---

### Task 10: Remove deprecated Config fields and legacy fallbacks

**Files:**
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Remove WorldFactory, Console, OnConsoleReady from Config**

In `pkg/universe/coordinator.go`, remove lines 40-42 from the Config struct:

```go
// Remove these three fields:
	WorldFactory        func(base *WorldBase, coord *Coordinator) GameWorld
	Console           *ConsoleOpts
	OnConsoleReady    func(c *engine.Console)
```

- [ ] **Step 2: Remove SetWorldFactory method**

Delete the `SetWorldFactory` method (lines 126-130):

```go
// Delete entirely:
func (c *Coordinator) SetWorldFactory(fn func(base *WorldBase, coord *Coordinator) GameWorld) {
	c.cfg.WorldFactory = fn
}
```

- [ ] **Step 3: Remove legacy fallbacks in createNode**

In `createNode`, remove the old WorldFactory path:

```go
// Before:
	var world GameWorld
	if c.worldFactory != nil {
		world = c.worldFactory(base)
	} else if cfg.WorldFactory != nil {
		world = cfg.WorldFactory(base, c)
	} else if c.onInit != nil {
		world = &onInitWorld{WorldBase: base, initFn: c.onInit}
	} else {
		world = base
	}

// After:
	var world GameWorld
	if c.worldFactory != nil {
		world = c.worldFactory(base)
	} else if c.onInit != nil {
		world = &onInitWorld{WorldBase: base, initFn: c.onInit}
	} else {
		world = base
	}
```

- [ ] **Step 4: Remove legacy fallbacks in startConsole**

```go
// Before:
	co := c.consoleOpts
	if co == nil {
		co = c.cfg.Console
	}
	// ...
	onReady := c.onConsoleReady
	if onReady == nil {
		onReady = c.cfg.OnConsoleReady
	}

// After:
	co := c.consoleOpts
	// ...
	onReady := c.onConsoleReady
```

- [ ] **Step 5: Update Build() validation**

```go
// Before:
	if c.worldFactory == nil && c.onInit == nil && c.cfg.WorldFactory == nil {
		panic("mmokit: coordinator requires SetWorld or OnInit before Build")
	}

// After:
	if c.worldFactory == nil && c.onInit == nil {
		panic("mmokit: coordinator requires SetWorld or OnInit before Build")
	}
```

- [ ] **Step 6: Verify compilation**

Run: `go vet ./...`
Expected: PASS — all callers have been migrated in previous tasks.

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "cleanup: remove deprecated WorldFactory, Console, OnConsoleReady from Config"
```

---

### Task 11: Update CLAUDE.md and README

**Files:**
- Modify: `CLAUDE.md`
- Modify: `pkg/mmokit/README.md`

- [ ] **Step 1: Update CLAUDE.md architecture section**

Update the Server Meshing section to document the new API:

- Replace references to `WorldFactory` with `SetWorld`/`OnInit`
- Update the code example showing coordinator setup
- Document the Init() lifecycle
- Update any example snippets

- [ ] **Step 2: Update pkg/mmokit/README.md**

Update quick start and API examples to use the new pattern.

- [ ] **Step 3: Verify the --dump-schema path still works**

Run: `cd examples/4node-basic && go run . --dump-schema | head -20`
Expected: JSON schema output (this exercises the protocol export path, which reads entity kinds).

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md pkg/mmokit/README.md
git commit -m "docs: update CLAUDE.md and README for new coordinator API"
```

---

### Task 12: Final verification

- [ ] **Step 1: Full compilation check**

Run: `go vet ./...`
Expected: PASS

- [ ] **Step 2: Run examples/simple**

Run: `cd examples/simple && go run .`
Expected: Starts, entity oscillates.

- [ ] **Step 3: Run examples/4node-basic**

Run: `cd examples/4node-basic && go run .`
Expected: 2x2 grid, players connect, click-to-move, cross-node transfers.

- [ ] **Step 4: Run examples/slither**

Run: `cd examples/slither && go run .`
Expected: Food and bots spawn via Init(), players can connect and play.

- [ ] **Step 5: Run make build**

Run: `make build`
Expected: Builds to bin/server.

- [ ] **Step 6: Verify --dump-schema**

Run: `cd examples/4node-basic && go run . --dump-schema > /dev/null`
Expected: Exits 0.

- [ ] **Step 7: Run examples/4node-basic with --dynamic-cells**

Run: `cd examples/4node-basic && go run . --dynamic-cells`
Expected: Starts with dynamic partitioning enabled. Test `cell split 0,0` in console.
