package universe

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	pkgnet "github.com/mmokit/mmokit/pkg/net"
)

// BindFlags registers every engine-universal CLI flag on flag.CommandLine,
// writing into c. Field-level default override: if a Config field is non-zero
// before this call, that value is used as the flag default; otherwise the
// engine default applies. Call before any game-specific flag.* registrations
// on flag.CommandLine, then call flag.Parse(). c will hold the final parsed
// values once flag.Parse returns.
//
// The method uses a pointer receiver so a plain local works:
//
//	cfg := mmokit.Config{ /* game defaults */ }
//	cfg.BindFlags()
//	flag.Parse()
//	coord := mmokit.New(cfg)
//
// Games never name the engine flags themselves.
// adminEnabledExplicitly reports whether --admin-enabled was named on the
// command line, as opposed to inheriting BindFlags' default of true.
//
// flag.Visit walks only the flags that were actually set, so this is false
// when nothing parsed flags at all — which is every test, since universe.New's
// `if !flag.Parsed()` guard skips BindFlags under `go test`. That is the right
// answer there: a fixture that did not ask for a dashboard should not be
// required to configure a database for one.
func adminEnabledExplicitly() bool {
	explicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "admin-enabled" {
			explicit = true
		}
	})
	return explicit
}

func (c *Config) BindFlags() {
	stringFlag := func(name, help, engineDefault string, field *string) {
		def := *field
		if def == "" {
			def = engineDefault
		}
		flag.StringVar(field, name, def, help)
	}
	intFlag := func(name, help string, engineDefault int, field *int) {
		def := *field
		if def == 0 {
			def = engineDefault
		}
		flag.IntVar(field, name, def, help)
	}

	stringFlag("mode",
		"role set: all | coordinator[,gateway][,host][,service] | host | gateway | service",
		"all", &c.Mode)
	flag.Func("services",
		"comma-separated list of service kinds to instantiate (RoleService only)",
		func(s string) error {
			parts := strings.Split(s, ",")
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					out = append(out, p)
				}
			}
			c.ServiceKinds = out
			return nil
		})
	stringFlag("postgres-url",
		"Postgres connection URL (empty = no DB; required by services with RequiresDB=true)",
		"", &c.PostgresURL)
	stringFlag("control-listen",
		"MeshControl listen addr (coordinator role)",
		":9100", &c.ControlListen)
	stringFlag("udp-listen",
		"client UDP game protocol listen addr (gateway role; AEAD-authenticated per packet under a key the client draws from POST /auth/udp-key; empty/off by default, pass e.g. :9000 to enable)",
		"", &c.UDPListen)
	stringFlag("admin-listen",
		"admin HTTP listen addr for /events, /commands, /metrics, /admin/* (default :9101, only binds on RoleCoordinator processes; pass empty to disable)",
		":9101", &c.AdminListen)
	stringFlag("tls-cert",
		"path to TLS certificate file for the client/admin HTTP listeners (requires --tls-key; empty = plaintext or proxy-terminated)",
		"", &c.TLSCertFile)
	stringFlag("tls-key",
		"path to TLS private key file (requires --tls-cert)",
		"", &c.TLSKeyFile)
	stringFlag("tls-mode",
		"TLS mode when no cert/key files are set: \"\" (plaintext) or \"self-signed\" (in-memory dev cert)",
		"", &c.TLSMode)
	// Read the env var BEFORE stringFlag so it becomes the flag's default:
	// stringFlag only applies its engineDefault when the field is still zero,
	// so an explicit --cluster-secret beats the env, which beats the preset
	// field. No flag.Visit needed (it is used nowhere in this module).
	if v := os.Getenv(clusterSecretEnvVar); v != "" {
		c.ClusterSecret = v
	}
	stringFlag("cluster-secret",
		"shared secret authenticating mesh peers (env: "+clusterSecretEnvVar+"); auto-generated for "+
			"single-process role sets, required on every process of a distributed cluster",
		"", &c.ClusterSecret)
	flag.Func("ws-allowed-origins",
		"comma-separated WebSocket Origin allowlist (empty = same-origin only)",
		func(s string) error {
			parts := strings.Split(s, ",")
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
			c.AllowedWSOrigins = out
			return nil
		})
	// Default-on: admin dashboard mounts whenever --admin-listen is set.
	// Operators can explicitly disable via --admin-enabled=false to get
	// the operational endpoints (/metrics, /commands, /events) without
	// the auth/UI surface.
	flag.BoolVar(&c.Admin.Enabled, "admin-enabled", true,
		"enable the admin dashboard at /admin/* (requires --admin-listen)")
	stringFlag("coordinator-addr",
		"MeshControl dial addr (host/gateway roles when running standalone)",
		"", &c.CoordinatorAddr)
	stringFlag("host-id",
		"stable host identifier when running as remote host (empty = auto)",
		"", &c.HostID)
	stringFlag("gateway-id",
		"stable gateway identifier for gateway role (empty = auto)",
		"", &c.GatewayID)
	stringFlag("gateway-mode",
		"bridge mode when in multi-host: local-shortcut (default) or always-proxy",
		"local-shortcut", &c.GatewayMode)
	stringFlag("log",
		"comma-separated log categories/groups to enable (e.g. mesh,net:conn)",
		"", &c.LogCategories)
	stringFlag("cors-origins",
		"comma-separated allowlist of browser origins for credentialed cross-origin requests (empty = none)",
		"", &c.CORSOrigins)
	intFlag("port",
		"HTTP server port for /ws, /metrics, /auth (gateway role only; -1 disables)",
		8080, &c.HTTPPort)
	// Client ingress decode ceilings (CE-002 criterion 3). Only the five the
	// criterion enumerates are exposed; the queue and per-drain caps stay
	// Config-only. Every default here is also applied as a zero-value fallback
	// in New() via WireLimits.Normalized, because a Config built by a test
	// fixture never sees a flag default — New's `if !flag.Parsed()` guard is
	// always false under `go test`.
	intFlag("wire-max-frame-bytes",
		"largest inbound client frame accepted, in bytes",
		pkgnet.DefaultMaxFrameBytes, &c.WireLimits.MaxFrameBytes)
	intFlag("wire-max-string-bytes",
		"largest wire-declared string accepted when decoding a client body",
		pkgnet.DefaultWireLimits().MaxStringBytes, &c.WireLimits.MaxStringBytes)
	intFlag("wire-max-slice-elems",
		"largest wire-declared slice element count accepted when decoding a client body",
		pkgnet.DefaultWireLimits().MaxSliceElems, &c.WireLimits.MaxSliceElems)
	intFlag("wire-max-depth",
		"maximum struct nesting depth accepted when decoding a client body",
		pkgnet.DefaultWireLimits().MaxDepth, &c.WireLimits.MaxDepth)
	intFlag("wire-max-alloc-bytes",
		"aggregate allocation budget for decoding one client body, in bytes",
		pkgnet.DefaultWireLimits().MaxTotalAllocBytes, &c.WireLimits.MaxTotalAllocBytes)
	flag.BoolVar(&c.Headless, "headless", c.Headless,
		"disable interactive console (for non-TTY environments)")
	flag.BoolVar(&c.DevInsecureCookie, "dev-insecure-cookie", c.DevInsecureCookie,
		"disable Secure flag on the auth session cookie (plain-HTTP local dev only)")
	flag.BoolVar(&c.DumpSchema, "dump-schema", false,
		"dump protocol schema JSON to stdout and exit (after Build, before Start)")
}

