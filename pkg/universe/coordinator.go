package universe

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	stdnet "net"
	"strconv"
	"strings"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/services/auth"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/metrics"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/persist/postgres"
	"github.com/zenion/mmoserver/pkg/service"
	"github.com/zenion/mmoserver/pkg/spatial"
)

const netIDRangeSize uint32 = 10_000_000

// Config holds all Process configuration. Zero values use sensible defaults.
type Config struct {
	CellsX              uint32  // number of cells wide (0 = 1)
	CellsY              uint32  // number of cells tall (0 = 1)
	CellSize            float32 // world units per cell (0 = default 8192)
	SpatialBucketSize   float32 // spatial hash bucket size (0 = CellSize/10)
	TickRate            int     // game loop tick rate (0 = 20)
	AoIRadius           float32 // area-of-interest radius (0 = 500)

	// VelQuantScale is the max-velocity range for the standard engine
	// bindings: int16 = (vel / VelQuantScale) * 32767. Larger values
	// support higher top speeds but reduce precision at typical speeds.
	// Default 2000 (max ±2000 u/s; precision ~0.06 u/s = scale/32767).
	VelQuantScale float32

	// SizeQuantScale is the max-radius range for the standard engine
	// bindings: int16 = (radius / SizeQuantScale) * 32767. Larger
	// values support larger entities but reduce precision.
	// Default 500 (max 500 units; precision ~0.015 = scale/32767).
	SizeQuantScale float32

	Headless bool
	DynamicPartitioning *PartitionConfig // nil = disabled (default)
	ConnManager         *net.ConnManager
	Logger              *logger.Logger
	LogCategories       string                             // comma-separated categories/groups to enable (overrides default enabled list)

	// DevInsecureCookie disables the Secure flag on the auth session
	// cookie. Default false (production-safe). Flip via the
	// --dev-insecure-cookie CLI flag for plain-HTTP local dev.
	DevInsecureCookie bool

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
	// Accepts role names: coordinator, host, gateway.
	// Preset aliases: "" or "all" → "coordinator,host,gateway" (default).
	//
	// Common combinations:
	//   - "" / "all"                  → coordinator + host + gateway (single-process dev)
	//   - "coordinator"               → control plane only (MeshControl, HostRegistry, admin console)
	//   - "coordinator,gateway"       → control plane + embedded WebSocket gateway
	//   - "coordinator,host"          → control plane + in-process cells, no gateway
	//   - "coordinator,host,gateway"  → explicit spelling of the default `all` preset
	//   - "host" + CoordinatorAddr    → remote host, dials coordinator, receives cells dynamically
	//   - "gateway"                   → standalone gateway, dials CoordinatorAddr
	//
	// Rule: bare "host" requires Config.CoordinatorAddr; all other
	// combinations are accepted. Enforced at Build() time.
	Mode string

	// ControlListen is the listen address for the MeshControl gRPC server.
	// Only used when Mode == "coordinator". Default ":9100".
	ControlListen string

	// AdminListen is the listen address for an HTTP admin server that
	// exposes /events, /commands, and /metrics. Useful for pure-
	// coordinator processes (`--mode=coordinator`) that don't have a
	// client-facing HTTP listener but still need operational
	// observability — commits run on coord, so the commit log lives
	// there. Empty = disabled (default). Format: ":9101" or
	// "127.0.0.1:9101".
	AdminListen string

	// CoordinatorAddr is the MeshControl server address to dial. Used by
	// remote hosts (`--mode=host` with no local coordinator) and by
	// standalone gateways (`--mode=gateway`). Empty for in-process roles.
	CoordinatorAddr string

	// HostID is a stable host identifier used when running as a remote host
	// (bare RoleHost with CoordinatorAddr set). Empty means auto-generate a
	// unique ID at Build() time. Set by tests to get deterministic host IDs.
	HostID string

	// PostgresURL is the connection string for player, marketplace, and
	// config persistence. Format:
	//   postgres://user:pass@host:port/dbname?sslmode=disable
	// Empty means use the local docker-compose default
	// (postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable). Callers
	// that open the PostgresStore themselves via mmokit.OpenPostgres
	// can ignore this field.
	PostgresURL string

	// ExtraMigrations layers additional Postgres migration filesystems on
	// top of the engine's built-in migrations. Engine migrations run first,
	// then each entry is applied in slice order. Used by pkg/services/auth/ and
	// similar packages to ship their own schema. Nil/empty means engine
	// migrations only.
	//
	// Each fs.FS must contain golang-migrate-style files
	// (NNN_name.up.sql / NNN_name.down.sql) at its root ("."). Each
	// source gets its own schema_migrations table keyed by index so
	// version numbers don't have to coordinate across sources.
	//
	// Callers that need a stable label (e.g. to avoid renumbering issues
	// if slices reorder) should use mmokit.WithExtraMigrations directly
	// via mmokit.OpenPostgres instead.
	// (Reordering ExtraMigrations between deploys will rename tracking tables
	// and force migrations to re-run, which usually fails on "already exists"
	// errors. Either keep order stable or use mmokit.WithExtraMigrations directly.)
	//
	// IMPORTANT: ExtraMigrations is honored only on the engine-auto-open path —
	// when cfg.DBStore is nil and cfg.PostgresURL is set. If a caller pre-opens
	// Postgres and assigns the *Store to cfg.DBStore, ExtraMigrations is silently
	// ignored. In that case the caller must apply extras themselves (e.g. by
	// passing them to mmokit.OpenPostgres).
	ExtraMigrations []fs.FS

	// DBStore is the cluster's Postgres handle. The engine plumbs it
	// through into service.Context.DB for service kinds. Game code
	// typically opens it via mmokit.OpenPostgres in main and assigns
	// it here before Build.
	//
	// Optional — services with RequiresDB=true panic at Build when this
	// is nil. Cell-host code paths today read Postgres via separate
	// repository plumbing in cmd/server/main.go and don't depend on
	// this field.
	//
	// Note: when DBStore is non-nil, cfg.ExtraMigrations is NOT applied — the
	// caller is responsible for any extra migrations needed.
	DBStore *postgres.Store

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

	// DefaultSpawn is the world-space login/respawn location used when no
	// SpawnResolver is registered or the resolver returns ok=false.
	// Topology-independent: the gateway resolves the current owning cell
	// via CellAtPosition at dispatch time.
	DefaultSpawn coords.Location

	// InvariantMode controls how invariant-check violations are handled.
	// Zero value is InvariantOff; tests and dev should set Panic, prod
	// typically sets Log. See integrity.go for the full enum.
	InvariantMode InvariantMode

	// CommitLogCapacity sets the size of the in-memory commit log ring.
	// 0 = use default (1024).
	CommitLogCapacity int

	// StrictNetIDIndex enforces the transition policy table. When false
	// (default during rollout), the index tracks state for observability
	// but transitions are advisory — existing spawn paths run unchanged.
	StrictNetIDIndex bool

	// BlinkDetectorTicks controls the per-connection recent-removals
	// window used by the ReplicationSystem blink detector. When the
	// system is about to emit SE_ENTITY_SPAWN for a netID that was
	// removed less than BlinkDetectorTicks ticks ago for the same
	// connection, the detector records a violation in the commit log
	// and (in InvariantPanic mode) panics.
	//
	// 0 = use default (30 ticks = 1.5s at 20Hz). Set higher for high-
	// latency deployments; set to 1 to effectively disable.
	BlinkDetectorTicks uint64

	// ClusterClockSyncInterval controls the cadence at which the
	// coordinator broadcasts CoordTimeSync to all registered hosts.
	// Production default is 10 s. Lower values converge faster after
	// network-latency step-changes at the cost of minor bandwidth.
	// Zero means "use the default".
	ClusterClockSyncInterval time.Duration

	// ShutdownGracePeriod bounds how long Shutdown() waits for the
	// MeshControl and MeshData gRPC servers to drain in-flight bidi
	// streams via GracefulStop before falling back to a hard Stop.
	// Production default is 5s. Tests using disposable fixtures should
	// set a small value (e.g. 50ms) — bidi streams from peer hosts/clients
	// rarely close cleanly during teardown, so the full grace period is
	// almost always burned. Zero means "use the default".
	ShutdownGracePeriod time.Duration

	// SettleWindow is how long the assignment engine waits after the
	// first host registers before running the first cell-assignment
	// pass; subsequent registrations within the window extend the
	// deadline so a startup burst settles in a single pass. Production
	// default is 5s. Tests bringing up a fixed roster all-at-once
	// should set a short value (e.g. 50ms) to skip the startup wait.
	// Zero means "use the default".
	SettleWindow time.Duration

	// Protocol holds the game's *mmokit.Protocol declaration — typed as any
	// to avoid an import cycle (pkg/mmokit imports pkg/universe). The
	// pkg/mmokit layer type-asserts back to *mmokit.Protocol via accessors.
	Protocol any

	// OpRouter, when set, is exposed via Process.OpRouter() for schema export.
	// Optional — games without operations leave this nil. Wired by the game's
	// main.go after constructing the router and registering its handlers.
	//
	// Required for processes with RoleService — service handlers are
	// registered against this router at Start.
	OpRouter *ops.Router

	// ServiceKinds is the list of service kind names this process should
	// instantiate when RoleService is in the role set. Each name must
	// match a Kind registered via Process.RegisterService. Bound to the
	// --services CLI flag.
	ServiceKinds []string

	// DumpSchema, when true, causes Process.Start to dump the protocol schema
	// JSON to stdout and exit before any listeners or game-loop goroutines
	// start. Engine-owned via the --dump-schema flag in BindFlags. Games never
	// set this directly.
	DumpSchema bool

	// PlayerRouter resolves a username to its target cell ID at login.
	// Optional — when nil, the gateway's default topology-based routing
	// applies. Forward-compat field; no consumer today.
	PlayerRouter PlayerRouter

	// Console configures the interactive admin console (optional). Pointer
	// so zero value (nil) means "not set" — ConsoleOpts contains func
	// fields and is not directly comparable.
	Console *ConsoleOpts

	// OnConsoleReady fires once the console is constructed. Receives the
	// owning *Process so admin commands can wire registries without
	// closure-capturing a pre-existing variable.
	OnConsoleReady func(p *Process, c *engine.Console)

	// AuthResolver validates a cookie/session token at WS-upgrade time
	// without touching the op channel. Stamped by
	// mmokit.RegisterAuthService after the auth Service finishes Init.
	// Read by the gateway's upgrade handler. Nil disables cookie-based
	// auth (the gateway will treat every connection as unauthenticated).
	AuthResolver auth.Resolver

	// AuthHTTPOpts is the HTTPOpts used by the auth service. The
	// gateway uses CookieName at WS-upgrade time to read the session
	// cookie. Stamped by mmokit.RegisterAuthService.
	AuthHTTPOpts auth.HTTPOpts

	// AnonymousAuth, when true, makes the gateway synthesize a session
	// for every WebSocket upgrade — random user_id, "anon-<connID>"
	// username, no token. The downstream pipeline (PlayerAssignment →
	// cell → OnPlayerJoin) runs unchanged, so examples and demos can
	// skip the entire auth/registration flow with one bool.
	//
	// IMPORTANT: only honored when AuthResolver is nil — registering
	// auth via mmokit.RegisterAuthService disables this fallback so
	// production games can never accidentally route around login. For
	// dev/example use only.
	AnonymousAuth bool
}

// IsRemoteHost reports whether the given role set represents a remote host —
// bare RoleHost with a non-empty CoordinatorAddr. Remote hosts dial the
// coordinator via MeshControl and receive cell assignments dynamically;
// in-process hosts (RoleHost paired with RoleCoordinator) create cells at
// Build() time.
func (c *Config) IsRemoteHost(roles Roles) bool {
	return len(roles) == 1 && roles.Has(RoleHost) && strings.TrimSpace(c.CoordinatorAddr) != ""
}

// collectServiceMigrations builds a postgres.Option list from every
// registered service Kind that declares Migrations. The label used for
// each kind's schema_migrations table is derived from the kind name
// (e.g. "service_echo"), so versioning is independent across services
// and from the engine's built-in schema. Order is the registry's stable
// Name-sorted order — deterministic across processes.
func collectServiceMigrations(reg *service.Registry) []postgres.Option {
	if reg == nil {
		return nil
	}
	kinds := reg.All()
	out := make([]postgres.Option, 0, len(kinds))
	for _, k := range kinds {
		if k.Migrations == nil {
			continue
		}
		root := k.MigrationsRoot
		if root == "" {
			root = "."
		}
		label := "service_" + k.Name
		out = append(out, postgres.WithExtraMigrations(k.Migrations, root, label))
	}
	return out
}

// ConsoleOpts provides game-specific console configuration.
// All fields are optional — omit what your game doesn't need.
type ConsoleOpts struct {
	Config          engine.Configurable // enables "config list/get/set"
	ConfigSave      func() error        // enables "config save"
	ConfigReset     func()              // enables "config reset"
	ConfigOnChanged func(field string)  // called on the game loop after "config set" mutates a field
}

// PlayerLocation tracks a player's current cell+host and whether the session is
// active or in a disconnected grace period. Single source of truth for
// username-based state.
//
// CellID is required for the gateway's reconnect-routing path: on a quick
// browser refresh the new connection's dispatchPlayerAssignment looks up
// the player's prior cell via this record so MsgPlayerAssignment{IsReconnect}
// reaches the same cell that holds the disconnected session. HostID is for
// cross-host coordination but is not sufficient on its own — host-only
// routing rejects the lookup as "node gone" and silently falls through to
// fresh-login, spawning a duplicate entity.
type PlayerLocation struct {
	HostID string
	CellID MeshCellID
	Active bool // false = disconnected (grace period)
}

// activeUser is the UUID-keyed view of an active player session. Tracks
// just enough state for the kick-old policy on duplicate authentication
// (find the gateway holding the existing session, send SE_KICKED, then
// tear it down).
type activeUser struct {
	UserID    uuid.UUID
	Username  string
	GatewayID string
	ConnID    uint32
	HostID    string
	CellID    MeshCellID
}

