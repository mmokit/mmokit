package universe

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
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
		"admin HTTP listen addr for /events, /commands, /metrics (empty = disabled)",
		"", &c.AdminListen)
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
	stringFlag("web-dir",
		"web asset source: 'embed' (Config.StaticFS), '' or 'disabled' (off), or a filesystem path",
		"embed", &c.WebDir)
	intFlag("port",
		"HTTP server port for /ws, /metrics, and static assets (gateway role only; -1 disables)",
		8080, &c.HTTPPort)
	flag.BoolVar(&c.Headless, "headless", c.Headless,
		"disable interactive console (for non-TTY environments)")
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
// --dump-schema is set. The Protocol must implement AssembleFromProcess +
// WriteSchema; the *mmokit.Protocol type satisfies the interface.
func (c *Process) dumpSchemaAndExit() {
	p, ok := c.cfg.Protocol.(interface {
		AssembleFromProcess(*Process)
		WriteSchema(io.Writer) error
	})
	if !ok {
		fmt.Fprintln(os.Stderr, "dump-schema: Config.Protocol is nil or not a *mmokit.Protocol")
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
// and serves /ws, /metrics, and static assets. No-op unless the process has
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
	mux.Handle("/metrics", c.MetricsHandler())
	mux.Handle("/commands", handleCommandList(c.registry))
	mux.Handle("/commands/", handleCommandDescribe(c.registry))
	mux.HandleFunc("/events", handleCommitLogEvents(c.commitLog))

	switch c.cfg.WebDir {
	case "", "disabled":
		c.Log.Log(CatMeshCell, "http: static asset serving disabled")
	case "embed":
		if c.cfg.StaticFS == nil {
			c.Log.Log(CatMeshCell, "http: WebDir=embed but Config.StaticFS is nil — skipping static serving")
			break
		}
		rootFS := c.cfg.StaticFS
		if c.cfg.StaticFSPrefix != "" {
			sub, err := fs.Sub(c.cfg.StaticFS, c.cfg.StaticFSPrefix)
			if err != nil {
				c.Log.Log(CatMeshCell, "http: fs.Sub(%q) failed: %v — skipping static serving", c.cfg.StaticFSPrefix, err)
				break
			}
			rootFS = sub
		}
		mux.Handle("/", http.FileServer(http.FS(rootFS)))
		c.Log.Log(CatMeshCell, "http: serving embedded static assets from StaticFS")
	default:
		mux.Handle("/", http.FileServer(http.Dir(c.cfg.WebDir)))
		c.Log.Log(CatMeshCell, "http: serving static assets from %s", c.cfg.WebDir)
	}

	if c.cfg.HTTPRoutes != nil {
		c.cfg.HTTPRoutes(mux)
	}

	addr := fmt.Sprintf(":%d", c.cfg.HTTPPort)
	c.httpServer = &http.Server{Addr: addr, Handler: mux}
	c.Log.Log(CatMeshCell, "http: listening on %s (roles=%s)", addr, c.roles)

	go func() {
		err := c.httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.Log.Log(CatMeshCell, "http: listener error: %v", err)
		}
	}()
}

// startAdminHTTPListener binds an admin HTTP server on Config.AdminListen
// exposing /events, /commands, and /metrics. Used by pure-coordinator
// processes (and any process that wants separate operational endpoints
// away from the client-facing HTTP listener). No-op when AdminListen is
// empty. Unlike startHTTPListener, this is gated purely on config and
// does NOT depend on the Gateway role.
func (c *Process) startAdminHTTPListener() {
	if c.cfg.AdminListen == "" {
		return
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", c.MetricsHandler())
	mux.Handle("/commands", handleCommandList(c.registry))
	mux.Handle("/commands/", handleCommandDescribe(c.registry))
	mux.HandleFunc("/events", handleCommitLogEvents(c.commitLog))

	c.adminHTTPServer = &http.Server{Addr: c.cfg.AdminListen, Handler: mux}
	c.Log.Log(CatMeshCell, "admin-http: listening on %s (roles=%s)", c.cfg.AdminListen, c.roles)

	go func() {
		err := c.adminHTTPServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.Log.Log(CatMeshCell, "admin-http: listener error: %v", err)
		}
	}()
}
