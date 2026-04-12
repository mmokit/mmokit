package universe

import (
	"context"
	"fmt"
	"log"
	"maps"
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
	"google.golang.org/protobuf/proto"
)

const netIDRangeSize uint32 = 10_000_000

// Config holds all Coordinator configuration. Zero values use sensible defaults.
type Config struct {
	CellsX              uint32  // number of cells wide (0 = 1)
	CellsY              uint32  // number of cells tall (0 = 1)
	CellSize            float32 // world units per cell (0 = default 8192)
	SpatialBucketSize   float32 // spatial hash bucket size (0 = CellSize/10)
	TickRate            int     // game loop tick rate (0 = 20)
	AoIRadius           float32 // area-of-interest radius (0 = 500)
	Headless            bool
	DynamicPartitioning *PartitionConfig // nil = disabled (default)
	ConnManager         *net.ConnManager
	Logger              *logger.Logger
	LogCategories       string                             // comma-separated categories/groups to enable (overrides default enabled list)
	DebugTopology       bool                               // send MeshState + CellTopology to clients (debug/visualization only)
	LoginHandler        LoginHandler                       // required: parses login messages, returns username
	LoginRejected       func(connID uint32, reason string) // optional: called on rejected login
	LoginTimeout        time.Duration                      // max time for login before disconnect (0 = 30s)

	// TestHosts distributes cells across N in-process Host instances when
	// non-empty. Each entry is a host ID. Cells are assigned to hosts via
	// round-robin at Build() time. Each multi-host Host gets its own
	// HostNetwork bound to an ephemeral port; peer addresses are exchanged
	// before cells start their game loops. Colocated mode (the default,
	// empty slice) creates a single "local" host with no HostNetwork.
	TestHosts []string

	// GatewayMode selects bridge behavior for colocated destinations in
	// multi-host mode. "local-shortcut" (default) uses the direct-channel
	// cellBridge path for cells on the same host. "always-proxy" forces
	// grpcBridge even for local destinations, exercising the gRPC
	// serialization path in tests.
	GatewayMode string
}

// ConsoleOpts provides game-specific console configuration.
// All fields are optional — omit what your game doesn't need.
type ConsoleOpts struct {
	Config          engine.Configurable    // enables "config list/get/set"
	ConfigSave      func() error           // enables "config save"
	ConfigReset     func()                 // enables "config reset"
	ConfigOnChanged func(field string)     // called on the game loop after "config set" mutates a field
	Entities        *engine.EntityOpts     // enables "entity summary/list/get/remove"
	Registry        *engine.EntityRegistry // enables "entity add"
}

// PlayerLocation tracks a player's current cell and whether the session is active
// or in a disconnected grace period. Single source of truth for username-based state.
type PlayerLocation struct {
	NodeID string
	Active bool // false = disconnected (grace period)
}