// Process manages multiple Cell instances, routes connections, and coordinates transfers.
type Process struct {
	// cmdsys.LocalProcessMarker satisfies cmdsys.LocalProcess so the
	// dispatcher can store *Process in Env.Local.Process without
	// requiring cmdsys to import universe.
	cmdsys.LocalProcessMarker

	Cells     map[MeshCellID]*Cell
	CellOwner map[CellID]MeshCellID // cell -> meshID
	Hosts     map[string]*Host      // hostID -> Host

	ConnMgr *net.ConnManager
	Log     *logger.Logger

	// Control holds RoleCoordinator state. Phase 1 wiring: pointer to
	// a ControlPlane whose fields mirror the raw Process fields
	// below. Phase 2 migration: callers move from coord.hostRegistry
	// to coord.Control.hostRegistry, then the raw fields unexport.
	Control *ControlPlane

	console      *engine.Console // nil if headless
	cfg          Config
	netIDAlloc   *NetIDAllocator
	partState    *partitionState // nil if dynamic partitioning disabled

	// invariantMode controls how invariant-check violations are handled.
	// Copied from Config.InvariantMode at New() time.
	invariantMode InvariantMode

	// commitLog is a bounded in-memory ring of CommitEvents covering
	// commit-plan steps, invariant violations, and host/session events.
	// Initialized in New() with Config.CommitLogCapacity (default 1024).
	commitLog *CommitLog

	// strictNetIDIndex mirrors Config.StrictNetIDIndex. Plumbed onto each
	// Stage at createNode time so spawn paths can consult the policy
	// without reaching back to the Process.
	strictNetIDIndex bool

	// blinkDetectorTicks mirrors Config.BlinkDetectorTicks, with zero
	// replaced by the 30-tick default at New() time.
	blinkDetectorTicks uint64

	systemDefs []engine.SystemDef
	built      bool
	roles      Roles // parsed from cfg.Mode at Build() time

	// coordEpoch is a fencing token that monotonically increases on every
	// coordinator restart. Every CoordMessage sent to a registered node
	// carries the current epoch; nodes track the highest-seen value and
	// reject anything lower. Prevents stale state from a restarted
	// coordinator from clobbering a fresh assignment run. Initialized
	// from time.Now().UnixNano() at New time so it
	// monotonically advances across restarts without persistence.
	coordEpoch uint64

	consoleOpts    *ConsoleOpts
	onConsoleReady func(c *engine.Console)

	// kindSpecs holds typed entity-kind registrations from
	// mmokit.RegisterKind[T]. Realized per-cell during createNode.
	kindSpecs []kindSpec

	// stageInitHooks holds per-stage setup callbacks registered via
	// Process.OnStageInit. Each hook fires once per Stage created by this
	// Process — both initial cells from Build() and stages created later
	// by dynamic partitioning (cell splits, host migrations). Used by
	// mmokit's All-suffix wrappers (HandleAll, OnWorldTickAll, etc.) to
	// auto-replay registrations onto every Stage.
	stageInitHooks []func(*Stage)

	// stateFactories holds per-stage state registrations from
	// mmokit.AddState[T]. Each cell's Stage instantiates one *T at
	// createNode time by calling every registered factory.
	stateFactories []stateFactory

	// onPlayerJoin / onPlayerLeave hold lifecycle hooks registered via
	// Process.OnPlayerJoin / OnPlayerLeave. Each cell's PlayerManager fans
	// these out on StateActive enter/exit during createNode.
	onPlayerJoin  []func(*engine.PlayerSession, *Stage)
	onPlayerLeave []func(*engine.PlayerSession, *Stage)

	mu            sync.RWMutex
	players       map[string]*PlayerLocation // username -> location (active + disconnected)
	// activeUsers indexes the same logical record by canonical user_id from
	// auth_users. The auth-service path stamps userID in the gateway's
	// authState; cell-side callers only have username, so both indexes are
	// kept consistent on every notifySession* mutation.
	activeUsers map[uuid.UUID]*activeUser
	// userIDByConn lets the gateway's kick-old path resolve a connID back to
	// the userID for the active session that needs to be torn down.
	userIDByConn  map[uint32]uuid.UUID
	sessionRoutes *sessionRoutes // connID -> cell routing; own mu, separate from c.mu

	spawnResolver SpawnResolver

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

	// vcm is the VirtualConnManager used in remote-host mode. It is
	// constructed in Build() when Config.IsRemoteHost(roles) is true and
	// passed as the engine's ConnSender to every cell created via
	// assignCellOnNode. Nil in `all` preset and coordinator modes.
	vcm *VirtualConnManager

	// httpServer is the engine-owned client HTTP server. Non-nil when the
	// process has the Gateway role and HTTPPort != -1. Started from Start()
	// after Build(); shut down at the top of Shutdown() before cells drain.
	httpServer *http.Server

	// adminHTTPServer is an optional admin HTTP server exposing /events,
	// /commands, and /metrics. Non-nil when Config.AdminListen is set.
	// Typically used on pure-coordinator processes that don't bind the
	// client HTTP listener but still need operational observability.
	adminHTTPServer *http.Server

	// C3: cross-process command dispatch.
	// registry and dispatcher are constructed in New so they are
	// available even in headless / pure-node / pure-gateway processes that
	// never create a console.
	registry   *cmdsys.Registry
	dispatcher *cmdsys.Dispatcher
	transport  *meshControlTransport
	resolver   *meshRouteResolver

	// hasPlayerDB is set by SetHasPlayerDB before Build(). Build() passes it
	// to RegisterLocal so the local host advertises the correct value even
	// when SetHasPlayerDB is called before any hosts are registered. Atomic
	// so the reconnect goroutine in mesh_control_client.runConnection can
	// read it concurrently with a post-Build SetHasPlayerDB call.
	hasPlayerDB atomic.Bool

	// playerDataLocator is the game-side hook for offline player lookups.
	// Installed via SetPlayerDataLocator; protected by mu.
	playerDataLocator PlayerDataLocator

	// services is the process-local catalog of service Kinds registered
	// via RegisterService. Populated before Build; consumed at Start to
	// instantiate Service instances for kinds named in cfg.ServiceKinds.
	// Always non-nil after New.
	services *service.Registry

	// opsSessions is the process-level connID→username map populated by
	// the gateway login flow and consumed by the OpRouter when dispatching
	// a service handler. Auto-created in New when RoleService is in the
	// role set and Config.OpRouter is unset.
	opsSessions *ops.PlayerSessions

	// coordServices is the cluster-wide service-instance roster. Only
	// initialized on processes with RoleCoordinator. Updated by
	// MeshControl ServiceAnnounce / ServiceLeave handlers and snapshotted
	// into PeerList broadcasts.
	coordServices *service.CoordRegistry

	// serviceRouting is the gateway/host-side cached view of the cluster-
	// wide op-code → kind → instance map. Built from PeerList. Always
	// non-nil after New so callers can range-over without nil checks.
	serviceRouting *service.RoutingIndex

	// runningServices is the set of Service instances created on this
	// process at Start. Keyed by Kind.Name. Empty when this process has
	// no RoleService. Used at Shutdown to drain in reverse-init order.
	runningServices map[string]*runningService

	// ownsDBStore is true when the engine opened cfg.DBStore itself
	// (via --postgres-url). Shutdown closes the store iff this is true
	// — games that pre-supply DBStore retain ownership.
	ownsDBStore bool

	// ClusterClock is the shared cluster clock used by this process.
	// For processes with a local coordinator (cfg.CoordinatorAddr == "")
	// it is pre-observed with offset=0 in New() so Observed() is true
	// immediately — no network handshake needed.
	//
	// For processes that dial a remote coordinator (remote hosts and
	// standalone gateways), the clock is left un-observed so the first
	// real CoordTimeSync snaps the offset to the correct value. Pre-
	// observing would flip `initialized=true` and force subsequent
	// broadcasts through the EMA branch, taking several broadcast
	// cycles to converge on the real offset.
	ClusterClock *ClusterClock

	// pendingAuthHook is set by InstallGatewayAuthHook (called from
	// mmokit.RegisterAuthService at facade time) and consumed by Build
	// after the Gateway is constructed. The struct is shared by reference:
	// the typed-op auth handlers hold the same pointer and call
	// NotifyXxx through it. The OnSuccess / OnLogout callbacks are filled
	// in by Build (so they can close over the live *Gateway). Nil when
	// auth isn't registered.
	pendingAuthHook *auth.GatewayHook

	// bus is the per-process typed pub/sub bus shared by every service
	// instance running on this Process. Initialized in New(); injected
	// into service.Context.Bus by serviceContext().
	//
	// Phase 1 fan-out is process-local only. Phase 3 will plumb a
	// peer-mesh dispatch callback so Publish[T] reaches remote subscribers.
	bus *service.Bus
}

// processIDFromConfig derives a stable per-process identifier used by the
// service.Bus for self-echo skip + diagnostics. Empty defaults to "local"
// for in-process dev servers.
func processIDFromConfig(cfg Config) string {
	if cfg.HostID != "" {
		return cfg.HostID
	}
	if cfg.GatewayID != "" {
		return cfg.GatewayID
	}
	return "local"
}

// New creates a coordinator with the given Config.
// Zero-value fields use sensible defaults (see Config field docs).
// Use AddSystem/SetWorld for Express-like setup, then call Build() or Start().
//
// If flag.Parse has not yet been called, New binds the engine-universal flags
// against cfg and parses the command line. Games that need to register their
// own flags OR mutate cfg based on parsed flag values should call
// cfg.BindFlags() + flag.Parse() themselves before calling New.
func New(cfg Config) *Process {
	if !flag.Parsed() {
		cfg.BindFlags()
		flag.Parse()
	}

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
	if cfg.VelQuantScale == 0 {
		cfg.VelQuantScale = 2000
	}
	if cfg.SizeQuantScale == 0 {
		cfg.SizeQuantScale = 500
	}
	if cfg.ConnManager == nil {
		cfg.ConnManager = net.NewConnManager()
	}
	if cfg.Logger == nil {
		cfg.Logger = logger.New()
	}
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = 8080
	}
	if cfg.WebDir == "" {
		cfg.WebDir = "embed"
	}
	if cfg.ClusterClockSyncInterval <= 0 {
		cfg.ClusterClockSyncInterval = 10 * time.Second
	}
	if cfg.ShutdownGracePeriod <= 0 {
		cfg.ShutdownGracePeriod = 5 * time.Second
	}
	if cfg.SettleWindow <= 0 {
		cfg.SettleWindow = settleWindow
	}

	if cfg.CellSize > 0 {
		coords.SetCellSize(cfg.CellSize)
	}

	c := &Process{
		Cells:         make(map[MeshCellID]*Cell),
		CellOwner:     make(map[CellID]MeshCellID),
		Hosts:         make(map[string]*Host),
		hostExecutors: make(map[string]*cellTransferExecutor),
		ConnMgr:       cfg.ConnManager,
		Log:           cfg.Logger,
		players:       make(map[string]*PlayerLocation),
		sessionRoutes: newSessionRoutes(),
		cfg:           cfg,
		coordEpoch:    uint64(time.Now().UnixNano()),
		bus:           service.NewBus(processIDFromConfig(cfg)),
	}
	c.invariantMode = cfg.InvariantMode
	c.strictNetIDIndex = cfg.StrictNetIDIndex
	c.blinkDetectorTicks = cfg.BlinkDetectorTicks
	if c.blinkDetectorTicks == 0 {
		c.blinkDetectorTicks = 30
	}
	// Replication Timeline Redesign: construct a shared cluster clock.
	// For processes with a local coordinator (no remote CoordinatorAddr)
	// pre-observe with offset=0 so Observed() is immediately true and no
	// cell needs to wait on a network handshake.
	//
	// For processes that dial a remote coordinator, skip the pre-observe:
	// the first real CoordTimeSync (sent by the coordinator immediately
	// after RegisterAck in Task C3) will snap the offset to the correct
	// value. A pre-observe here would flip `initialized=true` and force
	// the first real broadcast through the EMA branch, converging only
	// ~30% per sample rather than snapping.
	c.ClusterClock = NewClusterClock()
	if strings.TrimSpace(cfg.CoordinatorAddr) == "" {
		c.ClusterClock.Observe(uint64(time.Now().UnixMilli()), 0)
	}
	c.Log.RegisterCategories(EventCategories...)

	// Wire the bus's panic-recovery diagnostic into the logger so
	// panicking handlers leave a stack trace under "services:bus" rather
	// than silently vanishing.
	c.bus.SetPanicLogger(func(typeName, processID string, panicValue any, stack []byte) {
		c.Log.Log(CatServicesBus, "handler panic: type=%s proc=%s panic=%v\n%s",
			typeName, processID, panicValue, stack)
	})

	// Service framework: process-local Kind catalog and gateway-side
	// routing index initialized for every process — both are cheap and
	// every process either registers kinds (RoleService) or applies
	// PeerList updates (everyone). Coord-side CoordRegistry is created
	// lazily in Build when RoleCoordinator is present.
	c.services = service.NewRegistry()
	c.serviceRouting = service.NewRoutingIndex()

	// Auto-wire OpRouter for service-hosting processes that haven't
	// supplied one. The router exists primarily as the host for the typed-op
	// dispatcher's poll goroutine and connection-manager handle.
	if cfg.OpRouter == nil {
		c.opsSessions = ops.NewPlayerSessions()
		cfg.OpRouter = ops.NewRouter(cfg.ConnManager, c.opsSessions)
		c.cfg = cfg
	}

	commitCap := cfg.CommitLogCapacity
	if commitCap == 0 {
		commitCap = 1024
	}
	c.commitLog = newCommitLog(commitCap, c.Log)
	c.Control = newControlPlane(c.Log)
	c.Control.process = c
	c.Control.cellToHostMap = make(map[MeshCellID]string)
	c.orchestrator = newCellTransferOrchestrator(c)
	// Install the real dispatcher so production Begin* paths can ship
	// commands. Unit tests that want a fake dispatcher replace this via
	// orchestrator.setDispatcher in their own setup.
	c.orchestrator.setDispatcher(newCellTransferDispatcher(c))

	// C3: create the command registry and transport; the full dispatcher is
	// wired after Build() so the resolver has a fully-built coordinator to
	// query. A stub resolver is used during construction so InvokeLocal works
	// even before Build() (e.g., in tests that call New directly).
	c.registry = cmdsys.NewRegistry()
	c.transport = newMeshControlTransport(c)
	c.resolver = newMeshRouteResolver(c)
	c.dispatcher = cmdsys.NewDispatcher(cmdsys.DispatcherConfig{
		Registry:  c.registry,
		Resolver:  c.resolver,
		Transport: c.transport,
		Audit:     cmdsys.NoopAuditSink{},
		Process:   c,
	})

	// Register all builtin commands unconditionally — handler closures read
	// coord.Cells / coord.Hosts / coord.dispatcher at invocation time, so
	// registering before Build() populates them is safe. Remote-host and
	// standalone-gateway branches return early from Build() and would
	// otherwise miss console registration.
	c.registerAllBuiltins()

	// Install engine-default HandleClient handlers (e.g. Ping → Pong) via
	// the mmokit-side hook. The hook is nil in tests that build a Process
	// without importing mmokit; that's fine — those tests don't exercise
	// the typed-input client path.
	if EngineDefaultClientHandlers != nil {
		EngineDefaultClientHandlers(c)
	}

	return c
}

