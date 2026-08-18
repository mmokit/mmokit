package universe

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	stdnet "net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	meshpb "github.com/mmokit/mmokit/gen/go/meshpb"
	"github.com/mmokit/mmokit/pkg/cmdsys"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/metrics"
	"github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/ops"
	"github.com/mmokit/mmokit/pkg/persist/postgres"
	"github.com/mmokit/mmokit/pkg/service"
	"github.com/mmokit/mmokit/pkg/services/auth"
	"github.com/mmokit/mmokit/pkg/spatial"
	"github.com/mmokit/mmokit/pkg/system"
)

const netIDRangeSize uint32 = 10_000_000

// Config holds all Process configuration. Zero values use sensible defaults.
type Config struct {
	CellsX            uint32  // number of cells wide (0 = 1)
	CellsY            uint32  // number of cells tall (0 = 1)
	CellSize          float32 // world units per cell (0 = default 8192)
	SpatialBucketSize float32 // spatial hash bucket size (0 = CellSize/10)
	TickRate          int     // game loop tick rate (0 = 20)
	AoIRadius         float32 // area-of-interest radius (0 = 500)

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

	Headless            bool
	DynamicPartitioning *PartitionConfig // nil = disabled (default)
	ConnManager         *net.ConnManager
	Logger              *logger.Logger
	LogCategories       string // comma-separated categories/groups to enable (overrides default enabled list)

	// DevInsecureCookie disables the Secure flag on the auth session
	// cookie. Default false (production-safe). Flip via the
	// --dev-insecure-cookie CLI flag for plain-HTTP local dev.
	DevInsecureCookie bool

	// CORSOrigins is a comma-separated allowlist of browser origins
	// permitted to make credentialed cross-origin requests to the gateway
	// HTTP endpoints (/auth/*, diagnostics). Empty = no CORS headers
	// (same-origin / vite-proxy dev path). When non-empty, auth cookies are
	// also switched to SameSite=None; Secure so they ride cross-site
	// requests. Bound by --cors-origins.
	CORSOrigins string

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
	// Accepts role names: coordinator, host, gateway, service.
	// Preset aliases: "" or "all" include every built-in role. The service role
	// remains inert unless service kinds are selected or auto-added.
	//
	// Common combinations:
	//   - "" / "all"                  → every built-in role (single-process dev)
	//   - "coordinator"               → control plane only (MeshControl, HostRegistry, admin console)
	//   - "coordinator,gateway"       → control plane + embedded WebSocket gateway
	//   - "coordinator,host"          → control plane + in-process cells, no gateway
	//   - "coordinator,host,gateway"  → all core runtime roles without services
	//   - "host" + CoordinatorAddr    → remote host, dials coordinator, receives cells dynamically
	//   - "gateway"                   → standalone gateway, dials CoordinatorAddr
	//
	// Rule: bare "host" requires Config.CoordinatorAddr; all other
	// combinations are accepted. Enforced at Build() time.
	Mode string

	// ControlListen is the listen address for the MeshControl gRPC server.
	// Used by coordinator-bearing role sets when the control plane must accept
	// remote processes. Default ":9100" when flags are bound.
	ControlListen string

	// UDPListen is the listen address for the engine-owned client UDP server
	// (the custom reliable/unreliable game protocol — see pkg/net/udpproto).
	// Bound only on processes that bear the Gateway role (alongside the HTTP
	// /ws listener).
	//
	// EXPERIMENTAL and DISABLED BY DEFAULT. The framing is unauthenticated and
	// unencrypted: sessions are bound to a source address, which stops an
	// off-path attacker from hijacking or killing one, but an on-path observer
	// can still read and forge traffic. Enable with --udp-listen=:9000 or by
	// setting this field, and do not expose it to untrusted networks until the
	// authenticated secure framing work lands.
	UDPListen string

	// WireLimits bounds inbound frame size and per-connection queue depth on
	// every client ingress surface (WebSocket, UDP, and the host-side virtual
	// connections a gateway forwards to), and the decode ceilings applied to
	// any body a client supplies. Zero fields fall back to
	// net.DefaultWireLimits — which matters more than it looks, because
	// universe.New's `if !flag.Parsed()` guard is always false under
	// `go test`, so BindFlags never runs there and a Config built by a
	// fixture reaches the runtime with this field zeroed.
	//
	// The five decode ceilings are also settable from the command line:
	// --wire-max-frame-bytes, --wire-max-string-bytes, --wire-max-slice-elems,
	// --wire-max-depth, --wire-max-alloc-bytes. The queue and drain caps are
	// Config-only; they are a deployment shape rather than an operator dial.
	//
	// This configures the CLIENT profile only. Mesh and transfer bodies decode
	// under the fixed meshProfile — see pkg/universe/wire_limits.go for why
	// the two are not one number.
	WireLimits net.WireLimits

	// AdminListen is the listen address for an HTTP admin server that
	// exposes /events, /commands, /metrics, and /admin/*. Bound only on
	// processes that bear RoleCoordinator — admin state (commit log,
	// cluster view, audit ring) lives on the coordinator. Default
	// ":9101" via BindFlags. Pass "" to disable. Format: ":9101" or
	// "127.0.0.1:9101".
	AdminListen string

	// TLSCertFile and TLSKeyFile, when both non-empty, enable in-process TLS
	// on the client and admin HTTP listeners (production self-hosted). When
	// both empty, the listeners serve plaintext (localhost dev or behind a
	// TLS-terminating reverse proxy). Set via --tls-cert / --tls-key.
	TLSCertFile string
	TLSKeyFile  string

	// TLSMode is an opt-in escape hatch for local TLS testing. When set to
	// "self-signed" (and no cert/key files are provided), the listeners serve
	// TLS using an in-memory self-signed cert (SANs: localhost, 127.0.0.1,
	// ::1). Cert/key files always take precedence. Set via --tls-mode.
	TLSMode string

	// ClusterSecret authenticates mesh peers to each other. It is sent in
	// gRPC call metadata at stream open on both mesh channels and compared
	// with crypto/subtle. Precedence is --cluster-secret > MMO_CLUSTER_SECRET
	// > this field.
	//
	// Empty means: auto-generated for a self-contained role set (coordinator
	// + host, which includes the "all" preset), and unauthenticated with a
	// one-time warning for every other role set. See Build.
	//
	// NEVER include this in any dump, snapshot, admin response or log line.
	// Config is handed out mutably by Process.Config, and remote hosts install
	// a MeshControl log forwarder — a logged secret ships over the very
	// channel it protects. Log clusterSecretFingerprint instead.
	ClusterSecret string

	// AllowedWSOrigins is the WebSocket Origin allowlist for /ws upgrades.
	// Empty (default) means same-origin only — EXCEPT it falls back to
	// CORSOrigins when unset (an origin trusted for credentialed cross-origin
	// HTTP is also trusted to open a WebSocket), so a cross-origin client
	// typically needs only --cors-origins. Browser pages served from a
	// different origin than the WS endpoint that don't set CORS must be listed
	// here. Native / non-browser clients (no Origin header) are always
	// allowed. Set via --ws-allowed-origins (comma-separated).
	AllowedWSOrigins []string

	// Admin configures the optional admin dashboard. See AdminConfig.
	Admin AdminConfig

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
	// (WebSocket /ws, /metrics, /auth). Bound by --port. The
	// listener is only started on processes with the Gateway role. Set to
	// -1 to disable the engine HTTP listener regardless of role (used by
	// in-process integration tests that share the default port).
	// Default: 8080.
	HTTPPort int

	// HTTPRoutes is an optional hook invoked after the engine mounts its
	// default handlers (/ws, /metrics, /auth, diagnostics) so games can add custom
	// routes or override defaults. Runs last so game routes win.
	HTTPRoutes func(mux *http.ServeMux)

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

	// Name is the game's short identifier (e.g. "simple", "basic", "space").
	// Used as the schema prefix in --dump-schema output and as the class-name
	// prefix in the generated TypeScript SDK. mmokit.New synthesizes the
	// internal *Protocol from this field; games never construct it directly.
	Name string

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

	// Dimension is the spatial profile this process simulates in, chosen once
	// at construction. It selects behaviour — which engine bindings replicate,
	// and later which systems and validators run — and never selects types.
	// Zero value is Dimension2D.
	//
	// Deliberately not a flag. A cluster whose processes disagree about the
	// dimension is undetectable on the wire today (one component set across
	// profiles means identical type IDs and identical hashes; only a structural
	// schema fingerprint separates them — see docs/roadmap.md §7.2), so this is
	// a decision the game makes in code, not one an operator can create a skew
	// with from a command line.
	Dimension Dimension

	// AllowUnfingerprintedClients lets a client connect without presenting a
	// schema fingerprint at connection setup. Off by default.
	//
	// The escape hatch exists for wscat and browser devtools poking at a live
	// gateway. It permits an ABSENT fingerprint only — a present-but-wrong one
	// still refuses, because that is never a human at a terminal, it is a
	// stale build.
	AllowUnfingerprintedClients bool

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
	// so zero value (nil) means "not set".
	Console *ConsoleOpts

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

// AdminConfig configures the engine-shipped admin/observability dashboard
// at /admin/* on the AdminListen mux. When Enabled=false (default), the
// dashboard is not mounted; /metrics, /commands, /events still work.
//
// pkg/admin imports pkg/universe (for ClusterView), so universe cannot
// import pkg/admin directly. ServerFactory supplies the concrete Server
// from a higher layer (mmokit.DefaultAdminServerFactory) without
// introducing an import cycle.
type AdminConfig struct {
	Enabled bool // default true (only honored on RoleCoordinator processes with non-empty AdminListen)
	// EnabledExplicit records that an operator named --admin-enabled on the
	// command line, as opposed to inheriting the default. It only changes what
	// happens when no database is configured: a default-on dashboard downgrades
	// to a log line, an explicitly requested one is an error. Set by BindFlags;
	// callers constructing Config directly can leave it false.
	EnabledExplicit    bool
	SessionTTL         time.Duration // default 8h
	LockoutMaxAttempts int           // default 5
	LockoutWindow      time.Duration // default 15m
	AuditCap           int           // default 4096

	// ServerFactory builds the admin server when Enabled. Defaulted by
	// mmokit.New to DefaultAdminServerFactory() when nil. Games may
	// override with a custom factory.
	ServerFactory func(*Process) AdminServer
}

// AdminServer is the slim contract pkg/admin.Server satisfies. universe
// holds a reference for Mount/Stop without importing pkg/admin.
type AdminServer interface {
	Mount(mux *http.ServeMux)
	Stop()
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
// Currently empty — retained as the extension point referenced by Config and
// re-exported by mmokit.
type ConsoleOpts struct{}

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
// just enough state to detect duplicate-active logins (a second window
// authenticating with the same user_id) so the gateway can reject them.
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

	// remoteCellMetrics caches the latest LoadSnapshot reported by remote
	// hosts via Heartbeat metrics. Keyed by cellID. Used by the admin
	// dashboard's LocalClusterView when the coordinator process owns no
	// cells locally (distributed mode). Empty in single-process all-in-one.
	remoteCellMetrics   map[MeshCellID]remoteMetricsEntry
	remoteCellMetricsMu sync.RWMutex

	ConnMgr *net.ConnManager
	Log     *logger.Logger

	// Control holds RoleCoordinator state. Phase 1 wiring: pointer to
	// a ControlPlane whose fields mirror the raw Process fields
	// below. Phase 2 migration: callers move from coord.hostRegistry
	// to coord.Control.hostRegistry, then the raw fields unexport.
	Control *ControlPlane

	console    *engine.Console // nil if headless
	cfg        Config
	netIDAlloc *NetIDAllocator
	partState  *partitionState // nil if dynamic partitioning disabled

	// protocol is the schema dumper synthesized by mmokit.New from
	// cfg.Name. Typed as any so pkg/universe stays import-free of
	// mmokit; dumpSchemaAndExit type-asserts to the schema interface.
	protocol any

	// invariantMode controls how invariant-check violations are handled.
	// Copied from Config.InvariantMode at New() time.
	invariantMode InvariantMode

	// wireLimits is the client ingress decode profile, derived from
	// Config.WireLimits at New() time via clientProfile and frozen here.
	// Read through clientWireLimits().
	//
	// Frozen rather than re-derived from cfg on each use because Build()
	// reassigns c.cfg wholesale in three places (OpRouter auto-wire, DBStore
	// open, AdminConfig defaults); a profile resolved once cannot drift with
	// them. Copied by value into decodeState, never shared.
	wireLimits net.WireLimits

	// wire is this process's client-facing message registry: the types
	// clients may send, the types the server may send back, and the typed-op
	// bindings. Injected into every Stage this process creates, exactly as
	// baseCellSize is. See WireRegistry.
	wire *WireRegistry

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
	onConsoleReady []func(*Process, *engine.Console)

	// remoteLogBatch is invoked when a host forwards a LogBatch over
	// MeshControl. Wired by mmokit.DefaultAdminServerFactory so
	// pkg/universe doesn't import pkg/admin. nil = drop incoming
	// batches.
	remoteLogBatch func([]RemoteLogEntry)

	// remoteAdminTopic is invoked when a host forwards an AdminTopicEvent
	// over MeshControl (host-side mmokit.PublishAdminTopic in distributed
	// mode). Atomic so it can be installed while the control server is
	// already running (tests do this); fires on the controlServer goroutine
	// and must not block. nil = drop. Wired by
	// mmokit.DefaultAdminServerFactory.
	remoteAdminTopic atomic.Pointer[func(topic string, payload []byte)]

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

	mu      sync.RWMutex
	players map[string]*PlayerLocation // username -> location (active + disconnected)
	// activeUsers indexes the same logical record by canonical user_id from
	// auth.users. The auth-service path stamps userID in the gateway's
	// authState; cell-side callers only have username, so both indexes are
	// kept consistent on every notifySession* mutation.
	activeUsers map[uuid.UUID]*activeUser
	// userIDByConn maps a per-connection ID back to its user_id. Currently
	// only written (cleaned up on notifySessionRemoved); kept as a hook for
	// future per-connection→user_id lookups.
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

	// udpServer is the engine-owned client UDP server, non-nil once
	// startUDPListener has bound Config.UDPListen. Held so its limits can be
	// configured and its drop counters read: without a reference here,
	// SetLimits and every drop-counter accessor have no production caller and
	// the listener is unobservable.
	udpServer atomic.Pointer[net.UDPServer]

	// udpKeys holds the UDP session keys issued by POST /auth/udp-key and
	// resolved by the UDP handshake (CE-005b Tier 2). Created lazily via
	// udpKeyRegistry() because the HTTP listener and the UDP listener start
	// independently and either may reach it first.
	udpKeysOnce sync.Once
	udpKeys     *net.UDPKeyRegistry

	// tlsOnce memoizes the resolved client-facing TLS config so both HTTP
	// listeners present the same certificate. Resolved lazily via
	// httpTLSConfig(); nil tlsConfig means serve plaintext.
	tlsOnce       sync.Once
	tlsConfig     *tls.Config
	tlsSelfSigned bool

	// meshTLSOnce memoizes this process's mesh certificate, generated in
	// memory and never written to disk. Separate from tlsOnce above: the mesh
	// needs a certificate even when the client-facing posture is plaintext,
	// which is the shipped default. See meshTLSConfig.
	meshTLSOnce sync.Once
	meshTLSCert tls.Certificate

	// meshWarnOnce keeps the cluster-secret posture line to one per process.
	// CatMeshCell is force-enabled via StartupCategories and 37+ test
	// functions build distributed fixtures, so an unguarded line would add
	// roughly a hundred lines to `go test ./pkg/universe`.
	meshWarnOnce sync.Once

	// adminHTTPServer is an optional admin HTTP server exposing /events,
	// /commands, and /metrics. Non-nil when Config.AdminListen is set.
	// Typically used on pure-coordinator processes that don't bind the
	// client HTTP listener but still need operational observability.
	adminHTTPServer *http.Server

	// adminServer is the optional admin dashboard server (pkg/admin). Non-nil
	// only when Config.Admin.Enabled and Config.Admin.ServerFactory is set.
	adminServer AdminServer

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

	// bus is the per-process typed pub/sub bus shared by every service
	// instance running on this Process. Initialized in New(); injected
	// into service.Context.Bus by serviceContext().
	//
	// Phase 1 fan-out is process-local only. Phase 3 will plumb a
	// peer-mesh dispatch callback so Publish[T] reaches remote subscribers.
	bus *service.Bus

	// serviceEventRouter is the coord-internal routing table for the
	// service event bus. Populated by ServiceEventSubscribe messages
	// from every process; snapshotted into PeerList.event_routing.
	// Non-nil on every Process — cheap to allocate and avoids nil-checks
	// in test paths that don't go through Build's role-set decisioning.
	serviceEventRouter *serviceEventRouter
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
	if cfg.ClusterClockSyncInterval <= 0 {
		cfg.ClusterClockSyncInterval = 10 * time.Second
	}
	if cfg.ShutdownGracePeriod <= 0 {
		cfg.ShutdownGracePeriod = 5 * time.Second
	}
	if cfg.SettleWindow <= 0 {
		cfg.SettleWindow = settleWindow
	}

	// Apply the ingress limits as a zero-value fallback rather than relying on
	// BindFlags: flag defaults never reach a Config built by a test fixture.
	cfg.WireLimits = cfg.WireLimits.Normalized()

	// Same reason as WireLimits above, and one more: BindFlags is skipped both
	// under go test (flag.Parsed is already true) and for any game that calls
	// flag.Parse itself. An env read placed only in BindFlags is invisible on
	// both paths, so repeat it here.
	if cfg.ClusterSecret == "" {
		cfg.ClusterSecret = os.Getenv(clusterSecretEnvVar)
	}

	if cfg.CellSize > 0 {
	}

	c := &Process{
		Cells:              make(map[MeshCellID]*Cell),
		CellOwner:          make(map[CellID]MeshCellID),
		Hosts:              make(map[string]*Host),
		hostExecutors:      make(map[string]*cellTransferExecutor),
		ConnMgr:            cfg.ConnManager,
		Log:                cfg.Logger,
		players:            make(map[string]*PlayerLocation),
		sessionRoutes:      newSessionRoutes(),
		cfg:                cfg,
		coordEpoch:         uint64(time.Now().UnixNano()),
		bus:                service.NewBus(processIDFromConfig(cfg)),
		serviceEventRouter: newServiceEventRouter(),
	}
	if c.ConnMgr != nil {
		c.ConnMgr.SetWireLimits(cfg.WireLimits)
	}
	// Resolve the dimension profile now rather than at first replicator build:
	// an unimplemented profile panics, and a panic here names Config.Dimension
	// instead of surfacing several frames into schema assembly.
	_ = system.EngineBindingsFor(cfg.Dimension)

	c.invariantMode = cfg.InvariantMode
	c.wire = NewWireRegistry()
	c.wireLimits = clientProfile(cfg.WireLimits)
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

	// Wire the cross-process dispatcher onto the Bus. Lookup of the local
	// HostNetwork is lazy at publish time, so installing this in New (before
	// gateway / hosts are constructed in Build) is safe.
	c.installServiceEventDispatch()
	c.installSubscriptionFlush()

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
		Audit:     logAuditSink{log: c.Log},
		Process:   c,
	})

	// Register all builtin commands unconditionally — handler closures read
	// coord.Cells / coord.Hosts / coord.dispatcher at invocation time, so
	// registering before Build() populates them is safe. Remote-host and
	// standalone-gateway branches return early from Build() and would
	// otherwise miss console registration.
	c.registerAllBuiltins()

	return c
}

