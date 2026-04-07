package universe

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/metrics"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/spatial"
)

const netIDRangeSize uint32 = 10_000_000

// Config holds all Coordinator configuration. Zero values use sensible defaults.
type Config struct {
	CellsX            uint32  // number of cells wide (0 = 1)
	CellsY            uint32  // number of cells tall (0 = 1)
	CellSize          float32 // world units per cell (0 = default 8192)
	SpatialBucketSize float32 // spatial hash bucket size (0 = CellSize/10)
	TickRate          int     // game loop tick rate (0 = 20)
	AoIRadius         float32 // area-of-interest radius (0 = 500)
	DefaultCell       CellID
	Headless          bool
	ProxiesEnabled      bool // use lightweight proxy summaries instead of full replicas
	DynamicPartitioning *PartitionConfig // nil = disabled (default)
	ConnManager       *net.ConnManager
	Logger            *logger.Logger
	LogCategories     string // comma-separated categories/groups to enable (overrides default enabled list)
	DebugTopology     bool   // send MeshState + CellTopology to clients (debug/visualization only)
	LoginHandler    LoginHandler  // required: parses login messages, returns username
	LoginRejected   func(connID uint32, reason string) // optional: called on rejected login
	LoginTimeout    time.Duration // max time for login before disconnect (0 = 30s)
}

// ConsoleOpts provides game-specific console configuration.
// All fields are optional — omit what your game doesn't need.
type ConsoleOpts struct {
	Config      engine.Configurable    // enables "config list/get/set"
	ConfigSave  func() error           // enables "config save"
	ConfigReset func()                 // enables "config reset"
	Entities    *engine.EntityOpts     // enables "entity summary/list/get/remove"
	Registry    *engine.EntityRegistry // enables "entity add"
}

// Coordinator manages multiple Node instances, routes connections, and coordinates transfers.
type Coordinator struct {
	Nodes     map[string]*Node
	NodeOwner map[CellID]string // cell -> nodeID
	Topology  Topology

	ConnMgr *net.ConnManager
	Log     *logger.Logger

	console      *engine.Console // nil if headless
	defaultCell  CellID
	cfg          Config
	netIDAlloc   *NetIDAllocator
	partState    *partitionState // nil if dynamic partitioning disabled

	systemDefs []engine.SystemDef
	built      bool

	worldFactory   func(base *WorldBase) GameWorld
	onInit         func(w *WorldBase)
	consoleOpts    *ConsoleOpts
	onConsoleReady func(c *engine.Console)

	mu         sync.RWMutex
	playerNode map[uint32]string // connID -> nodeID

	activeUsers    map[string]string // username -> nodeID (for dupe detection)
	disconnected   map[string]string // username -> nodeID (for reconnection)
	loginSvc       *loginService
	playerRouter   PlayerRouter
}

// NewCoordinator creates a coordinator with the given Config.
// Zero-value fields use sensible defaults (see Config field docs).
// Use AddSystem/SetWorld for Express-like setup, then call Build() or Start().
func NewCoordinator(cfg Config) *Coordinator {
	// Apply defaults for zero values
	if cfg.CellsX == 0 {
		cfg.CellsX = 1
	}
	if cfg.CellsY == 0 {
		cfg.CellsY = 1
	}
	if cfg.TickRate == 0 {
		cfg.TickRate = 20
	}
	if cfg.AoIRadius == 0 {
		cfg.AoIRadius = 500
	}
	if cfg.ConnManager == nil {
		cfg.ConnManager = net.NewConnManager()
	}
	if cfg.Logger == nil {
		cfg.Logger = logger.New()
	}

	if cfg.CellSize > 0 {
		coords.SetCellSize(cfg.CellSize)
	}

	return &Coordinator{
		Nodes:       make(map[string]*Node),
		NodeOwner:   make(map[CellID]string),
		ConnMgr:     cfg.ConnManager,
		Log:         cfg.Logger,
		defaultCell: cfg.DefaultCell,
		playerNode:   make(map[uint32]string),
		activeUsers:  make(map[string]string),
		disconnected: make(map[string]string),
		cfg:          cfg,
	}
}