// registerAllBuiltins registers every coordinator builtin command into
// c.registry. Called once from New after the dispatcher is wired.
// All handlers resolve live state (cells, hosts, dispatcher) at invocation
// time, so it is safe to call before Build() populates the topology maps.
func (c *Process) registerAllBuiltins() {
	type regFn func(*cmdsys.Registry, *Process) error
	for _, fn := range []regFn{
		registerPerfBuiltins,
		registerCellBuiltins,
		registerHostBuiltins,
		registerGatewayBuiltins,
		registerSessionBuiltins,
		registerClusterBuiltins,
		registerLoadBuiltins,
		registerCommitLogBuiltins,
		registerServiceBuiltins,
	} {
		if err := fn(c.registry, c); err != nil {
			log.Printf("coordinator: registerAllBuiltins: %v", err)
		}
	}
	// Entity + player command registrars take only *Process (they access
	// coord.registry directly). Call them through one-liner adapters so the
	// signature mismatch stays out of the slice above.
	for _, fn := range []func(*Process) error{
		registerEntityCommands,
		registerPlayerCommands,
	} {
		if err := fn(c); err != nil {
			log.Printf("coordinator: registerAllBuiltins: %v", err)
		}
	}
}

// AddSystem registers a system definition. Systems are instantiated per-node
// during Build(). Use the mmokit factory helpers (NewPhysicsSystem,
// NewSpatialSystem, NewSystem, etc.) to build the SystemDef:
//
//	mmo.AddSystem(mmokit.NewPhysicsSystem())
//	mmo.AddSystem(mmokit.NewSystem(&BotSystem{}))
//	mmo.AddSystem(mmokit.NewSystem(&BotSystem{}).Named("AILogic"))
func (c *Process) AddSystem(def engine.SystemDef) {
	c.systemDefs = append(c.systemDefs, def)
}

// RegisterKindSpec registers a per-cell realizer for an entity kind. Called
// by mmokit.RegisterKind[T]; each registered realize fn runs against every
// cell's Stage during createNode. Internal API — game code uses
// mmokit.RegisterKind[T].
func (c *Process) RegisterKindSpec(realize func(*Stage)) {
	c.kindSpecs = append(c.kindSpecs, kindSpec{realize: realize})
}

// RealizeKindSpecs runs every registered kind spec against the given stage.
// Used by tests that build a stage outside the normal Build()/createNode
// path and still need entity kinds populated on the stage. Production code
// should use Build()/Start() which realizes kindSpecs automatically.
func (c *Process) RealizeKindSpecs(stage *Stage) {
	for _, spec := range c.kindSpecs {
		spec.realize(stage)
	}
}

// OnStageInit registers fn to fire once per Stage created by this Process —
// initial cells from Build() and stages created later by cell splits or
// host migrations. Use for per-stage setup like handler registration
// (mmokit.Handle) and tick callbacks (mmokit.OnWorldTick) that must be
// present on every cell.
//
// Mirrors RegisterKindSpec's auto-replay pattern but for non-kind setup.
// Safe to call before or after Build() — if the Process already has
// stages, fn fires immediately for each so the caller doesn't have to
// worry about registration order. Future stages created by partitioning
// fire their hooks at creation time (in createNode).
func (c *Process) OnStageInit(fn func(*Stage)) {
	c.stageInitHooks = append(c.stageInitHooks, fn)
	// Catch up: fire fn against any cells that already exist.
	// Holding c.mu prevents concurrent createNode/release from racing the
	// snapshot; fn is invoked outside the lock to avoid surprising callers.
	c.mu.RLock()
	stages := make([]*Stage, 0, len(c.Cells))
	for _, cell := range c.Cells {
		if cell != nil && cell.Stage != nil {
			stages = append(stages, cell.Stage)
		}
	}
	c.mu.RUnlock()
	for _, s := range stages {
		fn(s)
	}
}

// runStageInitHooks invokes every registered OnStageInit hook against
// stage. Called from createNode immediately after kindSpec realization so
// every cell-creation path (initial Build, split, migrate) shares one
// fan-out site.
func (c *Process) runStageInitHooks(stage *Stage) {
	for _, fn := range c.stageInitHooks {
		fn(stage)
	}
}

// RegisterStateFactory registers a per-stage state factory. Internal API
// — game code uses mmokit.AddState[T].
func (c *Process) RegisterStateFactory(name string, build func(*Stage) any) {
	c.stateFactories = append(c.stateFactories, stateFactory{typeName: name, build: build})
}

// OnPlayerJoin registers a callback fired when a player session enters
// StateActive on any cell, OR when a session reconnects after a grace-period
// disconnect (regardless of the resulting state — Active, Docked, Dead, etc.).
// Multiple hooks may be registered; they fire in registration order.
//
// The reconnect dispatch lets games handle "browser refresh while docked /
// dead / mid-dock" cleanly: the same hook receives the session, the game
// inspects sess.State to decide what welcome-back messages to send.
func (c *Process) OnPlayerJoin(fn func(*engine.PlayerSession, *Stage)) {
	c.onPlayerJoin = append(c.onPlayerJoin, fn)
}

// fireJoinHooks runs every registered OnPlayerJoin callback for the given
// session + stage. Internal — invoked from OnEnter(StateActive) for normal
// joins and from cell-level reconnect dispatch for reconnects into
// non-Active states (where OnEnter wouldn't fire the hooks).
func (c *Process) fireJoinHooks(s *engine.PlayerSession, stage *Stage) {
	for _, hook := range c.onPlayerJoin {
		hook(s, stage)
	}
}

// OnPlayerLeave registers a callback fired when a player session exits
// StateActive on any cell. Hooks fire in registration order, AFTER the
// runtime's default cleanup body (which marks any non-ghost player entity
// for removal and zeros session.Entity).
func (c *Process) OnPlayerLeave(fn func(*engine.PlayerSession, *Stage)) {
	c.onPlayerLeave = append(c.onPlayerLeave, fn)
}

// RegisterService records a service Kind so the engine can instantiate
// it when this process's role set includes RoleService and ServiceKinds
// names it. Must be called before Build(). Returns an error on
// duplicate Name or invalid descriptor.
func (c *Process) RegisterService(k service.Kind) error {
	if c.built {
		return fmt.Errorf("RegisterService: cannot register kind %q after Build()", k.Name)
	}
	return c.services.Register(k)
}

// HostForServiceKind returns the host_id of a live instance for the
// named service kind, or "" when no instance is registered. Used by
// cmdsys's RouteService route resolver to dispatch service-scoped
// console commands (e.g. chat.*, auth.*) to whichever process is
// hosting that service in the cluster.
//
// Resolution order:
//  1. Coordinator-side roster (coordServices) — present on processes
//     bearing RoleCoordinator. Authoritative cluster-wide view.
//  2. Gateway/host-side cached routing index (serviceRouting) — the
//     PeerList-driven mirror used on standalone gateways and remote
//     hosts. Lets a non-coordinator process resolve service routes
//     too (e.g. when admin commands originate from a gateway pane).
//
// First live instance wins; multi-instance kinds get an arbitrary but
// stable pick. Returns "" if no instance of `name` is live anywhere.
func (c *Process) HostForServiceKind(name string) string {
	if c.coordServices != nil {
		insts := c.coordServices.InstancesOfKind(name)
		if len(insts) > 0 {
			return insts[0].HostID
		}
	}
	if c.serviceRouting != nil {
		insts := c.serviceRouting.InstancesOfKind(name)
		if len(insts) > 0 {
			return insts[0].HostID
		}
	}
	return ""
}

// notifySessionActive is called when a player transitions to active on a host.
// Thread-safe — called from host game loops.
func (c *Process) notifySessionActive(username, hostID string, cellID MeshCellID) {
	c.mu.Lock()
	loc := c.players[username]
	if loc == nil {
		loc = &PlayerLocation{}
		c.players[username] = loc
	}
	loc.HostID = hostID
	loc.CellID = cellID
	loc.Active = true
	c.mu.Unlock()
}

// notifySessionDisconnected is called when a player disconnects (enters grace period).
// Thread-safe — called from host game loops.
func (c *Process) notifySessionDisconnected(username, hostID string, cellID MeshCellID) {
	c.mu.Lock()
	loc := c.players[username]
	if loc == nil {
		loc = &PlayerLocation{}
		c.players[username] = loc
	}
	loc.HostID = hostID
	loc.CellID = cellID
	loc.Active = false
	c.mu.Unlock()
}

// notifySessionRemoved is called when a player session is fully removed from a host.
// Thread-safe — called from host game loops.
func (c *Process) notifySessionRemoved(username string) {
	c.mu.Lock()
	delete(c.players, username)
	for uid, au := range c.activeUsers {
		if au.Username == username {
			delete(c.activeUsers, uid)
			delete(c.userIDByConn, au.ConnID)
		}
	}
	c.mu.Unlock()
}

// applyResolveSpawnReconnect populates resp with reconnect-routing info when
// activeUsers has a session for userID:
//
//   - Active=true (user online elsewhere) → kick old, leave resp untouched
//     (caller treats as fresh login).
//   - Active=false (in grace period) AND the cached cell is still owned →
//     stamp IsReconnect=true + the current host for the cell. After a merge
//     or migrate the cached CellID may be stale; HostForCellID returning ""
//     means the cell no longer exists, in which case the caller falls back
//     to fresh login spawn.
//
// Pulled out of handleInboundResolveSpawn so the policy is testable without
// constructing a real meshControlServer + gateway stream.
func (c *Process) applyResolveSpawnReconnect(userID uuid.UUID, resp *meshpb.SpawnResolved) {
	c.mu.RLock()
	loc := c.activeUserLocked(userID)
	c.mu.RUnlock()
	if loc == nil {
		return
	}
	if loc.Active {
		c.kickActiveUser(userID, "replaced by newer login")
		return
	}
	if loc.CellID == "" {
		return
	}
	currentHost := c.HostForCellID(loc.CellID)
	if currentHost == "" {
		return
	}
	resp.IsReconnect = true
	resp.TargetHostId = currentHost
	resp.TargetCellId = string(loc.CellID)
}

// registerAuthenticatedSession is called by the gateway after auth-service
// success and PlayerAssignment dispatch. It stamps the UUID-keyed activeUsers
// index so kickActiveUser can target the right gateway/connection on a
// duplicate-auth event.
func (c *Process) registerAuthenticatedSession(userID uuid.UUID, username, gatewayID string, connID uint32, hostID string, cellID MeshCellID) {
	if userID == uuid.Nil {
		return
	}
	c.mu.Lock()
	if c.activeUsers == nil {
		c.activeUsers = make(map[uuid.UUID]*activeUser)
	}
	if c.userIDByConn == nil {
		c.userIDByConn = make(map[uint32]uuid.UUID)
	}
	c.activeUsers[userID] = &activeUser{
		UserID:    userID,
		Username:  username,
		GatewayID: gatewayID,
		ConnID:    connID,
		HostID:    hostID,
		CellID:    cellID,
	}
	c.userIDByConn[connID] = userID
	c.mu.Unlock()
}

// activeUserLocked returns a *PlayerLocation-shaped view of the active user
// indexed by user_id. Caller must hold c.mu (read or write).
func (c *Process) activeUserLocked(userID uuid.UUID) *PlayerLocation {
	au := c.activeUsers[userID]
	if au == nil {
		// Fall back to username index if a notifySession* has populated the
		// per-username record without the UUID-keyed wrapper yet.
		return nil
	}
	if loc := c.players[au.Username]; loc != nil {
		return loc
	}
	return &PlayerLocation{HostID: au.HostID, CellID: au.CellID, Active: true}
}

// kickActiveUser tears down the existing session for userID and sends SE_KICKED
// to the old connection. No-op when no active session exists for that UUID.
// The new session is expected to install itself afterward via the normal
// dispatchPlayerAssignment path.
func (c *Process) kickActiveUser(userID uuid.UUID, reason string) {
	c.mu.Lock()
	au := c.activeUsers[userID]
	if au != nil {
		delete(c.activeUsers, userID)
		delete(c.userIDByConn, au.ConnID)
		delete(c.players, au.Username)
	}
	c.mu.Unlock()
	if au == nil {
		return
	}
	c.Log.Log(CatNetConn, "coordinator: kick old session user=%s conn=%d (reason=%s)", au.Username, au.ConnID, reason)
	if c.gateway != nil && c.gateway.id == au.GatewayID {
		c.gateway.kickConn(au.ConnID, reason)
	}
	// sessionRoutes cleanup so the upstream switch logic doesn't bounce traffic.
	c.sessionRoutes.Remove(SessionKey{GatewayID: au.GatewayID, ConnID: au.ConnID})
}

// ActiveUserHost returns the hostID for an active username, or "" if offline.
func (c *Process) ActiveUserHost(username string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if loc := c.players[username]; loc != nil && loc.Active {
		return loc.HostID
	}
	return ""
}

// ActiveUsers returns a snapshot of active usernames and their host IDs.
func (c *Process) ActiveUsers() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]string)
	for username, loc := range c.players {
		if loc.Active {
			result[username] = loc.HostID
		}
	}
	return result
}

// SystemDefs returns the registered system definitions (for testing/introspection).
func (c *Process) SystemDefs() []engine.SystemDef {
	return c.systemDefs
}

// Registry returns the cmdsys.Registry owned by this coordinator.
// Games and tests register commands here; the same registry backs the console
// and cross-process dispatch.
func (c *Process) CmdRegistry() *cmdsys.Registry {
	return c.registry
}

// CmdDispatcher returns the cmdsys.Dispatcher owned by this coordinator.
func (c *Process) CmdDispatcher() *cmdsys.Dispatcher {
	return c.dispatcher
}

// RegisterCommand registers a command with the coordinator's cmdsys registry.
// Convenience wrapper around CmdRegistry().Register().
func (c *Process) RegisterCommand(cmd cmdsys.Command) error {
	return c.registry.Register(cmd)
}

// Config returns a pointer to the process's underlying Config. Used by the
// mmokit facade (RegisterAuthService) and tests that need to inspect or
// adjust knobs after construction.
func (c *Process) Config() *Config { return &c.cfg }

// AppendExtraMigrations adds an additional migration filesystem to
// Config.ExtraMigrations. Called by service-registration helpers (e.g.
// mmokit.RegisterAuthService) so the auth schema lands at startup.
func (c *Process) AppendExtraMigrations(fsys fs.FS) {
	c.cfg.ExtraMigrations = append(c.cfg.ExtraMigrations, fsys)
}

// InstallGatewayAuthHook returns the *auth.GatewayHook the typed-op auth
// handlers will notify after each successful op. The OnSuccess / OnLogout
// callbacks are filled in by Build() once the gateway is constructed so
// they can close over the live *Gateway. Called by
// mmokit.RegisterAuthService at facade time.
//
// Returning the same pointer to subsequent callers is intentional — the
// hook is a per-process singleton; auth handler closures capture this
// pointer and call Notify* on it directly without round-tripping through
// the legacy proto-op router's post-handle observer.
func (c *Process) InstallGatewayAuthHook() *auth.GatewayHook {
	if c.pendingAuthHook == nil {
		c.pendingAuthHook = &auth.GatewayHook{Logger: c.Log}
	}
	return c.pendingAuthHook
}