// Coordinator manages multiple Cell instances, routes connections, and coordinates transfers.
type Coordinator struct {
	Cells     map[string]*Cell
	CellOwner map[CellID]string // cell -> cellID
	Hosts     map[string]*Host  // hostID -> Host
	Topology  Topology

	ConnMgr *net.ConnManager
	Log     *logger.Logger

	console      *engine.Console // nil if headless
	cfg          Config
	netIDAlloc   *NetIDAllocator
	partState    *partitionState // nil if dynamic partitioning disabled
	debugOverlay bool            // true when debug console command is active

	systemDefs []engine.SystemDef
	built      bool

	worldFactory   func(base *WorldBase) GameWorld
	onInit         func(w *WorldBase)
	consoleOpts    *ConsoleOpts
	onConsoleReady func(c *engine.Console)

	// cellToHostMap maps each cell's string ID (e.g. "cell_0_0") to its
	// owning host ID (e.g. "host-a"). Populated during Build(); used by
	// grpcBridge's cellToHost resolver in multi-host mode. Guarded by mu.
	cellToHostMap map[string]string

	mu        sync.RWMutex
	players   map[string]*PlayerLocation // username -> location (active + disconnected)
	connIndex map[uint32]string          // connID -> nodeID

	loginSvc     *loginService
	playerRouter PlayerRouter
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
		Cells:         make(map[string]*Cell),
		CellOwner:     make(map[CellID]string),
		Hosts:         make(map[string]*Host),
		cellToHostMap: make(map[string]string),
		ConnMgr:       cfg.ConnManager,
		Log:           cfg.Logger,
		players:       make(map[string]*PlayerLocation),
		connIndex:     make(map[uint32]string),
		cfg:           cfg,
		debugOverlay:  cfg.DebugTopology,
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
	for cell, nodeID := range c.CellOwner {
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
	loc := c.players[username]
	if loc == nil {
		loc = &PlayerLocation{}
		c.players[username] = loc
	}
	loc.NodeID = nodeID
	loc.Active = true
	c.mu.Unlock()
}

// notifySessionDisconnected is called when a player disconnects (enters grace period).
// Thread-safe — called from node game loops.
func (c *Coordinator) notifySessionDisconnected(username, nodeID string) {
	c.mu.Lock()
	loc := c.players[username]
	if loc == nil {
		loc = &PlayerLocation{}
		c.players[username] = loc
	}
	loc.NodeID = nodeID
	loc.Active = false
	c.mu.Unlock()
}

// notifySessionRemoved is called when a player session is fully removed from a node.
// Thread-safe — called from node game loops.
func (c *Coordinator) notifySessionRemoved(username string) {
	c.mu.Lock()
	delete(c.players, username)
	c.mu.Unlock()
}

// ActiveUserNode returns the nodeID for an active username, or "" if offline.
func (c *Coordinator) ActiveUserNode(username string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if loc := c.players[username]; loc != nil && loc.Active {
		return loc.NodeID
	}
	return ""
}

// ActiveUsers returns a snapshot of active usernames and their node IDs.
func (c *Coordinator) ActiveUsers() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]string)
	for username, loc := range c.players {
		if loc.Active {
			result[username] = loc.NodeID
		}
	}
	return result
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
		if cfg.DynamicPartitioning.OnTopologyChanged == nil {
			cfg.DynamicPartitioning.OnTopologyChanged = func() {
				c.BroadcastCellTopology()
			}
		}
		c.partState = newPartitionState()
	}

	// Initialize login service if LoginHandler is provided.
	if cfg.LoginHandler != nil {
		c.loginSvc = newLoginService(cfg.LoginHandler, cfg.LoginTimeout)
		c.loginSvc.onRejected = cfg.LoginRejected
	}

	// Build the host roster. Single-host colocated mode (default) creates
	// one "local" Host with no HostNetwork. Multi-host test mode creates
	// one Host per entry in cfg.TestHosts and boots a HostNetwork on each.
	hostIDs := cfg.TestHosts
	if len(hostIDs) == 0 {
		hostIDs = []string{"local"}
	}
	multiHost := len(hostIDs) > 1

	hosts := make([]*Host, 0, len(hostIDs))
	for _, hid := range hostIDs {
		h := NewHost(hid)
		h.Log = c.Log
		c.Hosts[hid] = h
		hosts = append(hosts, h)
		if multiHost {
			hn, err := NewHostNetwork(h, ":0", c.Log)
			if err != nil {
				panic(fmt.Errorf("coordinator: NewHostNetwork for %q: %w", hid, err))
			}
			h.Network = hn
		}
	}

	// Create grid of cells. createNode returns the systems slice so we can
	// defer Init() until after World.Init(). Cells are round-robin assigned
	// across the host roster and their cellToHost mapping is recorded for
	// grpcBridge's routing decisions.
	type nodeSetup struct {
		cell    *Cell
		systems []engine.System
	}
	var cells []CellID
	var setups []nodeSetup
	hostIdx := 0
	for sy := uint32(0); sy < cfg.CellsY; sy++ {
		for sx := uint32(0); sx < cfg.CellsX; sx++ {
			cell := CellID{X: int32(sx), Y: int32(sy)}
			cells = append(cells, cell)
			cell2, systems := c.createNode(cell, spatialCellSize)
			targetHost := hosts[hostIdx%len(hosts)]
			targetHost.AddCell(cell2.Cell, cell2)
			c.cellToHostMap[cell2.ID] = targetHost.ID
			hostIdx++
			setups = append(setups, nodeSetup{cell2, systems})
		}
	}

	// Compute topology and wire neighbors
	c.Topology = ComputeTopology(cells, coords.CellSize)
	for cell, neighborCells := range c.Topology.Neighbors {
		nodeID := c.CellOwner[cell]
		node := c.Cells[nodeID]
		for _, nc := range neighborCells {
			neighborID := c.CellOwner[nc]
			node.Neighbors[neighborID] = c.Cells[neighborID]
		}
	}

	// In multi-host mode, wrap each cell's cellBridge with a grpcBridge so
	// cross-host dispatch encodes through the gRPC codec + HostNetwork.
	// Single-host mode keeps the plain cellBridge — zero gRPC overhead.
	if multiHost {
		cellToHostFn := func(destCellID string) string {
			c.mu.RLock()
			defer c.mu.RUnlock()
			return c.cellToHostMap[destCellID]
		}
		for _, s := range setups {
			hostID := c.cellToHostMap[s.cell.ID]
			host := c.Hosts[hostID]
			localBridge, ok := s.cell.Bridge.(*cellBridge)
			if !ok {
				panic(fmt.Errorf("coordinator: cell %q has unexpected bridge type %T", s.cell.ID, s.cell.Bridge))
			}
			gb := newGrpcBridge(s.cell, c, host, cellToHostFn, localBridge, cfg.GatewayMode)
			s.cell.Bridge = gb
			s.cell.World.SetBridge(gb)
		}
	}

	// Cross-connect every Host's HostNetwork to every other Host so all
	// peer pairs have an open bidi MeshData stream before cells start
	// their game loops. In S4 this handshake will be replaced with
	// coordinator-driven PeerList broadcasts.
	if multiHost {
		for _, h := range hosts {
			for _, peer := range hosts {
				if peer.ID == h.ID {
					continue
				}
				if err := h.Network.ConnectPeer(peer.ID, peer.Network.Addr()); err != nil {
					panic(fmt.Errorf("coordinator: ConnectPeer %s->%s: %w", h.ID, peer.ID, err))
				}
			}
		}
		// Wait for every host to see all of its peers. This is a test
		// barrier — in S4 the rendezvous settle window replaces it.
		expectedPerHost := make([]string, 0, len(hosts)-1)
		for _, h := range hosts {
			expectedPerHost = expectedPerHost[:0]
			for _, peer := range hosts {
				if peer.ID != h.ID {
					expectedPerHost = append(expectedPerHost, peer.ID)
				}
			}
			if err := h.Network.WaitPeersReady(expectedPerHost, 2*time.Second); err != nil {
				panic(fmt.Errorf("coordinator: host %s peers not ready: %w", h.ID, err))
			}
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

	c.Log.Log(CatMeshCell, "coordinator: created %d nodes, topology computed", len(c.Cells))

	// Two-phase init: World.Init() first (registers entity kinds, login handlers),
	// then system Init() (discovers replicators, creates query filters).
	for _, s := range setups {
		s.cell.World.Init()
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

// createNode creates a single Cell for the given cell, including its ECS world,
// game systems, game loop, and metrics. The cell is registered in c.Cells and
// c.CellOwner but NOT started — call cell.Run(ctx) separately.
// System Init() is NOT called — the caller must call initSystems() after
// World.Init() so systems can discover entity kinds and other world state.
func (c *Coordinator) createNode(cell CellID, spatialBucketSize float32, fromSplit ...bool) (*Cell, []engine.System) {
	cfg := c.cfg
	platformCfg := engine.Config{TickRate: cfg.TickRate}

	id := MeshCellID(cell)
	eng := engine.New(platformCfg, cfg.ConnManager, cfg.Logger)
	eng.SetNetIDBase(c.netIDAlloc.Allocate())

	events := make(chan net.PlayerEvent, 64)

	base := NewWorldBase(eng, cell, cfg.AoIRadius, nil)
	base.spatialGrid = spatial.NewHashGrid(spatialBucketSize)
	if len(fromSplit) > 0 && fromSplit[0] {
		base.fromSplit = true
	}

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

	node := &Cell{
		ID:        id,
		Cell:      cell,
		Engine:    eng,
		World:     world,
		Base:      base,
		Inbox:     make(chan CellMessage, 256),
		Events:    events,
		Neighbors: make(map[string]*Cell),
		Log:       cfg.Logger,
	}

	// Wire session callbacks using node pointer — reads node.ID at call time
	// so renames during merge are reflected correctly.
	eng.Players.SetSessionCallbacks(
		func(username string) { c.notifySessionActive(username, node.ID) },
		func(username string) { c.notifySessionDisconnected(username, node.ID) },
		func(username string) { c.notifySessionRemoved(username) },
	)

	gameHooks := world.Hooks()
	mergedHooks := engine.Hooks{
		OnConnect:    gameHooks.OnConnect,
		OnDisconnect: gameHooks.OnDisconnect,
		PreFlush:     gameHooks.PreFlush,
		PostFlush:    gameHooks.PostFlush,
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

	nm := metrics.NewCellMetrics(id, platformCfg.TickRate, tickStatsFn, networkStatsFn)
	node.Metrics = nm
	eng.Metrics = nm

	// Wire bridge
	bridge := &cellBridge{cell: node, coord: c} // node var is *Cell
	node.Bridge = bridge
	node.World.SetBridge(bridge)

	// Callers during Build() don't need locking (single-threaded).
	// Callers during runtime (SplitCell) must hold c.mu write lock.
	c.Cells[id] = node
	c.CellOwner[cell] = id

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

	for _, node := range c.Cells {
		go node.Run(ctx)
	}
	c.Log.Log(CatMeshCell, "coordinator: all %d nodes started", len(c.Cells))

	// Start partition monitor if dynamic partitioning is enabled.
	if c.cfg.DynamicPartitioning != nil {
		monitor := newPartitionMonitor(c, c.cfg.DynamicPartitioning)
		go monitor.run(ctx)
		c.Log.Log(CatMeshCell, "coordinator: partition monitor started (eval every %s)", c.cfg.DynamicPartitioning.EvalInterval)
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
	c.console = engine.NewConsole(c.Log)

	// Set exec func to proxy to the first node's game loop
	for _, node := range c.Cells {
		eng := node.Engine
		c.console.SetExecFunc(func(fn func() string) string {
			result := make(chan string, 1)
			eng.PendingAdminCmds <- func() {
				result <- fn()
			}
			select {
			case r := <-result:
				return r
			case <-time.After(5 * time.Second):
				return "  game loop not responding (timeout)\n"
			}
		})
		break
	}

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
		builtinOpts.ConfigOnChanged = co.ConfigOnChanged
		builtinOpts.Entities = co.Entities
		builtinOpts.Registry = co.Registry
	}

	// Auto-wire default entity commands if game didn't provide its own.
	if builtinOpts.Entities == nil {
		for _, node := range c.Cells {
			builtinOpts.Entities = c.defaultEntityOpts(node)
			break
		}
	}

	c.console.RegisterBuiltins(builtinOpts)

	// Register perf/load commands on coordinator level
	c.registerPerfCommands(c.console)

	// Register cell commands if dynamic partitioning is enabled.
	if c.cfg.DynamicPartitioning != nil {
		c.registerCellCommands(c.console)
	}

	// Register debug toggle if DebugTopology is enabled.
	if c.cfg.DebugTopology {
		c.registerDebugCommand(c.console)
	}

	// Let game register custom commands.
	onReady := c.onConsoleReady
	if onReady != nil {
		onReady(c.console)
	}

	c.console.Run(ctx)
}

// registerDebugCommand registers the debug toggle command.
func (c *Coordinator) registerDebugCommand(console *engine.Console) {
	console.Register(engine.Command{
		Name: "debug", Aliases: []string{"dbg"},
		Category: "debug", Usage: "debug", Description: "toggle debug overlay on all clients (cell topology)",
		Fn: func(args []string) {
			newVal := !c.debugOverlay
			c.debugOverlay = newVal
			if newVal {
				c.BroadcastCellTopology()
				console.Printf("  debug overlay: ON\n")
			} else {
				c.BroadcastClearTopology()
				console.Printf("  debug overlay: OFF\n")
			}
		},
	})
}

// registerPerfCommands registers perf and load as coordinator-level commands.
func (c *Coordinator) registerPerfCommands(console *engine.Console) {
	var defaultEng *engine.Engine
	for _, node := range c.Cells {
		defaultEng = node.Engine
		break
	}
	if defaultEng == nil {
		return
	}

	console.Register(engine.Command{
		Name: "perf", Aliases: []string{"p"},
		Category: "perf", Usage: "perf [reset]", Description: "show tick timing, entities, network, load",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return []string{"reset"}
			}
			return nil
		},
		Fn: func(args []string) {
			if len(args) > 0 && args[0] == "reset" {
				output := console.ExecOnGameLoop(func() string {
					defaultEng.Perf.Reset()
					return "  perf counters reset\n"
				})
				fmt.Print(output)
				return
			}
			output := console.ExecOnGameLoop(func() string { return engine.FormatPerfOutput(defaultEng) })
			fmt.Print(output)
		},
	})

	console.Register(engine.Command{
		Name:     "load",
		Category: "perf", Usage: "load", Description: "show composite load score",
		Fn: func(args []string) {
			output := console.ExecOnGameLoop(func() string {
				if defaultEng.Metrics == nil {
					return "  metrics not wired\n"
				}
				snap := defaultEng.Metrics.Snapshot()
				tickBudget := time.Duration(1000/defaultEng.Config.TickRate) * time.Millisecond
				return fmt.Sprintf("  load: %.2f (tick=%.1f%% entity=%.1f%%)\n",
					snap.CompositeLoad,
					float64(snap.Tick.AvgDuration)/float64(tickBudget)*100,
					float64(snap.Entities.Real)/1000.0*100,
				)
			})
			fmt.Print(output)
		},
	})
}

// buildNodeRefs creates NodeRef entries from the coordinator's node map.
func (c *Coordinator) buildNodeRefs() []engine.NodeRef {
	refs := make([]engine.NodeRef, 0, len(c.Cells))
	for _, node := range c.Cells {
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
func (c *Coordinator) defaultEntityOpts(node *Cell) *engine.EntityOpts {
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
	for _, node := range c.Cells {
		node.Shutdown()
	}
	// Tear down each host's MeshData gRPC server (if any) concurrently.
	// Sequential shutdown causes host-A's GracefulStop to wait ~5s for
	// host-B's server-side Recv to drain (host-B is still alive), then
	// host-B waits the same for the already-dead host-A. Running both
	// in parallel lets their peer-stream cancellations race each other
	// to completion.
	var hnWG sync.WaitGroup
	for _, h := range c.Hosts {
		if h.Network == nil {
			continue
		}
		hnWG.Add(1)
		go func(h *Host) {
			defer hnWG.Done()
			if err := h.Network.Shutdown(); err != nil {
				c.Log.Log(CatMeshCell, "coordinator: host %s network shutdown: %v", h.ID, err)
			}
		}(h)
	}
	hnWG.Wait()
	c.Log.Log(CatMeshCell, "coordinator: all nodes shut down")
}

// sendServerConfig sends the server configuration (tick rate) to a newly connected client.
func (c *Coordinator) sendServerConfig(connID uint32) {
	msg := &enginepb.ServerConfigMsg{
		TickRate: uint32(c.cfg.TickRate),
	}
	inner, err := proto.Marshal(msg)
	if err != nil {
		return
	}
	evt := &enginepb.ServerEvent{
		Code: uint32(enginepb.ServerEventCode_SE_SERVER_CONFIG),
		Data: inner,
	}
	evtData, err := proto.Marshal(evt)
	if err != nil {
		return
	}
	frame := make([]byte, 1+len(evtData))
	frame[0] = 0x00 // event channel
	copy(frame[1:], evtData)
	c.ConnMgr.SendReliable(connID, frame)
}

// routeEvents drains ConnManager.Events() and processes logins.
// New connections are buffered in the login service. Authenticated players
// are routed to the appropriate node via the PlayerRouter.
func (c *Coordinator) routeEvents(ctx context.Context) {
	events := c.ConnMgr.Events()

	// Login processing ticker — same rate as game loop
	tickInterval := time.Duration(1000/c.cfg.TickRate) * time.Millisecond
	loginTicker := time.NewTicker(tickInterval)
	defer loginTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case evt := <-events:
			if evt.Connected {
				if c.loginSvc != nil {
					c.loginSvc.addPending(evt.ConnID)
					c.sendServerConfig(evt.ConnID)
					c.Log.Log(CatNetConn, "coordinator: conn %d pending login", evt.ConnID)
				} else {
					c.Log.Log(CatNetConn, "coordinator: conn %d but no login handler configured", evt.ConnID)
				}
			} else {
				// Disconnect: route to the node that owns this player
				nodeID := c.getPlayerNode(evt.ConnID)
				if nodeID != "" {
					if node, ok := c.getCell(nodeID); ok {
						node.Events <- evt
					}
					c.removePlayerNode(evt.ConnID)
				} else {
					// Player was still in pending login — just remove
					if c.loginSvc != nil {
						c.loginSvc.removePending(evt.ConnID)
					}
				}
			}

		case <-loginTicker.C:
			c.processLogins()
		}
	}
}

// processLogins processes all pending login attempts on the coordinator goroutine.
func (c *Coordinator) processLogins() {
	if c.loginSvc == nil {
		return
	}

	results, timedOut := c.loginSvc.processLogins(c.ConnMgr)

	// Disconnect timed-out connections
	for _, connID := range timedOut {
		c.Log.Log(CatNetConn, "coordinator: login timeout conn=%d", connID)
		c.ConnMgr.Remove(connID)
	}

	for _, r := range results {
		c.routeAuthenticatedPlayer(r.connID, r.username, r.data)
	}
}

// routeAuthenticatedPlayer routes a successfully authenticated player to the correct node.
func (c *Coordinator) routeAuthenticatedPlayer(connID uint32, username string, data any) {
	// 1. Check for reconnection (lingering disconnected session)
	var reconnectNodeID, existingNodeID string
	c.mu.RLock()
	if loc := c.players[username]; loc != nil {
		if loc.Active {
			existingNodeID = loc.NodeID
		} else {
			reconnectNodeID = loc.NodeID
		}
	}
	c.mu.RUnlock()

	if existingNodeID != "" {
		// Duplicate username — reject
		c.Log.Log(CatNetConn, "coordinator: duplicate username %q conn=%d (active on %s)", username, connID, existingNodeID)
		if c.loginSvc.onRejected != nil {
			c.loginSvc.onRejected(connID, "Username already connected")
		}
		c.ConnMgr.Remove(connID)
		return
	}

	if reconnectNodeID != "" {
		// Reconnect to the node with the lingering session
		if node, ok := c.getCell(reconnectNodeID); ok {
			c.setPlayerNode(connID, reconnectNodeID)
			node.Inbox <- CellMessage{
				Type: MsgPlayerAssignment,
				Assignment: &PlayerAssignment{
					ConnID:      connID,
					Username:    username,
					IsReconnect: true,
				},
			}
			c.Log.Log(CatNetConn, "coordinator: reconnect conn=%d user=%s -> %s", connID, username, reconnectNodeID)
			return
		}
		// Node gone (e.g., merged) — fall through to fresh login
	}

	// 2. Route via PlayerRouter
	var targetNodeID string
	if c.playerRouter != nil {
		targetNodeID = c.playerRouter(username)
	}
	if targetNodeID == "" {
		// Fallback: pick any node
		for id := range c.Cells {
			targetNodeID = id
			break
		}
	}

	node, ok := c.getCell(targetNodeID)
	if !ok {
		c.Log.Log(CatNetConn, "coordinator: no node %s for conn=%d user=%s", targetNodeID, connID, username)
		c.ConnMgr.Remove(connID)
		return
	}

	c.setPlayerNode(connID, targetNodeID)
	node.Inbox <- CellMessage{
		Type: MsgPlayerAssignment,
		Assignment: &PlayerAssignment{
			ConnID:   connID,
			Username: username,
			Data:     data,
		},
	}
	c.Log.Log(CatNetConn, "coordinator: conn=%d user=%s -> %s", connID, username, targetNodeID)
}

// GridWidth returns the number of cells wide in the mesh grid.
func (c *Coordinator) GridWidth() uint32 { return c.cfg.CellsX }

// DebugTopology returns whether debug topology info is sent to clients.
// Used by mmokit.BuildReplicators to conditionally include MeshState bindings.
func (c *Coordinator) DebugTopology() bool { return c.cfg.DebugTopology }

func (c *Coordinator) getPlayerNode(connID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connIndex[connID]
}

func (c *Coordinator) setPlayerNode(connID uint32, nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connIndex[connID] = nodeID
}

func (c *Coordinator) removePlayerNode(connID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.connIndex, connID)
}

// getCell returns a cell by ID under read lock.
func (c *Coordinator) getCell(cellID string) (*Cell, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n, ok := c.Cells[cellID]
	return n, ok
}

// getCellOwner returns the owning cell ID for a cell under read lock.
func (c *Coordinator) getCellOwner(cell CellID) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.CellOwner[cell]
}