// AddSystem registers a named system factory. Systems are instantiated per-node
// during Build().
func (c *Coordinator) AddSystem(name string, factory func() engine.System) {
	c.systemDefs = append(c.systemDefs, engine.SystemDef{Name: name, Factory: factory})
}

// SetWorld sets the factory function that creates a GameWorld for each node.
// The factory receives a fully constructed *WorldBase and should return a game
// world struct that embeds it. Use Init() on your GameWorld for post-wiring setup.
// Mutually exclusive with OnInit. Must be called before Build().
func (c *Coordinator) SetWorld(factory func(base *WorldBase) GameWorld) {
	c.worldFactory = factory
}

// OnInit sets an initialization function called on each node's WorldBase after
// all nodes are created and bridges are wired. Use this for simple games that
// don't need a custom world struct. Mutually exclusive with SetWorld.
// Must be called before Build().
func (c *Coordinator) OnInit(fn func(w *WorldBase)) {
	c.onInit = fn
}

// SetConsole configures game-specific console options (config, entity commands).
// Replaces the Console field that was previously on Config.
func (c *Coordinator) SetConsole(opts ConsoleOpts) {
	c.consoleOpts = &opts
}

// OnConsoleReady registers a callback invoked after the console is created and
// builtins are registered. Use it to register custom commands.
func (c *Coordinator) OnConsoleReady(fn func(c *engine.Console)) {
	c.onConsoleReady = fn
}

// SetPlayerRouter sets the callback that determines which node hosts a player.
// Called after successful login with the authenticated username.
// Must return a valid nodeID. Must be called before Start().
func (c *Coordinator) SetPlayerRouter(router PlayerRouter) {
	c.playerRouter = router
}

// NodeAtPosition returns the nodeID that owns the given world-space position.
// Handles dynamic cells — always finds the correct subcell.
// Returns "" if no node owns the position.
func (c *Coordinator) NodeAtPosition(worldX, worldY float32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for cell, nodeID := range c.NodeOwner {
		minX, minY, maxX, maxY := cell.WorldBounds(coords.CellSize)
		if worldX >= minX && worldX < maxX && worldY >= minY && worldY < maxY {
			return nodeID
		}
	}
	return ""
}

// notifySessionActive is called when a player transitions to active on a node.
// Thread-safe — called from node game loops.
func (c *Coordinator) notifySessionActive(username, nodeID string) {
	c.mu.Lock()
	c.activeUsers[username] = nodeID
	delete(c.disconnected, username)
	c.mu.Unlock()
}

// notifySessionDisconnected is called when a player disconnects (enters grace period).
// Thread-safe — called from node game loops.
func (c *Coordinator) notifySessionDisconnected(username, nodeID string) {
	c.mu.Lock()
	c.disconnected[username] = nodeID
	delete(c.activeUsers, username)
	c.mu.Unlock()
}

// notifySessionRemoved is called when a player session is fully removed from a node.
// Thread-safe — called from node game loops.
func (c *Coordinator) notifySessionRemoved(username string) {
	c.mu.Lock()
	delete(c.disconnected, username)
	delete(c.activeUsers, username)
	c.mu.Unlock()
}

// SystemDefs returns the registered system definitions (for testing/introspection).
func (c *Coordinator) SystemDefs() []engine.SystemDef {
	return c.systemDefs
}

// ConnManager returns the Coordinator's connection manager.
func (c *Coordinator) ConnManager() *net.ConnManager {
	return c.ConnMgr
}

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