// installPendingAuthHook fills in the GatewayHook's OnSuccess / OnLogout
// callbacks against the now-constructed Gateway. Called by Build() after
// the gateway is wired. No-op if RegisterAuthService wasn't called or the
// process doesn't have RoleGateway.
func (c *Process) installPendingAuthHook() {
	if c.pendingAuthHook == nil {
		return
	}
	if !c.roles.Has(RoleGateway) {
		return
	}
	if c.gateway == nil {
		return
	}
	c.pendingAuthHook.OnSuccess = func(connID uint32, userIDStr, username, token string, expiresAtMs int64) {
		uid, err := uuid.Parse(userIDStr)
		if err != nil {
			c.Log.Log(CatNetConn, "gateway: auth: bad user_id %q: %v", userIDStr, err)
			return
		}
		c.gateway.onAuthSuccess(connID, uid, username, token, expiresAtMs)
	}
	c.pendingAuthHook.OnLogout = func(connID uint32) {
		c.gateway.onAuthLogout(connID)
	}
	c.gateway.installAuthHook(c.pendingAuthHook)
}

// EntityHostForNetID returns the host ID owning the entity with the given
// network ID. Walks all local cells in all local hosts. For multi-process
// clusters this only resolves entities on the same process; remote entities
// require a broadcast-query RPC (deferred post-C3).
// Returns "" if the entity is not found on any local cell.
func (c *Process) EntityHostForNetID(netID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for hostID, host := range c.Hosts {
		for _, cell := range host.Cells {
			if cell.Stage == nil {
				continue
			}
			// Check the per-tick NetID map rebuilt by SpatialSystem.
			// The map is rebuilt each tick so accessing it from outside the
			// game loop is technically racy; however, EntityHostForNetID is
			// only called from console commands (which run via engine.RunOnLoop)
			// or from the route resolver (which is called during Invoke, also
			// typically from a game-loop-proxied context). For cross-process
			// tests where cells are run via goroutines, the map may lag by one
			// tick — acceptable for routing purposes.
			// Try the replication map (always populated, no tick dependency).
			if _, ok := cell.Stage.ReplicaNetIDs()[netID]; ok {
				return hostID
			}
		}
	}
	return ""
}

