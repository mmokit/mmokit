package universe

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/mlange-42/ark/ecs"

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
	WorldFactory        func(base *WorldBase) GameWorld
	Console           *ConsoleOpts
	OnConsoleReady    func(c *engine.Console)
	ConnManager       *net.ConnManager
	Logger            *logger.Logger
	LogCategories     string // comma-separated categories/groups to enable (overrides default enabled list)
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

	mu         sync.RWMutex
	playerNode map[uint32]string // connID -> nodeID
}

// NewCoordinator creates a coordinator with the given Config.
// Zero-value fields use sensible defaults (see Config field docs).
// Use AddSystem/SetWorldFactory for Express-like setup, then call Build() or Start().
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
		playerNode:  make(map[uint32]string),
		cfg:         cfg,
	}
}

// AddSystem registers a named system factory. Systems are instantiated per-node
// during Build(). Use with SetWorldFactory for the Express-like API.
func (c *Coordinator) AddSystem(name string, factory func() engine.System) {
	c.systemDefs = append(c.systemDefs, engine.SystemDef{Name: name, Factory: factory})
}

// SetWorldFactory sets a factory that creates a GameWorld from a WorldBase.
// Used with AddSystem for the Express-like API.
func (c *Coordinator) SetWorldFactory(fn func(base *WorldBase) GameWorld) {
	c.cfg.WorldFactory = fn
}

// SystemDefs returns the registered system definitions (for testing/introspection).
func (c *Coordinator) SystemDefs() []engine.SystemDef {
	return c.systemDefs
}

// ConnManager returns the Coordinator's connection manager.
func (c *Coordinator) ConnManager() *net.ConnManager {
	return c.ConnMgr
}

// Build creates all nodes, wires topology, bridges, and metrics.
// Called automatically by Start() if not called explicitly.
func (c *Coordinator) Build() {
	if c.built {
		return
	}
	c.built = true

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
		c.partState = newPartitionState()
	}

	// Create grid of cells
	var cells []CellID
	for sy := uint32(0); sy < cfg.CellsY; sy++ {
		for sx := uint32(0); sx < cfg.CellsX; sx++ {
			cell := CellID{X: int32(sx), Y: int32(sy)}
			cells = append(cells, cell)
			c.createNode(cell, spatialCellSize)
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
}

// createNode creates a single Node for the given cell, including its ECS world,
// game systems, game loop, and metrics. The node is registered in c.Nodes and
// c.NodeOwner but NOT started — call node.Run(ctx) separately.
// The node's Bridge is wired as a nodeBridge connected to this coordinator.
func (c *Coordinator) createNode(cell CellID, spatialBucketSize float32) *Node {
	cfg := c.cfg
	platformCfg := engine.Config{TickRate: cfg.TickRate}

	id := MeshNodeID(cell)
	eng := engine.New(platformCfg, cfg.ConnManager, cfg.Logger)
	eng.SetNetIDBase(c.netIDAlloc.Allocate())

	events := make(chan net.PlayerEvent, 64)

	base := NewWorldBase(eng, cell, cfg.AoIRadius, nil)
	base.spatialGrid = spatial.NewHashGrid(spatialBucketSize)

	var world GameWorld
	if cfg.WorldFactory != nil {
		world = cfg.WorldFactory(&base)
	} else {
		world = &base
	}

	gameSystems := make([]engine.System, len(c.systemDefs))
	systemNames := make([]string, len(c.systemDefs))
	for i, def := range c.systemDefs {
		sys := def.Factory()

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

		gameSystems[i] = sys
		systemNames[i] = def.Name
	}

	eng.OnEntityRemoved = func(e ecs.Entity) {
		base.spatialGrid.Deregister(e)
	}

	if bw, ok := world.(BoundaryWorld); ok {
		bs := &BoundarySystem{bw: bw}
		bs.SetDeps(eng.ECS, eng, world)
		bs.Init()
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

	return node
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
	if c.cfg.Console != nil {
		co := c.cfg.Console
		builtinOpts.Config = co.Config
		builtinOpts.ConfigSave = co.ConfigSave
		builtinOpts.ConfigReset = co.ConfigReset
		builtinOpts.Entities = co.Entities
		builtinOpts.Registry = co.Registry
	}

	c.console.RegisterBuiltins(builtinOpts)

	// Register cell commands if dynamic partitioning is enabled.
	if c.cfg.DynamicPartitioning != nil {
		c.registerCellCommands(c.console)
	}

	// Let game register custom commands.
	if c.cfg.OnConsoleReady != nil {
		c.cfg.OnConsoleReady(c.console)
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
	return func() (real, replica, ghost, players int) {
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

		// Player entities: have PlayerConn + NetworkID, not Replica, not Ghost
		playerFilter := ecs.NewFilter2[component.PlayerConn, component.NetworkID](w).
			Without(ecs.C[component.Replica](), ecs.C[component.Ghost]())
		q4 := playerFilter.Query()
		for q4.Next() {
			players++
		}

		return
	}
}