// Build creates all nodes, wires topology, bridges, and metrics.
// Called automatically by Start() if not called explicitly.
func (c *Coordinator) Build() {
	if c.built {
		return
	}
	c.built = true

	if c.worldFactory == nil && c.onInit == nil {
		panic("mmokit: coordinator requires SetWorld or OnInit before Build")
	}

	cfg := c.cfg

	// Compute spatial hash cell size (default: CellSize / 10)
	spatialCellSize := cfg.SpatialBucketSize
	if spatialCellSize <= 0 {
		spatialCellSize = coords.CellSize / 10
	}

	// Initialize net ID allocator
	c.netIDAlloc = NewNetIDAllocator(0, netIDRangeSize)

	// Resolve dynamic partitioning defaults
	if cfg.DynamicPartitioning != nil {
		if cfg.DynamicPartitioning.MinCellSize <= 0 {
			cfg.DynamicPartitioning.MinCellSize = coords.CellSize / 4
		}
		if cfg.DynamicPartitioning.OnTopologyChanged == nil && cfg.DebugTopology {
			cfg.DynamicPartitioning.OnTopologyChanged = func() {
				c.BroadcastCellTopology()
			}
		}
		c.partState = newPartitionState()
	}

	// Create grid of cells. createNode returns the systems slice so we can
	// defer Init() until after World.Init().
	type nodeSetup struct {
		node    *Node
		systems []engine.System
	}
	var cells []CellID
	var setups []nodeSetup
	for sy := uint32(0); sy < cfg.CellsY; sy++ {
		for sx := uint32(0); sx < cfg.CellsX; sx++ {
			cell := CellID{X: int32(sx), Y: int32(sy)}
			cells = append(cells, cell)
			node, systems := c.createNode(cell, spatialCellSize)
			setups = append(setups, nodeSetup{node, systems})
		}
	}

	// Compute topology and wire neighbors
	c.Topology = ComputeTopology(cells, coords.CellSize)
	for cell, neighborCells := range c.Topology.Neighbors {
		nodeID := c.NodeOwner[cell]
		node := c.Nodes[nodeID]
		for _, nc := range neighborCells {
			neighborID := c.NodeOwner[nc]
			node.Neighbors[neighborID] = c.Nodes[neighborID]
		}
	}

	// Auto-register /metrics endpoint on the ConnManager's HTTP mux.
	cfg.ConnManager.Handle("/metrics", c.MetricsHandler())

	// Apply --log flag if provided (overrides default enabled list).
	if c.cfg.LogCategories != "" {
		c.Log.EnableFromFlag(c.cfg.LogCategories)
	}

	// Ensure startup categories are always enabled so lifecycle info is visible.
	c.Log.Enable(StartupCategories...)

	c.Log.Log(CatMeshNode, "coordinator: created %d nodes, topology computed", len(c.Nodes))

	// Two-phase init: World.Init() first (registers entity kinds, login handlers),
	// then system Init() (discovers replicators, creates query filters).
	for _, s := range setups {
		s.node.World.Init()
	}
	for _, s := range setups {
		initSystems(s.systems)
	}
}

// initSystems calls Init() on each system that implements it.
func initSystems(systems []engine.System) {
	type initializable interface{ Init() }
	for _, sys := range systems {
		if init, ok := sys.(initializable); ok {
			init.Init()
		}
	}
}