// LiveHostIDs returns the IDs of all hosts that are currently considered live.
// In all-in-one mode this is the single in-process host. In coordinator
// mode with a live HostRegistry these are the registered remote hosts.
func (c *Process) LiveHostIDs() []string {
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
func (c *Process) LiveGatewayIDs() []string {
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

// snapshotCellOwnership returns a point-in-time copy of the full cell→host
// map. Delegates to Control.AllOwnedCells which merges hostRegistry
// (distributed) and cellToHostMap (in-process). Used by the transfer
// orchestrator for rendezvous locality scoring. Retained as a named
// abstraction point; new code can call Control.AllOwnedCells directly.
func (c *Process) snapshotCellOwnership() map[string]string {
	ownership := make(map[string]string)
	c.Control.AllOwnedCells(func(k, v string) bool {
		ownership[k] = v
		return true
	})
	return ownership
}

// HostForCellID returns the host ID owning the given cell, or "" if no
// host owns it. The cellID argument MUST be in mesh form (use cell.MeshID()
// to obtain a typed MeshCellID); the compiler enforces this distinction.
// Delegates to Control.OwnerOf which unifies HostRegistry (authoritative
// in distributed deployments) and the local cellToHostMap (populated by
// Build() for local hosts and applyPeerList on remote hosts). Retained for
// existing callers; new code should use Control.OwnerOf directly to also
// get the (hostID, ok) bool.
func (c *Process) HostForCellID(cellID MeshCellID) string {
	h, _ := c.Control.OwnerOf(cellID)
	return h
}

// ConnManager returns the Process's connection manager.
func (c *Process) ConnManager() *net.ConnManager {
	return c.ConnMgr
}

// GatewayID returns the stable identifier of the local gateway, or "" if
// this process doesn't run RoleGateway. Used by service handlers (auth,
// chat, etc.) to stamp the GatewayID field on bus events when the
// publisher and gateway share a process. Safe to call after Build();
// before Build() the gateway is unwired and "" is returned.
func (c *Process) GatewayID() string {
	if c.gateway == nil {
		return ""
	}
	return c.gateway.id
}

// Roles returns the parsed role set for this Process. Populated by
// Build() from Config.Mode via ParseRoles. Safe to call after Build().
func (c *Process) Roles() Roles {
	return c.roles
}

// ServesClients reports whether this process terminates client WebSocket
// connections. True when RoleGateway is in the role set. Use this to gate
// the WebSocket + UDP listeners in main.go so that pure-control-plane and
// node-only processes don't collide with a standalone gateway on the same
// host.
func (c *Process) ServesClients() bool {
	return c.roles.Has(RoleGateway)
}

// BlinkDetectorTicks returns the configured recent-removals window
// (in ticks) for the ReplicationSystem blink detector.
func (c *Process) BlinkDetectorTicks() uint64 { return c.blinkDetectorTicks }

// InvariantMode returns the configured invariant-check mode.
func (c *Process) InvariantMode() InvariantMode { return c.invariantMode }

// Cfg returns a copy of the Process's effective configuration (with
// defaults applied). Read-only — modifying the returned value has no
// effect on the running Process.
func (c *Process) Cfg() Config { return c.cfg }

// HasInflightTransfers reports whether the orchestrator has any
// cell-transfer request (split / merge / migrate) currently in flight.
// Used by the debugBroadcaster to suppress topology sends during the
// brief window where the cellToHostMap holds transient intermediate
// state (e.g. merge's rename step). Returns false when the
// orchestrator hasn't been wired (early-Build, tests).
func (c *Process) HasInflightTransfers() bool {
	if c.orchestrator == nil {
		return false
	}
	return c.orchestrator.HasInflight()
}

// Protocol returns the user-supplied Config.Protocol unchanged. Callers in
// pkg/mmokit type-assert to *mmokit.Protocol via mmokit.ProtocolOf.
func (c *Process) Protocol() any { return c.cfg.Protocol }

// OpRouter returns the operations router from Config.OpRouter, or nil if unset.
func (c *Process) OpRouter() *ops.Router { return c.cfg.OpRouter }

// CellByID returns the *Cell with the given ID under c.mu.RLock(). The id
// argument MUST be in mesh form (use cell.MeshID() to obtain a typed MeshCellID);
// the compiler enforces this distinction. Use this from any goroutine that
// doesn't hold c.mu — the Cells map is mutated by orchestrator commits
// (split/merge/migrate) and concurrent reads without the lock are a data race.
func (c *Process) CellByID(id MeshCellID) *Cell {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Cells[id]
}

// CommitLog returns the in-memory commit log (may be nil on bare coord
// processes before Build). Used by ReplicationSystem blink-detector
// wiring.
func (c *Process) CommitLog() *CommitLog { return c.commitLog }

// kindSpec captures one mmokit.RegisterKind[T] call. The realize closure
// runs once per cell to materialize the kind's components against that
// cell's ecs.World.
type kindSpec struct {
	realize func(*Stage)
}

// stateFactory captures one mmokit.AddState[T] call. The build closure
// produces a typed value (boxed in any) for each cell's Stage.
type stateFactory struct {
	typeName string
	build    func(*Stage) any
}

// Build creates all nodes, wires topology, bridges, and metrics.
// Called automatically by Start() if not called explicitly.
func (c *Process) Build() {
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

	// Service framework cross-validation.
	hasServiceRole := roles.Has(RoleService)
	hasServiceKinds := len(cfg.ServiceKinds) > 0
	// RoleService without --services= is a silent no-op — having the
	// role is "this binary CAN host services" not "this process WILL".
	// The inverse (ServiceKinds without RoleService) is still an error
	// because it almost certainly means an operator typo.
	if hasServiceKinds && !hasServiceRole {
		panic(fmt.Errorf("coordinator: --services=%v is set but RoleService is missing — add 'service' to --mode (or use --mode=all)", cfg.ServiceKinds))
	}
	// v1 limitation: services share the gateway's OpRouter (op codes
	// dispatch by code-match, no cross-process forwarding). Standalone
	// service-host processes (RoleService alone) are deferred — they'd
	// need a VCM-equivalent to receive ClientInput frames. Warn only
	// when --services= actually names something to instantiate, so
	// `--mode=all` on a default dev-server doesn't spam.
	if hasServiceRole && hasServiceKinds && !roles.Has(RoleGateway) {
		c.Log.Log(CatMeshCell, "service: WARNING — RoleService without RoleGateway is not yet supported (v1 limitation); ops will not route to service handlers. Add 'gateway' to --mode for colocated service hosting.")
	}
	// Run registry-level validation regardless of role: registrations must
	// be internally consistent even on processes that don't host services
	// (they may share the same binary). RequiresDB is enforced only for
	// kinds actually being instantiated (per-kind check below).
	if err := c.services.Validate(); err != nil {
		panic(fmt.Errorf("coordinator: %w", err))
	}
	// Auto-open Postgres when --postgres-url is set and the game hasn't
	// already supplied a *postgres.Store via DBStore. The engine owns
	// the lifecycle from here on — Shutdown closes it. Games that need
	// to share a Store with non-engine code (e.g. cmd/server's PlayerDB
	// uses Players() before Build) can still open it themselves and
	// pass via cfg.DBStore; that path skips the auto-open.
	if cfg.DBStore == nil && cfg.PostgresURL != "" {
		extras := collectServiceMigrations(c.services)
		for i, fsys := range cfg.ExtraMigrations {
			label := fmt.Sprintf("extra_%d", i)
			extras = append(extras, postgres.WithExtraMigrations(fsys, ".", label))
		}
		store, err := postgres.Open(context.Background(), cfg.PostgresURL, extras...)
		if err != nil {
			panic(fmt.Errorf("coordinator: open postgres: %w", err))
		}
		cfg.DBStore = store
		c.cfg = cfg
		c.ownsDBStore = true
	}

	// Auto-register the engine's per-player debug-flag console
	// commands (`debug.grant/revoke/list/features`) when DBStore is
	// available. These are mmokit-owned commands, not game-specific —
	// games never call this directly.
	if cfg.DBStore != nil {
		if err := registerDebugCommands(c); err != nil {
			panic(fmt.Errorf("coordinator: register debug commands: %w", err))
		}
	}

	if hasServiceRole {
		selected, err := c.services.SelectKinds(cfg.ServiceKinds)
		if err != nil {
			panic(fmt.Errorf("coordinator: invalid --services list: %w", err))
		}
		for _, k := range selected {
			if k.RequiresDB && cfg.DBStore == nil {
				panic(fmt.Errorf("coordinator: kind %q requires DB but Config.PostgresURL is empty", k.Name))
			}
		}
	}
	// Coordinator-side service roster — populated by MeshControl
	// announce/leave handlers and by in-process startServices when
	// colocated.
	if roles.Has(RoleCoordinator) {
		c.coordServices = service.NewCoordRegistry()
	}

	// Log categories up-front so every subsequent log line in Build() —
	// including MeshControl listen, host registration, etc. — respects
	// the --log flag and StartupCategories. Previously this ran at the
	// END of Build(), silently dropping all mode-setup logs that fire
	// during the coordinator/host initialization path.
	if c.cfg.LogCategories != "" {
		c.Log.EnableFromFlag(c.cfg.LogCategories)
	}
	c.Log.Enable(StartupCategories...)

	// Console + OnConsoleReady from Config. (PlayerRouter has no consumer
	// today — gateway uses topology-based routing — but the field is kept
	// for forward compat.)
	c.consoleOpts = c.cfg.Console
	if c.cfg.OnConsoleReady != nil {
		c.onConsoleReady = func(con *engine.Console) {
			c.cfg.OnConsoleReady(c, con)
		}
	}

	// Bare RoleHost alone represents a remote host — it dials the coordinator.
	// Anything else requires the dial address to be empty OR would be caught
	// by control-plane setup. Check here, before any control-plane or remote
	// dialing runs, to fail fast with a clear operator message.
	if len(roles) == 1 && roles.Has(RoleHost) && !c.cfg.IsRemoteHost(roles) {
		panic(`--mode=host alone requires --coordinator-addr=HOST:PORT (was --mode=node)`)
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
	pureCoordinator := len(roles) == 1 && roles.Has(RoleCoordinator)
	coordGatewayOnly := roles.Has(RoleCoordinator) && roles.Has(RoleGateway) && !roles.Has(RoleHost)
	if roles.Has(RoleCoordinator) && (pureCoordinator || coordGatewayOnly || cfg.ControlListen != "") {
		c.startControlPlane()
	}

	// Remote host: dial the coordinator, register, receive cell assignments
	// dynamically. Spelled `--mode=host --coordinator-addr=HOST:PORT` on the
	// CLI; distinguished from in-process hosts by Config.IsRemoteHost(roles).
	if c.cfg.IsRemoteHost(roles) {
		c.buildRemoteHost()
	}

	// RoleGateway (standalone): dial a remote coordinator, no local cells.
	// This branch only runs when RoleGateway is set without RoleCoordinator.
	if c.isStandaloneGateway() {
		c.buildStandaloneGateway()
	}

	// RoleGateway (embedded): coordinator is present; create an in-process
	// gateway. Auth-service responses drive PlayerAssignment dispatch via
	// the GatewayHook installed by mmokit.RegisterAuthService.
	//
	// Two sub-modes:
	//
	//   coord+gateway+host — the classic `all` preset. Every cell is colocated
	//       in-process so the gateway dispatches straight to cell.Inbox.
	//
	//   coord+gateway  (no RoleHost) — coordinator control plane + embedded
	//       gateway, but every cell lives on a remote `--mode=host` process.
	//       The gateway needs its own HostNetwork to (a) dial remote hosts
	//       for outbound PlayerAssignment/ClientInput frames and (b) receive
	//       ClientFrames back from nodes.
	if roles.Has(RoleGateway) && roles.Has(RoleCoordinator) {
		gwID := cfg.GatewayID
		if gwID == "" {
			gwID = InprocGatewayID
		}
		c.gateway = &Gateway{
			id:         gwID,
			connMgr:    c.ConnMgr,
			log:        c.Log,
			coord:      c,
			process:    c,
			cfg:        &c.cfg,
			sessions:   make(map[uint32]*localSession),
			authStates: make(map[uint32]connAuthState),
			topology:   newCachedTopology(c),
			tickRate:   uint32(cfg.TickRate),
		}
		c.gateway.spawnResolver = c.spawnResolver
		c.gateway.sessionRoutes = c.sessionRoutes
		// c.httpServer is nil here — startHTTPListener() runs in Start() after Build().
		// Phase 2 must either re-mirror after Start() calls startHTTPListener() or
		// move the httpServer lifecycle onto Gateway directly.
		c.gateway.httpServer = c.httpServer

		// Coord+gateway without a local host: needs its own HostNetwork so
		// remote nodes can be reached for outbound dispatch and can dial back
		// with ClientFrames. Mirrors the standalone gateway wiring above, but
		// with coord != nil so the gateway can read coord state directly
		// instead of going through meshGatewayClient.
		var gwGrpcAddr string
		if !roles.Has(RoleHost) {
			gwHost := NewHost(gwID)
			gwHost.Log = c.Log
			gwHost.coord = c
			c.Hosts[gwID] = gwHost
			hn, err := NewHostNetwork(gwHost, ":0", c.Log, c.cfg.ShutdownGracePeriod)
			if err != nil {
				panic(fmt.Errorf("coordinator: coord+gateway NewHostNetwork: %w", err))
			}
			gwHost.Network = hn
			hn.SetCoord(c)
			c.gateway.hostNetwork = hn
			hn.SetGateway(c.gateway)
			gwGrpcAddr = hn.Addr()
			c.Log.Log(CatNetConn, "coordinator: embedded gateway %q (grpc=%s) — no local host, routes via MeshData", gwID, gwGrpcAddr)
		} else {
			c.Log.Log(CatNetConn, "coordinator: in-process gateway %q created", gwID)
		}

		// Publish the local gateway into gatewayRegistry so broadcastPeerList
		// includes it in GatewayRecord lists sent to remote nodes, and so
		// cluster.overview / gateway.list see the embedded gateway. Fires
		// in every topology that creates c.gateway (all preset, coord+gateway
		// without host). grpcAddr is empty when there's no HostNetwork (the
		// all-preset shares the host's listener and has no separate gateway
		// gRPC endpoint).
		if c.gatewayRegistry != nil {
			c.gatewayRegistry.RegisterLocal(gwID, gwGrpcAddr)
		}
		// Now that the gateway exists, install the auth response hook
		// queued by mmokit.RegisterAuthService at facade time. No-op if
		// auth wasn't registered.
		c.installPendingAuthHook()
		c.gateway.connMgr.OnUpgrade = c.gateway.onWSUpgrade
	}

	// RoleHost (local): create in-process cells with static (pre-Build) assignment.
	// Remote hosts take the buildRemoteHost() path above and skip this block.
	if roles.Has(RoleHost) && !c.cfg.IsRemoteHost(roles) {
		// Single-host-per-process: exactly one local Host. HostID comes from
		// cfg.HostID (tests + deterministic production) or defaults to "local"
		// for single-process dev mode. Multi-host testing lives in the
		// distributedFixture (multi-process-in-binary).
		hostID := cfg.HostID
		if hostID == "" {
			hostID = "local"
		}

		h := NewHost(hostID)
		h.Log = c.Log
		c.Hosts[hostID] = h
		c.hostExecutors[hostID] = newCellTransferExecutor(c, h)
		h.netIDAlloc = c.netIDAlloc
		h.systemDefs = c.systemDefs
		h.executor = c.hostExecutors[hostID]
		h.vcm = c.vcm
		h.coord = c
		hosts := []*Host{h}

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
				targetHost := hosts[hostIdx%len(hosts)]
				cell2, systems := c.createNode(cell, spatialCellSize, targetHost)
				targetHost.AddCell(cell2.Cell, cell2)
				c.Control.mu.Lock()
				c.Control.cellToHostMap[cell2.MeshID] = targetHost.ID
				c.Control.mu.Unlock()
				hostIdx++
				setups = append(setups, nodeSetup{cell2, systems})
			}
		}

		// Compute topology and wire neighbors
		c.Control.Topology = ComputeTopology(cells, coords.CellSize)
		for cell, neighborCells := range c.Control.Topology.Neighbors {
			nodeID := c.CellOwner[cell]
			node := c.Cells[nodeID]
			for _, nc := range neighborCells {
				neighborID := c.CellOwner[nc]
				node.Neighbors[neighborID] = c.Cells[neighborID]
			}
		}

		// Auto-register /metrics endpoint on the ConnManager's HTTP mux.
		cfg.ConnManager.Handle("/metrics", c.MetricsHandler())
		// Auto-register /commands introspection endpoints.
		cfg.ConnManager.Handle("/commands", handleCommandList(c.registry))
		cfg.ConnManager.Handle("/commands/", handleCommandDescribe(c.registry))
		cfg.ConnManager.Handle("/events", handleCommitLogEvents(c.commitLog))

		c.Log.Log(CatMeshCell, "coordinator: created %d cells, topology computed", len(c.Cells))

		// When the control plane is running (pure coordinator mode OR
		// RoleCoordinator + non-empty ControlListen) AND this process also
		// has RoleHost, auto-register each local host in the HostRegistry
		// so "host list" and PeerList broadcasts include it alongside any
		// remote nodes that join. Local hosts participate in rendezvous
		// rebalance on equal footing with remote nodes.
		if c.hostRegistry != nil {
			for _, h := range hosts {
				var ownedCells []MeshCellID
				for _, cell := range h.Cells {
					ownedCells = append(ownedCells, cell.MeshID)
				}
				grpcAddr := ""
				if h.Network != nil {
					grpcAddr = h.Network.Addr()
				}
				c.hostRegistry.RegisterLocal(h.ID, grpcAddr, ownedCells, c.hasPlayerDB.Load())
				if c.commitLog != nil {
					c.commitLog.Append(CommitEvent{
						Kind:    EventHostJoin,
						Step:    "registered-local",
						HostIDs: []string{h.ID},
						Success: true,
					})
				}
			}
			c.Log.Log(CatMeshCell, "coordinator: %d local host(s) registered with control plane", len(hosts))
		}

		// Two-phase init: World.Init() first (registers entity kinds, login handlers),
		// then system Init() (discovers replicators, creates query filters).
		for _, s := range setups {
			s.cell.Stage.Init()
		}
		for _, s := range setups {
			initSystems(s.systems)
		}
	}
	// For coordinator and remote-host modes, cell creation and host wiring are
	// driven by control-plane events (CellAssign / CellRelease).
	// Log categories were enabled at the top of Build() so every
	// lifecycle log line above respects the --log flag.

}

// isStandaloneGateway returns true when this process is a gateway without a
// colocated coordinator — i.e. it dials a remote coordinator for topology.
func (c *Process) isStandaloneGateway() bool {
	return c.roles.Has(RoleGateway) && !c.roles.Has(RoleCoordinator)
}

// buildRemoteHost wires a remote host that dials a coordinator for cell
// assignments. Called from Build() when Config.IsRemoteHost(roles) is true.
func (c *Process) buildRemoteHost() {
	cfg := c.cfg

	hostID := cfg.HostID
	if hostID == "" {
		// Auto-generate. UnixNano is monotonic enough for S4 tests.
		hostID = fmt.Sprintf("host-%d", time.Now().UnixNano())
	}

	host := NewHost(hostID)
	host.Log = c.Log
	c.Hosts[hostID] = host
	c.hostExecutors[hostID] = newCellTransferExecutor(c, host)

	hn, err := NewHostNetwork(host, ":0", c.Log, c.cfg.ShutdownGracePeriod)
	if err != nil {
		panic(fmt.Errorf("coordinator: remote host mode NewHostNetwork: %w", err))
	}
	host.Network = hn
	hn.SetCoord(c)

	vcm := NewVirtualConnManager(hn, c.Log)
	hn.SetVCM(vcm)
	c.vcm = vcm

	c.controlClient = newMeshControlClient(c, hostID, cfg.CoordinatorAddr)
	c.Control.controlClient = c.controlClient
	// Start never errors — the reconnect loop spawns in the
	// background and handles dial failures via exponential
	// backoff. The node will keep trying to reach the coordinator
	// forever; operators can Ctrl+C to stop.
	_ = c.controlClient.Start(context.Background())

	host.netIDAlloc = c.netIDAlloc
	host.systemDefs = c.systemDefs
	host.executor = c.hostExecutors[hostID]
	host.vcm = c.vcm
	host.coord = c
}

// buildStandaloneGateway wires a standalone gateway that dials a remote
// coordinator. Called from Build() when isStandaloneGateway() is true.
func (c *Process) buildStandaloneGateway() {
	cfg := c.cfg

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
	gwHost.coord = c
	c.Hosts[gwID] = gwHost
	hn, err := NewHostNetwork(gwHost, ":0", c.Log, c.cfg.ShutdownGracePeriod)
	if err != nil {
		panic(fmt.Errorf("coordinator: gateway mode NewHostNetwork: %w", err))
	}
	gwHost.Network = hn
	hn.SetCoord(c)

	c.gateway = &Gateway{
		id:           gwID,
		connMgr:      c.ConnMgr,
		log:          c.Log,
		coord:        nil, // standalone: no direct coordinator reference
		process:      c,   // local process backref — always set, used for cmdsys dispatch + serviceRouting
		cfg:          &c.cfg,
		sessions:     make(map[uint32]*localSession),
		authStates:   make(map[uint32]connAuthState),
		topology:     newCachedTopology(nil), // populated by PeerList broadcasts
		hostNetwork:  hn,
		defaultSpawn: cfg.DefaultSpawn,
		spawnOrch:    newSpawnOrchestrator(),
		tickRate:     uint32(cfg.TickRate),
		// wsAddr: TODO — plumb via Config.GatewayWSAddr when flag lands
	}
	c.gateway.spawnResolver = c.spawnResolver
	c.gateway.sessionRoutes = c.sessionRoutes
	// c.httpServer is nil here — startHTTPListener() runs in Start() after Build().
	// Phase 2 must either re-mirror after Start() calls startHTTPListener() or
	// move the httpServer lifecycle onto Gateway directly.
	c.gateway.httpServer = c.httpServer
	hn.SetGateway(c.gateway)

	c.gateway.controlClient = newMeshGatewayClient(c.gateway, cfg.CoordinatorAddr)
	_ = c.gateway.controlClient.Start(context.Background())

	// Install any auth-response hook queued by mmokit.RegisterAuthService
	// at facade time, now that the gateway exists.
	c.installPendingAuthHook()
	c.gateway.connMgr.OnUpgrade = c.gateway.onWSUpgrade

	c.Log.Log(CatNetConn, "coordinator: standalone gateway %q -> coordinator %s (grpc=%s)", gwID, cfg.CoordinatorAddr, hn.Addr())
}

// initSystems calls Init() on each system that implements it, then
// triggers the query build phase for any system whose queries were
// auto-discovered via BindQueries.
func initSystems(systems []engine.System) {
	type initializable interface{ Init() }
	type queryBuilder interface{ BuildQueries() }
	for _, sys := range systems {
		if init, ok := sys.(initializable); ok {
			init.Init()
		}
		if qb, ok := sys.(queryBuilder); ok {
			qb.BuildQueries()
		}
	}
}

// startControlPlane starts the MeshControl gRPC server, creates the
// HostRegistry, GatewayRegistry, meshControlServer, and AssignmentEngine,
// and wires them together. Shared between coordinator mode and `all` preset
// mode (when Config.ControlListen is set). Panics on listen failure.
func (c *Process) startControlPlane() {
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
	c.Control.hostRegistry = c.hostRegistry
	c.hostRegistry.SetOwnershipChangedCallback(func(cellID MeshCellID) {
		c.Control.rebuildTopologyForCell(cellID)
	})
	c.gatewayRegistry = NewGatewayRegistry(c.Log)
	c.Control.gatewayRegistry = c.gatewayRegistry

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
	c.Control.controlServer = c.controlServer
	c.controlGrpcServer = grpcSrv
	c.Control.controlGrpcServer = c.controlGrpcServer
	c.controlListener = listener
	c.Control.controlListener = c.controlListener
	c.assignmentEngine = eng
	c.Control.assignmentEngine = c.assignmentEngine

	// Start the settle-window loop. It runs until Process.Shutdown()
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
//
// owningHost determines the bridge type: when non-nil and the host has a
// HostNetwork, the cell gets a grpcBridge for cross-host dispatch;
// otherwise it gets a plain cellBridge (zero gRPC overhead).
func (c *Process) createNode(cell CellID, spatialBucketSize float32, owningHost *Host, fromSplit ...bool) (*Cell, []engine.System) {
	cfg := c.cfg
	platformCfg := engine.Config{TickRate: cfg.TickRate}

	id := string(cell.MeshID())
	// In remote-host mode, cells use VirtualConnManager as their ConnSender so
	// that outbound client frames are forwarded to the gateway via MeshData.
	// All-in-one mode uses the real ConnManager which holds the WebSocket listener.
	var connSender net.ConnSender = cfg.ConnManager
	if c.vcm != nil {
		connSender = c.vcm
	}
	eng := engine.New(platformCfg, connSender, cfg.Logger)
	eng.SetNetIDBase(c.netIDAlloc.Allocate())

	events := make(chan net.PlayerEvent, 64)

	base := NewStage(eng, cell, cfg.AoIRadius, nil)

	// Wire the typed client-input dispatch path (channel 0x00 post
	// Plan 1 Phase 5 unification; mmokit.HandleClient). All
	// client-originated inputs flow through this path — the legacy
	// OnInput / OnInputWith / InputBinding surface was deleted in
	// Plan G Phase 7.
	eng.SetClientInputTick(base.DispatchClientInput)
	base.spatialGrid = spatial.NewHashGrid(spatialBucketSize)
	if len(fromSplit) > 0 && fromSplit[0] {
		base.fromSplit = true
	}

	base.coord = c
	base.strictNetIDIndex = c.strictNetIDIndex
	if c.ClusterClock != nil {
		base.clusterClock = c.ClusterClock
	}

	// Topology-transparent protocol: no default hook ever synthesizes a
	// client-visible event on cell rename or player transfer. The
	// framework only needs `onPlayerTransferReceived` to wire the
	// destination-side PlayerSession so the engine's InputRouter
	// dispatches to the right entity — client state reset is driven
	// purely by the FRAME_FLAG_FRESH_SNAPSHOT bit the destination
	// cell's ReplicationSystem sets on its first frame for the
	// migrated conn. Games that need custom post-transfer logic
	// override via SetOnPlayerTransferReceived / SetOnCellBoundsChanged.
	//
	// Historical defaults sent SE_PLAYER_SPAWNED here, which caused the
	// client to wipe `state.entities` and `state.cellTopology` on every
	// merge rename — the 3+ tick blank visible on the screen. Removed
	// in favor of the topology-transparent delta stream.
	base.onPlayerTransferReceived = func(entity ecs.Entity, frame *TransferFrame) {
		// Two callers fire this hook with different session-state
		// preconditions:
		//
		//  - cell_transfer_executor.populateCell (split/merge/migrate):
		//    the session is NOT yet registered when SpawnFromTransferCore
		//    runs, so the by-connID lookup returns nil and this hook
		//    no-ops. populateCell registers the session AFTER spawn and
		//    sets DebugFlags from spawnedFrame itself.
		//
		//  - cell.MsgHandoff → drainPendingPromotes (boundary handoff):
		//    the session IS pre-registered by RegisterTransferSession
		//    BEFORE SpawnLiveFromTransfer runs. This hook is the only
		//    point that re-attaches the spawned entity AND restores the
		//    DebugFlags bitmask carried in the transfer frame. Without
		//    the DebugFlags assignment, every cross-cell walk would
		//    silently zero a player's debug grants and the
		//    debugBroadcaster would stop sending SE_DEBUG_INFO updates
		//    to that connID — topology overlay freezes after the first
		//    boundary crossing even though split/merge keep firing.
		if s := eng.Players.ByConnID(frame.ConnID); s != nil {
			s.Entity = entity
			s.DebugFlags = engine.DebugFlag(frame.DebugFlags)
		}
	}

	// Realize all registered kind specs against this cell's Stage. Must run
	// BEFORE the world factory so game-defined worlds can rely on
	// EntityKindDefs being populated when their constructor runs (e.g. to
	// spawn initial cell content via SpawnEntity + WithComponents). Also
	// before system creation so systems like NetworkSystem see a fully-
	// populated EntityKindDefs.
	for _, spec := range c.kindSpecs {
		spec.realize(base)
	}

	// Fire OnStageInit hooks against this stage. Runs after kindSpec
	// realization so hooks can rely on EntityKindDefs being populated.
	// Used by mmokit.HandleAll / OnWorldTickAll / etc. to auto-replay
	// per-stage handler & tick registrations onto every Stage —
	// initial Build, split-created, and migrate-created.
	c.runStageInitHooks(base)

	// Register the default entity-cleanup Action on the universal "leaving"
	// transitions BEFORE the world factory runs. The factory's call to
	// gw.Players.AddTransitions overrides these for games that need bespoke
	// behavior (e.g. the space game's disconnectKeepEntity for grace-period
	// reconnect) — last-writer-wins is the contract. Games that don't define
	// custom Actions (e.g. 4node-basic) inherit safe defaults.
	//
	// CRITICAL: do NOT put this cleanup in OnExit(StateActive). OnExit fires
	// on EVERY exit from Active — including transitions to game-defined
	// states like Docking where the entity must persist. The transition
	// Action fires only on the specific destinations registered here.
	defaultLeaveCleanup := func(s *engine.PlayerSession, _ *engine.PlayerManager) {
		if s.Entity == (ecs.Entity{}) || !base.ECSWorld().Alive(s.Entity) {
			return
		}
		if base.IsGhost(s.Entity) {
			return
		}
		base.MarkForRemoval(s.Entity)
		s.Entity = ecs.Entity{}
	}
	eng.Players.AddTransitions([]engine.StateTransition{
		{From: engine.StateActive, To: engine.StateTransferring, Action: defaultLeaveCleanup},
		{From: engine.StateActive, To: engine.StateDisconnected, Action: defaultLeaveCleanup},
	})

	// Instantiate registered per-stage state. Runs after kind realization
	// so factories can read the cell's EntityKindDefs if they need to,
	// and BEFORE OnState wiring so user hooks can call mmokit.State[T]
	// from their first invocation.
	if len(c.stateFactories) > 0 {
		if base.state == nil {
			base.state = make(map[string]any, len(c.stateFactories))
		}
		for _, sf := range c.stateFactories {
			base.state[sf.typeName] = sf.build(base)
		}
	}

	// Wire lifecycle hooks into this cell's PlayerManager.
	//
	// Entity cleanup on session exit is handled by the default Action
	// registered above on Active→Transferring / Active→Disconnected — it
	// runs BEFORE OnExit so user-supplied OnPlayerLeave hooks observe an
	// already-cleaned-up session. Game-defined "leaving" transitions
	// (e.g. Active→Dead) need their own Action; transitions where the
	// entity must persist (e.g. Active→Docking) intentionally have no
	// Action and the entity stays alive.
	//
	// Games MUST register player-spawn / reconnect logic via
	// Process.OnPlayerJoin, not by calling gw.Players.OnState(StateActive)
	// directly. PlayerManager.OnState is last-writer-wins, and the world
	// factory runs above (line ~1699) — any direct OnState(StateActive)
	// from a game would be silently overwritten by this block. The
	// OnPlayerJoin / OnPlayerLeave path is the supported API.
	{
		joinHooks := c.onPlayerJoin
		leaveHooks := c.onPlayerLeave
		pm := eng.Players
		pm.OnState(engine.StateActive, engine.StateCallbacks{
			OnEnter: func(s *engine.PlayerSession, _ *engine.PlayerManager) {
				// Hydrate persistent debug flags from the configured
				// PlayerRepository before user hooks fire so handlers see
				// the effective flag set. OR-semantics means we can run
				// this on transfer-receive too without clobbering the
				// flags that traveled in TransferFrame.DebugFlags.
				if s.Username != "" && c.cfg.DBStore != nil {
					if names, err := c.cfg.DBStore.Players().LoadDebugFlags(context.Background(), s.Username); err == nil {
						s.DebugFlags |= engine.DebugFlagsFromNames(names)
					}
					// ErrNotFound for first-time players is normal — no flags to load.
				}
				for _, hook := range joinHooks {
					hook(s, base)
				}
			},
			OnExit: func(s *engine.PlayerSession, _ *engine.PlayerManager) {
				for _, hook := range leaveHooks {
					hook(s, base)
				}
			},
		})
	}

	// Phase 1: create systems and inject dependencies. Init() is deferred
	// to Build() after World.Init() so that systems like NetworkSystem can
	// discover entity kinds registered during World.Init().
	gameSystems := make([]engine.System, len(c.systemDefs))
	systemNames := make([]string, len(c.systemDefs))
	for i, def := range c.systemDefs {
		sys := def.Factory()

		type depsInjectable interface {
			SetDeps(w *ecs.World, eng *engine.Engine)
		}
		if di, ok := sys.(depsInjectable); ok {
			di.SetDeps(eng.ECS, eng)
		}

		type stageInjectable interface {
			InitStage(s *Stage)
		}
		if si, ok := sys.(stageInjectable); ok {
			si.InitStage(base)
		}

		type queryBinder interface {
			BindQueries(outer any)
		}
		if qb, ok := sys.(queryBinder); ok {
			qb.BindQueries(sys)
		}

		gameSystems[i] = sys
		systemNames[i] = def.Name
	}

	eng.OnEntityRemoved = func(e ecs.Entity) {
		base.spatialGrid.Deregister(e)
		if base.netIDIdx != nil && base.netIDMap.HasAll(e) {
			netID := base.netIDMap.Get(e).ID
			base.netIDIdx.Exit(netID)
		}
	}

	{
		bs := &BoundarySystem{stage: base}
		bs.SetDeps(eng.ECS, eng)
		bs.BindQueries(bs)
		gameSystems = append(gameSystems, bs)
		systemNames = append(systemNames, "CellBoundary")
	}

	node := &Cell{
		MeshID:    cell.MeshID(),
		Cell:      cell,
		Engine:    eng,
		Stage:     base,
		Inbox:     make(chan CellMessage, 256),
		Events:    events,
		Neighbors: make(map[MeshCellID]*Cell),
		Log:       cfg.Logger,
	}

	// Wire session callbacks. notifySessionActive/Disconnected take both the
	// host ID (for cross-host coordination) and the cell ID (for the
	// gateway's reconnect-routing path: a quick browser refresh's new conn
	// dispatches PlayerAssignment{IsReconnect} to coord.players[user].CellID
	// to find the cell holding the lingering disconnected session). Both
	// resolve at call time so merge renames + post-create migrations are
	// reflected.
	eng.Players.SetSessionCallbacks(
		func(username string) { c.notifySessionActive(username, c.HostForCellID(node.MeshID), node.MeshID) },
		func(username string) { c.notifySessionDisconnected(username, c.HostForCellID(node.MeshID), node.MeshID) },
		func(username string) { c.notifySessionRemoved(username) },
	)

	gameHooks := base.Hooks()
	tickDt := float32(1.0 / float32(platformCfg.TickRate))
	mergedHooks := engine.Hooks{
		OnConnect:    gameHooks.OnConnect,
		OnDisconnect: gameHooks.OnDisconnect,
		PreFlush: func() {
			// Fire stage-registered per-tick callbacks (mmokit.OnWorldTick /
			// OnTick / OnTickEach) right after systems run, before
			// FlushRemovals — same window where game systems' Update
			// observed the world.
			for _, fn := range base.TickCallbacks() {
				fn(tickDt)
			}
			if gameHooks.PreFlush != nil {
				gameHooks.PreFlush()
			}
		},
		PostFlush: gameHooks.PostFlush,
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

	// Wire bridge — newBridgeForCell picks cellBridge vs grpcBridge based
	// on whether the owning host has a HostNetwork.
	bridge := newBridgeForCell(node, c, owningHost, c.cellToHostResolver(), c.cfg.GatewayMode)
	node.Bridge = bridge
	node.Stage.SetBridge(bridge)

	// Callers during Build() don't need locking (single-threaded).
	// Callers during runtime (SplitCell) must hold c.mu write lock.
	c.Cells[node.MeshID] = node
	c.CellOwner[cell] = node.MeshID

	return node, gameSystems
}

// Start launches all node goroutines, the event router, and — unless headless —
// the interactive console. Start blocks until the context is cancelled or the
// user types "quit" in the console. On return all nodes have been shut down.
//
// The parent argument is optional — when omitted, context.Background() is used.
// Start installs its own SIGINT/SIGTERM handlers regardless, so passing a
// context is only required when the caller needs to drive shutdown externally.
func (c *Process) Start(parent ...context.Context) {
	ctx := context.Background()
	if len(parent) > 0 {
		ctx = parent[0]
	}
	c.Build()

	if c.cfg.DumpSchema {
		c.dumpSchemaAndExit()
		return // unreachable — dumpSchemaAndExit calls os.Exit
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Service framework runs BEFORE startHTTPListener so any HTTPRoutes
	// hooks installed by services (e.g. mmokit.RegisterAuthService
	// mounts /auth/*) see a populated liveService at the time the HTTP
	// mux is built. Reversing this order silently 404s every HTTP
	// endpoint a service contributes.
	if c.roles.Has(RoleService) && len(c.cfg.ServiceKinds) > 0 {
		if err := c.startServices(ctx); err != nil {
			panic(fmt.Errorf("service framework: %w", err))
		}
	}

	c.startHTTPListener()
	c.startAdminHTTPListener()

	go c.routeEvents(ctx)

	// Replication Timeline Redesign Task C4: periodic CoordTimeSync
	// broadcast. Only launched on coordinator-bearing processes — remote
	// hosts and standalone gateways don't have a controlServer. The loop
	// itself also guards on c.controlServer != nil defensively.
	if c.controlServer != nil {
		go c.startClusterTimeBroadcast(ctx)
	}

	for _, node := range c.Cells {
		go node.Run(ctx)
	}

	// Run the OpRouter polling loop on processes that own a typed-op
	// drain surface — clients connected directly via WebSocket on a
	// gateway, or gateway-forwarded sessions queued in the host's VCM.
	// The run is harmless when no handlers are registered (just polls +
	// ignores unknown codes). Starts after startServices so service
	// handlers are already registered.
	//
	// Drain source selection:
	//   - Remote host (c.vcm != nil): swap the router from the
	//     placeholder WS ConnManager (set in main.go before role-aware
	//     setup) to the VCM, which is where ClientInput frames forwarded
	//     by the gateway actually queue. Without this swap the host
	//     never drains its typed-op queue and BankRequest et al. hang.
	//   - Otherwise (all-in-one, coord+gateway): keep the WS
	//     ConnManager wired in by main.go.
	//
	// Skip the OpRouter when the gateway runs a per-conn pump
	// (Gateway.runSessionPump). That pump drains the same WS opInput
	// queue every 1ms and forks per-frame on RouteKind: RouteGatewayLocal
	// ops dispatched inline, RoutePlayerCell ops forwarded to the
	// player's host via MeshData. Running the OpRouter alongside the
	// pump races the drain — the OpRouter's CellOpRouter path can't
	// reach a remote cell (returns OperationError synchronously), so
	// any RoutePlayerCell frame the OpRouter wins is lost. The pump's
	// gate is g.hostNetwork != nil, so we mirror it here.
	pumpOwnsDrain := c.gateway != nil && c.gateway.hostNetwork != nil
	hasGatewayClients := c.roles.Has(RoleGateway) && !pumpOwnsDrain
	hasVCMClients := c.vcm != nil
	if c.cfg.OpRouter != nil && (hasGatewayClients || hasVCMClients) {
		if hasVCMClients {
			c.cfg.OpRouter.SetDrainSource(c.vcm)
		}
		// Install the typed-op dispatcher. Every drained 0x01 frame is a
		// typed-op request; DispatchTypedOpInbound routes it via the
		// matching RegisterOp handler. The Process is passed as the
		// CellOpRouter so RoutePlayerCell entries route through
		// Process.DispatchCellRoutedOp (engine.RunOnLoop on the player's
		// authoritative cell).
		c.cfg.OpRouter.SetTypedOpHandler(func(payload []byte, ctx *ops.OpContext) []byte {
			return DispatchTypedOpInbound(payload, ctx, c)
		})
		go c.cfg.OpRouter.Run(ctx)
	}

	// Startup ready-message varies by role so the operator gets
	// something meaningful instead of "all 0 cells started" on processes
	// that don't host cells locally.
	switch {
	case c.cfg.IsRemoteHost(c.roles):
		c.Log.Log(CatMeshCell, "host: ready, awaiting CellAssign from coordinator %s", c.cfg.CoordinatorAddr)
	case c.roles.Has(RoleHost):
		c.Log.Log(CatMeshCell, "coordinator: all %d cells started (roles=%s)", len(c.Cells), c.roles)
	case c.roles.Has(RoleCoordinator):
		c.Log.Log(CatMeshCell, "coordinator: ready, waiting for host registrations on %s", c.cfg.ControlListen)
	case c.roles.Has(RoleGateway):
		c.Log.Log(CatMeshCell, "gateway: ready, awaiting sessions via %s", c.cfg.CoordinatorAddr)
	case c.roles.Has(RoleService) && len(c.runningServices) > 0:
		c.Log.Log(CatMeshCell, "service: ready, %d kind(s) instantiated", len(c.runningServices))
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

// startClusterTimeBroadcast periodically publishes CoordTimeSync to
// every registered host over the MeshControl stream. Runs as long as
// the coordinator is alive. Remote hosts update their ClusterClock's
// EMA offset; in-process hosts are filtered out inside
// broadcastCoordTimeSync (they have no control stream and their offset
// is pre-seeded to 0 by construction).
func (c *Process) startClusterTimeBroadcast(ctx context.Context) {
	interval := c.cfg.ClusterClockSyncInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if c.controlServer == nil {
				continue
			}
			c.controlServer.broadcastCoordTimeSync()
		}
	}
}

// promptLabel builds the console prompt prefix. Prefers configured IDs over
// role names so multiple processes with the same role set (e.g. several
// `--mode=host` workers) are distinguishable at a glance. Falls back to the
// role string when no ID is available.
func (c *Process) promptLabel() string {
	var parts []string
	if c.roles.Has(RoleCoordinator) {
		parts = append(parts, "coord")
	}
	if c.roles.Has(RoleHost) {
		if c.cfg.HostID != "" {
			parts = append(parts, c.cfg.HostID)
		} else {
			parts = append(parts, "host")
		}
	}
	if c.roles.Has(RoleGateway) {
		if c.cfg.GatewayID != "" && c.cfg.GatewayID != InprocGatewayID {
			parts = append(parts, c.cfg.GatewayID)
		} else {
			parts = append(parts, "gateway")
		}
	}
	if len(parts) == 0 {
		return c.roles.String()
	}
	return strings.Join(parts, "+")
}

// startConsole creates the console, registers builtins, and runs it (blocking).
// The console shares the coordinator's registry and dispatcher so that commands
// registered before Build() are available in the REPL and vice-versa.
func (c *Process) startConsole(ctx context.Context) {
	c.console = engine.NewConsoleWithDispatcher(c.Log, c.registry, c.dispatcher)
	c.console.SetPrompt(fmt.Sprintf("%s > ", c.promptLabel()))

	// Resolve the first cell's engine — used for both perf builtins and the
	// loop-safe entity/config handler wiring below.
	var defaultEng *engine.Engine
	for _, node := range c.Cells {
		defaultEng = node.Engine
		break
	}

	builtinOpts := engine.BuiltinOpts{
		Engine: defaultEng,
	}

	// Merge game-provided builtins if Console was set.
	co := c.consoleOpts
	if co != nil {
		builtinOpts.Config = co.Config
		builtinOpts.ConfigSave = co.ConfigSave
		builtinOpts.ConfigReset = co.ConfigReset
		builtinOpts.ConfigOnChanged = co.ConfigOnChanged
	}

	// Wire dynamic completion sources so tab-complete on args like
	// <hostID>, <cellID>, <gatewayID>, <sessionKey> pulls live values
	// from the coord's registries. `players` is set by game lifecycle.
	c.wireCompletionSources()

	// Let the game (if any) register its own commands. Games that need custom
	// config opts call console.RegisterBuiltins(...) themselves in this callback.
	onReady := c.onConsoleReady
	if onReady != nil {
		onReady(c.console)
	}

	// Fallback: register the coordinator-level config builtins if the game
	// didn't (e.g. pure-coordinator mode with no local cells). The cluster-aware
	// entity.* commands are registered unconditionally in registerAllBuiltins.
	if builtinOpts.Config != nil {
		if _, ok := c.registry.Lookup("config.list"); !ok {
			c.console.RegisterBuiltins(builtinOpts)
		}
	}

	c.console.Run(ctx)
}


// cellToHostResolver returns a closure that maps a cell ID string to its
// owning host ID. Used by newBridgeForCell / grpcBridge to route cross-host dispatch.
func (c *Process) cellToHostResolver() func(MeshCellID) string {
	return func(destCellID MeshCellID) string {
		h, _ := c.Control.OwnerOf(destCellID)
		return h
	}
}

// resolveSpatialCellSize mirrors the logic in Build() so
// assignCellOnNode can pass the right bucket size to createNode.
func (c *Process) resolveSpatialCellSize() float32 {
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
// In remote-host mode the owning host has a HostNetwork, so createNode
// automatically wires a grpcBridge for cross-host MeshData routing.
func (c *Process) assignCellOnNode(cellID MeshCellID) {
	cell, err := ParseCellID(string(cellID))
	if err != nil {
		c.Log.Log(CatMeshCell, "host: ignoring CellAssign: invalid cell id %q: %v", cellID, err)
		return
	}

	host := c.localHost()
	if host == nil {
		c.Log.Log(CatMeshCell, "host: assignCellOnNode with no local host")
		return
	}

	// Check if already running (idempotent if the coordinator re-sends).
	c.mu.Lock()
	if _, exists := c.Cells[cellID]; exists {
		c.mu.Unlock()
		c.Log.Log(CatMeshCell, "host: cell %s already running, ignoring duplicate CellAssign", cellID)
		return
	}

	// createNode registers the cell into c.Cells and c.CellOwner itself.
	// Passing the host lets createNode pick the right bridge type: grpcBridge
	// when the host has a HostNetwork (remote-host mode), plain cellBridge otherwise.
	spatialCellSize := c.resolveSpatialCellSize()
	node, systems := c.createNode(cell, spatialCellSize, host)
	host.AddCell(cell, node)
	c.mu.Unlock()

	// Wire neighbor relationships for the new cell. Includes both LOCAL
	// neighbors (real *Cell pointers) AND REMOTE neighbors (stub *Cell
	// entries carrying just ID + CellID). Without this, the border
	// dispatcher either early-exits on an empty Neighbors map or only
	// sees one side of the boundary — cross-cell border replicas never
	// flow, so entities in adjacent cells on other nodes are invisible
	// to clients on this node. See reconcileCellNeighbors for the remote-
	// stub rationale.
	c.reconcileCellNeighbors(node)

	// Two-phase init matches the Build() path: World.Init registers
	// entity kinds + world state; then systems discover them.
	node.Stage.Init()
	initSystems(systems)

	go node.Run(context.Background())

	// Tell the coordinator we're live.
	if c.controlClient != nil {
		_ = c.controlClient.send(&meshpb.HostMessage{
			Msg: &meshpb.HostMessage_CellReady{
				CellReady: &meshpb.CellReady{
					HostId: c.controlClient.hostID,
					CellId: string(cellID),
				},
			},
		})
	}
	c.Log.Log(CatMeshCell, "host: cell %s ready", cellID)
}

// releaseCellOnNode stops and removes a cell that the coordinator
// has released (typically during reassignment after crash recovery).
// Sends CellStopped back once the shutdown is complete.
func (c *Process) releaseCellOnNode(cellID MeshCellID) {
	host := c.localHost()
	if host == nil {
		return
	}

	c.mu.Lock()
	node, ok := c.Cells[cellID]
	if !ok {
		c.mu.Unlock()
		c.Log.Log(CatMeshCell, "host: releaseCellOnNode: unknown cell %q", cellID)
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
					CellId: string(cellID),
				},
			},
		})
	}
	c.Log.Log(CatMeshCell, "host: cell %s stopped", cellID)
}

// renameCellOnNode rekeys a local cell from `from` to `to`. Rewrites
// Host.cells map under h.mu, coord.Cells map under c.mu, and the
// *Cell struct's ID/Cell fields on the cell's own game loop (so
// PostSystems reads don't race with the write).
func (c *Process) renameCellOnNode(from, to MeshCellID) error {
	host := c.localHost()
	if host == nil {
		return fmt.Errorf("host: renameCellOnNode: no local host")
	}

	c.mu.Lock()
	cell := host.CellByID(from)
	if cell == nil {
		c.mu.Unlock()
		return fmt.Errorf("host: renameCellOnNode: unknown cell %q", from)
	}
	// Validate the destination ID before mutating any state — otherwise a
	// malformed `to` would orphan the cell (removed from host.cells but
	// still in c.Cells[from]).
	toCellID, err := ParseCellID(string(to))
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("host: renameCellOnNode: parse %q: %w", to, err)
	}
	host.RemoveCell(cell.Cell)
	host.AddCell(toCellID, cell)
	// Update coord's Cells / CellOwner maps (local-host copies — in
	// remote-host mode c.Cells is only the cells this process owns).
	delete(c.Cells, from)
	delete(c.CellOwner, cell.Cell)
	c.Cells[to] = cell
	c.CellOwner[toCellID] = to
	c.mu.Unlock()

	// Rewrite the cell's own identity on its game loop so PostSystems
	// reads don't race.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := cell.Engine.RunOnLoop(ctx, func() error {
		cell.MeshID = to
		cell.Cell = toCellID
		if cell.Stage != nil {
			cell.Stage.UpdateCellBounds(toCellID, coords.CellSize)
		}
		if cell.Metrics != nil {
			cell.Metrics.SetCellID(string(to))
		}
		// Clear any drain-for-merge freeze that the MERGE Receive set on
		// this cell when it was a survivor. RenameCell is the natural
		// "merge has committed on this host" signal: the topology now
		// reflects the merged parent, so the survivor's handoff_driver
		// can resume queueing crossings (which will now target the
		// outside-of-parent neighbors, not the soon-to-be-doomed
		// siblings). Idempotent: SetDrainingForMerge(false) is a no-op
		// when the flag wasn't set (non-merge renames, or cells that
		// were never frozen).
		if cell.Stage != nil {
			cell.Stage.SetDrainingForMerge(false)
		}
		return nil
	})
	if runErr != nil {
		return fmt.Errorf("host: renameCellOnNode: RunOnLoop: %w", runErr)
	}
	return nil
}

