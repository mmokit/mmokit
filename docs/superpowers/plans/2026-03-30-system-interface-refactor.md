# System Interface & Registration Refactor

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace boilerplate system constructors and Name() methods with an Express-like API where systems embed SystemBase for dependency injection.

**Architecture:** Systems embed `engine.SystemBase` which the framework populates via `SetDeps()` before calling `Init()`. The Coordinator gains `AddSystem(name, factory)` for Express-like registration. Node creation moves from `NewCoordinator` to a `Build()` method called by `Start()`. ConnManager is created internally by default.

**Tech Stack:** Go, mlange-42/ark ECS, existing mmokit framework

---

## File Structure

### New files

None - all changes modify existing files.

### Modified files (core infrastructure)

| File | Responsibility |
| ---- | -------------- |
| `pkg/engine/system.go` | System interface (just Update), SystemBase struct, SystemDef type |
| `pkg/engine/loop.go` | NewGameLoop accepts names separately from systems |
| `pkg/universe/coordinator.go` | AddSystem, Build, WorldFactory, deferred node creation |
| `pkg/mmokit/mmokit.go` | Re-export SystemBase, SystemDef, remove deleted constructors |

### Modified files (system migrations - 37 systems)

| Area | Files | Change pattern |
| ---- | ----- | -------------- |
| pkg/system/ | `physics.go`, `lifetime.go`, `replica_dead_reckoning.go` | Embed SystemBase, add Init(), delete constructor + Name() |
| pkg/engine/ | `input_router.go` | Delete Name() only |
| pkg/universe/ | `boundary_system.go` | Embed SystemBase, add Init(), delete constructor + Name() |
| internal/system/ | 14 files (collision, docking, economy, equipment, ability, statuseffect, lifetime, mining, network, physics, shieldregen, shipcontrol, spatial, targetlock) | Embed SystemBase, add Init(), delete constructor + Name() |
| examples/4node-basic/ | `system_movement.go`, `system_spatial.go`, `system_network.go`, `system_input.go`, `main.go` | Same pattern + update registration |
| examples/slither/ | 11 system files + `main.go` | Same pattern + update registration |

### Modified files (registration sites)

| File | Change |
| ---- | ------ |
| `examples/4node-basic/main.go` | Express-like AddSystem + WithWorldFactory |
| `examples/slither/main.go` | Same |
| `internal/universe/factory.go` | Refactor to world factory + system defs |
| `cmd/server/main.go` | Use new coordinator API |

### Modified files (tests)

| File | Change |
| ---- | ------ |
| `pkg/engine/loop_test.go` | Update mock system, use new NewGameLoop sig |
| `pkg/universe/universe_test.go` | Update mock factory pattern |
| `internal/universe/testutil_test.go` | Update newTestNode helper |
| `internal/universe/coordinator_test.go` | Use Build() instead of accessing Nodes directly after NewCoordinator |
| `internal/universe/node_test.go` | Update system creation |
| `internal/universe/replica_test.go` | Update system creation |

---

## Task 1: System Interface + SystemBase

**Files:**
- Modify: `pkg/engine/system.go`

- [ ] **Step 1: Read current file**

Read `pkg/engine/system.go` (currently 7 lines).

- [ ] **Step 2: Replace with new System interface and SystemBase**

```go
package engine

import "github.com/mlange-42/ark/ecs"

// System is the interface all game systems implement.
// Embed SystemBase for automatic dependency injection via SetDeps/Init.
type System interface {
	Update(dt float32)
}

// SystemBase provides dependency injection for systems.
// Embed it in your system struct to get ECSWorld(), Engine(), and GameWorld().
// The framework calls SetDeps() then Init() before the first Update().
type SystemBase struct {
	ecsWorld  *ecs.World
	eng       *Engine
	gameWorld any
}

// ECSWorld returns the ECS world for this node.
func (b *SystemBase) ECSWorld() *ecs.World { return b.ecsWorld }

// Engine returns the engine for this node.
func (b *SystemBase) Engine() *Engine { return b.eng }

// GameWorld returns the game world for this node.
// Type-assert to your concrete world type in Init().
func (b *SystemBase) GameWorld() any { return b.gameWorld }

// Init is called once after SetDeps. Override to create filters, etc.
func (b *SystemBase) Init() {}

// SetDeps is called by the framework to inject dependencies.
func (b *SystemBase) SetDeps(w *ecs.World, eng *Engine, gw any) {
	b.ecsWorld = w
	b.eng = eng
	b.gameWorld = gw
}

// SystemDef pairs a name with a factory that creates a fresh system instance.
type SystemDef struct {
	Name    string
	Factory func() System
}
```

- [ ] **Step 3: Verify compilation**

Run: `cd . && go vet ./pkg/engine/...`

Expected: Compilation errors in loop.go (Name() no longer on interface) and any file calling s.Name(). This is expected — we fix loop.go in Task 2.

- [ ] **Step 4: Commit**

```bash
git add pkg/engine/system.go
git commit -m "refactor(engine): replace System interface with Update-only, add SystemBase"
```

---

## Task 2: Update GameLoop

**Files:**
- Modify: `pkg/engine/loop.go`
- Modify: `pkg/engine/loop_test.go`

- [ ] **Step 1: Read current files**

Read `pkg/engine/loop.go` and `pkg/engine/loop_test.go`.

- [ ] **Step 2: Update NewGameLoop signature**

Change `NewGameLoop` to accept system names as a separate parameter instead of extracting from `s.Name()`. The function should also call `SetDeps` and `Init` on systems that support it.

