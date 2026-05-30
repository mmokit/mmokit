package universe

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	pkgnet "github.com/zenion/mmoserver/pkg/net"
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
	stringFlag("admin-listen",
		"admin HTTP listen addr for /events, /commands, /metrics, /admin/* (default :9101, only binds on RoleCoordinator processes; pass empty to disable)",
		":9101", &c.AdminListen)
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

// DisabledPartitionConfig returns a *PartitionConfig with auto-split and
// auto-merge both disabled. Use this when a game wants to opt out of the
// default-on dynamic cell partitioning that New installs when
// Config.DynamicPartitioning is nil. Keeping the field non-nil prevents the
// default from re-enabling it, while the zeroed thresholds guarantee the
// monitor never triggers a split or merge.
func DisabledPartitionConfig() *PartitionConfig {
	return &PartitionConfig{
		AutoSplitEnabled: false,
		AutoMergeEnabled: false,
		EvalInterval:     1 * time.Hour,
	}
}

// dumpSchemaAndExit assembles the ProtocolSchema from the runtime registries
// and prints it as JSON to stdout, then exits. Called from Start when
// --dump-schema is set. The protocol must implement AssembleFromProcess +
// WriteSchema; the *mmokit.Protocol type satisfies the interface and is
// installed by mmokit.New via SetProtocol.
func (c *Process) dumpSchemaAndExit() {
	p, ok := c.protocol.(interface {
		AssembleFromProcess(*Process)
		WriteSchema(io.Writer) error
	})
	if !ok {
		fmt.Fprintln(os.Stderr, "dump-schema: no protocol installed — was the Process constructed via mmokit.New with Config.Name set?")
		os.Exit(1)
	}
	p.AssembleFromProcess(c)
	if err := p.WriteSchema(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "dump-schema: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
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
	mux.HandleFunc("/ws", c.ConnMgr.HandleWebSocket)
	// Diagnostic endpoints — heartbeat WS + write-path stats. Live
	// alongside /ws on the gateway listener so the Bun probe and
	// in-browser overlay can reach them without any extra config.
	mux.HandleFunc("/probe-ws", pkgnet.HandleProbeWS)
	mux.HandleFunc("/debug/conn-stats", c.ConnMgr.HandleConnStats)
	mux.Handle("/metrics", c.MetricsHandler())
	mux.Handle("/commands", handleCommandList(c.registry))
	mux.Handle("/commands/", handleCommandDescribe(c.registry))
	mux.HandleFunc("/events", handleCommitLogEvents(c.commitLog))

	if c.cfg.HTTPRoutes != nil {
		c.cfg.HTTPRoutes(mux)
	}

	addr := fmt.Sprintf(":%d", c.cfg.HTTPPort)
	c.httpServer = &http.Server{Addr: addr, Handler: corsMiddleware(c.cfg.CORSOrigins, mux)}
	if c.cfg.CORSOrigins != "" {
		c.Log.Log(CatMeshCell, "http: CORS enabled for origins: %s", c.cfg.CORSOrigins)
	}
	c.Log.Log(CatMeshCell, "http: listening on %s (roles=%s)", addr, c.roles)

	go func() {
		err := c.httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.Log.Log(CatMeshCell, "http: listener error: %v", err)
		}
	}()
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

	c.adminHTTPServer = &http.Server{Addr: c.cfg.AdminListen, Handler: mux}
	c.Log.Log(CatMeshCell, "admin-http: listening on %s (roles=%s)", c.cfg.AdminListen, c.roles)

	go func() {
		err := c.adminHTTPServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.Log.Log(CatMeshCell, "admin-http: listener error: %v", err)
		}
	}()
}