// Wire returns this process's client-facing message registry: the types
// clients may send, the types the server may send back, and the typed-op
// bindings. Every Stage this process creates is injected with it.
//
// Registries are per-Process. A binary that builds two Processes gives them
// two registries, which is the point: they used to share four package-global
// maps, and RegisterOp's duplicate path ends by overwriting the stored handler
// closure — so the second Process's RegisterAuthService silently repointed the
// first Process's auth ops at the second Process's service.
//
// Nil for a nil receiver, matching clientWireLimits: fixtures across
// pkg/universe build a Gateway or a Stage with no Process behind it, and every
// read on a nil *WireRegistry answers "nothing registered" — which is the
// truth for a process that does not exist.
func (c *Process) Wire() *WireRegistry {
	if c == nil {
		return nil
	}
	return c.wire
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
		registerServiceBusBuiltins,
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
	if def.Configure != nil {
		def.Configure(c)
	}
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

// OnConsoleReady registers a callback fired once the interactive admin console
// is constructed (after Build()). Receives the owning *Process so admin
// commands can wire registries without closure-capturing a pre-existing
// variable. Multiple hooks may be registered; they fire in registration order.
func (c *Process) OnConsoleReady(fn func(*Process, *engine.Console)) {
	c.onConsoleReady = append(c.onConsoleReady, fn)
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
//   - Active=true (user online elsewhere) → set Rejected=true. The gateway
//     closes the new WebSocket without dispatching a PlayerAssignment, so the
//     duplicate window sees a clean "already logged in" error and the
//     original session keeps playing uninterrupted.
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
		resp.Rejected = true
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
// index so a subsequent duplicate-login attempt for the same user_id can be
// detected (via activeUserLocked) and rejected with a clean error.
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

// touchActiveUser updates the activeUsers entry for a player who just landed
// on this Process via cross-cell transfer (boundary handoff or split/merge/
// migrate populate). Preserves GatewayID + ConnID (the client-facing
// gateway side hasn't changed across the transfer) and refreshes HostID +
// CellID to point at the new authoritative cell.
//
// If no entry exists yet (the source cell's notifySessionRemoved scrubbed
// it before our touch, or this is a fresh transfer on a process that never
// saw the original auth), the entry is recreated from the GatewayID +
// GatewayConnID carried in the TransferFrame. Without this call, a tab
// refresh after a cell crossing would see activeUsers[userID]==nil, miss
// the reconnect-routing path, and spawn a fresh duplicate entity that
// shadows the lingering grace-period session for ~30s — the exploit the
// player reported, since killing the clone yields the original's gear.
func (c *Process) touchActiveUser(userID uuid.UUID, username, gatewayID string, gatewayConnID uint32, hostID string, cellID MeshCellID) {
	if userID == uuid.Nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeUsers == nil {
		c.activeUsers = make(map[uuid.UUID]*activeUser)
	}
	if c.userIDByConn == nil {
		c.userIDByConn = make(map[uint32]uuid.UUID)
	}
	au := c.activeUsers[userID]
	if au == nil {
		au = &activeUser{
			UserID:    userID,
			Username:  username,
			GatewayID: gatewayID,
			ConnID:    gatewayConnID,
		}
		c.activeUsers[userID] = au
		if gatewayConnID != 0 {
			c.userIDByConn[gatewayConnID] = userID
		}
	}
	au.HostID = hostID
	au.CellID = cellID
}

// IsUserSessionActive reports whether userID currently holds a LIVE session
// (not the disconnected grace period). It is the gate the op-channel login
// guard uses to reject a duplicate-active login with a clean error response
// — mirroring the gateway's inline duplicate-active check
// (dispatchPostAuthAssignment) so the two agree. Without this guard the
// gateway's downstream rejection would Remove() the logging-in connection
// mid-op-dispatch, dropping the login response and hanging the client.
func (c *Process) IsUserSessionActive(userID uuid.UUID) bool {
	if userID == uuid.Nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	loc := c.activeUserLocked(userID)
	return loc != nil && loc.Active
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

// HostIDForCell returns the id of the host owning cell, or "" if not found.
// Exported wrapper around the internal locking helper so the mmokit facade
// (which can't take c.mu / host.mu) can resolve a cell's owning host for
// --node filtering in fan-out verbs.
func (c *Process) HostIDForCell(cell *Cell) string {
	return localHostIDFor(c, cell)
}

// AppendExtraMigrations adds an additional migration filesystem to
// Config.ExtraMigrations. Called by service-registration helpers (e.g.
// mmokit.RegisterAuthService) so the auth schema lands at startup.
func (c *Process) AppendExtraMigrations(fsys fs.FS) {
	c.cfg.ExtraMigrations = append(c.cfg.ExtraMigrations, fsys)
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
			// Consult the authoritative netID index (LookupNetID), the same
			// thread-safe Live+Replica index the entity command handlers use.
			// Route to the host whose cell holds the entity as Live — that's
			// the authoritative owner. (The earlier ReplicaNetIDs() path only
			// tracked border replicas, so it missed any live entity not near a
			// cell boundary, breaking RouteEntityOwner for them.)
			if _, pres, ok := cell.Stage.LookupNetID(netID); ok && pres == PresenceLive {
				return hostID
			}
		}
	}
	return ""
}

// LiveHostIDs returns the IDs of all hosts eligible to receive cell-
// related commands (RouteAllHosts fan-out). In all-in-one mode this is
// the single in-process host. In coordinator mode with a live
// HostRegistry these are the registered cell-bearing remote hosts —
// service-only hosts (--mode=service alone) are filtered out because
// they have no executor / systemDefs / VCM and cannot run cells or
// answer cell-targeted commands.
func (c *Process) LiveHostIDs() []string {
	if c.hostRegistry != nil {
		hosts := c.hostRegistry.cellBearingHosts()
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

// RemoteLogEntry is one log line forwarded by a host over MeshControl,
// stamped with the sender's host_id at decode time. Mmokit's admin
// factory wires Process.OnRemoteLogBatch to bridge this into
// admin.LogEntry + LogRing + TopicBus.
type RemoteLogEntry struct {
	HostID string
	Cat    string
	Msg    string
	TimeMs int64
}

// OnRemoteLogBatch installs the callback invoked when a host forwards a
// LogBatch over MeshControl. Wired by mmokit.DefaultAdminServerFactory.
// Safe to call before Build; the callback fires on the controlServer
// goroutine.
func (c *Process) OnRemoteLogBatch(fn func([]RemoteLogEntry)) {
	c.remoteLogBatch = fn
}

// Bus returns the per-process service event bus. Initialized in New;
// safe to call before Build. Exposed so external callers (tests,
// game-side wiring) can publish or subscribe directly without going
// through a service.Context. Cross-process fan-out is wired in Build,
// so for full distributed semantics call this after Build returns.
func (c *Process) Bus() *service.Bus {
	return c.bus
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

// SetProtocol stores the internal schema-dumper produced by mmokit.New
// from Config.Name. Engine-internal — games never call this.
func (c *Process) SetProtocol(p any) { c.protocol = p }

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
	// Typed-op routing (RouteGatewayLocal) requires the gateway colocated
	// with the service kind. Pure-RoleService processes can host the
	// service event bus end-to-end (Phase 3) but the gateway-local op
	// router won't fan out to remote services in this mode. Warn only
	// when --services= actually names something to instantiate, so
	// `--mode=all` on a default dev-server doesn't spam.
	if hasServiceRole && hasServiceKinds && !roles.Has(RoleGateway) {
		c.Log.Log(CatMeshCell, "service: --mode=service without gateway: typed-op handlers will not route from clients (RouteGatewayLocal needs the gateway colocated), but the service event bus IS reachable from publishers (Phase 3)")
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

	// Apply AdminConfig defaults so games don't have to spell them out.
	// AdminListen non-empty implies "user wants admin" — enable unless
	// they explicitly disabled it via --admin-enabled=false (which sets
	// Enabled to false at flag-parse time). The flag default is true.
	// Only run on coordinator-bearing processes — admin state lives on
	// coord, so host/gateway-only processes inherit the default
	// :9101 from BindFlags but skip the wiring (and the Postgres
	// requirement) here.
	// Set when the admin dashboard is turned off for want of a database, so
	// the explanation can be logged after categories are enabled below.
	adminDisabledNoDB := false
	if cfg.AdminListen != "" && cfg.Admin.Enabled && roles.Has(RoleCoordinator) {
		if cfg.Admin.SessionTTL == 0 {
			cfg.Admin.SessionTTL = 8 * time.Hour
		}
		if cfg.Admin.LockoutMaxAttempts == 0 {
			cfg.Admin.LockoutMaxAttempts = 5
		}
		if cfg.Admin.LockoutWindow == 0 {
			cfg.Admin.LockoutWindow = 15 * time.Minute
		}
		if cfg.Admin.AuditCap == 0 {
			cfg.Admin.AuditCap = 4096
		}
		if cfg.DBStore == nil {
			// Admin is default-ON (bootstrap.go binds --admin-enabled=true and
			// --admin-listen=:9101), so this panic fired for the smallest
			// possible program — the one docs/mmokit-guide.md puts at the top
			// of the page:
			//
			//	mmokit.New(mmokit.Config{Name: "x", AnonymousAuth: true}).Start()
			//
			// Nobody asked for a dashboard there. Every recipe in this
			// repository passes a database, which is the only reason it was
			// never noticed. Degrade instead: serve the operational endpoints
			// and say why the UI is absent.
			//
			// An operator who typed --admin-enabled explicitly is asking for
			// something this process cannot provide, and still gets an error.
			if cfg.Admin.EnabledExplicit || adminEnabledExplicitly() {
				panic(fmt.Errorf("coordinator: --admin-enabled requires a database — set --postgres-url, or drop the flag to run without the dashboard"))
			}
			// Clear the flag and fall through. Build() still has to register
			// debug commands, select service kinds and create the coordinator
			// service registry below; returning here would skip all of it.
			//
			// The message is deferred rather than logged here: log categories
			// are not enabled until further down this function, and
			// Logger.Log early-returns on a disabled category — the same trap
			// the comment above that enable call describes.
			cfg.Admin.Enabled = false
			adminDisabledNoDB = true
		}
		c.cfg = cfg
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

	if adminDisabledNoDB {
		c.Log.Log(CatMeshCell, "admin: dashboard disabled — no database configured (set --postgres-url to enable); /metrics, /commands and /events are still served on %s", c.cfg.AdminListen)
	}

	// Decide the mesh authentication posture before anything binds a mesh
	// listener or dials a peer. Must sit after the category enable above:
	// Logger.Log early-returns on a disabled category, so an earlier call
	// would silently drop the fingerprint line.
	c.resolveClusterSecretPosture(&cfg, roles)
	c.cfg = cfg

	// Console from Config; OnConsoleReady hooks register via Process.OnConsoleReady.
	// (PlayerRouter has no consumer today — gateway uses topology-based routing —
	// but the field is kept for forward compat.)
	c.consoleOpts = c.cfg.Console

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
		spatialCellSize = c.CellSize() / 10
	}

	// Initialize net ID allocator
	c.netIDAlloc = NewNetIDAllocator(0, netIDRangeSize)

	// Resolve dynamic partitioning defaults
	if cfg.DynamicPartitioning != nil {
		if cfg.DynamicPartitioning.MinCellSize <= 0 {
			cfg.DynamicPartitioning.MinCellSize = c.CellSize() / 4
		}
		c.partState = newPartitionState()
	}

	// RoleCoordinator: start the control plane (MeshControl gRPC server,
	// HostRegistry, AssignmentEngine). Always runs for pure-coordinator
	// processes (RoleCoordinator alone) and for coord+gateway-without-host
	// processes (which cannot function without a remote node joining).
	// For role sets that include RoleHost (`all` preset or an explicit
	// coordinator+host combination) the listener is OPT-IN via
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

	// Pure --mode=service process: open a HostNetwork listener so
	// publishers (gateways, hosts, other service-hosts) can reach this
	// process for direct ServiceEvent dispatch. No cells, no client WS
	// listener — the listener exists solely as a peer-mesh endpoint.
	// The MeshControl client registers us with coord so we appear in
	// PeerList.hosts and receive PeerList broadcasts (and so the bus
	// can ship ServiceEventSubscribe over the wire).
	if roles.Has(RoleService) && !roles.Has(RoleHost) && !roles.Has(RoleGateway) && !roles.Has(RoleCoordinator) {
		c.buildServiceHost()
	}

	// RoleGateway (embedded): coordinator is present; create an in-process
	// gateway. Auth-service responses drive PlayerAssignment dispatch via
	// the service.Bus subscribers wired by Gateway.subscribeToAuthEvents.
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
			topology:   newCachedTopology(c, c.CellSize()),
			tickRate:   uint32(cfg.TickRate),
		}
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
			hn, err := c.newMeshDataListener(gwHost, ":0")
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
		// Now that the gateway exists, subscribe it to the bus so it
		// observes auth login/logout events and populates authStates.
		if c.gateway != nil && c.bus != nil {
			c.gateway.subscribeToAuthEvents(c.bus)
		}
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
				targetHost.AddCell(cell2.CellID(), cell2)
				c.Control.mu.Lock()
				c.Control.cellToHostMap[cell2.MeshID()] = targetHost.ID
				c.Control.mu.Unlock()
				hostIdx++
				setups = append(setups, nodeSetup{cell2, systems})
			}
		}

		// Compute topology and wire neighbors
		c.Control.Topology = ComputeTopology(cells, c.CellSize())
		c.mu.Lock()
		for cell, neighborCells := range c.Control.Topology.Neighbors {
			nodeID := c.CellOwner[cell]
			node := c.Cells[nodeID]
			for _, nc := range neighborCells {
				neighborID := c.CellOwner[nc]
				node.Neighbors[neighborID] = c.Cells[neighborID]
			}
		}
		c.mu.Unlock()

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
					ownedCells = append(ownedCells, cell.MeshID())
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
			fireCellSystemsReady(c, s.cell)
		}
	}
	// For coordinator and remote-host modes, cell creation and host wiring are
	// driven by control-plane events (CellAssign / CellRelease).
	// Log categories were enabled at the top of Build() so every
	// lifecycle log line above respects the --log flag.

	// Re-stamp the bus's processID now that gateway/host IDs are finalized.
	// At New time, the Bus was constructed with processIDFromConfig(cfg)
	// which sees an empty cfg.GatewayID for auto-generated standalone
	// gateways — the actual gateway.id is populated above in
	// buildStandaloneGateway / buildEmbeddedGateway. Without this re-stamp,
	// the Bus's self-echo-skip identifier would be "local" while wire
	// ServiceEvents stamp the post-Build "gateway-XXXX" — self-echo would
	// fail and events would round-trip through the publisher's own gateway
	// peer.
	if c.bus != nil {
		c.bus.SetProcessID(c.processID())
	}

	c.assembleProtocol()
}