In `pkg/engine/loop.go`, change the `NewGameLoop` function:

```go
// NewGameLoop creates a game loop with the given systems and lifecycle hooks.
// Names are used for profiling. The framework calls SetDeps/Init on systems
// that embed SystemBase before the first tick.
func NewGameLoop(eng *Engine, systems []System, names []string, hooks Hooks) *GameLoop {
	perf := NewTickProfile(names)
	eng.Perf = perf
```

Remove the old name extraction loop (`names := make([]string, len(systems))` ... `names[i] = s.Name()`).

The rest of the function stays the same.

- [ ] **Step 3: Update loop_test.go**

Read the test file and update mock systems and NewGameLoop calls:
- Remove `Name()` from mock system (or leave it — it's now dead code, but won't break)
- Update `NewGameLoop(eng, systems, hooks)` calls to `NewGameLoop(eng, systems, names, hooks)`
- Build the `names` slice alongside the `systems` slice

- [ ] **Step 4: Verify tests pass**

Run: `cd . && go test ./pkg/engine/... -v -count=1`

Expected: All engine tests pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/engine/loop.go pkg/engine/loop_test.go
git commit -m "refactor(engine): NewGameLoop accepts names separately from systems"
```

---

## Task 3: Coordinator Express-like API

**Files:**
- Modify: `pkg/universe/coordinator.go`

This is the largest single task. The coordinator gains:
1. `AddSystem(name, factory)` method
2. `WithWorldFactory` option
3. `Build()` method that creates nodes (moved from NewCoordinator)
4. `ConnManager()` accessor (ConnManager created internally by default)

- [ ] **Step 1: Read full coordinator.go**

Read `pkg/universe/coordinator.go` completely.

- [ ] **Step 2: Add new types and fields to Coordinator**

Add to the `Coordinator` struct:

```go
type Coordinator struct {
	// ... existing fields ...

	// Express-like system registration (populated via AddSystem before Start)
	systemDefs   []engine.SystemDef
	worldFactory func(base *WorldBase) GameWorld
	built        bool // true after Build() has run

	// Stored config for deferred node creation
	grid         MeshConfig
	platformCfg  engine.Config
	opts         coordOpts
}
```

- [ ] **Step 3: Add AddSystem method**

```go
// AddSystem registers a named system factory. Systems are instantiated per node
// during Build()/Start(). Order of AddSystem calls determines execution order.
// Must be called before Start().
func (c *Coordinator) AddSystem(name string, factory func() System) {
	c.systemDefs = append(c.systemDefs, engine.SystemDef{
		Name:    name,
		Factory: func() engine.System { return factory() },
	})
}