// createNode creates a single Node for the given cell, including its ECS world,
// game systems, game loop, and metrics. The node is registered in c.Nodes and
// c.NodeOwner but NOT started — call node.Run(ctx) separately.
// System Init() is NOT called — the caller must call initSystems() after
// World.Init() so systems can discover entity kinds and other world state.
func (c *Coordinator) createNode(cell CellID, spatialBucketSize float32) (*Node, []engine.System) {
	cfg := c.cfg
	platformCfg := engine.Config{TickRate: cfg.TickRate}

	id := MeshNodeID(cell)
	eng := engine.New(platformCfg, cfg.ConnManager, cfg.Logger)
	eng.SetNetIDBase(c.netIDAlloc.Allocate())

	nodeID := id // capture for closures
	eng.Players.SetSessionCallbacks(
		func(username string) { c.notifySessionActive(username, nodeID) },
		func(username string) { c.notifySessionDisconnected(username, nodeID) },
		func(username string) { c.notifySessionRemoved(username) },
	)

	events := make(chan net.PlayerEvent, 64)

	base := NewWorldBase(eng, cell, cfg.AoIRadius, nil)
	base.spatialGrid = spatial.NewHashGrid(spatialBucketSize)

	base.coord = c

	// Set framework defaults for common hooks. Games can override these
	// in their WorldFactory by calling the corresponding Set* methods.
	base.onCellBoundsChanged = func(connID uint32) {
		s := eng.Players.ByConnID(connID)
		if s != nil && s.Entity != (ecs.Entity{}) && base.eng.ECS.Alive(s.Entity) {
			base.SendSpawnedMsg(connID, s.Entity)
		}
	}
	base.onPlayerTransferReceived = func(entity ecs.Entity, frame *TransferFrame) {
		if s := eng.Players.ByConnID(frame.ConnID); s != nil {
			s.Entity = entity
		}
		base.SendSpawnedMsg(frame.ConnID, entity)
	}

	var world GameWorld
	if c.worldFactory != nil {
		world = c.worldFactory(base)
	} else if c.onInit != nil {
		world = &onInitWorld{WorldBase: base, initFn: c.onInit}
	} else {
		world = base
	}

	// Phase 1: create systems and inject dependencies. Init() is deferred
	// to Build() after World.Init() so that systems like NetworkSystem can
	// discover entity kinds registered during World.Init().
	gameSystems := make([]engine.System, len(c.systemDefs))
	systemNames := make([]string, len(c.systemDefs))
	for i, def := range c.systemDefs {
		sys := def.Factory()

		type depsInjectable interface {
			SetDeps(w *ecs.World, eng *engine.Engine, gw any)
		}
		if di, ok := sys.(depsInjectable); ok {
			di.SetDeps(eng.ECS, eng, world)
		}

		gameSystems[i] = sys
		systemNames[i] = def.Name
	}

	eng.OnEntityRemoved = func(e ecs.Entity) {
		base.spatialGrid.Deregister(e)
	}

	if bw, ok := world.(BoundaryWorld); ok {
		bs := &BoundarySystem{bw: bw}
		bs.SetDeps(eng.ECS, eng, world)
		gameSystems = append(gameSystems, bs)
		systemNames = append(systemNames, "CellBoundary")
	}

	node := &Node{
		ID:        id,
		Cell:      cell,
		Engine:    eng,
		World:     world,
		Inbox:     make(chan NodeMessage, 256),
		Events:    events,
		Neighbors: make(map[string]*Node),
		Log:       cfg.Logger,
	}

	gameHooks := world.Hooks()
	mergedHooks := engine.Hooks{
		OnConnect:     gameHooks.OnConnect,
		OnDisconnect:  gameHooks.OnDisconnect,
		ProcessLogins: gameHooks.ProcessLogins,
		PreFlush:      gameHooks.PreFlush,
		PostFlush:     gameHooks.PostFlush,
		ClearTickState: func() {
			if gameHooks.ClearTickState != nil {
				gameHooks.ClearTickState()
			}
			node.Bridge.PreTick()
		},
		PostTick: func() {
			node.Bridge.PostSystems()
			if gameHooks.PostTick != nil {
				gameHooks.PostTick()
			}
		},
	}

	gameLoop := engine.NewGameLoop(eng, gameSystems, systemNames, mergedHooks)
	gameLoop.SetEventsCh(events)
	node.Loop = gameLoop

	cm := cfg.ConnManager
	tickStatsFn := func() metrics.TickStats {
		s := eng.Perf.Stats()
		ts := metrics.TickStats{
			SystemNames: s.SystemNames,
			Total:       convertTimingStats(s.Total),
			SampleCount: s.SampleCount,
		}
		ts.Systems = make([]metrics.TimingStats, len(s.Systems))
		for i, sys := range s.Systems {
			ts.Systems[i] = convertTimingStats(sys)
		}
		return ts
	}
	networkStatsFn := func() (uint64, uint64, int) {
		return cm.TotalBytesSent(), cm.TotalBytesRecv(), cm.ConnectionCount()
	}
	eng.EntityCounter = makeEntityCounter(eng.ECS)

	nm := metrics.NewNodeMetrics(id, platformCfg.TickRate, tickStatsFn, networkStatsFn)
	node.Metrics = nm
	eng.Metrics = nm

	// Wire bridge
	bridge := &nodeBridge{node: node, coord: c}
	node.Bridge = bridge
	node.World.SetBridge(bridge)

	// Callers during Build() don't need locking (single-threaded).
	// Callers during runtime (SplitCell) must hold c.mu write lock.
	c.Nodes[id] = node
	c.NodeOwner[cell] = id

	return node, gameSystems
}