// sendCellRelease tells a remote host to shut down a cell it owns via
// CellRelease over MeshControl. The remote host's releaseCellOnNode()
// will stop the game loop, remove the cell from local maps, and send
// CellStopped back. No-op when no control server is active (single-
// process mode where srcCell is always non-nil).
func (c *Process) sendCellRelease(hostID, cellID string) {
	if c.controlServer == nil {
		return
	}
	msg := &meshpb.CoordMessage{
		CoordEpoch: c.coordEpoch,
		Msg: &meshpb.CoordMessage_CellRelease{
			CellRelease: &meshpb.CellRelease{
				CellId: cellID,
			},
		},
	}
	if err := c.controlServer.sendCoordMessageToHost(hostID, msg); err != nil {
		c.Log.Log(CatMeshCell, "coordinator: CellRelease to %s for %s failed: %v", hostID, cellID, err)
	}
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
func (c *Process) drainHost(ctx context.Context, hostID string) error {
	// Snapshot the set of cells currently owned by hostID via CellsOwnedBy,
	// which unifies hostRegistry (authoritative) and cellToHostMap (fallback
	// for in-process fixtures that wire neither a control listener nor a
	// HostRegistry).
	var cellKeys []string
	c.Control.CellsOwnedBy(hostID, func(k string) bool {
		cellKeys = append(cellKeys, k)
		return true
	})

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
func (c *Process) survivingHostIDs(leavingHostID string) []string {
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
	// Fallback: derive from AllOwnedCells (test fixtures without a HostRegistry).
	seen := make(map[string]struct{})
	var ids []string
	c.Control.AllOwnedCells(func(_, h string) bool {
		if h == leavingHostID {
			return true
		}
		if _, ok := seen[h]; ok {
			return true
		}
		seen[h] = struct{}{}
		ids = append(ids, h)
		return true
	})
	return ids
}

// localHost returns the local host instance in remote-host mode (or
// `all` preset mode where a single "local" Host owns all cells).
// Returns nil if the coordinator hasn't built any hosts yet.
// Helper used by meshControlClient to fill RegisterHost with the
// grpc listen address.
func (c *Process) localHost() *Host {
	for _, h := range c.Hosts {
		return h
	}
	return nil
}

// localHostExecutor returns the cellTransferExecutor attached to a local
// Host, or nil if hostID is not one of this process's in-process hosts.
// Used by the S7 dispatcher and the executor's inter-host fast path to
// skip the wire for colocated source/destination hosts.
func (c *Process) localHostExecutor(hostID string) *cellTransferExecutor {
	if hostID == "" {
		return nil
	}
	return c.hostExecutors[hostID]
}

// Shutdown saves state on all nodes.
func (c *Process) Shutdown() {
	// Service framework: drain services before HTTP and cells go away so
	// (a) in-flight service ops complete via the still-active OpRouter,
	// (b) the coordinator stops routing to us before our handlers go.
	// stopServices is a no-op when no services are running.
	if c.roles.Has(RoleService) {
		c.stopServices(context.Background())
	}

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
	if c.adminHTTPServer != nil {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 3*time.Second)
		if err := c.adminHTTPServer.Shutdown(shutdownCtx); err != nil {
			c.Log.Log(CatMeshCell, "admin-http: shutdown error: %v", err)
		}
		cancelShutdown()
		c.adminHTTPServer = nil
	}

	// S7-T7 graceful leave for remote-host mode: send HostMessage.GracefulLeave to
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
			c.Log.Log(CatMeshCell, "host: GracefulLeave send failed: %v — skipping drain wait", err)
		} else {
			c.Log.Log(CatMeshCell, "host: sent GracefulLeave, waiting for CellsDrained")
			// Slightly larger than drainHost's 30s budget so the
			// coordinator's timeout path wins (ack + log) instead of
			// ours (log timeout + proceed).
			select {
			case <-drained:
				c.Log.Log(CatMeshCell, "host: CellsDrained received, proceeding with shutdown")
			case <-time.After(32 * time.Second):
				c.Log.Log(CatMeshCell, "host: timed out waiting for CellsDrained, proceeding with shutdown")
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
		case <-time.After(c.cfg.ShutdownGracePeriod):
			c.Log.Log(CatMeshCell, "coordinator: MeshControl hard-stop after GracefulStop timeout")
			c.controlGrpcServer.Stop()
			<-stopped
		}
	}
	// Close the engine-owned Postgres store last so any in-flight handler
	// queries (cell flushers, service shutdown writes) drain first.
	if c.ownsDBStore && c.cfg.DBStore != nil {
		c.cfg.DBStore.Close()
	}
	c.Log.Log(CatMeshCell, "coordinator: all nodes shut down")
}