// System is re-exported here for the AddSystem factory signature.
type System = engine.System
```

- [ ] **Step 4: Add WithWorldFactory option**

```go
// WithWorldFactory sets a per-node world creation function.
// The factory receives a pre-configured WorldBase and returns a GameWorld.
// If not set, WorldBase itself is used as the GameWorld.
func WithWorldFactory(fn func(base *WorldBase) GameWorld) CoordinatorOption {
	return func(o *coordOpts) { o.worldFactory = fn }
}
```

Add `worldFactory` field to `coordOpts`.

- [ ] **Step 5: Add ConnManager accessor**

```go
// ConnManager returns the connection manager. Created automatically
// if not provided via WithConnManager.
func (c *Coordinator) ConnManager() *net.ConnManager {
	return c.ConnMgr
}
```

- [ ] **Step 6: Refactor NewCoordinator to defer node creation**

`NewCoordinator` should:
1. Process options (create ConnManager/Logger if needed, apply CellSize)
2. Store config for later use by Build()
3. NOT create nodes (move that to Build)
4. Keep backward compat: if a `NodeFactory` is passed (old API), store it as `worldFactory` + immediate `Build()`

The old `NodeFactory` parameter should become optional. For backward compat during migration, keep the old signature but also support the new pattern where factory is nil and AddSystem is used instead.

**Approach:** Change `NewCoordinator` signature to accept an optional factory:

```go
func NewCoordinator(
	grid MeshConfig,
	platformCfg engine.Config,
	opts ...CoordinatorOption,
) *Coordinator {
```

The `NodeFactory` argument is removed from the positional params. Instead, add a `WithNodeFactory` option for backward compat:

```go
func WithNodeFactory(factory NodeFactory) CoordinatorOption {
	return func(o *coordOpts) { o.nodeFactory = factory }
}
```

Add `nodeFactory` to `coordOpts`.

When `Build()` runs, if `nodeFactory` is set (old pattern), use it. Otherwise use `worldFactory` + `systemDefs` (new pattern).

- [ ] **Step 7: Implement Build() method**

`Build()` creates all nodes. This is the code currently in `NewCoordinator` (lines 170-295), moved into a separate method.

Key changes in Build():
- If using new pattern (systemDefs + worldFactory):
  - Create WorldBase per node
  - Call worldFactory (or use WorldBase as GameWorld)
  - For each systemDef: call Factory(), then SetDeps(w, eng, gw), then Init()
  - Auto-append BoundarySystem
- If using old pattern (nodeFactory):
  - Same as current code (call factory, get systems)
- Wire topology, bridges, metrics (same as current)

```go
// Build creates all nodes, systems, and wires topology.
// Called automatically by Start() if not already called.
// Call explicitly to access Nodes before starting game loops (e.g., in tests).
func (c *Coordinator) Build() {
	if c.built {
		return
	}
	c.built = true
	// ... moved node creation code ...
}
```

In the new-pattern path, for each system instance:

```go
sys := def.Factory()
// Inject dependencies if system embeds SystemBase
type depsInjectable interface {
	SetDeps(w *ecs.World, eng *engine.Engine, gw any)
}
type initializable interface {
	Init()
}
if di, ok := sys.(depsInjectable); ok {
	di.SetDeps(eng.ECS, eng, world)
}
if init, ok := sys.(initializable); ok {
	init.Init()
}
```

- [ ] **Step 8: Update Start() to call Build()**

```go
func (c *Coordinator) Start(ctx context.Context) {
	c.Build() // no-op if already built
	// ... existing Start code (routeEvents, node.Run, console, etc.) ...
}
```

- [ ] **Step 9: Update all callers of NewCoordinator**

Every call to `NewCoordinator` currently passes a `NodeFactory` as the 3rd positional arg. Update them to use `WithNodeFactory(factory)` as an option. This is a temporary migration step — later tasks will switch to the Express pattern.

Files to update:
- `cmd/server/main.go`
- `examples/4node-basic/main.go`
- `examples/slither/main.go`
- `pkg/universe/universe_test.go`
- `internal/universe/coordinator_test.go`
- `internal/universe/testutil_test.go`

For each, change:
```go
// Old:
NewCoordinator(grid, cfg, factory, opts...)
// New:
NewCoordinator(grid, cfg, WithNodeFactory(factory), opts...)
```

- [ ] **Step 10: Update test calls that access Nodes**

Tests that access `coord.Nodes` after `NewCoordinator` need to call `coord.Build()` first (since node creation is now deferred):

```go
coord := NewCoordinator(grid, cfg, WithNodeFactory(factory), WithHeadless())
coord.Build() // explicitly create nodes for testing
// now coord.Nodes is populated
```

- [ ] **Step 11: Verify all tests pass**

Run: `go vet ./... && go test ./... -count=1`

Expected: All tests pass. The codebase now supports both old (WithNodeFactory) and new (AddSystem) patterns.

- [ ] **Step 12: Commit**

```bash
git add pkg/universe/coordinator.go cmd/server/main.go examples/4node-basic/main.go examples/slither/main.go pkg/universe/universe_test.go internal/universe/coordinator_test.go internal/universe/testutil_test.go
git commit -m "refactor(universe): Express-like AddSystem API, deferred Build, WithNodeFactory compat"
```

---

## Task 4: Update mmokit Facade

**Files:**
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Read current file**

Read `pkg/mmokit/mmokit.go`.

- [ ] **Step 2: Add new type aliases and constructors**

Add to the Engine section:

```go
type SystemBase = engine.SystemBase
type SystemDef = engine.SystemDef
```

Add to the Constructors section:

```go
WithWorldFactory = universe.WithWorldFactory
WithNodeFactory  = universe.WithNodeFactory
```

Keep existing constructor aliases — they'll be removed when systems are migrated.

- [ ] **Step 3: Verify compilation**

Run: `go vet ./pkg/mmokit/...`

- [ ] **Step 4: Commit**

```bash
git add pkg/mmokit/mmokit.go
git commit -m "refactor(mmokit): add SystemBase, SystemDef, WithWorldFactory re-exports"
```

---

## Task 5: Migrate pkg/system/ (3 systems)

**Files:**
- Modify: `pkg/system/physics.go`
- Modify: `pkg/system/lifetime.go`
- Modify: `pkg/system/replica_dead_reckoning.go`

Each system: embed SystemBase, move lazy filter init to Init(), delete constructor and Name().

- [ ] **Step 1: Migrate PhysicsSystem**

```go
package system

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
)

// PhysicsSystem integrates velocity into position each tick.
// Skips Ghost and Replica entities.
type PhysicsSystem struct {
	engine.SystemBase
	filter *ecs.Filter2[component.Position, component.Velocity]
}

func (s *PhysicsSystem) Init() {
	s.filter = ecs.NewFilter2[component.Position, component.Velocity](s.ECSWorld()).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
}

func (s *PhysicsSystem) Update(dt float32) {
	query := s.filter.Query()
	for query.Next() {
		pos, vel := query.Get()
		pos.X += vel.X * dt
		pos.Y += vel.Y * dt
	}
}
```

- [ ] **Step 2: Migrate LifetimeSystem**

```go
package system

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
)

// LifetimeSystem despawns entities whose Lifetime component has expired.
// Skips Ghost and Replica entities.
type LifetimeSystem struct {
	engine.SystemBase
	filter *ecs.Filter1[component.Lifetime]
}

func (s *LifetimeSystem) Init() {
	s.filter = ecs.NewFilter1[component.Lifetime](s.ECSWorld()).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
}

func (s *LifetimeSystem) Update(dt float32) {
	query := s.filter.Query()
	for query.Next() {
		lifetime := query.Get()
		lifetime.Remaining -= dt
		if lifetime.Remaining <= 0 {
			s.Engine().MarkForRemoval(query.Entity())
		}
	}
}
```

Note: `EntityRemover` interface and `remover` field are removed. `s.Engine().MarkForRemoval()` replaces `s.remover.MarkForRemoval()`.

- [ ] **Step 3: Migrate ReplicaDeadReckoningSystem**

```go
package system

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
)

type ReplicaDeadReckoningSystem struct {
	engine.SystemBase
	replicaFilt *ecs.Filter3[component.Position, component.Velocity, component.Replica]
	ghostFilt   *ecs.Filter3[component.Position, component.Velocity, component.Ghost]
}