// Start launches all node goroutines, the event router, and — unless headless —
// the interactive console. Start blocks until the context is cancelled or the
// user types "quit" in the console. On return all nodes have been shut down.
func (c *Coordinator) Start(ctx context.Context) {
	c.Build()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go c.routeEvents(ctx)

	for _, node := range c.Nodes {
		go node.Run(ctx)
	}
	c.Log.Log(CatMeshNode, "coordinator: all %d nodes started", len(c.Nodes))

	// Start partition monitor if dynamic partitioning is enabled.
	if c.cfg.DynamicPartitioning != nil {
		monitor := newPartitionMonitor(c, c.cfg.DynamicPartitioning)
		go monitor.run(ctx)
		c.Log.Log(CatMeshNode, "coordinator: partition monitor started (eval every %s)", c.cfg.DynamicPartitioning.EvalInterval)
	}

	// Install signal handler.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			log.Println("shutting down...")
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(sigCh)
	}()

	if !c.cfg.Headless {
		c.startConsole(ctx)
	} else {
		<-ctx.Done()
	}

	c.Shutdown()
}

// startConsole creates the console, registers builtins, and runs it (blocking).
func (c *Coordinator) startConsole(ctx context.Context) {
	defaultNode := c.DefaultNode()
	c.console = engine.NewConsole(defaultNode.Engine, c.Log)

	// Auto-wire node builtins from coordinator's node map.
	nodeRefs := c.buildNodeRefs()

	builtinOpts := engine.BuiltinOpts{
		Nodes: nodeRefs,
	}

	// Merge game-provided builtins if Console was set.
	co := c.consoleOpts
	if co != nil {
		builtinOpts.Config = co.Config
		builtinOpts.ConfigSave = co.ConfigSave
		builtinOpts.ConfigReset = co.ConfigReset
		builtinOpts.Entities = co.Entities
		builtinOpts.Registry = co.Registry
	}

	// Auto-wire default entity commands if game didn't provide its own.
	if builtinOpts.Entities == nil {
		builtinOpts.Entities = c.defaultEntityOpts(defaultNode)
	}

	c.console.RegisterBuiltins(builtinOpts)

	// Register cell commands if dynamic partitioning is enabled.
	if c.cfg.DynamicPartitioning != nil {
		c.registerCellCommands(c.console)
	}

	// Let game register custom commands.
	onReady := c.onConsoleReady
	if onReady != nil {
		onReady(c.console)
	}

	c.console.Run(ctx)
}

// buildNodeRefs creates NodeRef entries from the coordinator's node map.
func (c *Coordinator) buildNodeRefs() []engine.NodeRef {
	refs := make([]engine.NodeRef, 0, len(c.Nodes))
	for _, node := range c.Nodes {
		n := node
		refs = append(refs, engine.NodeRef{
			ID: n.ID,
			Exec: func(fn func() string) string {
				result := make(chan string, 1)
				n.Engine.PendingAdminCmds <- func() { result <- fn() }
				select {
				case r := <-result:
					return r
				case <-time.After(5 * time.Second):
					return "  node not responding (timeout)\n"
				}
			},
			Metrics: n.Metrics,
		})
	}
	return refs
}