// routeEvents drains ConnManager.Events() and forwards every connect /
// disconnect event to the gateway, which holds the per-connection auth
// state. PlayerAssignment dispatch happens later, in Gateway.onAuthSuccess,
// driven by the auth-service response interceptor.
func (c *Process) routeEvents(ctx context.Context) {
	events := c.ConnMgr.Events()

	for {
		select {
		case <-ctx.Done():
			return

		case evt := <-events:
			// Client connect/disconnect events are only emitted when this
			// process has a gateway role (and thus a WS listener). Any
			// PlayerEvent arriving here implies c.gateway != nil; routing
			// goes through Gateway.handleEvent / handleDisconnect, which
			// own session routing post-auth. Non-gateway processes
			// (pure coord, pure node) still spin this goroutine but never
			// receive events.
			if c.gateway == nil {
				c.Log.Log(CatNetConn, "coordinator: conn %d event received with no gateway — ignoring", evt.ConnID)
				continue
			}
			if evt.Connected {
				c.gateway.handleEvent(evt)
			} else {
				c.gateway.handleDisconnect(evt)
			}
		}
	}
}

// GridWidth returns the number of cells wide in the mesh grid.
func (c *Process) GridWidth() uint32 { return c.cfg.CellsX }

// notifyPlayerMigrated is called when a cross-host player handoff commits.
// Updates sessionRoutes atomically (bumping epoch) and notifies the gateway
// holding the session via direct call (embedded) or targeted CoordMessage
// (standalone). Called from two entry points:
//   - grpcBridge.OnPlayerTransfer when the destination is on a different host
//     (single-process `all` preset — passes InprocGatewayID)
//   - meshControlServer.handleHostControl when a remote node emits
//     HostMessage.PlayerMigrated over its control stream (passes proto GatewayId,
//     which the node resolved from its VirtualConnManager reverse lookup)
//
// gatewayID is the gateway that owns the session — InprocGatewayID for
// embedded-gateway deployments, the real gateway peer ID for multi-process.
func (c *Process) notifyPlayerMigrated(gatewayID string, connID uint32, srcHost, destHost string, destCellID MeshCellID) {
	key := SessionKey{GatewayID: gatewayID, ConnID: connID}
	// Read the username before Migrate so we can sync c.players to the new host.
	var username string
	if existing, ok := c.sessionRoutes.Get(key); ok {
		username = existing.Username
	}
	newEpoch, ok := c.sessionRoutes.Migrate(key, destHost, destCellID)
	if !ok {
		c.Log.Log(CatMeshCell, "coordinator: PlayerMigrated for unknown session conn=%d src=%s dst=%s", connID, srcHost, destHost)
		return
	}
	c.Log.Log(CatMeshTransfer, "coordinator: PlayerMigrated conn=%d %s->%s cell=%s epoch=%d", connID, srcHost, destHost, destCellID, newEpoch)
	// Sync the username→hostID index so ActiveUserHost returns the new host
	// after cross-host handoff. The host's local session callback fires on
	// the host's own in-process coordinator instance; this path is the only
	// way the real coordinator process learns about the host change.
	if username != "" {
		c.notifySessionActive(username, destHost, destCellID)
	}
	// Register the session on the destination host's VCM so it can stamp
	// the correct epoch on outbound frames.
	c.dispatchSessionRegister(destHost, key, newEpoch, destCellID)
	// Embedded gateway: direct call.
	if c.gateway != nil && c.gateway.id == key.GatewayID {
		c.gateway.OnUpstreamSwitch(connID, destHost, destCellID, newEpoch)
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
				GatewayId:  key.GatewayID,
				ConnId:     connID,
				NewHostId:  destHost,
				NewCellId:  string(destCellID),
				NewEpoch:   newEpoch,
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
func (c *Process) dispatchUpstreamSwitch(key SessionKey, destHost string, destCellID MeshCellID, newEpoch uint64) {
	if c.gateway != nil && c.gateway.id == key.GatewayID {
		c.gateway.OnUpstreamSwitch(key.ConnID, destHost, destCellID, newEpoch)
		return
	}
	if c.controlServer == nil {
		return
	}
	msg := &meshpb.CoordMessage{
		CoordEpoch: c.coordEpoch,
		Msg: &meshpb.CoordMessage_UpstreamSwitch{
			UpstreamSwitch: &meshpb.UpstreamSwitch{
				GatewayId:  key.GatewayID,
				ConnId:     key.ConnID,
				NewHostId:  destHost,
				NewCellId:  string(destCellID),
				NewEpoch:   newEpoch,
			},
		},
	}
	if err := c.controlServer.sendCoordMessageToGateway(key.GatewayID, msg); err != nil {
		c.Log.Log(CatMeshCell, "coordinator: UpstreamSwitch to gateway %s failed: %v", key.GatewayID, err)
	}
}

// dispatchSessionRegister tells the destination host to register (or update)
// a player session in its VirtualConnManager with the correct epoch. This
// ensures the VCM stamps outbound ClientFrames with the epoch the gateway
// expects after an UpstreamSwitch.
//
// Three dispatch paths, tried in order:
//  1. In-process host with a VCM (remote-host mode): call RegisterSession
//     directly.
//  2. Process's own vcm (remote-host-mode process): register locally.
//  3. Remote host: send SessionRegister via MeshControl.
func (c *Process) dispatchSessionRegister(hostID string, key SessionKey, epoch uint64, cellID MeshCellID) {
	// In-process host: check if it has a VCM and register directly.
	c.mu.RLock()
	hostObj, ok := c.Hosts[hostID]
	c.mu.RUnlock()
	if ok && hostObj != nil && hostObj.Network != nil {
		if vcm := hostObj.Network.VCM(); vcm != nil {
			vcm.RegisterSession(key, "", epoch, cellID)
			return
		}
	}
	// Process's own VCM (remote-host-mode process where the
	// coordinator object and the host share the same process).
	if c.vcm != nil {
		c.vcm.RegisterSession(key, "", epoch, cellID)
		return
	}
	// Remote host: send via MeshControl.
	if c.controlServer != nil {
		msg := &meshpb.CoordMessage{
			CoordEpoch: c.coordEpoch,
			Msg: &meshpb.CoordMessage_SessionRegister{
				SessionRegister: &meshpb.SessionRegister{
					GatewayId: key.GatewayID,
					ConnId:    key.ConnID,
					Epoch:     epoch,
					CellId:    string(cellID),
				},
			},
		}
		if err := c.controlServer.sendCoordMessageToHost(hostID, msg); err != nil {
			c.Log.Log(CatMeshCell, "coordinator: SessionRegister to %s failed: %v", hostID, err)
		}
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
func (c *Process) applyRegistryDelta(mutation topologyMutation, preOwnership map[MeshCellID]string) {
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
func (c *Process) broadcastPeerListIfReady() {
	if c.assignmentEngine == nil {
		return
	}
	// The engine's broadcast method tolerates a missing control server
	// (sendCoordMessageToHost fails gracefully and the embedded-gateway
	// direct reconcile path still runs). Guarding here on controlServer
	// would skip the in-process gateway reconcile, which we want for
	// all-preset dev setups.
	c.assignmentEngine.broadcastPeerList()
}

func (c *Process) setPlayerNode(connID uint32, nodeID MeshCellID) {
	key := SessionKey{GatewayID: InprocGatewayID, ConnID: connID}
	if !c.sessionRoutes.UpdateCell(key, nodeID) {
		c.sessionRoutes.Set(&SessionRoute{
			Key:    key,
			CellID: nodeID,
			Epoch:  1,
		})
	}
}

func (c *Process) removePlayerNode(connID uint32) {
	c.sessionRoutes.Remove(SessionKey{GatewayID: InprocGatewayID, ConnID: connID})
}

// getCell returns a cell by ID under read lock.
func (c *Process) getCell(cellID MeshCellID) (*Cell, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n, ok := c.Cells[cellID]
	return n, ok
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
func (c *Process) reconcileCellNeighbors(newCell *Cell) {
	if newCell == nil {
		return
	}
	// Snapshot remote cell keys before taking the write lock — AllOwnedCells
	// acquires its own read lock internally and cannot be called under c.mu.Lock().
	var remoteIDs []string
	// Control is always non-nil in production (New wires it at
	// line 334). This guard exists only to tolerate minimal test fixtures
	// that construct &Process{...} directly; Phase 2.5 updates those.
	if c.Control != nil {
		c.Control.AllOwnedCells(func(id, _ string) bool {
			remoteIDs = append(remoteIDs, id)
			return true
		})
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	baseSize := c.baseCellSize()

	// Build the universe of known cells: start with local c.Cells and
	// merge in any remote cells from AllOwnedCells that aren't already covered.
	// Local entries win over remote stubs.
	type candidate struct {
		id   string
		cid  CellID
		cell *Cell // nil for remote stubs
	}
	seen := make(map[string]bool)
	var candidates []candidate
	for id, cc := range c.Cells {
		if id == newCell.MeshID {
			continue
		}
		seen[string(id)] = true
		candidates = append(candidates, candidate{id: string(id), cid: cc.Cell, cell: cc})
	}
	for _, id := range remoteIDs {
		if MeshCellID(id) == newCell.MeshID || seen[id] {
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
			newCell.Neighbors[MeshCellID(cand.id)] = cand.cell
			cand.cell.Neighbors[newCell.MeshID] = newCell
			if eb := unwrapCellBridge(cand.cell.Bridge); eb != nil {
				eb.invalidateBorderDispatcher()
			}
			continue
		}
		// Remote neighbor — stub entry. The remote host handles its own
		// reverse-side wiring when it applies the same PeerList.
		newCell.Neighbors[MeshCellID(cand.id)] = &Cell{
			MeshID: MeshCellID(cand.id),
			Cell:   cand.cid,
		}
	}
	if nb := unwrapCellBridge(newCell.Bridge); nb != nil {
		nb.invalidateBorderDispatcher()
	}
}

// ClusterCellInfo describes one cell's identity and its owning host.
// Returned by Process.ClusterCells; lets games build their own
// SE_DEBUG_INFO messages without engine-side broadcast plumbing.
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
func (c *Process) ClusterCells() []ClusterCellInfo {
	// Collect from AllOwnedCells (hostRegistry + cellToHostMap, with own locking).
	var out []ClusterCellInfo
	c.Control.AllOwnedCells(func(cellIDStr, hostID string) bool {
		cell, err := ParseCellID(cellIDStr)
		if err != nil {
			return true
		}
		out = append(out, ClusterCellInfo{Cell: cell, HostID: hostID})
		return true
	})
	if len(out) > 0 {
		return out
	}
	// Fallback: single-host `all` preset — AllOwnedCells returns nothing,
	// but CellOwner has the authoritative cell set.
	c.mu.RLock()
	defer c.mu.RUnlock()
	out = make([]ClusterCellInfo, 0, len(c.CellOwner))
	for cell := range c.CellOwner {
		out = append(out, ClusterCellInfo{Cell: cell})
	}
	return out
}

// cellLoad returns the current load snapshot for a node.
// Used by dynamic partitioning (split/merge) for rebalancing decisions.
func (c *Process) cellLoad(nodeID MeshCellID) (metrics.LoadSnapshot, bool) {
	c.mu.RLock()
	node, ok := c.Cells[nodeID]
	c.mu.RUnlock()
	if !ok || node.Metrics == nil {
		return metrics.LoadSnapshot{}, false
	}
	return node.Metrics.Snapshot(), true
}

// allCellLoads returns load snapshots for all nodes. Used by MetricsHandler.
func (c *Process) allCellLoads() map[string]metrics.LoadSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]metrics.LoadSnapshot, len(c.Cells))
	for id, node := range c.Cells {
		if node.Metrics != nil {
			result[string(id)] = node.Metrics.Snapshot()
		}
	}
	return result
}

// MetricsHandler returns an HTTP handler that serves Prometheus-compatible
// metrics for all nodes. Mount on your HTTP mux: mux.Handle("/metrics", coord.MetricsHandler())
func (c *Process) MetricsHandler() http.HandlerFunc {
	return metrics.Handler(c.allCellLoads)
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

// baseCellSize returns the base cell size from the coordinator config.
func (c *Process) baseCellSize() float32 {
	if c.cfg.CellSize > 0 {
		return c.cfg.CellSize
	}
	return 8192
}

// ---------------------------------------------------------------------------
// Test-harness shim methods
//
// These methods expose coordinator-internal state to multi-process test
// harnesses (e.g. examples/4node-basic/mesh_e2e_test.go) that need to seed
// cell layout across separate coord-role + host-role Coordinators connected
// via real gRPC MeshControl. They are thin delegators to the internal
// assignmentEngine and hostRegistry. Named with "Harness" prefix to signal
// their intended use.
// ---------------------------------------------------------------------------

// ControlListenAddr returns the network address the MeshControl gRPC server
// is listening on. Non-empty only on coord-role processes after Build().
// Returns "" if no listener is bound (e.g. pure host-role processes).
func (c *Process) ControlListenAddr() string {
	if c.controlListener != nil {
		return c.controlListener.Addr().String()
	}
	return ""
}

// HarnessWaitForHost blocks until the named host has registered with this
// coordinator's hostRegistry, or ctx expires. Returns nil when the host is
// registered, or a context error. Only meaningful on a coord-role process.
func (c *Process) HarnessWaitForHost(ctx context.Context, hostID string) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if c.hostRegistry != nil && c.hostRegistry.Get(hostID) != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("HarnessWaitForHost: host %q not registered before deadline", hostID)
		case <-tick.C:
		}
	}
}

// HarnessDispatchCellAssign sends a NetIDRangeGrant + CellAssign to the
// named host for the given cell key. Must be called on a coord-role process
// after the assignmentEngine is constructed (i.e. after Build()). The settle
// window should be bypassed (HarnessSetSettled) before calling this so the
// settle loop doesn't stomp the manual assignment.
func (c *Process) HarnessDispatchCellAssign(hostID, cellKey string) {
	if c.assignmentEngine != nil {
		c.assignmentEngine.dispatchCellAssign(hostID, cellKey)
	}
}

// HarnessBroadcastPeerList forces an immediate PeerList broadcast to all
// registered hosts. Call after all cell assignments have been dispatched so
// every host learns the full cell-ownership map. Must be called on a
// coord-role process.
func (c *Process) HarnessBroadcastPeerList() {
	if c.assignmentEngine != nil {
		c.assignmentEngine.broadcastPeerList()
	}
}

// HarnessSetSettled bypasses the 5-second settle window, marking the
// assignmentEngine as settled so the background rebalance loop does not
// stomp manually-seeded cell assignments. Call this on a coord-role process
// after all hosts have registered and before dispatching cell assignments.
func (c *Process) HarnessSetSettled() {
	if c.assignmentEngine != nil {
		c.assignmentEngine.mu.Lock()
		c.assignmentEngine.settled = true
		c.assignmentEngine.mu.Unlock()
	}
}

// HarnessWaitForCellOnLocalHost blocks until the local Host on this
// Process owns the named cell (i.e. the *Cell exists in its Cells map),
// or ctx expires. Returns nil when the cell is present. Call on a host-role
// process after the coord has dispatched CellAssign.
func (c *Process) HarnessWaitForCellOnLocalHost(ctx context.Context, cellKey string) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		lh := c.localHost()
		if lh != nil && lh.CellByID(MeshCellID(cellKey)) != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("HarnessWaitForCellOnLocalHost: cell %q not present before deadline", cellKey)
		case <-tick.C:
		}
	}
}