func (s *ReplicaDeadReckoningSystem) Init() {
	s.replicaFilt = ecs.NewFilter3[component.Position, component.Velocity, component.Replica](s.ECSWorld())
	s.ghostFilt = ecs.NewFilter3[component.Position, component.Velocity, component.Ghost](s.ECSWorld())
}

func (s *ReplicaDeadReckoningSystem) Update(dt float32) {
	rq := s.replicaFilt.Query()
	for rq.Next() {
		pos, vel, _ := rq.Get()
		pos.X += vel.X * dt
		pos.Y += vel.Y * dt
	}
	gq := s.ghostFilt.Query()
	for gq.Next() {
		pos, vel, _ := gq.Get()
		pos.X += vel.X * dt
		pos.Y += vel.Y * dt
	}
}
```

- [ ] **Step 4: Update callers of deleted constructors**

Search for `NewPhysicsSystem`, `NewLifetimeSystem`, `NewReplicaDeadReckoningSystem` across the codebase. These are called in:
- `internal/system/physics.go` (wraps pkg PhysicsSystem)
- `internal/system/lifetime.go` (wraps pkg LifetimeSystem)
- `internal/universe/factory.go` (uses internal wrappers, not pkg directly)
- `examples/4node-basic/main.go` (uses `mmokit.NewPhysicsSystem`, `mmokit.NewReplicaDeadReckoningSystem`)
- `examples/slither/main.go` (uses `mmokit.NewPhysicsSystem`, `mmokit.NewReplicaDeadReckoningSystem`)
- `pkg/system/replication_test.go` (if any)

For `internal/system/physics.go` and `internal/system/lifetime.go` wrappers — these delegate to the pkg system. Update them to create the inner system directly:

```go
// internal/system/physics.go
type PhysicsSystem struct {
	inner *pkgsystem.PhysicsSystem
}

func NewPhysicsSystem(gw *game.GameWorld) *PhysicsSystem {
	s := &pkgsystem.PhysicsSystem{}
	s.SetDeps(gw.ECS, gw.Engine, gw) // manual SetDeps since not framework-managed
	s.Init()
	return &PhysicsSystem{inner: s}
}
```

Wait — these internal wrappers will be migrated to SystemBase in Task 8. For now, just update them to not call the deleted constructors. Temporary fix:

```go
// internal/system/physics.go — temporary, will be migrated in Task 8
func NewPhysicsSystem(gw *game.GameWorld) *PhysicsSystem {
	inner := &pkgsystem.PhysicsSystem{}
	// SetDeps + Init will be called when this wrapper is itself migrated
	return &PhysicsSystem{inner: inner, gw: gw}
}
```

Actually, this gets complex. Better approach: **keep the pkg constructors as thin wrappers during migration**, delete them when all callers are migrated. Add temporary constructors:

```go
// NewPhysicsSystem creates a physics system. Deprecated: use struct literal + SystemBase.
func NewPhysicsSystem(world *ecs.World) *PhysicsSystem {
	s := &PhysicsSystem{}
	s.SetDeps(world, nil, nil)
	s.Init()
	return s
}
```

This way existing callers keep working. Delete these in later tasks when callers are migrated.

- [ ] **Step 5: Add temporary compatibility constructors**

For each migrated pkg/system, add a deprecated constructor that calls SetDeps + Init:

`physics.go`:
```go
func NewPhysicsSystem(world *ecs.World) *PhysicsSystem {
	s := &PhysicsSystem{}
	s.SetDeps(world, nil, nil)
	s.Init()
	return s
}
```

`lifetime.go`:
```go
func NewLifetimeSystem(world *ecs.World, remover EntityRemover) *LifetimeSystem {
	s := &LifetimeSystem{}
	s.SetDeps(world, nil, nil)
	s.Init()
	return s
}
```

Note: LifetimeSystem no longer uses remover — it calls `s.Engine().MarkForRemoval()`. But Engine is nil when called from the old constructor. Need to handle this: store the remover as a fallback.

Actually, this is getting messy. **Better approach: migrate callers first, then delete constructors.** Since internal/system wrappers and examples will be migrated in Tasks 8-10, just leave the constructors for now and delete them all at the end.

**Revised step:** Keep the old constructors alongside SystemBase for now. They'll be dead code after Tasks 8-10, then we delete them.

For LifetimeSystem, keep `EntityRemover` and `remover` field alongside SystemBase. The Init() method tries Engine first, falls back to remover:

```go
type LifetimeSystem struct {
	engine.SystemBase
	Remover EntityRemover // set by old constructor or manually
	filter  *ecs.Filter1[component.Lifetime]
}

func (s *LifetimeSystem) Init() {
	s.filter = ecs.NewFilter1[component.Lifetime](s.ECSWorld()).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
}

func (s *LifetimeSystem) Update(dt float32) {
	query := s.filter.Query()
	for query.Next() {
		lifetime := query.Get()
		lifetime.Remaining -= dt
		if lifetime.Remaining <= 0 {
			if s.Engine() != nil {
				s.Engine().MarkForRemoval(query.Entity())
			} else if s.Remover != nil {
				s.Remover.MarkForRemoval(query.Entity())
			}
		}
	}
}