// defaultEntityOpts builds EntityOpts from generic components on WorldBase.
// Provides entity list/get/summary/remove without game-specific configuration.
func (c *Coordinator) defaultEntityOpts(node *Node) *engine.EntityOpts {
	wb, ok := node.World.(interface {
		EntityKindDefs() map[uint8]*EntityKindDef
		ECSWorld() *ecs.World
		MarkForRemoval(ecs.Entity)
	})
	if !ok {
		return nil
	}

	kindName := func(kindType uint8) string {
		if def, ok := wb.EntityKindDefs()[kindType]; ok && def.Name != "" {
			return def.Name
		}
		return fmt.Sprintf("kind_%d", kindType)
	}

	w := wb.ECSWorld()
	posMap := ecs.NewMap1[component.Position](w)
	velMap := ecs.NewMap1[component.Velocity](w)
	cellMap := ecs.NewMap1[component.CellCoord](w)

	return &engine.EntityOpts{
		Summary: func() map[string]int {
			counts := make(map[string]int)
			filter := ecs.NewFilter2[component.NetworkID, component.EntityKind](w)
			query := filter.Query()
			for query.Next() {
				_, kind := query.Get()
				counts[kindName(kind.Type)]++
			}
			return counts
		},
		List: func(typeName string) []engine.EntityInfo {
			var result []engine.EntityInfo
			filter := ecs.NewFilter2[component.NetworkID, component.EntityKind](w)
			query := filter.Query()
			for query.Next() {
				nid, kind := query.Get()
				name := kindName(kind.Type)
				if typeName != "" && name != typeName {
					continue
				}
				entity := query.Entity()
				info := engine.EntityInfo{
					NetID:  nid.ID,
					NodeID: node.ID,
					Type:   name,
				}
				if posMap.HasAll(entity) {
					pos := posMap.Get(entity)
					info.X, info.Y = pos.X, pos.Y
				}
				if velMap.HasAll(entity) {
					vel := velMap.Get(entity)
					info.VX, info.VY = vel.X, vel.Y
				}
				if cellMap.HasAll(entity) {
					cc := cellMap.Get(entity)
					info.CellSX, info.CellSY = cc.CellX, cc.CellY
				}
				result = append(result, info)
			}
			return result
		},
		Get: func(netID uint32) (engine.EntityInfo, bool) {
			filter := ecs.NewFilter2[component.NetworkID, component.EntityKind](w)
			query := filter.Query()
			for query.Next() {
				nid, kind := query.Get()
				if nid.ID != netID {
					continue
				}
				entity := query.Entity()
				query.Close()
				info := engine.EntityInfo{
					NetID:  nid.ID,
					NodeID: node.ID,
					Type:   kindName(kind.Type),
				}
				if posMap.HasAll(entity) {
					pos := posMap.Get(entity)
					info.X, info.Y = pos.X, pos.Y
				}
				if velMap.HasAll(entity) {
					vel := velMap.Get(entity)
					info.VX, info.VY = vel.X, vel.Y
				}
				if cellMap.HasAll(entity) {
					cc := cellMap.Get(entity)
					info.CellSX, info.CellSY = cc.CellX, cc.CellY
				}
				return info, true
			}
			return engine.EntityInfo{}, false
		},
		Remove: func(netID uint32) bool {
			filter := ecs.NewFilter1[component.NetworkID](w)
			query := filter.Query()
			for query.Next() {
				nid := query.Get()
				if nid.ID == netID {
					entity := query.Entity()
					query.Close()
					wb.MarkForRemoval(entity)
					return true
				}
			}
			return false
		},
	}
}

// Shutdown saves state on all nodes.
func (c *Coordinator) Shutdown() {
	for _, node := range c.Nodes {
		node.Shutdown()
	}
	c.Log.Log(CatMeshNode, "coordinator: all nodes shut down")
}