// assembleProtocol derives the process's protocol schema now that every
// registry is populated, so the answer is identical on every role and
// available at runtime rather than only on the --dump-schema path.
//
// Reached through the c.protocol `any` seam that SetProtocol installs, for the
// same reason dumpSchemaAndExit uses it: *mmokit.Protocol lives in the facade,
// which imports this package. A Process built without the facade has no
// protocol and nothing to assemble.
func (c *Process) assembleProtocol() {
	if p, ok := c.protocol.(interface{ AssembleFromProcess(*Process) }); ok {
		p.AssembleFromProcess(c)
	}
}

// SchemaFingerprint returns the structural hash of this process's
// client-visible protocol, or 0 when the process has no protocol installed —
// which is what a Process built through universe.New without the facade has,
// and is why 0 is reserved rather than being a possible hash value.
//
// Reached through the c.protocol `any` seam for the same reason
// assembleProtocol is: *mmokit.Protocol lives in the facade, which imports
// this package.
func (c *Process) SchemaFingerprint() uint32 {
	if p, ok := c.protocol.(interface{ SchemaFingerprint() uint32 }); ok {
		return p.SchemaFingerprint()
	}
	return 0
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

	hn, err := c.newMeshDataListener(host, ":0")
	if err != nil {
		panic(fmt.Errorf("coordinator: remote host mode NewHostNetwork: %w", err))
	}
	host.Network = hn
	hn.SetCoord(c)

	vcm := NewVirtualConnManager(hn, c.Log)
	vcm.SetWireLimits(c.cfg.WireLimits)
	hn.SetVCM(vcm)
	c.vcm = vcm

	c.controlClient = newMeshControlClient(c, hostID, cfg.CoordinatorAddr)
	c.Control.controlClient = c.controlClient
	// Start never errors — the reconnect loop spawns in the
	// background and handles dial failures via exponential
	// backoff. The node will keep trying to reach the coordinator
	// forever; operators can Ctrl+C to stop.
	_ = c.controlClient.Start(context.Background())

	// Forward every Log() emission to the coordinator over MeshControl
	// so the admin /logs view surfaces remote-host lines alongside
	// coord-local ones. Drops on burst; never blocks the game loop.
	forwarder := newMeshLogForwarder(c.controlClient.send)
	c.Log.AddHook(forwarder)
	go forwarder.Run(context.Background())

	host.netIDAlloc = c.netIDAlloc
	host.systemDefs = c.systemDefs
	host.executor = c.hostExecutors[hostID]
	host.vcm = c.vcm
	host.coord = c
}

