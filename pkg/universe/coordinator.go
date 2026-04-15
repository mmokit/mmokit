package universe

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	stdnet "net"
	"strconv"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"encoding/json"
	"reflect"

	"github.com/mlange-42/ark/ecs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/cmdsys"
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

	// GatewayMode selects dispatch behavior for colocated destinations in
	// multi-host mode. "local-shortcut" (default) uses the direct-channel
	// cellBridge path for cells on the same host. "always-proxy" forces
	// grpcBridge even for local destinations, exercising the gRPC
	// serialization path in tests.
	//
	// This flag applies in two places:
	//   1. grpcBridge cell-to-cell dispatch (inter-cell messages).
	//   2. Gateway client-traffic dispatch: Gateway.isLocalShortcut returns
	//      false when "always-proxy", routing client input through the
	//      MeshData codec path even for colocated target hosts.
	GatewayMode string

	// Mode is a comma-separated role set that selects what this process does.
	// Accepts role names: coordinator, host, gateway, node.
	// Preset aliases: "" or "all" → "coordinator,host,gateway" (default).
	//
	// Common combinations:
	//   - "" / "all"                  → coordinator + host + gateway (single-process dev)
	//   - "coordinator"               → control plane only (MeshControl, HostRegistry, admin console)
	//   - "coordinator,gateway"       → control plane + embedded WebSocket gateway
	//   - "coordinator,host"          → control plane + in-process cells, no gateway
	//   - "coordinator,host,gateway"  → explicit spelling of the default `all` preset
	//   - "node"                      → dials CoordinatorAddr, receives cells dynamically
	//   - "gateway"                   → standalone gateway, dials CoordinatorAddr
	//
	// Rules: node cannot combine with anything; host requires coordinator;
	// gateway can stand alone or pair with coordinator.
	Mode string

	// ControlListen is the listen address for the MeshControl gRPC server.
	// Only used when Mode == "coordinator". Default ":9100".
	ControlListen string

	// CoordinatorAddr is the MeshControl server address to dial.
	// Only used when Mode == "node". No default — required in node mode.
	CoordinatorAddr string

	// HostID is a stable host identifier used when Mode == "node". Empty
	// means auto-generate a unique ID at Build() time. Set by tests to
	// get deterministic host IDs.
	HostID string

	// PostgresURL is the connection string for player, marketplace, and
	// config persistence. Format:
	//   postgres://user:pass@host:port/dbname?sslmode=disable
	// Empty means use the local docker-compose default
	// (postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable). Callers
	// that open the PostgresStore themselves via mmokit.OpenPostgres
	// can ignore this field.
	PostgresURL string

	// GatewayID is the stable identifier used when the gateway role runs in
	// the same process as the coordinator. Defaults to InprocGatewayID
	// ("inproc"). Only relevant when RoleGateway is in the role set alongside
	// RoleCoordinator (the default `all` preset or `--mode=coordinator,gateway`).
	GatewayID string

	// HTTPPort is the listen port for the engine-owned client HTTP server
	// (WebSocket /ws, /metrics, and static assets). Bound by --port. The
	// listener is only started on processes with the Gateway role. Set to
	// -1 to disable the engine HTTP listener regardless of role (used by
	// in-process integration tests that share the default port).
	// Default: 8080.
	HTTPPort int

	// WebDir selects the static-asset source for the engine HTTP server:
	//   "embed"     → serve from Config.StaticFS (sub by StaticFSPrefix if set)
	//   ""          → no static serving
	//   "disabled"  → no static serving
	//   <fs-path>   → http.FileServer(http.Dir(path)) for dev iteration
	// Bound by --web-dir. Default: "embed".
	WebDir string

	// StaticFS is the embedded web-asset filesystem the engine serves when
	// WebDir == "embed". Games typically pass the raw //go:embed FS; set
	// StaticFSPrefix to the subdirectory inside that FS (e.g. "web/dist")
	// and the engine calls fs.Sub for you. Nil is tolerated: the engine
	// logs a warning and skips static serving.
	StaticFS fs.FS

	// StaticFSPrefix is the optional sub-path inside StaticFS. When non-empty
	// the engine calls fs.Sub(StaticFS, StaticFSPrefix) before mounting the
	// file server. Example: "web/dist".
	StaticFSPrefix string

	// HTTPRoutes is an optional hook invoked after the engine mounts its
	// default handlers (/ws, /metrics, static) so games can add custom
	// routes or override defaults. Runs last so game routes win.
	HTTPRoutes func(mux *http.ServeMux)
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

	systemDefs []engine.SystemDef
	built      bool
	roles      Roles // parsed from cfg.Mode at Build() time

	// coordEpoch is a fencing token that monotonically increases on every
	// coordinator restart. Every CoordMessage sent to a registered node
	// carries the current epoch; nodes track the highest-seen value and
	// reject anything lower. Prevents stale state from a restarted
	// coordinator from clobbering a fresh assignment run. Initialized
	// from time.Now().UnixNano() at NewCoordinator time so it
	// monotonically advances across restarts without persistence.
	coordEpoch uint64

	worldFactory   func(base *WorldBase) GameWorld
	onInit         func(w *WorldBase)
	consoleOpts    *ConsoleOpts
	onConsoleReady func(c *engine.Console)

	// cellToHostMap maps each cell's string ID (e.g. "cell_0_0") to its
	// owning host ID (e.g. "host-a"). Populated during Build(); used by
	// grpcBridge's cellToHost resolver in multi-host mode. Guarded by mu.
	cellToHostMap map[string]string

	mu            sync.RWMutex
	players       map[string]*PlayerLocation // username -> location (active + disconnected)
	sessionRoutes *sessionRoutes             // connID -> cell routing; own mu, separate from c.mu

	loginSvc     *loginService
	playerRouter PlayerRouter

	gateway *Gateway // non-nil when in-process gateway is enabled (default in `all` preset/coordinator modes)

	controlServer          *meshControlServer
	controlGrpcServer      *grpc.Server
	controlListener        stdnet.Listener
	hostRegistry           *HostRegistry
	gatewayRegistry        *GatewayRegistry
	assignmentEngine       *assignmentEngine
	assignmentEngineCancel context.CancelFunc

	controlClient *meshControlClient

	// orchestrator drives S7 cell-transfer state machines (split, merge,
	// live migrate). Constructed here; wired into SplitCell / MergeCell
	// and into the real CellTransferReady path in T4+.
	orchestrator *cellTransferOrchestrator

	// hostExecutors maps hostID -> cellTransferExecutor for every local
	// Host. Populated in Build() after hosts are created. Used by the real
	// cellTransferDispatcherImpl and by remote-source routing to deliver
	// commands directly into an in-process host without a wire hop.
	hostExecutors map[string]*cellTransferExecutor

	// vcm is the VirtualConnManager used in node mode. It is constructed in
	// Build() for "node" mode and passed as the engine's ConnSender to every
	// cell created via assignCellOnNode. Nil in `all` preset and coordinator modes.
	vcm *VirtualConnManager

	// httpServer is the engine-owned client HTTP server. Non-nil when the
	// process has the Gateway role and HTTPPort != -1. Started from Start()
	// after Build(); shut down at the top of Shutdown() before cells drain.
	httpServer *http.Server

	// C3: cross-process command dispatch.
	// registry and dispatcher are constructed in NewCoordinator so they are
	// available even in headless / pure-node / pure-gateway processes that
	// never create a console.
	registry   *cmdsys.Registry
	dispatcher *cmdsys.Dispatcher
	transport  *meshControlTransport
	resolver   *meshRouteResolver
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
	if cfg.DynamicPartitioning == nil {
		cfg.DynamicPartitioning = DefaultPartitionConfig()
	}
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = 8080
	}
	if cfg.WebDir == "" {
		cfg.WebDir = "embed"
	}

	if cfg.CellSize > 0 {
		coords.SetCellSize(cfg.CellSize)
	}

	c := &Coordinator{
		Cells:         make(map[string]*Cell),
		CellOwner:     make(map[CellID]string),
		Hosts:         make(map[string]*Host),
		hostExecutors: make(map[string]*cellTransferExecutor),
		cellToHostMap: make(map[string]string),
		ConnMgr:       cfg.ConnManager,
		Log:           cfg.Logger,
		players:       make(map[string]*PlayerLocation),
		sessionRoutes: newSessionRoutes(),
		cfg:           cfg,
		coordEpoch:    uint64(time.Now().UnixNano()),
	}
	c.orchestrator = newCellTransferOrchestrator(c)
	// Install the real dispatcher so production Begin* paths can ship
	// commands. Unit tests that want a fake dispatcher replace this via
	// orchestrator.setDispatcher in their own setup.
	c.orchestrator.setDispatcher(newCellTransferDispatcher(c))

	// C3: create the command registry and transport; the full dispatcher is
	// wired after Build() so the resolver has a fully-built coordinator to
	// query. A stub resolver is used during construction so InvokeLocal works
	// even before Build() (e.g., in tests that call NewCoordinator directly).
	c.registry = cmdsys.NewRegistry()
	c.transport = newMeshControlTransport(c)
	c.resolver = newMeshRouteResolver(c)
	c.dispatcher = cmdsys.NewDispatcher(cmdsys.DispatcherConfig{
		Registry:  c.registry,
		Resolver:  c.resolver,
		Transport: c.transport,
		Audit:     cmdsys.NoopAuditSink{},
	})

	return c
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