// routeEvents drains ConnManager.Events() and fans out to per-node Events channels.
func (c *Coordinator) routeEvents(ctx context.Context) {
	events := c.ConnMgr.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-events:
			if evt.Connected {
				defaultNode := c.findDefaultNode()
				if defaultNode == nil {
					c.Log.Log(CatNetConn, "coordinator: no default node for conn %d", evt.ConnID)
					continue
				}
				c.setPlayerNode(evt.ConnID, defaultNode.ID)
				defaultNode.Events <- evt
				c.Log.Log(CatNetConn, "coordinator: conn %d -> %s", evt.ConnID, defaultNode.ID)
			} else {
				nodeID := c.getPlayerNode(evt.ConnID)
				if nodeID != "" {
					if node, ok := c.getNode(nodeID); ok {
						node.Events <- evt
					}
					c.removePlayerNode(evt.ConnID)
				}
			}
		}
	}
}

// NodeForCell returns the node that owns the given cell.
func (c *Coordinator) NodeForCell(cell CellID) *Node {
	nodeID := c.NodeOwner[cell]
	return c.Nodes[nodeID]
}

// DefaultNode returns the node that new connections are routed to.
func (c *Coordinator) DefaultNode() *Node {
	return c.NodeForCell(c.defaultCell)
}

// findDefaultNode returns the node that new connections should be routed to.
// If the default cell was split, finds a sub-cell that contains the default
// cell's origin using NodeOwnerAtPos.
func (c *Coordinator) findDefaultNode() *Node {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Try direct lookup first
	nodeID := c.NodeOwner[c.defaultCell]
	if node, ok := c.Nodes[nodeID]; ok {
		return node
	}
	// Default cell was split — find which sub-cell owns the origin
	ox, oy := c.defaultCell.WorldOrigin(coords.CellSize)
	for cell, nID := range c.NodeOwner {
		minX, minY, maxX, maxY := cell.WorldBounds(coords.CellSize)
		if ox >= minX && ox < maxX && oy >= minY && oy < maxY {
			return c.Nodes[nID]
		}
	}
	// Fallback: return any node
	for _, node := range c.Nodes {
		return node
	}
	return nil
}

// DefaultCell returns the cell that new connections are routed to.
// Defaults to {0,0}; override with Config.DefaultCell.
func (c *Coordinator) DefaultCell() CellID {
	return c.defaultCell
}

// Console returns the Coordinator's interactive console, or nil if headless.
func (c *Coordinator) Console() *engine.Console { return c.console }

// GridWidth returns the number of cells wide in the mesh grid.
func (c *Coordinator) GridWidth() uint32 { return c.cfg.CellsX }

// DebugTopology returns whether debug topology info is sent to clients.
func (c *Coordinator) DebugTopology() bool { return c.cfg.DebugTopology }

func (c *Coordinator) getPlayerNode(connID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.playerNode[connID]
}

func (c *Coordinator) setPlayerNode(connID uint32, nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.playerNode[connID] = nodeID
}

func (c *Coordinator) removePlayerNode(connID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.playerNode, connID)
}

// getNode returns a node by ID under read lock.
func (c *Coordinator) getNode(nodeID string) (*Node, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n, ok := c.Nodes[nodeID]
	return n, ok
}

// getNodeOwner returns the owning node ID for a cell under read lock.
func (c *Coordinator) getNodeOwner(cell CellID) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.NodeOwner[cell]
}

// ActiveCells returns all active cell IDs and their owning node IDs.
func (c *Coordinator) ActiveCells() map[CellID]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[CellID]string, len(c.NodeOwner))
	for cell, nodeID := range c.NodeOwner {
		result[cell] = nodeID
	}
	return result
}

// SendCellTopology sends the current cell topology to a specific client.
func (c *Coordinator) SendCellTopology(connID uint32) {
	if !c.cfg.DebugTopology {
		return
	}
	frame := c.buildCellTopologyFrame()
	c.cfg.ConnManager.Send(connID, frame)
}