// buildServiceHost wires a pure --mode=service process that dials a remote
// coordinator. Mirrors buildRemoteHost minus the cell-bearing setup
// (no executor, no VCM, no netID allocator) — the host exists solely as
// a MeshData listener so publishers can dispatch ServiceEvents directly,
// and as a MeshControl peer so coord includes it in PeerList broadcasts
// and routes ServiceEventSubscribe announcements through it.
func (c *Process) buildServiceHost() {
	cfg := c.cfg

	if cfg.CoordinatorAddr == "" {
		panic("coordinator: --mode=service requires --coordinator-addr=HOST:PORT")
	}

	hostID := cfg.HostID
	if hostID == "" {
		hostID = "service-" + uuid.NewString()[:8]
	}

	host := NewHost(hostID)
	host.Log = c.Log
	host.coord = c
	c.Hosts[hostID] = host

	hn, err := c.newMeshDataListener(host, ":0")
	if err != nil {
		panic(fmt.Errorf("coordinator: service-host NewHostNetwork: %w", err))
	}
	host.Network = hn
	hn.SetCoord(c)

	c.controlClient = newMeshControlClient(c, hostID, cfg.CoordinatorAddr)
	c.Control.controlClient = c.controlClient
	// Start never errors — the reconnect loop spawns in the background
	// and handles dial failures via exponential backoff.
	_ = c.controlClient.Start(context.Background())

	c.Log.Log(CatMeshCell, "service-host %q listening on %s -> coordinator %s", hostID, hn.Addr(), cfg.CoordinatorAddr)
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
	hn, err := c.newMeshDataListener(gwHost, ":0")
	if err != nil {
		panic(fmt.Errorf("coordinator: gateway mode NewHostNetwork: %w", err))
	}
	gwHost.Network = hn
	hn.SetCoord(c)

	c.gateway = &Gateway{
		id:          gwID,
		connMgr:     c.ConnMgr,
		log:         c.Log,
		coord:       nil, // standalone: no direct coordinator reference
		process:     c,   // local process backref — always set, used for cmdsys dispatch + serviceRouting
		cfg:         &c.cfg,
		sessions:    make(map[uint32]*localSession),
		authStates:  make(map[uint32]connAuthState),
		topology:    newCachedTopology(nil, c.CellSize()), // populated by PeerList broadcasts
		hostNetwork: hn,
		spawnOrch:   newSpawnOrchestrator(),
		tickRate:    uint32(cfg.TickRate),
		// wsAddr: TODO — plumb via Config.GatewayWSAddr when flag lands
	}
	c.gateway.sessionRoutes = c.sessionRoutes
	// c.httpServer is nil here — startHTTPListener() runs in Start() after Build().
	// Phase 2 must either re-mirror after Start() calls startHTTPListener() or
	// move the httpServer lifecycle onto Gateway directly.
	c.gateway.httpServer = c.httpServer
	hn.SetGateway(c.gateway)

	c.gateway.controlClient = newMeshGatewayClient(c.gateway, cfg.CoordinatorAddr)
	_ = c.gateway.controlClient.Start(context.Background())

	// Subscribe the gateway to bus events queued by mmokit.RegisterAuthService
	// at facade time, now that the gateway exists.
	if c.gateway != nil && c.bus != nil {
		c.gateway.subscribeToAuthEvents(c.bus)
	}
	c.gateway.connMgr.OnUpgrade = c.gateway.onWSUpgrade

	c.Log.Log(CatNetConn, "coordinator: standalone gateway %q -> coordinator %s (grpc=%s)", gwID, cfg.CoordinatorAddr, hn.Addr())
}