// DefaultPlayerRouter returns a PlayerRouter that routes every player to the
// cell containing world position (x, y). On processes without RoleHost
// (standalone gateway) it returns "" — the gateway's auth-success path uses
// cached topology to resolve the destination. Use this from every simple
// example/game that spawns everyone at a single point.
func DefaultPlayerRouter(coord *Process, x, y float32) PlayerRouter {
	return func(_ uuid.UUID, _ string) string {
		if !coord.Roles().Has(RoleHost) {
			return ""
		}
		return coord.CellAtPosition(x, y)
	}
}

// DisabledPartitionConfig returns a non-nil *PartitionConfig with auto-split
// and auto-merge disabled. Nil is the normal way to leave dynamic partitioning
// off; this helper is for callers that need an explicit non-nil sentinel while
// retaining other partition-related settings.
func DisabledPartitionConfig() *PartitionConfig {
	return &PartitionConfig{
		AutoSplitEnabled: false,
		AutoMergeEnabled: false,
		EvalInterval:     1 * time.Hour,
	}
}

// dumpSchemaAndExit prints the ProtocolSchema as JSON to stdout, then exits.
// Called from Start when --dump-schema is set.
//
// It only WRITES. Build() assembles the schema on every process, so what this
// dumps is the same value the running server would report — which is the
// property that lets a client be checked against it.
func (c *Process) dumpSchemaAndExit() {
	p, ok := c.protocol.(interface {
		WriteSchema(io.Writer) error
	})
	if !ok {
		fmt.Fprintln(os.Stderr, "dump-schema: no protocol installed — was the Process constructed via mmokit.New with Config.Name set?")
		os.Exit(1)
	}
	if err := p.WriteSchema(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "dump-schema: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// wsAllowedOrigins resolves the WebSocket Origin allowlist passed to the
// accept path. Explicit AllowedWSOrigins wins; otherwise it falls back to the
// CORSOrigins list — a browser origin trusted for credentialed cross-origin
// HTTP is also trusted to open a WebSocket, so operators serving the client
// cross-origin only configure --cors-origins. Empty result => same-origin only.
func wsAllowedOrigins(cfg Config) []string {
	if len(cfg.AllowedWSOrigins) > 0 {
		return cfg.AllowedWSOrigins
	}
	if cfg.CORSOrigins == "" {
		return nil
	}
	var out []string
	for _, o := range strings.Split(cfg.CORSOrigins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// startHTTPListener binds the engine-owned client HTTP server on c.cfg.HTTPPort
// and serves /ws, /auth, /metrics, and diagnostics. No-op unless the process has
// the Gateway role. HTTPPort < 0 unconditionally disables the listener so
// tests that call coord.Start directly can share a port-less config.
func (c *Process) startHTTPListener() {
	if !c.ServesClients() {
		c.Log.Log(CatMeshCell, "http: no listener — roles=%s does not serve clients", c.roles)
		return
	}
	if c.cfg.HTTPPort < 0 {
		c.Log.Log(CatMeshCell, "http: listener disabled (HTTPPort=%d)", c.cfg.HTTPPort)
		return
	}

	mux := http.NewServeMux()
	c.ConnMgr.AllowedOrigins = wsAllowedOrigins(c.cfg)
	mux.HandleFunc("/ws", c.ConnMgr.HandleWebSocket)
	// Diagnostic endpoints — heartbeat WS + write-path stats. Live
	// alongside /ws on the gateway listener so the Bun probe and
	// in-browser overlay can reach them without any extra config.
	mux.HandleFunc("/probe-ws", pkgnet.HandleProbeWS)
	mux.HandleFunc("/debug/conn-stats", c.ConnMgr.HandleConnStats)
	// UDP session-key issuance (CE-005b Tier 2). Mounted here rather than in
	// the auth service so it uses the gateway's own AuthResolver: the process
	// that hands out the key is then always the process that terminates the
	// UDP session, and no key crosses a process boundary in distributed mode.
	mux.HandleFunc("POST /auth/udp-key", c.handleUDPKeyIssue)
	mux.Handle("/metrics", c.MetricsHandler())
	mux.Handle("/commands", handleCommandList(c.registry))
	mux.Handle("/commands/", handleCommandDescribe(c.registry))
	mux.HandleFunc("/events", handleCommitLogEvents(c.commitLog))

	if c.cfg.HTTPRoutes != nil {
		c.cfg.HTTPRoutes(mux)
	}

	addr := fmt.Sprintf(":%d", c.cfg.HTTPPort)
	tlsCfg, selfSigned := c.httpTLSConfig()
	c.httpServer = &http.Server{
		Addr:      addr,
		Handler:   corsMiddleware(c.cfg.CORSOrigins, mux),
		TLSConfig: tlsCfg,
	}
	if c.cfg.CORSOrigins != "" {
		c.Log.Log(CatMeshCell, "http: CORS enabled for origins: %s", c.cfg.CORSOrigins)
	}
	if tlsCfg == nil && !isLoopbackBind(addr) {
		c.Log.Log(CatMeshCell, "http: WARNING serving plaintext on non-loopback address %s — session cookie and client traffic are unencrypted; set --tls-cert/--tls-key or terminate TLS at a reverse proxy", addr)
	}
	if selfSigned {
		c.Log.Log(CatMeshCell, "http: WARNING using in-memory self-signed TLS cert (--tls-mode=self-signed) — DO NOT use in production")
	}
	c.Log.Log(CatMeshCell, "http: listening on %s (roles=%s, tls=%v)", addr, c.roles, tlsCfg != nil)

	go func() {
		var err error
		if tlsCfg != nil {
			err = c.httpServer.ListenAndServeTLS("", "")
		} else {
			err = c.httpServer.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.Log.Log(CatMeshCell, "http: listener error: %v", err)
		}
	}()
}

// startUDPListener binds the engine-owned client UDP server on c.cfg.UDPListen
// and registers inbound connections with the shared ConnManager (same path as
// the HTTP /ws listener). No-op unless the process has the Gateway role; an
// empty UDPListen disables it. Stopped when ctx is cancelled (Start's ctx).
func (c *Process) startUDPListener(ctx context.Context) {
	if !c.ServesClients() {
		return
	}
	if c.cfg.UDPListen == "" {
		c.Log.Log(CatMeshCell, "udp: listener disabled (UDPListen empty)")
		return
	}
	udpServer, err := pkgnet.NewUDPServer(c.cfg.UDPListen, c.ConnMgr)
	if err != nil {
		c.Log.Log(CatMeshCell, "udp: listener bind error on %s: %v", c.cfg.UDPListen, err)
		return
	}
	// No exposure warning here any more. Until CE-005b Tier 2 every datagram
	// was cleartext and forgeable, and a non-loopback bind genuinely was a
	// hazard worth shouting about. Now every packet is sealed under a key
	// drawn over HTTPS and bound to a user, so the old warning would be
	// describing a transport that no longer exists — and a false warning
	// teaches operators to ignore the real ones.
	// Apply the configured ingress limits and keep the reference. Dropping
	// the *UDPServer on the floor here is what left SetWireLimits and every
	// drop-counter accessor without a production caller.
	udpServer.SetWireLimits(c.cfg.WireLimits)
	// Without this the listener cannot resolve the key IDs clients present in
	// ConnConfirm, and refuses every handshake.
	udpServer.SetKeyRegistry(c.udpKeyRegistry())
	// And without this the session would be encrypted but anonymous: the key
	// vouches for a user, but nothing downstream would learn who, so no
	// PlayerAssignment would ever be dispatched and the client would sit in a
	// working session with no entity. This is the UDP counterpart of the
	// cookie read in Gateway.onWSUpgrade — both turn an HTTPS-established
	// identity into a bound player session.
	udpServer.SetOnAuthenticated(c.bindUDPSession)
	c.udpServer.Store(udpServer)
	c.Log.Log(CatMeshCell, "udp: listening on %s (roles=%s, maxFrameBytes=%d)",
		c.cfg.UDPListen, c.roles, c.cfg.WireLimits.MaxFrameBytes)
	go udpServer.Run(ctx)
}

// bindUDPSession binds a freshly authenticated UDP session to the player it
// belongs to. Called once per session by the UDP listener, with the identity
// the presented key vouches for.
//
// The session token is deliberately empty. The UDP key registry holds a 32-byte
// transport secret per entry; storing the session cookie alongside it would
// raise the value of a registry compromise from "forge this client's UDP
// traffic until the key expires" to "hold the account". Nothing downstream
// reads PlayerAssignment.SessionToken — it is carried across the mesh and never
// consulted — and the AnonymousAuth path has always passed an empty one, so
// this costs nothing that is currently used.
func (c *Process) bindUDPSession(connID uint32, userID, username string) {
	if c.gateway == nil {
		c.Log.Log(CatNetConn, "udp: authenticated conn=%d user=%s but this process has no gateway to bind it to", connID, username)
		return
	}
	uid, err := uuid.Parse(userID)
	if err != nil {
		// Only reachable if something issued a key with a malformed user ID;
		// handleUDPKeyIssue writes resolved.UserID.String(). Refuse rather
		// than invent an identity — a random UUID here would spawn a fresh
		// character for a returning player.
		c.Log.Log(CatNetConn, "udp: authenticated conn=%d user=%s has unparseable user_id %q: %v", connID, username, userID, err)
		return
	}
	// This runs on its own goroutine, so the connection it names can already be
	// gone: a client may send an authenticated Disconnect straight after its
	// ConnConfirm, and the listener processes both before this ever reaches the
	// gateway. Binding a dead connection would spawn a player nothing owns and
	// stamp an activeUsers entry that no disconnect will clear — locking the
	// account out of its own next login until the entry ages out.
	if c.ConnMgr.Get(connID) == nil {
		c.Log.Log(CatNetConn, "udp: conn=%d user=%s disconnected before its identity bound; not assigning a player", connID, username)
		return
	}
	c.gateway.onAuthSuccess(connID, uid, username, "", 0)

	// Re-check after dispatch and undo if the connection went away while the
	// spawn was being resolved — resolveSpawn is an RPC on a standalone
	// gateway, so that window is milliseconds, not nanoseconds. handleDisconnect
	// has already run by then and will not run again, so this is the only place
	// the orphan can be cleaned up.
	if c.ConnMgr.Get(connID) == nil {
		c.Log.Log(CatNetConn, "udp: conn=%d user=%s disconnected mid-assignment; tearing the session back down", connID, username)
		c.gateway.handleDisconnect(pkgnet.PlayerEvent{ConnID: connID, Disconnect: true})
	}
}

// UDPServerStats is a snapshot of the client UDP listener's refusal counters.
// Bounded cardinality by construction: aggregate counters only, no per-peer or
// per-token breakdown, so an attacker cannot grow the reported set.
type UDPServerStats struct {
	SourceMismatchDrops uint64
	CapacityDrops       uint64
	// HandshakeRejectDrops counts ConnConfirms refused because the stateless
	// cookie failed, the named key did not resolve, or no key registry is
	// configured. It replaces the old PendingFullDrops/PendingCount pair, which
	// described a pending-handshake table that no longer exists.
	HandshakeRejectDrops uint64
}

// UDPStats reports the client UDP listener's refusal counters, and whether a
// listener is bound at all. Returns ok=false on processes without the Gateway
// role or with Config.UDPListen empty.
func (c *Process) UDPStats() (stats UDPServerStats, ok bool) {
	s := c.udpServer.Load()
	if s == nil {
		return UDPServerStats{}, false
	}
	return UDPServerStats{
		SourceMismatchDrops:  s.SourceMismatchDrops(),
		CapacityDrops:        s.CapacityDrops(),
		HandshakeRejectDrops: s.HandshakeRejectDrops(),
	}, true
}

// startAdminHTTPListener binds an admin HTTP server on Config.AdminListen
// exposing /events, /commands, /metrics, and /admin/*. Bound only on
// processes that bear RoleCoordinator — admin state (commit log, cluster
// view, audit ring) lives on the coordinator, and host/gateway-only
// processes would just bind a port serving empty data (and would conflict
// with the coord on shared-host multi-process setups). Pass
// --admin-listen="" to disable on the coordinator too.
func (c *Process) startAdminHTTPListener() {
	if c.cfg.AdminListen == "" || !c.roles.Has(RoleCoordinator) {
		return
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", c.MetricsHandler())
	mux.Handle("/commands", handleCommandList(c.registry))
	mux.Handle("/commands/", handleCommandDescribe(c.registry))
	mux.HandleFunc("/events", handleCommitLogEvents(c.commitLog))

	// Mount the admin dashboard when configured.
	if c.cfg.Admin.Enabled && c.cfg.Admin.ServerFactory != nil {
		c.adminServer = c.cfg.Admin.ServerFactory(c)
		c.adminServer.Mount(mux)
		c.Log.Log(CatMeshCell, "admin: mounted /admin/* on %s", c.cfg.AdminListen)
	}

	tlsCfg, selfSigned := c.httpTLSConfig()
	c.adminHTTPServer = &http.Server{Addr: c.cfg.AdminListen, Handler: mux, TLSConfig: tlsCfg}
	if tlsCfg == nil && !isLoopbackBind(c.cfg.AdminListen) {
		c.Log.Log(CatMeshCell, "admin-http: WARNING serving plaintext on non-loopback address %s — admin session cookie is unencrypted; set --tls-cert/--tls-key or terminate TLS at a reverse proxy", c.cfg.AdminListen)
	}
	// Log the self-signed banner here only when the client listener did not
	// (pure-coordinator processes don't serve clients, so this admin listener
	// is the sole TLS surface and would otherwise emit no warning). When the
	// client listener ran it already logged it once — httpTLSConfig memoizes,
	// so we'd be double-logging.
	if selfSigned && !c.ServesClients() {
		c.Log.Log(CatMeshCell, "admin-http: WARNING using in-memory self-signed TLS cert (--tls-mode=self-signed) — DO NOT use in production")
	}
	c.Log.Log(CatMeshCell, "admin-http: listening on %s (roles=%s, tls=%v)", c.cfg.AdminListen, c.roles, tlsCfg != nil)

	go func() {
		var err error
		if tlsCfg != nil {
			err = c.adminHTTPServer.ListenAndServeTLS("", "")
		} else {
			err = c.adminHTTPServer.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.Log.Log(CatMeshCell, "admin-http: listener error: %v", err)
		}
	}()
}