// HarnessWaitForCellToHostMap blocks until every key in wantKeys is present
// in this process's cellToHostMap (populated by PeerList broadcasts). Call on
// a host-role process after HarnessBroadcastPeerList() fires on the coord,
// to close the async race before the test body starts sending cross-host
// messages.
func (c *Process) HarnessWaitForCellToHostMap(ctx context.Context, wantKeys []string) error {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		c.Control.mu.RLock()
		missing := 0
		for _, k := range wantKeys {
			if _, ok := c.Control.cellToHostMap[MeshCellID(k)]; !ok {
				missing++
			}
		}
		c.Control.mu.RUnlock()
		if missing == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("HarnessWaitForCellToHostMap: %d/%d cells missing before deadline", missing, len(wantKeys))
		case <-tick.C:
		}
	}
}

// HarnessLocalHostCells returns a snapshot of all *Cell instances on the
// local Host of this Process. Returns nil if this process has no local
// host. Call on a host-role process to iterate cells for assertions.
func (c *Process) HarnessLocalHostCells() []*Cell {
	lh := c.localHost()
	if lh == nil {
		return nil
	}
	lh.mu.RLock()
	out := make([]*Cell, 0, len(lh.Cells))
	for _, cell := range lh.Cells {
		out = append(out, cell)
	}
	lh.mu.RUnlock()
	return out
}