// OnCellSystemsReady, if set, is invoked after a cell's systems are constructed
// and Init'd — boot, dynamic assignment, and split/transfer alike — on the
// goroutine that built the cell, BEFORE its game loop starts ticking. mmokit
// sets this (via init()) to apply registry tunable values to fresh system
// instances. It must not be on the per-tick path.
var OnCellSystemsReady func(p *Process, cell *Cell)

func fireCellSystemsReady(p *Process, cell *Cell) {
	if OnCellSystemsReady != nil && p != nil && cell != nil {
		OnCellSystemsReady(p, cell)
	}
}

// defaultPlayerLeaveCleanup returns the framework lifecycle action for a
// player leaving an authoritative cell. A handoff demotes Live -> Replica
// before it transitions the source session to StateTransferring, so replicas
// are detached from the session but deliberately retained for visual
// continuity and eventual local-only expiry.
func defaultPlayerLeaveCleanup(base *Stage) func(*engine.PlayerSession, *engine.PlayerManager) {
	return func(s *engine.PlayerSession, _ *engine.PlayerManager) {
		if s.Entity == (ecs.Entity{}) || !base.ECSWorld().Alive(s.Entity) {
			return
		}
		if base.IsGhost(s.Entity) {
			return
		}
		if base.IsReplica(s.Entity) {
			s.Entity = ecs.Entity{}
			return
		}
		base.MarkForRemoval(s.Entity)
		s.Entity = ecs.Entity{}
	}
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
	// Fail rather than degrade to plaintext. Unlike the client-facing HTTP
	// listener, the mesh has no plaintext posture to fall back to: every peer
	// dials with TLS credentials and a plaintext server would reject them all
	// at handshake anyway, just less legibly.
	meshTLS, err := c.meshTLSConfig()
	if err != nil {
		panic(fmt.Errorf("coordinator: MeshControl TLS: %w", err))
	}
	grpcSrv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(meshTLS)),
		grpc.ChainStreamInterceptor(clusterSecretStreamInterceptor(cfg.ClusterSecret)),
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
		streams:         make(map[string]*controlStream),
		gatewayStreams:  make(map[string]*controlStream),
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
	// Systems in pkg/system read geometry from the engine: they cannot import
	// pkg/universe, so Stage is not reachable from them.
	eng.SetCellSize(c.CellSize())

	events := make(chan net.PlayerEvent, 64)

	base := NewStage(eng, cell, cfg.AoIRadius, nil, c.Wire())

	// Wire the typed client-input dispatch path (channel 0x00 post
	// Plan 1 Phase 5 unification; mmokit.HandleClient). All
	// client-originated inputs flow through this path — the legacy
	// OnInput / OnInputWith / InputBinding surface was deleted in
	// Plan G Phase 7.
	eng.SetClientInputTick(base.DispatchClientInput)
	// Inject this process's cell geometry, the same way clusterClock is
	// injected just below. NewStage defaults it, because a Stage built outside
	// a Process (tests, benchmarks) still needs a working value.
	base.baseCellSize = c.CellSize()
	base.spatialGrid = spatial.NewHashGrid(spatialBucketSize)
	if len(fromSplit) > 0 && fromSplit[0] {
		base.fromSplit = true
	}

	base.coord = c
	base.strictNetIDIndex = c.strictNetIDIndex
	if c.ClusterClock != nil {
		base.clusterClock = c.ClusterClock
	}

	// Framework-required session wiring (Entity / DebugFlags / UserID) on
	// player transfers now lives directly in Stage.SpawnFromTransferCore so
	// games that override onPlayerTransferReceived for game-specific behavior
	// (e.g. the space game's MapData send + PlayerSessions tracking) can't
	// accidentally drop those field assignments. The hook is purely an
	// extension point — no framework default is installed.
	//
	// Topology-transparent protocol: no default hook synthesizes a
	// client-visible event on cell rename or player transfer. Client state
	// reset is driven purely by the FRAME_FLAG_FRESH_SNAPSHOT bit the
	// destination cell's ReplicationSystem sets on its first frame for the
	// migrated conn. Historical defaults sent SE_PLAYER_SPAWNED here, which
	// caused the client to wipe `state.entities` and `state.cellTopology`
	// on every merge rename — the 3+ tick blank visible on the screen.

	// Realize all registered kind specs against this cell's Stage. Must run
	// BEFORE the world factory so game-defined worlds can rely on
	// EntityKindDefs being populated when their constructor runs (e.g. to
	// spawn initial cell content via Stage.Spawn). Also before system
	// creation so systems like NetworkSystem see a fully-populated
	// EntityKindDefs.
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
	defaultLeaveCleanup := defaultPlayerLeaveCleanup(base)
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

	{
		bs := &BoundarySystem{stage: base}
		bs.SetDeps(eng.ECS, eng)
		bs.BindQueries(bs)
		gameSystems = append(gameSystems, bs)
		systemNames = append(systemNames, "CellBoundary")
	}

	node := NewCell(cell.MeshID(), cell)
	node.Engine = eng
	node.Stage = base
	node.Inbox = make(chan CellMessage, 256)
	node.Events = events
	node.Neighbors = make(map[MeshCellID]*Cell)
	node.Log = cfg.Logger

	// Wire session callbacks. notifySessionActive/Disconnected take both the
	// host ID (for cross-host coordination) and the cell ID (for the
	// gateway's reconnect-routing path: a quick browser refresh's new conn
	// dispatches PlayerAssignment{IsReconnect} to coord.players[user].CellID
	// to find the cell holding the lingering disconnected session). Both
	// resolve at call time so merge renames + post-create migrations are
	// reflected.
	eng.Players.SetSessionCallbacks(
		func(username string) { c.notifySessionActive(username, c.HostForCellID(node.MeshID()), node.MeshID()) },
		func(username string) {
			c.notifySessionDisconnected(username, c.HostForCellID(node.MeshID()), node.MeshID())
		},
		func(username string) { c.notifySessionRemoved(username) },
	)

	gameHooks := base.Hooks()
	tickDt := float32(1.0 / float32(platformCfg.TickRate))
	mergedHooks := engine.Hooks{
		OnConnect:    gameHooks.OnConnect,
		OnDisconnect: gameHooks.OnDisconnect,
		AfterSystem: func() {
			base.Commands().Flush()
		},
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
	c.Cells[node.MeshID()] = node
	c.CellOwner[cell] = node.MeshID()

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
	c.startUDPListener(ctx)

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
			return DispatchTypedOpInbound(c.Wire(), payload, ctx, c, c.clientWireLimits())
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

	// Wire dynamic completion sources so tab-complete on args like
	// <hostID>, <cellID>, <gatewayID>, <sessionKey> pulls live values
	// from the coord's registries. `players` is set by game lifecycle.
	c.wireCompletionSources()

	// Let the game (if any) register its own commands via the OnConsoleReady hooks.
	for _, hook := range c.onConsoleReady {
		hook(c, c.console)
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
		bucket = c.CellSize() / 10
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
	fireCellSystemsReady(c, node)

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
	delete(c.CellOwner, node.CellID())
	host.RemoveCell(node.CellID())
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
	host.RemoveCell(cell.CellID())
	host.AddCell(toCellID, cell)
	// Update coord's Cells / CellOwner maps (local-host copies — in
	// remote-host mode c.Cells is only the cells this process owns).
	delete(c.Cells, from)
	delete(c.CellOwner, cell.CellID())
	c.Cells[to] = cell
	c.CellOwner[toCellID] = to
	c.mu.Unlock()

	// Rewrite the cell's own identity on its game loop so PostSystems
	// reads don't race.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := cell.Engine.RunOnLoop(ctx, func() error {
		// One atomic swap, not two field writes: a concurrent reader
		// (Host.CellByID from a gRPC goroutine, admin views) sees either the
		// whole old identity or the whole new one, never a mesh ID paired
		// with the wrong coordinate.
		cell.setIdentity(to, toCellID)
		if cell.Stage != nil {
			cell.Stage.UpdateCellBounds(toCellID, c.CellSize())
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
// Service-only hosts are filtered out — drain/migrate destinations must
// be cell-bearing.
func (c *Process) survivingHostIDs(leavingHostID string) []string {
	if c.hostRegistry != nil {
		live := c.hostRegistry.cellBearingHosts()
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
	if c.adminServer != nil {
		c.adminServer.Stop()
	}

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
				GatewayId: key.GatewayID,
				ConnId:    connID,
				NewHostId: destHost,
				NewCellId: string(destCellID),
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
				GatewayId: key.GatewayID,
				ConnId:    key.ConnID,
				NewHostId: destHost,
				NewCellId: string(destCellID),
				NewEpoch:  newEpoch,
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

func (c *Process) setPlayerNode(connID uint32, nodeID MeshCellID) uint64 {
	key := SessionKey{GatewayID: InprocGatewayID, ConnID: connID}
	return c.sessionRoutes.AdvanceCell(key, nodeID)
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
	baseSize := c.CellSize()

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
		if id == newCell.MeshID() {
			continue
		}
		seen[string(id)] = true
		candidates = append(candidates, candidate{id: string(id), cid: cc.CellID(), cell: cc})
	}
	for _, id := range remoteIDs {
		if MeshCellID(id) == newCell.MeshID() || seen[id] {
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
		if !AreAdjacent(newCell.CellID(), cand.cid, baseSize) {
			continue
		}
		if cand.cell != nil {
			// Local neighbor — wire both directions and invalidate the
			// existing neighbor's dispatcher so it picks us up too.
			newCell.Neighbors[MeshCellID(cand.id)] = cand.cell
			cand.cell.Neighbors[newCell.MeshID()] = newCell
			if eb := unwrapCellBridge(cand.cell.Bridge); eb != nil {
				eb.invalidateBorderDispatcher()
			}
			continue
		}
		// Remote neighbor — stub entry. The remote host handles its own
		// reverse-side wiring when it applies the same PeerList.
		// Remote stub: identity only, never runs a loop here.
		newCell.Neighbors[MeshCellID(cand.id)] = NewCell(MeshCellID(cand.id), cand.cid)
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
	if ok && node.Metrics != nil {
		return node.Metrics.Snapshot(), true
	}
	// Fall through to remote-cell cache for distributed-mode lookups.
	c.remoteCellMetricsMu.RLock()
	defer c.remoteCellMetricsMu.RUnlock()
	if e, ok := c.remoteCellMetrics[nodeID]; ok {
		return e.snap, true
	}
	return metrics.LoadSnapshot{}, false
}

// allCellLoads returns load snapshots for all nodes. Used by MetricsHandler.
// Local-cell metrics take precedence; remote-cell metrics (cached from
// Heartbeat reports) fill in cells the coordinator process doesn't own —
// the distributed-mode case where the dashboard runs on a pure coordinator.
func (c *Process) allCellLoads() map[string]metrics.LoadSnapshot {
	remote := c.remoteCellMetricsSnapshot()
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]metrics.LoadSnapshot, len(c.Cells)+len(remote))
	for id, snap := range remote {
		result[id] = snap
	}
	for id, node := range c.Cells {
		if node.Metrics != nil {
			result[string(id)] = node.Metrics.Snapshot()
		}
	}
	return result
}

// processMetrics returns the process-scoped half of a scrape: the ingress
// rejection tally plus the client UDP listener's packet-level refusals.
//
// These are deliberately NOT shipped to the coordinator's aggregated /metrics
// the way per-cell load is. A remote host's cell metrics travel inside
// Heartbeat.metrics as a CellMetricsReport, so carrying ingress counters the
// same way would mean new proto fields — but the reason not to is
// architectural, not the cost of a schema change (roadmap §6.8.2 is explicit
// that a proto change is a normal cost here). Non-goal 3 says the coordinator
// is a control plane and must never become a payload or diagnostics relay, and
// ingress counters are per-process by nature: the number an operator needs is
// "how much is THIS gateway refusing", which is meaningless once summed across
// a cluster and attributed to the coordinator. Scrape each process's own
// /metrics — every role now serves a non-empty one.
func (c *Process) processMetrics() metrics.ProcessSnapshot {
	snap := metrics.ProcessSnapshot{Ingress: metrics.Ingress().Snapshot()}
	if udp, ok := c.UDPStats(); ok {
		snap.UDP = metrics.UDPDropSnapshot{
			Bound:                true,
			SourceMismatchDrops:  udp.SourceMismatchDrops,
			CapacityDrops:        udp.CapacityDrops,
			HandshakeRejectDrops: udp.HandshakeRejectDrops,
		}
	}
	return snap
}

// MetricsHandler returns an HTTP handler that serves Prometheus-compatible
// metrics for all nodes. Mount on your HTTP mux: mux.Handle("/metrics", coord.MetricsHandler())
//
// Both halves are wired: allCellLoads is empty on a process that owns no cells
// (a gateway-role-only one), and processMetrics is what makes such a process
// serve real samples instead of a body of header comments.
func (c *Process) MetricsHandler() http.HandlerFunc {
	return metrics.Handler(c.allCellLoads, c.processMetrics)
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
// CellSize returns the base cell width/height this process was configured with.
//
// Exported because four call sites hold a *Process and can never hold a *Stage:
// they compute the world position that RESOLVES which cell applies, so there is
// no stage to ask yet. Game code with a stage in hand should prefer
// Stage.CellSize() — same value, no lookup.
// Dimension returns the spatial profile this process was constructed with.
// Fixed for the life of the process.
func (c *Process) Dimension() Dimension { return c.cfg.Dimension }

func (c *Process) CellSize() float32 {
	if c.cfg.CellSize > 0 {
		return c.cfg.CellSize
	}
	return coords.DefaultCellSize
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