// Deprecated: use struct literal + SystemBase injection.
func NewLifetimeSystem(world *ecs.World, remover EntityRemover) *LifetimeSystem {
	s := &LifetimeSystem{Remover: remover}
	s.SetDeps(world, nil, nil)
	s.Init()
	return s
}
```

- [ ] **Step 6: Remove `EntityRemover` only after callers migrate**

Keep `EntityRemover` type exported for now. It will become unused after Task 8.

- [ ] **Step 7: Update mmokit.go constructor aliases**

`NewPhysicsSystem`, `NewLifetimeSystem`, `NewReplicaDeadReckoningSystem` aliases still work since the constructors still exist (deprecated).

- [ ] **Step 8: Verify**

Run: `go vet ./... && go test ./... -count=1`

- [ ] **Step 9: Commit**

```bash
git add pkg/system/physics.go pkg/system/lifetime.go pkg/system/replica_dead_reckoning.go
git commit -m "refactor(system): migrate pkg/system to SystemBase with compat constructors"
```

---

## Task 6: Migrate InputRouter + BoundarySystem

**Files:**
- Modify: `pkg/engine/input_router.go`
- Modify: `pkg/universe/boundary_system.go`

- [ ] **Step 1: InputRouter — delete Name() only**

In `pkg/engine/input_router.go`, delete:
```go
func (r *InputRouter) Name() string { return "InputRouter" }
```

Keep the constructor and everything else. InputRouter is infrastructure, not a typical system.

- [ ] **Step 2: BoundarySystem — embed SystemBase, add Init()**

Read `pkg/universe/boundary_system.go`, then update:
- Delete `Name()` method
- Delete `NewBoundarySystem` constructor
- Embed `engine.SystemBase`
- Add `Init()` that type-asserts GameWorld to BoundaryWorld
- Keep the `bw` field for the BoundaryWorld interface

```go
type BoundarySystem struct {
	engine.SystemBase
	bw        BoundaryWorld
	filter    *ecs.Filter2[component.Position, component.CellCoord]
	playerMap *ecs.Map1[component.PlayerConn]
	velMap    *ecs.Map1[component.Velocity]
}

func (s *BoundarySystem) Init() {
	s.bw = s.GameWorld().(BoundaryWorld)
	s.filter = ecs.NewFilter2[component.Position, component.CellCoord](s.ECSWorld()).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
	s.playerMap = ecs.NewMap1[component.PlayerConn](s.ECSWorld())
	s.velMap = ecs.NewMap1[component.Velocity](s.ECSWorld())
}
```

- [ ] **Step 3: Update BoundarySystem creation in coordinator.go**

In `Build()`, where BoundarySystem is auto-appended, update to use SystemBase:

```go
if bw, ok := world.(BoundaryWorld); ok {
	bs := &BoundarySystem{}
	bs.SetDeps(eng.ECS, eng, world)
	bs.Init()
	gameSystems = append(gameSystems, bs)
	systemNames = append(systemNames, "CellBoundary")
}
```

Also keep a temporary `NewBoundarySystem` constructor for the old-pattern path in Build():

```go
func NewBoundarySystem(world *ecs.World, bw BoundaryWorld) *BoundarySystem {
	s := &BoundarySystem{bw: bw}
	s.SetDeps(world, nil, nil)
	s.Init()
	return s
}
```

- [ ] **Step 4: Verify**

Run: `go vet ./... && go test ./... -count=1`

- [ ] **Step 5: Commit**

```bash
git add pkg/engine/input_router.go pkg/universe/boundary_system.go pkg/universe/coordinator.go
git commit -m "refactor: migrate InputRouter and BoundarySystem, delete Name() methods"
```

---

## Task 7: Migrate internal/system/ (14 files)

**Files:**
- Modify: all 14 files in `internal/system/`

All game-specific systems follow the same pattern: they store `gw *game.GameWorld` and use lazy filter init. Migration is mechanical:

1. Delete `Name()` method
2. Embed `engine.SystemBase` (via mmokit)
3. Add `Init()` that type-asserts `GameWorld()` to `*game.GameWorld` and creates filters
4. Delete constructor (or keep for complex ones)
5. Remove lazy `if s.filter == nil` from Update

- [ ] **Step 1: Read all internal/system files**

Read each file to understand its specific fields and constructor logic.

- [ ] **Step 2: Migrate simple systems (store gw + lazy filter)**

For each of these systems, apply the pattern:

**statuseffect.go, shieldregen.go, shipcontrol.go, targetlock.go, docking.go, economy.go, mining.go, spatial.go:**

```go
type XxxSystem struct {
	mmokit.SystemBase
	gw     *game.GameWorld
	filter *ecs.FilterN[...]  // specific to each
}

func (s *XxxSystem) Init() {
	s.gw = s.GameWorld().(*game.GameWorld)
	s.filter = ecs.NewFilterN[...](s.ECSWorld()).Without(...)
}

// Update stays the same minus the lazy init block
```

Delete the constructor and Name() from each.

- [ ] **Step 3: Migrate equipment.go (no filter, just gw)**

```go
type EquipmentSystem struct {
	mmokit.SystemBase
	gw *game.GameWorld
}

func (s *EquipmentSystem) Init() {
	s.gw = s.GameWorld().(*game.GameWorld)
}
```

- [ ] **Step 4: Migrate ability.go (pre-allocates deferred slice)**

```go
type AbilitySystem struct {
	mmokit.SystemBase
	gw       *game.GameWorld
	filter   *ecs.Filter4[...]
	deferred []abilityAction
}

func (s *AbilitySystem) Init() {
	s.gw = s.GameWorld().(*game.GameWorld)
	s.filter = ecs.NewFilter4[...](s.ECSWorld()).Without(...)
	s.deferred = make([]abilityAction, 0, 16)
}
```

- [ ] **Step 5: Migrate collision.go (pre-allocates buffer)**

```go
type CollisionSystem struct {
	mmokit.SystemBase
	gw     *game.GameWorld
	nearby []mmokit.SpatialEntry
}