// Registry returns the cmdsys.Registry owned by this coordinator.
// Games and tests register commands here; the same registry backs the console
// and cross-process dispatch.
func (c *Coordinator) CmdRegistry() *cmdsys.Registry {
	return c.registry
}

// CmdDispatcher returns the cmdsys.Dispatcher owned by this coordinator.
func (c *Coordinator) CmdDispatcher() *cmdsys.Dispatcher {
	return c.dispatcher
}

// RegisterCommand registers a command with the coordinator's cmdsys registry.
// Convenience wrapper around CmdRegistry().Register().
func (c *Coordinator) RegisterCommand(cmd cmdsys.Command) error {
	return c.registry.Register(cmd)
}

// InvokeCmd invokes a command through the coordinator's dispatcher, using the
// args-aware resolver for context-sensitive routes (RoutePlayerOwner,
// RouteEntityOwner, RouteSpecificHost, RouteSpecificCell). For static routes
// (RouteLocal, RouteAllHosts, etc.) it behaves identically to
// CmdDispatcher().Invoke.
func (c *Coordinator) InvokeCmd(ctx context.Context, caller cmdsys.Caller, verb string, args any) (cmdsys.Result, error) {
	cmd, ok := c.registry.Lookup(verb)
	if !ok {
		return cmdsys.Result{}, cmdsys.ErrUnknownVerb
	}

	// For routes that require inspecting the args struct to determine the
	// target host, resolve targets here and inject them via a single-target
	// override path through the standard Invoke machinery.
	//
	// We do this by registering the resolved target ID into args for
	// RouteSpecificHost/RouteSpecificCell/RoutePlayerOwner/RouteEntityOwner
	// via a short-circuit path: resolve targets, then run a bespoke loop
	// mirroring Dispatcher.Invoke's remote path using the transport directly.
	switch cmd.Route {
	case cmdsys.RoutePlayerOwner, cmdsys.RouteEntityOwner, cmdsys.RouteSpecificHost, cmdsys.RouteSpecificCell:
		targets, err := c.resolver.ResolveWithArgs(cmd.Route, args)
		if err != nil {
			return cmdsys.Result{}, err
		}
		// Use the dispatcher but inject the already-resolved target. We do
		// this by temporarily wrapping args with the HostID field so
		// RouteSpecificHost's standard path picks it up — but that requires
		// mutating args. Simpler: call the transport directly for each target
		// and build a Result ourselves, mirroring what Dispatcher.Invoke does
		// for the remote path.
		return c.invokeCmdTargets(ctx, caller, verb, args, cmd, targets)

	default:
		return c.dispatcher.Invoke(ctx, caller, verb, args)
	}
}

// invokeCmdTargets runs command dispatch to a pre-resolved set of targets.
// Mirrors Dispatcher.Invoke's remote-dispatch branch but with targets already
// resolved by the args-aware resolver.
func (c *Coordinator) invokeCmdTargets(ctx context.Context, caller cmdsys.Caller, verb string, args any, cmd cmdsys.Command, targets []cmdsys.Target) (cmdsys.Result, error) {
	if _, ok := ctx.Deadline(); !ok {
		return cmdsys.Result{}, cmdsys.ErrNoDeadline
	}

	argsJSONBytes, merr := json.Marshal(args)
	if merr != nil {
		return cmdsys.Result{}, merr
	}

	deadline, _ := ctx.Deadline()
	var perTarget []cmdsys.TargetResult
	var firstErr error

	for _, target := range targets {
		tr := cmdsys.TargetResult{TargetID: target.ID}

		if c.transport.isColocated(target.ID) {
			resultJSON, _, herr := c.dispatcher.InvokeLocal(ctx, caller, verb, argsJSONBytes)
			if herr != nil {
				tr.OK = false
				tr.Error = herr.Error()
				if firstErr == nil {
					firstErr = herr
				}
			} else {
				tr.OK = true
				tr.Result = unmarshalResult(cmd.Result, resultJSON)
			}
		} else {
			req := &cmdsys.RemoteRequest{
				Verb:              verb,
				ArgsJSON:          argsJSONBytes,
				Caller:            caller,
				DeadlineUnixNanos: deadline.UnixNano(),
				SchemaVersion:     cmd.ArgsSchemaHash,
			}
			respCh, serr := c.transport.Send(ctx, target, req)
			if serr != nil {
				tr.OK = false
				tr.Error = serr.Error()
				if firstErr == nil {
					firstErr = serr
				}
				perTarget = append(perTarget, tr)
				continue
			}
			var resp *cmdsys.RemoteResponse
			select {
			case resp = <-respCh:
			case <-ctx.Done():
				tr.OK = false
				tr.Error = ctx.Err().Error()
				if firstErr == nil {
					firstErr = ctx.Err()
				}
				perTarget = append(perTarget, tr)
				continue
			}
			if resp == nil || !resp.OK {
				tr.OK = false
				errStr := "nil response"
				if resp != nil {
					errStr = resp.Error
				}
				tr.Error = errStr
				if firstErr == nil {
					firstErr = errors.New(errStr)
				}
			} else {
				tr.OK = true
				tr.Result = unmarshalResult(cmd.Result, resp.ResultJSON)
			}
		}
		perTarget = append(perTarget, tr)
	}

	return cmdsys.Result{Verb: verb, Caller: caller, PerTarget: perTarget}, firstErr
}

// unmarshalResult JSON-decodes resultJSON into a new value of the same type as
// proto (the command's Result zero value). Returns nil if proto is nil or
// decoding fails.
func unmarshalResult(proto any, resultJSON []byte) any {
	if proto == nil || len(resultJSON) == 0 {
		return nil
	}
	rt := reflect.TypeOf(proto)
	if rt.Kind() == reflect.Ptr {
		rt = rt.Elem()
	}
	rp := reflect.New(rt)
	if err := json.Unmarshal(resultJSON, rp.Interface()); err != nil {
		return nil
	}
	return rp.Elem().Interface()
}