// BroadcastCellTopology sends the current cell topology to all connected clients.
func (c *Coordinator) BroadcastCellTopology() {
	if !c.cfg.DebugTopology {
		return
	}
	frame := c.buildCellTopologyFrame()
	for _, connID := range c.cfg.ConnManager.ActiveConnIDs() {
		c.cfg.ConnManager.Send(connID, frame)
	}
}

func (c *Coordinator) buildCellTopologyFrame() []byte {
	cells := c.ActiveCells()
	baseCellSize := coords.CellSize
	msg := &enginepb.CellTopologyMsg{
		GridW:        int32(c.cfg.CellsX),
		GridH:        int32(c.cfg.CellsY),
		BaseCellSize: baseCellSize,
	}
	for cell, nodeID := range cells {
		size := cell.Size(baseCellSize)
		ox, oy := cell.WorldOrigin(baseCellSize)
		msg.Cells = append(msg.Cells, &enginepb.CellInfo{
			CellX:   cell.X,
			CellY:   cell.Y,
			Depth:   uint32(cell.Depth),
			Size:    size,
			OriginX: ox,
			OriginY: oy,
			NodeId:  nodeID,
		})
	}
	return makeEventFrame(uint32(enginepb.ServerEventCode_SE_CELL_TOPOLOGY), msg)
}

// NodeLoad returns the current load snapshot for a node.
// Used by Feature #7 (dynamic partitioning) for rebalancing decisions.
func (c *Coordinator) NodeLoad(nodeID string) (metrics.LoadSnapshot, bool) {
	c.mu.RLock()
	node, ok := c.Nodes[nodeID]
	c.mu.RUnlock()
	if !ok || node.Metrics == nil {
		return metrics.LoadSnapshot{}, false
	}
	return node.Metrics.Snapshot(), true
}

// AllNodeLoads returns load snapshots for all nodes.
func (c *Coordinator) AllNodeLoads() map[string]metrics.LoadSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]metrics.LoadSnapshot, len(c.Nodes))
	for id, node := range c.Nodes {
		if node.Metrics != nil {
			result[id] = node.Metrics.Snapshot()
		}
	}
	return result
}

// MetricsHandler returns an HTTP handler that serves Prometheus-compatible
// metrics for all nodes. Mount on your HTTP mux: mux.Handle("/metrics", coord.MetricsHandler())
func (c *Coordinator) MetricsHandler() http.HandlerFunc {
	return metrics.Handler(c.AllNodeLoads)
}

// convertTimingStats converts engine.TimingStats to metrics.TimingStats.
func convertTimingStats(s engine.TimingStats) metrics.TimingStats {
	return metrics.TimingStats{
		Latest: s.Latest,
		Avg:    s.Avg,
		P50:    s.P50,
		P95:    s.P95,
		P99:    s.P99,
		Max:    s.Max,
	}
}

// makeEntityCounter returns an EntityCounter callback that counts entities
// using ECS component filters. Called from the game loop goroutine (safe).
func makeEntityCounter(w *ecs.World) func() (int, int, int, int) {
	return func() (real, replica, ghost, connected int) {
		// Real entities: have NetworkID, not Replica, not Ghost
		realFilter := ecs.NewFilter1[component.NetworkID](w).
			Without(ecs.C[component.Replica](), ecs.C[component.Ghost]())
		q := realFilter.Query()
		for q.Next() {
			real++
		}

		// Replica entities
		replicaFilter := ecs.NewFilter1[component.Replica](w)
		q2 := replicaFilter.Query()
		for q2.Next() {
			replica++
		}

		// Ghost entities
		ghostFilter := ecs.NewFilter1[component.Ghost](w)
		q3 := ghostFilter.Query()
		for q3.Next() {
			ghost++
		}

		// Connected entities: have PlayerConn + NetworkID, not Replica, not Ghost
		connFilter := ecs.NewFilter2[component.PlayerConn, component.NetworkID](w).
			Without(ecs.C[component.Replica](), ecs.C[component.Ghost]())
		q4 := connFilter.Query()
		for q4.Next() {
			connected++
		}

		return
	}
}