func (s *CollisionSystem) Init() {
	s.gw = s.GameWorld().(*game.GameWorld)
	s.nearby = make([]mmokit.SpatialEntry, 0, 64)
}
```

- [ ] **Step 6: Migrate physics.go wrapper**

```go
type PhysicsSystem struct {
	mmokit.SystemBase
	inner *pkgsystem.PhysicsSystem
}

func (s *PhysicsSystem) Init() {
	s.inner = &pkgsystem.PhysicsSystem{}
	s.inner.SetDeps(s.ECSWorld(), s.Engine(), s.GameWorld())
	s.inner.Init()
}

func (s *PhysicsSystem) Update(dt float32) {
	s.inner.Update(dt)
}
```

- [ ] **Step 7: Migrate lifetime.go wrapper**

```go
type LifetimeSystem struct {
	mmokit.SystemBase
	inner *pkgsystem.LifetimeSystem
}

func (s *LifetimeSystem) Init() {
	s.inner = &pkgsystem.LifetimeSystem{}
	s.inner.SetDeps(s.ECSWorld(), s.Engine(), s.GameWorld())
	s.inner.Init()
}

func (s *LifetimeSystem) Update(dt float32) {
	s.inner.Update(dt)
}
```

- [ ] **Step 8: Migrate network.go (complex — keep constructor logic in Init)**

NetworkSystem creates a ReplicationSystem with complex config. Move constructor body to Init:

```go
type NetworkSystem struct {
	mmokit.SystemBase
	gw         *game.GameWorld
	replSys    *mmokit.ReplicationSystem
	ctx        *gameNetContext
	lockFilter *ecs.Filter2[gamecomp.TargetLock, comp.NetworkID]
	// ... other fields from current struct
}

func (s *NetworkSystem) Init() {
	s.gw = s.GameWorld().(*game.GameWorld)

	// All the logic currently in NewNetworkSystem goes here:
	// - Create gameNetContext
	// - Register entity replicators
	// - Create ReplicationSystem with config
	// - etc.
}
```

- [ ] **Step 9: Migrate input_handlers.go**

The `RegisterInputHandlers` function returns an `*mmokit.InputRouter`. With the new pattern, create an InputSystem wrapper:

```go
type InputSystem struct {
	mmokit.SystemBase
	router *mmokit.InputRouter
}

func (s *InputSystem) Init() {
	gw := s.GameWorld().(*game.GameWorld)
	eng := s.Engine()
	s.router = mmokit.NewInputRouter(eng, mmokit.ProtoEnvelopeParser)

	// All handler registration from RegisterInputHandlers goes here
	registerGameInputHandlers(s.router, eng, gw)
}

func (s *InputSystem) Update(dt float32) {
	s.router.ProcessInput()
}

// registerGameInputHandlers registers all game-specific input handlers.
// Extracted from the old RegisterInputHandlers function.
func registerGameInputHandlers(router *mmokit.InputRouter, eng *mmokit.Engine, gw *game.GameWorld) {
	// ... all the existing handler registrations ...
}
```

- [ ] **Step 10: Verify**

Run: `go vet ./... && go test ./... -count=1`

- [ ] **Step 11: Commit**

```bash
git add internal/system/
git commit -m "refactor(internal/system): migrate all game systems to SystemBase"
```

---

## Task 8: Migrate examples/4node-basic/

**Files:**
- Modify: `examples/4node-basic/main.go`
- Modify: `examples/4node-basic/system_movement.go`
- Modify: `examples/4node-basic/system_spatial.go`
- Modify: `examples/4node-basic/system_network.go`
- Modify: `examples/4node-basic/system_input.go`
- Modify: `examples/4node-basic/world.go`

- [ ] **Step 1: Migrate systems**

Apply the same pattern to each system file:
- Embed `mmokit.SystemBase`
- Delete `Name()` and constructor
- Add `Init()` with type assertion + filter creation

**system_movement.go:**
```go
type MovementSystem struct {
	mmokit.SystemBase
	filter *ecs.Filter3[mmokit.Position, mmokit.Velocity, PlayerInput]
}

func (s *MovementSystem) Init() {
	s.filter = ecs.NewFilter3[mmokit.Position, mmokit.Velocity, PlayerInput](s.ECSWorld()).
		Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
}
```

**system_spatial.go:**
```go
type SpatialSystem struct {
	mmokit.SystemBase
	gw     *BasicWorld
	filter *ecs.Filter3[mmokit.Position, mmokit.Collider, mmokit.NetworkID]
}

func (s *SpatialSystem) Init() {
	s.gw = s.GameWorld().(*BasicWorld)
	s.filter = ecs.NewFilter3[mmokit.Position, mmokit.Collider, mmokit.NetworkID](s.ECSWorld())
}
```

**system_network.go** — complex, move constructor logic to Init.

**system_input.go** — create InputSystem wrapper:
```go
type InputSystem struct {
	mmokit.SystemBase
	SetupFn func(r *mmokit.InputRouter, gw *BasicWorld)
	router  *mmokit.InputRouter
}

func (s *InputSystem) Init() {
	gw := s.GameWorld().(*BasicWorld)
	s.router = mmokit.NewInputRouter(s.Engine(), mmokit.ProtoEnvelopeParser)
	s.SetupFn(s.router, gw)
}