// EntityHostForNetID returns the host ID owning the entity with the given
// network ID. Walks all local cells in all local hosts. For multi-process
// clusters this only resolves entities on the same process; remote entities
// require a broadcast-query RPC (deferred post-C3).
// Returns "" if the entity is not found on any local cell.
func (c *Coordinator) EntityHostForNetID(netID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for hostID, host := range c.Hosts {
		for _, cell := range host.Cells {
			if cell.Base == nil {
				continue
			}
			// Check the per-tick NetID map rebuilt by SpatialSystem.
			// The map is rebuilt each tick so accessing it from outside the
			// game loop is technically racy; however, EntityHostForNetID is
			// only called from console commands (which run via ExecOnGameLoop)
			// or from the route resolver (which is called during Invoke, also
			// typically from a game-loop-proxied context). For cross-process
			// tests where cells are run via goroutines, the map may lag by one
			// tick — acceptable for routing purposes.
			// Try the replication map (always populated, no tick dependency).
			if _, ok := cell.Base.ReplicaNetIDs()[netID]; ok {
				return hostID
			}
		}
	}
	return ""
}

// LiveHostIDs returns the IDs of all hosts that are currently considered live.
// In all-in-one / TestHosts mode these are the in-process hosts. In coordinator
// mode with a live HostRegistry these are the registered remote hosts.
func (c *Coordinator) LiveHostIDs() []string {
	if c.hostRegistry != nil {
		hosts := c.hostRegistry.LiveHosts()
		ids := make([]string, len(hosts))
		for i, h := range hosts {
			ids[i] = h.ID
		}
		return ids
	}
	c.mu.RLock()
	ids := make([]string, 0, len(c.Hosts))
	for id := range c.Hosts {
		ids = append(ids, id)
	}
	c.mu.RUnlock()
	return ids
}

// LiveGatewayIDs returns the IDs of all registered gateway processes.
func (c *Coordinator) LiveGatewayIDs() []string {
	if c.gatewayRegistry == nil {
		return nil
	}
	gws := c.gatewayRegistry.LiveGateways()
	ids := make([]string, len(gws))
	for i, g := range gws {
		ids[i] = g.ID
	}
	return ids
}

// HostForCellID returns the host ID owning the given cell string ID.
// Checks HostRegistry first (multi-process), then falls back to local cellToHostMap.
func (c *Coordinator) HostForCellID(cellID string) string {
	if c.hostRegistry != nil {
		if h := c.hostRegistry.HostForCell(cellID); h != "" {
			return h
		}
	}
	c.mu.RLock()
	h := c.cellToHostMap[cellID]
	c.mu.RUnlock()
	return h
}

// ConnManager returns the Coordinator's connection manager.
func (c *Coordinator) ConnManager() *net.ConnManager {
	return c.ConnMgr
}

// Roles returns the parsed role set for this Coordinator. Populated by
// Build() from Config.Mode via ParseRoles. Safe to call after Build().
func (c *Coordinator) Roles() Roles {
	return c.roles
}