// activeCells returns all active cell IDs and their owning node IDs.
func (c *Coordinator) activeCells() map[CellID]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[CellID]string, len(c.CellOwner))
	maps.Copy(result, c.CellOwner)
	return result
}

// SendCellTopology sends the current cell topology to a specific client.
// Only sends if debug overlay is active.
func (c *Coordinator) SendCellTopology(connID uint32) {
	if !c.debugOverlay {
		return
	}
	frame := c.buildCellTopologyFrame()
	c.cfg.ConnManager.SendReliable(connID, frame)
}

// BroadcastCellTopology sends the current cell topology to all connected clients.
// Only sends if debug overlay is active.
func (c *Coordinator) BroadcastCellTopology() {
	if !c.debugOverlay {
		return
	}
	frame := c.buildCellTopologyFrame()
	for _, connID := range c.cfg.ConnManager.ActiveConnIDs() {
		c.cfg.ConnManager.SendReliable(connID, frame)
	}
}

// BroadcastClearTopology sends an empty topology to all clients, clearing overlays.
func (c *Coordinator) BroadcastClearTopology() {
	frame := makeEventFrame(uint32(enginepb.ServerEventCode_SE_CELL_TOPOLOGY), &enginepb.CellTopologyMsg{})
	for _, connID := range c.cfg.ConnManager.ActiveConnIDs() {
		c.cfg.ConnManager.SendReliable(connID, frame)
	}
}

func (c *Coordinator) buildCellTopologyFrame() []byte {
	cells := c.activeCells()
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

// nodeLoad returns the current load snapshot for a node.
// Used by dynamic partitioning (split/merge) for rebalancing decisions.
func (c *Coordinator) nodeLoad(nodeID string) (metrics.LoadSnapshot, bool) {
	c.mu.RLock()
	node, ok := c.Cells[nodeID]
	c.mu.RUnlock()
	if !ok || node.Metrics == nil {
		return metrics.LoadSnapshot{}, false
	}
	return node.Metrics.Snapshot(), true
}

// allNodeLoads returns load snapshots for all nodes. Used by MetricsHandler.
func (c *Coordinator) allNodeLoads() map[string]metrics.LoadSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]metrics.LoadSnapshot, len(c.Cells))
	for id, node := range c.Cells {
		if node.Metrics != nil {
			result[id] = node.Metrics.Snapshot()
		}
	}
	return result
}

// MetricsHandler returns an HTTP handler that serves Prometheus-compatible
// metrics for all nodes. Mount on your HTTP mux: mux.Handle("/metrics", coord.MetricsHandler())
func (c *Coordinator) MetricsHandler() http.HandlerFunc {
	return metrics.Handler(c.allNodeLoads)
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