func (s *InputSystem) Update(dt float32) {
	s.router.ProcessInput()
}
```

And extract the handler registration into a standalone function:
```go
func setupInputHandlers(router *mmokit.InputRouter, gw *BasicWorld) {
	router.StateFilter(mmokit.StateActive, func(ctx *mmokit.InputContext) bool {
		return gw.ECSWorld().Alive(ctx.Entity)
	})
	mmokit.HandleProto(router, ...)
}
```

- [ ] **Step 2: Update main.go to Express-like API**

```go
func main() {
	port := flag.Int("port", 8080, "HTTP server port")
	flag.Parse()

	coord := mmokit.NewCoordinator(
		mmokit.MeshConfig{
			CellsX: MeshCellsX, CellsY: MeshCellsY,
			CellSize: CellSize,
		},
		mmokit.EngineConfig{TickRate: TickRate},
		mmokit.WithWorldFactory(func(base *mmokit.WorldBase) mmokit.GameWorld {
			return NewBasicWorld(base)
		}),
		mmokit.WithAoIRadius(AoIRadius),
	)

	coord.AddSystem("InputRouter", func() mmokit.System {
		return &InputSystem{SetupFn: setupInputHandlers}
	})
	coord.AddSystem("Movement", func() mmokit.System { return &MovementSystem{} })
	coord.AddSystem("Physics", func() mmokit.System { return &PhysicsSystem{} })
	coord.AddSystem("DeadReckoning", func() mmokit.System { return &ReplicaDeadReckoningSystem{} })
	coord.AddSystem("Spatial", func() mmokit.System { return &SpatialSystem{} })
	coord.AddSystem("Network", func() mmokit.System { return &NetworkSystem{} })

	cm := coord.ConnManager()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", cm.HandleWebSocket)
	mux.Handle("/", http.FileServer(http.Dir("web")))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("4node-basic starting on http://localhost%s", addr)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("FATAL: http server: %v", err)
			os.Exit(1)
		}
	}()

	coord.Start(context.Background())
}
```

Note: `PhysicsSystem` and `ReplicaDeadReckoningSystem` here are the ones from `pkg/system/` (via mmokit aliases), not local types. The examples can use them directly since they embed SystemBase.

- [ ] **Step 3: Verify example compiles and runs**

Run: `cd examples/4node-basic && go vet .`

- [ ] **Step 4: Commit**

```bash
git add examples/4node-basic/
git commit -m "refactor(4node-basic): migrate to Express-like AddSystem API"
```

---

## Task 9: Migrate examples/slither/

**Files:**
- Modify: `examples/slither/main.go` + all 11 system files

- [ ] **Step 1: Migrate all system files**

Same pattern as Task 8. For each system:
- Embed `mmokit.SystemBase`
- Delete `Name()` and constructor
- Add `Init()` with `s.gw = s.GameWorld().(*SlitherWorld)` + filter creation

Systems to migrate: `system_boost.go`, `system_bot.go`, `system_collision.go`, `system_death.go`, `system_decay.go`, `system_eating.go`, `system_food_spawn.go`, `system_leaderboard.go`, `system_movement.go`, `system_network.go`, `system_spatial.go`.

Input handler: create InputSystem wrapper (same pattern as 4node-basic).

- [ ] **Step 2: Update main.go to Express-like API**

Same pattern as 4node-basic: `WithWorldFactory` + `coord.AddSystem(...)` calls.

- [ ] **Step 3: Verify**

Run: `cd examples/slither && go vet .`

- [ ] **Step 4: Commit**

```bash
git add examples/slither/
git commit -m "refactor(slither): migrate to Express-like AddSystem API"
```

---

## Task 10: Migrate internal/universe/factory.go + cmd/server/main.go

**Files:**
- Modify: `internal/universe/factory.go`
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Refactor GameNodeFactory**

Convert from returning a `NodeFactory` to providing a world factory + system registration:

```go
// GameSetup configures the coordinator with the game-specific world factory
// and systems. Call this after creating the coordinator.
func GameSetup(
	coord *mmokit.Coordinator,
	gameCfg game.GameConfig,
	playerDB *game.PlayerRepo,
	playerSessions *mmokit.PlayerSessions,
) {
	// World factory
	coord.SetWorldFactory(func(base *mmokit.WorldBase) mmokit.GameWorld {
		eng := base.Engine()
		cell := base.Cell()
		id := base.NodeID()

		gw := game.NewGameWorld(eng, gameCfg, playerDB, base.SpatialGrid(), mmokit.CellCoord{
			SX: cell.SX, SY: cell.SY,
		})
		gw.NodeID = id
		gw.PlayerSessions = playerSessions

		replRegistry := buildReplicationRegistry(gw)
		base.SetReplicationRegistry(replRegistry)

		base.SetOnTransferReceived(func(entity mmokit.Entity, frame *mmokit.TransferFrame) {
			gw.FinishTransferSpawn(entity, frame)
		})
		base.SetOnPlayerTransferReceived(func(entity mmokit.Entity, frame *mmokit.TransferFrame) {
			// ... existing hook logic ...
		})

		seRegistry := buildSideEffectRegistry(gw)
		return newGameWorldAdapter(base, gw, seRegistry)
	})

	// Systems
	coord.AddSystem("InputRouter", func() mmokit.System { return &system.InputSystem{} })
	coord.AddSystem("Docking", func() mmokit.System { return &system.DockingSystem{} })
	coord.AddSystem("TargetLock", func() mmokit.System { return &system.TargetLockSystem{} })
	coord.AddSystem("ShipControl", func() mmokit.System { return &system.ShipControlSystem{} })
	coord.AddSystem("Mining", func() mmokit.System { return &system.MiningSystem{} })
	coord.AddSystem("Economy", func() mmokit.System { return &system.EconomySystem{} })
	coord.AddSystem("Equipment", func() mmokit.System { return &system.EquipmentSystem{} })
	coord.AddSystem("Ability", func() mmokit.System { return &system.AbilitySystem{} })
	coord.AddSystem("StatusEffect", func() mmokit.System { return &system.StatusEffectSystem{} })
	coord.AddSystem("Physics", func() mmokit.System { return &system.PhysicsSystem{} })
	coord.AddSystem("DeadReckoning", func() mmokit.System { return &mmokit.ReplicaDeadReckoningSystem{} })
	coord.AddSystem("Lifetime", func() mmokit.System { return &system.LifetimeSystem{} })
	coord.AddSystem("Spatial", func() mmokit.System { return &system.SpatialSystem{} })
	coord.AddSystem("Collision", func() mmokit.System { return &system.CollisionSystem{} })
	coord.AddSystem("ShieldRegen", func() mmokit.System { return &system.ShieldRegenSystem{} })
	coord.AddSystem("Network", func() mmokit.System { return &system.NetworkSystem{} })
}
```

Note: `SetWorldFactory` is a method on coordinator (or use the option pattern). Add if not already available.

- [ ] **Step 2: Update cmd/server/main.go**

```go
// Old:
factory := internaluniverse.GameNodeFactory(gameCfg, playerDB, playerSessions)
coordinator = mmokit.NewCoordinator(grid, platformCfg, factory, opts...)