// ServesClients reports whether this process terminates client WebSocket
// connections. True when RoleGateway is in the role set. Use this to gate
// the WebSocket + UDP listeners in main.go so that pure-control-plane and
// node-only processes don't collide with a standalone gateway on the same
// host.
func (c *Coordinator) ServesClients() bool {
	return c.roles.Has(RoleGateway)
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

	cfg := c.cfg

	roles, err := ParseRoles(cfg.Mode)
	if err != nil {
		panic(fmt.Errorf("coordinator: invalid Mode %q: %w", cfg.Mode, err))
	}
	c.roles = roles

	// Log categories up-front so every subsequent log line in Build() —
	// including MeshControl listen, node registration, etc. — respects
	// the --log flag and StartupCategories. Previously this ran at the
	// END of Build(), silently dropping all mode-setup logs that fire
	// during the coordinator/node initialization path.
	if c.cfg.LogCategories != "" {
		c.Log.EnableFromFlag(c.cfg.LogCategories)
	}
	c.Log.Enable(StartupCategories...)

	// Roles that create local cells (RoleHost) require SetWorld/OnInit.
	// Pure coordinator, node, and standalone gateway do not.
	if !roles.Has(RoleHost) && !roles.Has(RoleNode) && !roles.Has(RoleGateway) {
		// coordinator-only: no cells, no world factory needed
	} else if roles.Has(RoleNode) || (roles.Has(RoleGateway) && !roles.Has(RoleHost)) {
		// node or standalone gateway: no cells, no world factory needed
	} else if roles.Has(RoleHost) && c.worldFactory == nil && c.onInit == nil {
		panic("mmokit: coordinator requires SetWorld or OnInit before Build")
	}

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

	// Initialize login service if LoginHandler is provided.
	if cfg.LoginHandler != nil {
		c.loginSvc = newLoginService(cfg.LoginHandler, cfg.LoginTimeout)
		c.loginSvc.onRejected = cfg.LoginRejected
	}

	// RoleCoordinator: start the control plane (MeshControl gRPC server,
	// HostRegistry, AssignmentEngine). Always runs for pure-coordinator
	// processes (RoleCoordinator alone) and for coord+gateway-without-host
	// processes (which cannot function without a remote node joining).
	// For role sets that include RoleHost (`all` preset, coordinator+host,
	// coordinator+host+gateway) the listener is OPT-IN via
	// Config.ControlListen — an empty ControlListen means "don't listen,
	// nobody remote can join us". This preserves the status-quo of
	// `all` preset dev processes; set ControlListen on an `all` preset or
	// coordinator+host process to open remote joins.
	pureCoordinator := roles == Roles(RoleCoordinator)
	coordGatewayOnly := roles.Has(RoleCoordinator) && roles.Has(RoleGateway) && !roles.Has(RoleHost)
	if roles.Has(RoleCoordinator) && (pureCoordinator || coordGatewayOnly || cfg.ControlListen != "") {
		c.startControlPlane()
	}

	// RoleNode: dial a remote coordinator, register, receive cell assignments.
	// Cannot combine with other roles.
	if roles.Has(RoleNode) {
		if cfg.CoordinatorAddr == "" {
			panic("coordinator: node mode requires Config.CoordinatorAddr")
		}
		hostID := cfg.HostID
		if hostID == "" {
			// Auto-generate. UnixNano is monotonic enough for S4 tests.
			hostID = fmt.Sprintf("host-%d", time.Now().UnixNano())
		}

		host := NewHost(hostID)
		host.Log = c.Log
		c.Hosts[hostID] = host
		c.hostExecutors[hostID] = newCellTransferExecutor(c, host)

		hn, err := NewHostNetwork(host, ":0", c.Log)
		if err != nil {
			panic(fmt.Errorf("coordinator: node mode NewHostNetwork: %w", err))
		}
		host.Network = hn
		hn.SetCoord(c)

		vcm := NewVirtualConnManager(hn, c.Log)
		hn.SetVCM(vcm)
		c.vcm = vcm

		c.controlClient = newMeshControlClient(c, hostID, cfg.CoordinatorAddr)
		// Start never errors — the reconnect loop spawns in the
		// background and handles dial failures via exponential
		// backoff. The node will keep trying to reach the coordinator
		// forever; operators can Ctrl+C to stop.
		_ = c.controlClient.Start(context.Background())
		return
	}

	// RoleGateway (standalone): dial a remote coordinator, no local cells.
	// This branch only runs when RoleGateway is set without RoleCoordinator.
	if roles.Has(RoleGateway) && !roles.Has(RoleCoordinator) {
		if cfg.CoordinatorAddr == "" {
			panic("coordinator: gateway mode requires Config.CoordinatorAddr")
		}
		gwID := cfg.GatewayID
		if gwID == "" {
			gwID = "gateway-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		}

		// Gateway needs its own HostNetwork so nodes can stream ClientFrames back to it.
		gwHost := NewHost(gwID)
		gwHost.Log = c.Log
		c.Hosts[gwID] = gwHost
		hn, err := NewHostNetwork(gwHost, ":0", c.Log)
		if err != nil {
			panic(fmt.Errorf("coordinator: gateway mode NewHostNetwork: %w", err))
		}
		gwHost.Network = hn
		hn.SetCoord(c)

		c.gateway = &Gateway{
			id:           gwID,
			connMgr:      c.ConnMgr,
			loginSvc:     c.loginSvc,
			log:          c.Log,
			coord:        nil, // standalone: no direct coordinator reference
			sessions:     make(map[uint32]*localSession),
			topology:     newCachedTopology(nil), // populated by PeerList broadcasts
			hostNetwork:  hn,
			playerRouter: c.playerRouter,
			tickRate:     uint32(cfg.TickRate),
			// wsAddr: TODO — plumb via Config.GatewayWSAddr when flag lands
		}
		hn.SetGateway(c.gateway)

		c.gateway.controlClient = newMeshGatewayClient(c.gateway, cfg.CoordinatorAddr)
		_ = c.gateway.controlClient.Start(context.Background())

		c.Log.Log(CatNetConn, "coordinator: standalone gateway %q -> coordinator %s (grpc=%s)", gwID, cfg.CoordinatorAddr, hn.Addr())
		return // skip cells, admin console, MeshControl server already started above (n/a here)
	}

	// RoleGateway (embedded): coordinator is present; create an in-process gateway
	// that takes ownership of loginSvc. Login handling runs through
	// Gateway.processLogins() rather than the coordinator directly.
	//
	// Two sub-modes:
	//
	//   coord+gateway+host — the classic `all` preset. Every cell is colocated
	//       in-process so the gateway dispatches straight to cell.Inbox. No
	//       HostNetwork needed on the gateway side; isLocalShortcut returns
	//       true for every session.
	//
	//   coord+gateway  (no RoleHost)  — coordinator control plane + embedded
	//       gateway, but every cell lives on a remote --mode=node. The gateway
	//       needs its own HostNetwork to (a) dial remote nodes for outbound
	//       PlayerAssignment/ClientInput frames and (b) receive ClientFrames
	//       back from nodes. The local gateway is also registered in
	//       gatewayRegistry (Local=true) so broadcastPeerList includes it in
	//       the GatewayRecord list sent to remote nodes — otherwise nodes
	//       wouldn't know where to dial back.
	if roles.Has(RoleGateway) && cfg.LoginHandler != nil {
		gwID := cfg.GatewayID
		if gwID == "" {
			gwID = InprocGatewayID
		}
		c.gateway = &Gateway{
			id:       gwID,
			connMgr:  c.ConnMgr,
			loginSvc: c.loginSvc,
			log:      c.Log,
			coord:    c,
			sessions: make(map[uint32]*localSession),
			topology: newCachedTopology(c),
			tickRate: uint32(cfg.TickRate),
		}

		// Coord+gateway without a local host: needs its own HostNetwork so
		// remote nodes can be reached for outbound dispatch and can dial back
		// with ClientFrames. Mirrors the standalone gateway wiring above, but
		// with coord != nil so the gateway can read coord state directly
		// instead of going through meshGatewayClient.
		if !roles.Has(RoleHost) {
			gwHost := NewHost(gwID)
			gwHost.Log = c.Log
			c.Hosts[gwID] = gwHost
			hn, err := NewHostNetwork(gwHost, ":0", c.Log)
			if err != nil {
				panic(fmt.Errorf("coordinator: coord+gateway NewHostNetwork: %w", err))
			}
			gwHost.Network = hn
			hn.SetCoord(c)
			c.gateway.hostNetwork = hn
			hn.SetGateway(c.gateway)

			// Publish the local gateway into gatewayRegistry so broadcastPeerList
			// includes it in GatewayRecord lists sent to remote nodes. Nodes
			// reconcile this list into outbound MeshData streams back to us.
			if c.gatewayRegistry != nil {
				c.gatewayRegistry.RegisterLocal(gwID, hn.Addr())
			}
			c.Log.Log(CatNetConn, "coordinator: embedded gateway %q (grpc=%s) — no local host, routes via MeshData", gwID, hn.Addr())
		} else {
			c.Log.Log(CatNetConn, "coordinator: in-process gateway %q created", gwID)
		}
	}

	// RoleHost: create in-process cells with static (pre-Build) assignment.
	if roles.Has(RoleHost) {
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
			c.hostExecutors[hid] = newCellTransferExecutor(c, h)
			hosts = append(hosts, h)
			if multiHost {
				hn, err := NewHostNetwork(h, ":0", c.Log)
				if err != nil {
					panic(fmt.Errorf("coordinator: NewHostNetwork for %q: %w", hid, err))
				}
				h.Network = hn
				hn.SetCoord(c)
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
					if err := h.Network.ConnectPeer(peer.ID, peer.Network.Addr(), peerKindNode); err != nil {
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

		c.Log.Log(CatMeshCell, "coordinator: created %d cells, topology computed", len(c.Cells))

		// When the control plane is running (pure coordinator mode OR
		// RoleCoordinator + non-empty ControlListen) AND this process also
		// has RoleHost, auto-register each local host in the HostRegistry
		// so "host list" and PeerList broadcasts include it alongside any
		// remote nodes that join. Local hosts participate in rendezvous
		// rebalance on equal footing with remote nodes.
		if c.hostRegistry != nil {
			for _, h := range hosts {
				var ownedCells []string
				for _, cell := range h.Cells {
					ownedCells = append(ownedCells, cell.ID)
				}
				grpcAddr := ""
				if h.Network != nil {
					grpcAddr = h.Network.Addr()
				}
				c.hostRegistry.RegisterLocal(h.ID, grpcAddr, ownedCells)
			}
			c.Log.Log(CatMeshCell, "coordinator: %d local host(s) registered with control plane", len(hosts))
		}

		// Two-phase init: World.Init() first (registers entity kinds, login handlers),
		// then system Init() (discovers replicators, creates query filters).
		for _, s := range setups {
			s.cell.World.Init()
		}
		for _, s := range setups {
			initSystems(s.systems)
		}
	}
	// For coordinator and node modes, cell creation and host wiring are
	// driven by control-plane events (CellAssign / CellRelease).
	// Log categories were enabled at the top of Build() so every
	// lifecycle log line above respects the --log flag.
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

// startControlPlane starts the MeshControl gRPC server, creates the
// HostRegistry, GatewayRegistry, meshControlServer, and AssignmentEngine,
// and wires them together. Shared between coordinator mode and `all` preset
// mode (when Config.ControlListen is set). Panics on listen failure.
func (c *Coordinator) startControlPlane() {
	cfg := c.cfg
	addr := cfg.ControlListen
	if addr == "" {
		addr = ":9100"
	}
	listener, err := stdnet.Listen("tcp", addr)
	if err != nil {
		panic(fmt.Errorf("coordinator: MeshControl listen: %w", err))
	}
	grpcSrv := grpc.NewServer(
		grpc.MaxRecvMsgSize(meshMaxMsgBytes),
		grpc.MaxSendMsgSize(meshMaxMsgBytes),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    60 * time.Second,
			Timeout: 20 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             30 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	c.hostRegistry = NewHostRegistry(c.Log)
	c.gatewayRegistry = NewGatewayRegistry(c.Log)

	ctrl := &meshControlServer{
		coord:           c,
		log:             c.Log,
		registry:        c.hostRegistry,
		gatewayRegistry: c.gatewayRegistry,
		streams:         make(map[string]meshpb.MeshControl_ControlServer),
		streamMu:        make(map[string]*sync.Mutex),
		streamKill:      make(map[string]chan struct{}),
		gatewayStreams:  make(map[string]meshpb.MeshControl_ControlServer),
		gatewayMu:       make(map[string]*sync.Mutex),
		gatewayKill:     make(map[string]chan struct{}),
	}
	eng := newAssignmentEngine(c, c.hostRegistry, ctrl)
	ctrl.engine = eng

	meshpb.RegisterMeshControlServer(grpcSrv, ctrl)
	go func() {
		if err := grpcSrv.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			c.Log.Log(CatMeshCell, "coordinator: MeshControl serve: %v", err)
		}
	}()
	c.controlServer = ctrl
	c.controlGrpcServer = grpcSrv
	c.controlListener = listener
	c.assignmentEngine = eng

	// Start the settle-window loop. It runs until Coordinator.Shutdown()
	// cancels its context.
	engineCtx, engineCancel := context.WithCancel(context.Background())
	eng.Start(engineCtx)
	c.assignmentEngineCancel = engineCancel

	c.Log.Log(CatMeshCell, "coordinator: MeshControl listening on %s", listener.Addr())
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
	// In node mode, cells use VirtualConnManager as their ConnSender so that
	// outbound client frames are forwarded to the gateway via MeshData.
	// All-in-one mode uses the real ConnManager which holds the WebSocket listener.
	var connSender net.ConnSender = cfg.ConnManager
	if c.vcm != nil {
		connSender = c.vcm
	}
	eng := engine.New(platformCfg, connSender, cfg.Logger)
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
	c.startHTTPListener()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go c.routeEvents(ctx)

	for _, node := range c.Cells {
		go node.Run(ctx)
	}

	// Startup ready-message varies by role so the operator gets
	// something meaningful instead of "all 0 cells started" on processes
	// that don't host cells locally.
	switch {
	case c.roles.Has(RoleNode):
		c.Log.Log(CatMeshCell, "node: ready, awaiting CellAssign from coordinator %s", c.cfg.CoordinatorAddr)
	case c.roles.Has(RoleHost):
		c.Log.Log(CatMeshCell, "coordinator: all %d cells started (roles=%s)", len(c.Cells), c.roles)
	case c.roles.Has(RoleCoordinator):
		c.Log.Log(CatMeshCell, "coordinator: ready, waiting for host registrations on %s", c.cfg.ControlListen)
	case c.roles.Has(RoleGateway):
		c.Log.Log(CatMeshCell, "gateway: ready, awaiting sessions via %s", c.cfg.CoordinatorAddr)
	}

	// Start partition monitor if dynamic partitioning is enabled.
	if c.cfg.DynamicPartitioning != nil {
		monitor := newPartitionMonitor(c, c.cfg.DynamicPartitioning)
		go monitor.run(ctx)
		c.Log.Log(CatMeshCell, "coordinator: partition monitor started (eval every %s)", c.cfg.DynamicPartitioning.EvalInterval)
	}

	// Start the auto-rebalance loop only when explicitly opted in. The
	// primitive (orchestrator.BeginMigrate) is always available via the
	// `cell migrate` console command regardless of this flag — this only
	// gates the background decision loop.
	if c.cfg.DynamicPartitioning != nil && c.cfg.DynamicPartitioning.AutoRebalance {
		loop := newRebalanceLoop(
			c.cfg.DynamicPartitioning,
			&coordRebalanceSource{coord: c},
			&coordRebalanceMigrator{coord: c},
			realClock{},
			func(format string, args ...any) {
				c.Log.Log(CatMeshCell, format, args...)
			},
		)
		go loop.Run(ctx)
		c.Log.Log(CatMeshCell, "coordinator: auto-rebalance loop started (eval every %s, threshold=%.2f, sustain=%s, min_delta=%.2f, cooldown=%s)",
			c.cfg.DynamicPartitioning.RebalanceEvalInterval,
			c.cfg.DynamicPartitioning.RebalanceLoadThreshold,
			c.cfg.DynamicPartitioning.RebalanceSustainTime,
			c.cfg.DynamicPartitioning.RebalanceMinDelta,
			c.cfg.DynamicPartitioning.RebalanceCooldown)
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
// The console shares the coordinator's registry and dispatcher so that commands
// registered before Build() are available in the REPL and vice-versa.
func (c *Coordinator) startConsole(ctx context.Context) {
	c.console = engine.NewConsoleWithDispatcher(c.Log, c.registry, c.dispatcher)

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

	builtinOpts := engine.BuiltinOpts{}

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

	// Register cell commands unconditionally; partition-specific sub-commands
	// (split/merge/cooldowns/etc.) are gated internally on c.partState != nil.
	c.registerCellCommands(c.console)

	// Register host commands for coordinator and `all` preset modes.
	c.registerHostCommands(c.console)

	// Let game register custom commands.
	onReady := c.onConsoleReady
	if onReady != nil {
		onReady(c.console)
	}

	c.console.Run(ctx)
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

// resolveSpatialCellSize mirrors the logic in Build() so
// assignCellOnNode can pass the right bucket size to createNode.
func (c *Coordinator) resolveSpatialCellSize() float32 {
	bucket := c.cfg.SpatialBucketSize
	if bucket <= 0 {
		bucket = coords.CellSize / 10
	}
	return bucket
}

// assignCellOnNode creates and starts a cell that the coordinator
// has assigned to this node. Thread-safe: called from the
// meshControlClient's recv goroutine. Holds c.mu for map mutations.
//
// Flow: parse the cell ID, call createNode under the lock (which
// registers the cell in c.Cells/c.CellOwner), install the cell in
// the local Host, run World.Init() + initSystems(), launch the game
// loop goroutine, and send CellReady back to the coordinator.
//
// In node mode the freshly-created cell's plain cellBridge is wrapped
// in a grpcBridge so cross-host MeshData routing works. The
// cellToHost closure reads from c.cellToHostMap which is populated
// by applyPeerList from coordinator PeerList broadcasts.
func (c *Coordinator) assignCellOnNode(cellID string) {
	cell, err := ParseCellID(cellID)
	if err != nil {
		c.Log.Log(CatMeshCell, "node: ignoring CellAssign: invalid cell id %q: %v", cellID, err)
		return
	}

	host := c.localHost()
	if host == nil {
		c.Log.Log(CatMeshCell, "node: assignCellOnNode with no local host")
		return
	}

	// Check if already running (idempotent if the coordinator re-sends).
	c.mu.Lock()
	if _, exists := c.Cells[cellID]; exists {
		c.mu.Unlock()
		c.Log.Log(CatMeshCell, "node: cell %s already running, ignoring duplicate CellAssign", cellID)
		return
	}

	// createNode registers the cell into c.Cells and c.CellOwner itself.
	spatialCellSize := c.resolveSpatialCellSize()
	node, systems := c.createNode(cell, spatialCellSize)
	host.AddCell(cell, node)
	c.mu.Unlock()

	// In node mode (RoleNode), wrap the plain cellBridge in a grpcBridge
	// so cross-host border frames and handoffs route through HostNetwork.
	// The cellToHost closure reads from c.cellToHostMap which is populated
	// by applyPeerList from coordinator broadcasts. Same pattern as the
	// `all` preset TestHosts multi-host build path.
	if c.roles.Has(RoleNode) {
		localBridge, ok := node.Bridge.(*cellBridge)
		if !ok {
			c.Log.Log(CatMeshCell, "node: unexpected bridge type %T on cell %s, skipping grpcBridge wrap", node.Bridge, cellID)
		} else {
			cellToHostFn := func(destCellID string) string {
				c.mu.RLock()
				defer c.mu.RUnlock()
				return c.cellToHostMap[destCellID]
			}
			gb := newGrpcBridge(node, c, host, cellToHostFn, localBridge, c.cfg.GatewayMode)
			node.Bridge = gb
			node.World.SetBridge(gb)
		}
	}

	// Wire neighbor relationships for the new cell. Includes both LOCAL
	// neighbors (real *Cell pointers) AND REMOTE neighbors (stub *Cell
	// entries carrying just ID + CellID). Without this, the border
	// dispatcher either early-exits on an empty Neighbors map or only
	// sees one side of the boundary — cross-node border replicas never
	// flow, so entities in adjacent cells on other nodes are invisible
	// to clients on this node. See reconcileCellNeighbors for the remote-
	// stub rationale.
	c.reconcileCellNeighbors(node)

	// Two-phase init matches the Build() path: World.Init registers
	// entity kinds + world state; then systems discover them.
	node.World.Init()
	initSystems(systems)

	go node.Run(context.Background())

	// Tell the coordinator we're live.
	if c.controlClient != nil {
		_ = c.controlClient.send(&meshpb.HostMessage{
			Msg: &meshpb.HostMessage_CellReady{
				CellReady: &meshpb.CellReady{
					HostId: c.controlClient.hostID,
					CellId: cellID,
				},
			},
		})
	}
	c.Log.Log(CatMeshCell, "node: cell %s ready", cellID)
}

// releaseCellOnNode stops and removes a cell that the coordinator
// has released (typically during reassignment after crash recovery).
// Sends CellStopped back once the shutdown is complete.
func (c *Coordinator) releaseCellOnNode(cellID string) {
	host := c.localHost()
	if host == nil {
		return
	}

	c.mu.Lock()
	node, ok := c.Cells[cellID]
	if !ok {
		c.mu.Unlock()
		c.Log.Log(CatMeshCell, "node: releaseCellOnNode: unknown cell %q", cellID)
		return
	}
	delete(c.Cells, cellID)
	delete(c.CellOwner, node.Cell)
	host.RemoveCell(node.Cell)
	c.mu.Unlock()

	// Shutdown runs on this goroutine. node.Shutdown() stops the
	// game loop and saves state.
	node.Shutdown()

	if c.controlClient != nil {
		_ = c.controlClient.send(&meshpb.HostMessage{
			Msg: &meshpb.HostMessage_CellStopped{
				CellStopped: &meshpb.CellStopped{
					HostId: c.controlClient.hostID,
					CellId: cellID,
				},
			},
		})
	}
	c.Log.Log(CatMeshCell, "node: cell %s stopped", cellID)
}

// drainHost migrates every cell currently owned by hostID to one of the
// surviving hosts, picking destinations via rendezvous over the live-host
// roster (excluding the leaving host). Used by:
//
//   - meshControlServer.handleHostControl when a remote node sends
//     HostMessage.GracefulLeave (the coordinator-side entry point)
//   - integration tests that drive the graceful-leave flow directly
//     without a control stream
//
// Returns nil if the host has no owned cells to drain or if there are no
// surviving hosts to migrate to (the latter is logged — there's nowhere
// to go, so the caller should still send CellsDrained and let the node
// exit; the cells will be torn down by the node's local cleanup loop).
//
// On partial failure (one migration fails out of N) drainHost logs the
// error but continues — the leaving node is exiting regardless, so a
// half-drained state is strictly better than hanging its Shutdown.
// The returned error aggregates all per-cell failures for caller logging.
func (c *Coordinator) drainHost(ctx context.Context, hostID string) error {
	// Snapshot the set of cells currently owned by hostID. Prefer the
	// authoritative HostRegistry snapshot (populated by CellReady /
	// ReleaseCell via the real control plane); fall back to scanning
	// cellToHostMap for in-process fixtures that wire neither a control
	// listener nor a HostRegistry.
	var cellKeys []string
	if c.hostRegistry != nil {
		if snap := c.hostRegistry.Get(hostID); snap != nil {
			for k := range snap.OwnedCells {
				cellKeys = append(cellKeys, k)
			}
		}
	}
	if len(cellKeys) == 0 {
		c.mu.RLock()
		for k, h := range c.cellToHostMap {
			if h == hostID {
				cellKeys = append(cellKeys, k)
			}
		}
		c.mu.RUnlock()
	}

	if len(cellKeys) == 0 {
		c.Log.Log(CatMeshCell, "coordinator: drainHost %s — no owned cells, nothing to migrate", hostID)
		return nil
	}

	// Build the list of surviving destination hosts. Prefer the registry
	// live set; fall back to cellToHostMap for test fixtures.
	survivors := c.survivingHostIDs(hostID)
	if len(survivors) == 0 {
		c.Log.Log(CatMeshCell, "coordinator: drainHost %s — no surviving hosts, %d cells orphaned (node exiting anyway)", hostID, len(cellKeys))
		return nil
	}

	c.Log.Log(CatMeshCell, "coordinator: drainHost %s — migrating %d cells to %d surviving hosts",
		hostID, len(cellKeys), len(survivors))

	// Kick off every migration in parallel so the drain window is the
	// slowest single migration rather than their sum.
	type pending struct {
		cellKey string
		req     *CellTransferRequest
		err     error
	}
	pendings := make([]pending, 0, len(cellKeys))
	for _, cellKey := range cellKeys {
		target := AssignCellToHost(cellKey, survivors)
		if target == "" {
			pendings = append(pendings, pending{cellKey: cellKey, err: fmt.Errorf("no rendezvous winner")})
			continue
		}
		cid, err := ParseCellID(cellKey)
		if err != nil {
			pendings = append(pendings, pending{cellKey: cellKey, err: fmt.Errorf("parse cell id %q: %w", cellKey, err)})
			continue
		}
		req, err := c.orchestrator.BeginMigrate(cid, target)
		if err != nil {
			c.Log.Log(CatMeshCell, "coordinator: drainHost %s: BeginMigrate %s->%s: %v",
				hostID, cellKey, target, err)
			pendings = append(pendings, pending{cellKey: cellKey, err: err})
			continue
		}
		pendings = append(pendings, pending{cellKey: cellKey, req: req})
	}

	// Wait for every kicked-off migration to finish, bounded by ctx.
	var errs []error
	for _, p := range pendings {
		if p.err != nil {
			errs = append(errs, fmt.Errorf("cell %s: %w", p.cellKey, p.err))
			continue
		}
		select {
		case <-p.req.Done:
			if p.req.Result != nil {
				c.Log.Log(CatMeshCell, "coordinator: drainHost %s: migrate %s failed: %v",
					hostID, p.cellKey, p.req.Result)
				errs = append(errs, fmt.Errorf("cell %s: %w", p.cellKey, p.req.Result))
			}
		case <-ctx.Done():
			c.Log.Log(CatMeshCell, "coordinator: drainHost %s: context done waiting for %s: %v",
				hostID, p.cellKey, ctx.Err())
			errs = append(errs, fmt.Errorf("cell %s: %w", p.cellKey, ctx.Err()))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	c.Log.Log(CatMeshCell, "coordinator: drainHost %s — all %d cells drained", hostID, len(cellKeys))
	return nil
}

// survivingHostIDs returns the set of live host IDs excluding the leaving
// one. Prefers the HostRegistry snapshot; falls back to the ownership map.
func (c *Coordinator) survivingHostIDs(leavingHostID string) []string {
	if c.hostRegistry != nil {
		live := c.hostRegistry.LiveHosts()
		out := make([]string, 0, len(live))
		for _, h := range live {
			if h.ID == leavingHostID {
				continue
			}
			if h.State == RemoteHostRegistered || h.State == RemoteHostLive {
				out = append(out, h.ID)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	// Fallback: derive from ownership map (test fixtures without a
	// HostRegistry).
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := make(map[string]struct{}, len(c.cellToHostMap))
	var ids []string
	for _, h := range c.cellToHostMap {
		if h == leavingHostID {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		ids = append(ids, h)
	}
	return ids
}

// localHost returns the local host instance in node mode (or
// `all` preset mode where a single "local" Host owns all cells).
// Returns nil if the coordinator hasn't built any hosts yet.
// Helper used by meshControlClient to fill RegisterHost with the
// grpc listen address.
func (c *Coordinator) localHost() *Host {
	for _, h := range c.Hosts {
		return h
	}
	return nil
}

// localHostExecutor returns the cellTransferExecutor attached to a local
// Host, or nil if hostID is not one of this process's in-process hosts.
// Used by the S7 dispatcher and the executor's inter-host fast path to
// skip the wire for colocated source/destination hosts.
func (c *Coordinator) localHostExecutor(hostID string) *cellTransferExecutor {
	if hostID == "" {
		return nil
	}
	return c.hostExecutors[hostID]
}

// Shutdown saves state on all nodes.
func (c *Coordinator) Shutdown() {
	// Shut the engine-owned HTTP listener first so in-flight client requests
	// drain before cells stop ticking.
	if c.httpServer != nil {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 3*time.Second)
		if err := c.httpServer.Shutdown(shutdownCtx); err != nil {
			c.Log.Log(CatMeshCell, "http: shutdown error: %v", err)
		}
		cancelShutdown()
		c.httpServer = nil
	}

	// S7-T7 graceful leave for node mode: send HostMessage.GracefulLeave to
	// the coordinator and wait for a matching CellsDrained reply before
	// closing the control stream. The coordinator-side handler runs
	// BeginMigrate for each owned cell, waits for commit, and then acks.
	//
	// Any cell that doesn't migrate cleanly (dispatcher error, timeout) is
	// surfaced as an error log by drainHost on the coordinator — the ack is
	// still sent so the leaving node doesn't hang forever. The local
	// `for _, node := range c.Cells { node.Shutdown() }` loop below is the
	// cleanup safety net for cells the migrate commit left behind on this
	// host (migrate commit teardown is deferred to T9).
	if c.controlClient != nil {
		drained := c.controlClient.armDrainWaiter()
		hostID := c.controlClient.hostID
		err := c.controlClient.send(&meshpb.HostMessage{
			Msg: &meshpb.HostMessage_GracefulLeave{
				GracefulLeave: &meshpb.GracefulLeave{HostId: hostID},
			},
		})
		if err != nil {
			c.Log.Log(CatMeshCell, "node: GracefulLeave send failed: %v — skipping drain wait", err)
		} else {
			c.Log.Log(CatMeshCell, "node: sent GracefulLeave, waiting for CellsDrained")
			// Slightly larger than drainHost's 30s budget so the
			// coordinator's timeout path wins (ack + log) instead of
			// ours (log timeout + proceed).
			select {
			case <-drained:
				c.Log.Log(CatMeshCell, "node: CellsDrained received, proceeding with shutdown")
			case <-time.After(32 * time.Second):
				c.Log.Log(CatMeshCell, "node: timed out waiting for CellsDrained, proceeding with shutdown")
			}
		}
	}

	if c.controlClient != nil {
		c.controlClient.Shutdown()
	}
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
	// Stop the assignment engine's settle-window goroutine (coordinator mode only).
	if c.assignmentEngineCancel != nil {
		c.assignmentEngineCancel()
	}
	// Tear down the MeshControl gRPC server (coordinator mode only).
	if c.controlGrpcServer != nil {
		stopped := make(chan struct{})
		go func() {
			c.controlGrpcServer.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			c.Log.Log(CatMeshCell, "coordinator: MeshControl hard-stop after GracefulStop timeout")
			c.controlGrpcServer.Stop()
			<-stopped
		}
	}
	c.Log.Log(CatMeshCell, "coordinator: all nodes shut down")
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
			// Client connect/disconnect events are only emitted when this
			// process has a gateway role (and thus a WS listener). Any
			// PlayerEvent arriving here implies c.gateway != nil; routing
			// goes through Gateway.handleEvent / handleDisconnect, which
			// own the login pipeline and session routing. Non-gateway
			// processes (pure coord, pure node) still spin this goroutine
			// but never receive events.
			if c.gateway == nil {
				c.Log.Log(CatNetConn, "coordinator: conn %d event received with no gateway — ignoring", evt.ConnID)
				continue
			}
			if evt.Connected {
				c.gateway.handleEvent(evt)
			} else {
				c.gateway.handleDisconnect(evt)
			}

		case <-loginTicker.C:
			c.processLogins()
		}
	}
}

// processLogins processes all pending login attempts on the coordinator goroutine.
func (c *Coordinator) processLogins() {
	if c.gateway != nil {
		// Gateway owns the loginSvc in embedded mode.
		c.gateway.processLogins()
		return
	}
	// Fallback: no embedded gateway — coordinator owns loginSvc directly.
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

// notifyPlayerMigrated is called when a cross-host player handoff commits.
// Updates sessionRoutes atomically (bumping epoch) and notifies the gateway
// holding the session via direct call (embedded) or targeted CoordMessage
// (standalone). Called from two entry points:
//   - grpcBridge.OnPlayerTransfer when the destination is on a different host
//     (single-process `all` preset with multiple TestHosts — passes InprocGatewayID)
//   - meshControlServer.handleHostControl when a remote node emits
//     HostMessage.PlayerMigrated over its control stream (passes proto GatewayId,
//     which the node resolved from its VirtualConnManager reverse lookup)
//
// gatewayID is the gateway that owns the session — InprocGatewayID for
// embedded-gateway deployments, the real gateway peer ID for multi-process.
func (c *Coordinator) notifyPlayerMigrated(gatewayID string, connID uint32, srcHost, destHost, destCellID string) {
	key := SessionKey{GatewayID: gatewayID, ConnID: connID}
	newEpoch, ok := c.sessionRoutes.Migrate(key, destHost, destCellID)
	if !ok {
		c.Log.Log(CatMeshCell, "coordinator: PlayerMigrated for unknown session conn=%d src=%s dst=%s", connID, srcHost, destHost)
		return
	}
	c.Log.Log(CatMeshTransfer, "coordinator: PlayerMigrated conn=%d %s->%s cell=%s epoch=%d", connID, srcHost, destHost, destCellID, newEpoch)
	// Embedded gateway: direct call.
	if c.gateway != nil && c.gateway.id == key.GatewayID {
		c.gateway.OnUpstreamSwitch(connID, destHost, newEpoch)
		return
	}
	// Standalone gateway: send targeted CoordMessage.
	if c.controlServer == nil {
		return
	}
	msg := &meshpb.CoordMessage{
		CoordEpoch: c.coordEpoch,
		Msg: &meshpb.CoordMessage_UpstreamSwitch{
			UpstreamSwitch: &meshpb.UpstreamSwitch{
				GatewayId: key.GatewayID,
				ConnId:    connID,
				NewHostId: destHost,
				NewEpoch:  newEpoch,
			},
		},
	}
	if err := c.controlServer.sendCoordMessageToGateway(key.GatewayID, msg); err != nil {
		c.Log.Log(CatMeshCell, "coordinator: UpstreamSwitch to gateway %s failed: %v", key.GatewayID, err)
	}
}

// dispatchUpstreamSwitch sends a targeted UpstreamSwitch to the gateway owning
// key, informing it that subsequent client input for that session should now
// route to destHost. Reuses the embedded-vs-standalone-gateway fork from
// notifyPlayerMigrated, but skips the sessionRoutes.Migrate step — the caller
// has already bumped the session's epoch atomically via a batch remap and
// passes the resulting (host, epoch) here explicitly.
//
// Used exclusively by the cell-transfer commit path so that a single
// atomic remapCell / remapHostCell is followed by N targeted dispatches
// without additional epoch bumps.
func (c *Coordinator) dispatchUpstreamSwitch(key SessionKey, destHost string, newEpoch uint64) {
	if c.gateway != nil && c.gateway.id == key.GatewayID {
		c.gateway.OnUpstreamSwitch(key.ConnID, destHost, newEpoch)
		return
	}
	if c.controlServer == nil {
		return
	}
	msg := &meshpb.CoordMessage{
		CoordEpoch: c.coordEpoch,
		Msg: &meshpb.CoordMessage_UpstreamSwitch{
			UpstreamSwitch: &meshpb.UpstreamSwitch{
				GatewayId: key.GatewayID,
				ConnId:    key.ConnID,
				NewHostId: destHost,
				NewEpoch:  newEpoch,
			},
		},
	}
	if err := c.controlServer.sendCoordMessageToGateway(key.GatewayID, msg); err != nil {
		c.Log.Log(CatMeshCell, "coordinator: UpstreamSwitch to gateway %s failed: %v", key.GatewayID, err)
	}
}

// applyRegistryDelta reconciles HostRegistry.OwnedCells with a topology
// mutation that has just been committed to cellToHostMap. For every cell in
// the remove set, the current owner (prior to the mutation) releases it; for
// every cell in the add set, the new owner claims it. Callers must supply the
// pre-mutation ownership map so ReleaseCell can target the right host.
//
// No-op when hostRegistry is nil (unit-test fixtures that skip Build's
// registry wiring) or when the mutation is empty.
func (c *Coordinator) applyRegistryDelta(mutation topologyMutation, preOwnership map[string]string) {
	if c.hostRegistry == nil {
		return
	}
	for _, cellID := range mutation.remove {
		if prev, ok := preOwnership[cellID]; ok && prev != "" {
			c.hostRegistry.ReleaseCell(prev, cellID)
		}
	}
	for cellID, newOwner := range mutation.add {
		// Release any stale owner first (covers migrate: same cellID
		// stays under mutation.add but its owner changes).
		if prev, ok := preOwnership[cellID]; ok && prev != "" && prev != newOwner {
			c.hostRegistry.ReleaseCell(prev, cellID)
		}
		if err := c.hostRegistry.AssignCell(newOwner, cellID); err != nil {
			c.Log.Log(CatMeshCell, "coordinator: AssignCell %s->%s: %v", cellID, newOwner, err)
		}
	}
}

// broadcastPeerListIfReady fires a PeerList broadcast through the assignment
// engine when one is wired. Commit paths call this after every successful
// cell-transfer commit so every registered host + gateway sees the new
// ownership immediately, not just at the next rebalance tick.
//
// No-op when the assignment engine is absent (unit-test fixtures) or when
// the control server isn't listening (pure single-process `all` preset with
// no --control-listen). Safe to call outside c.mu.
func (c *Coordinator) broadcastPeerListIfReady() {
	if c.assignmentEngine == nil {
		return
	}
	// The engine's broadcast method tolerates a missing control server
	// (sendCoordMessageToHost fails gracefully and the embedded-gateway
	// direct reconcile path still runs). Guarding here on controlServer
	// would skip the in-process gateway reconcile, which we want for
	// all-preset dev setups with multiple TestHosts.
	c.assignmentEngine.broadcastPeerList()
}

func (c *Coordinator) getPlayerNode(connID uint32) string {
	if r, ok := c.sessionRoutes.Get(SessionKey{GatewayID: InprocGatewayID, ConnID: connID}); ok {
		return r.CellID
	}
	return ""
}

func (c *Coordinator) setPlayerNode(connID uint32, nodeID string) {
	c.sessionRoutes.Set(&SessionRoute{
		Key:    SessionKey{GatewayID: InprocGatewayID, ConnID: connID},
		CellID: nodeID,
		Epoch:  1,
	})
}

func (c *Coordinator) removePlayerNode(connID uint32) {
	c.sessionRoutes.Remove(SessionKey{GatewayID: InprocGatewayID, ConnID: connID})
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

// reconcileCellNeighbors rebuilds newCell.Neighbors from the current cluster
// topology snapshot. LOCAL neighbors get real *Cell pointers; REMOTE
// neighbors (cells on other hosts, learned via PeerList) get stub *Cell
// entries carrying just ID + CellID. The stub is sufficient because
// cellBridge.ensureBorderDispatcher only reads .ID / .Cell from the
// neighbor; the actual dispatch runs through the wrapping grpcBridge which
// routes SendBorderFrame(destCellID, ...) to the owning host via MeshData.
//
// Also invalidates each affected LOCAL cell's borderDispatcher so the next
// PostSystems tick rebuilds the viewer set.
//
// Takes c.mu around the map mutations; callers must NOT already hold it.
func (c *Coordinator) reconcileCellNeighbors(newCell *Cell) {
	if newCell == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	baseSize := c.baseCellSize()

	// Build the universe of known cells: start with local c.Cells and
	// merge in any remote cells from cellToHostMap that aren't already
	// covered. Local entries win over remote stubs.
	type candidate struct {
		id   string
		cid  CellID
		cell *Cell // nil for remote stubs
	}
	seen := make(map[string]bool)
	var candidates []candidate
	for id, cc := range c.Cells {
		if id == newCell.ID {
			continue
		}
		seen[id] = true
		candidates = append(candidates, candidate{id: id, cid: cc.Cell, cell: cc})
	}
	for id := range c.cellToHostMap {
		if id == newCell.ID || seen[id] {
			continue
		}
		cid, err := ParseCellID(id)
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{id: id, cid: cid, cell: nil})
	}

	// Drop existing Neighbors and rebuild from scratch — this keeps the
	// map clean after topology churn (remote cell moved, node left, etc.).
	for k := range newCell.Neighbors {
		delete(newCell.Neighbors, k)
	}
	for _, cand := range candidates {
		if !AreAdjacent(newCell.Cell, cand.cid, baseSize) {
			continue
		}
		if cand.cell != nil {
			// Local neighbor — wire both directions and invalidate the
			// existing neighbor's dispatcher so it picks us up too.
			newCell.Neighbors[cand.id] = cand.cell
			cand.cell.Neighbors[newCell.ID] = newCell
			if eb := unwrapCellBridge(cand.cell.Bridge); eb != nil {
				eb.invalidateBorderDispatcher()
			}
			continue
		}
		// Remote neighbor — stub entry. The remote host handles its own
		// reverse-side wiring when it applies the same PeerList.
		newCell.Neighbors[cand.id] = &Cell{
			ID:   cand.id,
			Cell: cand.cid,
		}
	}
	if nb := unwrapCellBridge(newCell.Bridge); nb != nil {
		nb.invalidateBorderDispatcher()
	}
}

// ClusterCellInfo describes one cell's identity and its owning host.
// Returned by Coordinator.ClusterCells; lets games build their own
// SE_CELL_TOPOLOGY messages without engine-side broadcast plumbing.
type ClusterCellInfo struct {
	Cell   CellID // X, Y, Depth
	HostID string // owning host's ID (may be empty in single-host `all` preset)
}

// ClusterCells returns the current cluster topology — every cell known
// to this coordinator, whether locally owned or learned from PeerList
// broadcasts. Works in any role:
//   - `all` preset or coordinator+host: reads from local CellOwner
//   - pure coordinator: reads from HostRegistry's owned-cells aggregation
//     (or the local cellToHostMap which the broadcaster populates)
//   - node / standalone gateway: reads from cellToHostMap (populated by
//     PeerList broadcasts from the coordinator)
//
// Returns an empty slice when nothing is known yet (e.g. standalone
// gateway before its first PeerList).
func (c *Coordinator) ClusterCells() []ClusterCellInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.cellToHostMap) > 0 {
		out := make([]ClusterCellInfo, 0, len(c.cellToHostMap))
		for cellIDStr, hostID := range c.cellToHostMap {
			cell, err := ParseCellID(cellIDStr)
			if err != nil {
				continue
			}
			out = append(out, ClusterCellInfo{Cell: cell, HostID: hostID})
		}
		return out
	}
	// Fallback: single-host `all` preset without TestHosts — cellToHostMap is
	// empty, but CellOwner has the authoritative cell set.
	out := make([]ClusterCellInfo, 0, len(c.CellOwner))
	for cell := range c.CellOwner {
		out = append(out, ClusterCellInfo{Cell: cell})
	}
	return out
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