// New:
coordinator = mmokit.NewCoordinator(grid, platformCfg, opts...)
internaluniverse.GameSetup(coordinator, gameCfg, playerDB, playerSessions)
coordinator.Start(ctx)
```

- [ ] **Step 3: Verify**

Run: `go vet ./... && go test ./... -count=1`

- [ ] **Step 4: Commit**

```bash
git add internal/universe/factory.go cmd/server/main.go
git commit -m "refactor: migrate game factory to Express-like GameSetup"
```

---

## Task 11: Update Tests

**Files:**
- Modify: `pkg/universe/universe_test.go`
- Modify: `internal/universe/testutil_test.go`
- Modify: `internal/universe/coordinator_test.go`
- Modify: `internal/universe/node_test.go`
- Modify: `internal/universe/replica_test.go`

- [ ] **Step 1: Read all test files**

Read each test file to understand current patterns.

- [ ] **Step 2: Update mock systems in universe_test.go**

Mock systems should no longer need Name(). If the test directly constructs GameLoop, update the NewGameLoop call to pass names separately.

- [ ] **Step 3: Update testutil_test.go helper**

The `newTestNode` helper creates Engine, systems, and GameLoop. Update to pass names to NewGameLoop.

- [ ] **Step 4: Update coordinator_test.go**

Tests that access `coord.Nodes` after `NewCoordinator`:
- If still using `WithNodeFactory`, add `coord.Build()` call
- Or migrate to new pattern

- [ ] **Step 5: Update node_test.go and replica_test.go**

Update system construction and GameLoop creation to use new signatures.

- [ ] **Step 6: Verify all tests pass**

Run: `go test ./... -v -count=1`

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/universe_test.go internal/universe/
git commit -m "test: update all tests for new System interface and coordinator API"
```

---

## Task 12: Cleanup

**Files:**
- Modify: `pkg/system/physics.go`, `lifetime.go`, `replica_dead_reckoning.go` — delete deprecated constructors
- Modify: `pkg/universe/boundary_system.go` — delete deprecated constructor
- Modify: `pkg/universe/coordinator.go` — remove `WithNodeFactory` and old-pattern code path
- Modify: `pkg/mmokit/mmokit.go` — remove `NodeFactory` alias, remove deprecated constructor aliases
- Modify: `pkg/system/lifetime.go` — remove `EntityRemover` type if unused

- [ ] **Step 1: Delete all deprecated constructors**

Search for all `// Deprecated` constructors added during migration and delete them.

- [ ] **Step 2: Remove NodeFactory backward compat**

Delete `WithNodeFactory`, remove old-pattern code path from `Build()`. The `NodeFactory` type can remain in coordinator.go but isn't exported through mmokit.

- [ ] **Step 3: Remove unused mmokit re-exports**

Delete from mmokit.go:
- `NodeFactory` type alias
- `NewPhysicsSystem`, `NewLifetimeSystem`, `NewReplicaDeadReckoningSystem` constructor aliases
- `NewBoundarySystem` constructor alias
- Any other dead constructor aliases

- [ ] **Step 4: Remove EntityRemover if unused**

Check if `EntityRemover` is still referenced anywhere. If not, delete from `pkg/system/lifetime.go`.

- [ ] **Step 5: Final verification**

Run:

```bash
go vet ./...
go test ./... -count=1
cd examples/4node-basic && go vet .
cd examples/slither && go vet .
make build
```

All must pass with zero warnings.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "cleanup: remove deprecated constructors, NodeFactory compat, dead re-exports"
```

---

## Verification Checklist

After all tasks are complete:

- [ ] `go vet ./...` passes
- [ ] `go test ./... -count=1` passes (all existing tests)
- [ ] `cd examples/4node-basic && go vet .` passes
- [ ] `cd examples/slither && go vet .` passes
- [ ] `make build` succeeds
- [ ] No system has a `Name()` method
- [ ] No system uses lazy `if s.filter == nil` in Update
- [ ] All systems embed `SystemBase` (or are complex systems with constructors)
- [ ] `NewCoordinator` no longer requires a factory function
- [ ] ConnManager is created internally by default
- [ ] `coord.AddSystem(name, factory)` is the primary registration API
