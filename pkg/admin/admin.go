package admin

import (
	"context"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/admin/static"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/universe"
)

// Logger is the minimal logger surface admin.Server needs. *logger.Logger
// satisfies it implicitly.
type Logger interface {
	Log(category, format string, args ...any)
}

// Config is the construction-time bundle for NewServer.
type Config struct {
	BindAddr   string        // for cookie Secure-flag relaxing on loopback
	SessionTTL time.Duration // default 8h
	CookieOpts CookieOpts    // default: Path=/admin, Secure, SameSite=Strict
	LockoutMax int           // default 5
	LockoutWin time.Duration // default 15m
	AuditCap   int           // default 4096

	Operators []OperatorConfig
}

// OperatorConfig is one entry in the Admin operators list.
type OperatorConfig struct {
	Username     string
	PasswordHash string
	Grants       []string
}

// Server is the admin HTTP handler set. Construct with NewServer; mount onto
// any net/http mux with Mount.
type Server struct {
	view       ClusterView
	registry   *cmdsys.Registry
	dispatcher *cmdsys.Dispatcher
	sessions   SessionStore
	audit      *AuditLog
	lockout    *Lockout
	operators  map[string]OperatorConfig
	panels     *PanelRegistry
	bus        *TopicBus
	log        Logger
	logger     *logger.Logger

	cfg Config

	cancel context.CancelFunc
}

// ServerOpts is the runtime-injected dependency bundle. Built by the universe
// layer in bootstrap.go.
type ServerOpts struct {
	View         ClusterView
	Registry     *cmdsys.Registry
	Dispatcher   *cmdsys.Dispatcher
	SessionStore SessionStore
	Panels       *PanelRegistry
	Logger       *logger.Logger
	Process      *universe.Process // for publishers; only the Process needs to live this long
	Config       Config
}

// NewServer wires the dependencies. Caller still owns the publishers' lifetime
// — they stop when Server.Stop is called.
func NewServer(opts ServerOpts) *Server {
	cfg := opts.Config
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 8 * time.Hour
	}
	if cfg.AuditCap == 0 {
		cfg.AuditCap = 4096
	}
	if cfg.CookieOpts == (CookieOpts{}) {
		cfg.CookieOpts = devLoopbackRelaxed(defaultCookieOpts(), cfg.BindAddr)
	}
	ops := make(map[string]OperatorConfig, len(cfg.Operators))
	for _, o := range cfg.Operators {
		ops[strings.ToLower(o.Username)] = o
	}
	bus := NewTopicBus(0)
	s := &Server{
		view:       opts.View,
		registry:   opts.Registry,
		dispatcher: opts.Dispatcher,
		sessions:   opts.SessionStore,
		audit:      NewAuditLog(cfg.AuditCap),
		lockout:    NewLockout(cfg.LockoutMax, cfg.LockoutWin),
		operators:  ops,
		panels:     opts.Panels,
		bus:        bus,
		log:        opts.Logger,
		logger:     opts.Logger,
		cfg:        cfg,
	}
	if opts.Process != nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		startPublishers(ctx, opts.Process, opts.View, bus)
	}
	if opts.Panels != nil {
		_ = RegisterBuiltinPanels(opts.Panels)
	}
	return s
}

// Stop halts the publishers and closes the bus.
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.bus.Close()
}

// Mount registers all admin routes on the given mux. Pass the AdminListen
// or HTTPPort mux; the static SPA is served from /admin/.
func (s *Server) Mount(mux *http.ServeMux) {
	// Static SPA — Task 16 puts the real embed.FS at staticDist.
	mux.Handle("/admin/", http.StripPrefix("/admin", http.FileServer(http.FS(staticDist))))

	// Auth — login is unauthenticated (lockout is the only gate).
	mux.HandleFunc("/admin/api/auth/login", s.handleLogin)
	mux.Handle("/admin/api/auth/logout", s.requireSession(http.HandlerFunc(s.handleLogout)))
	mux.Handle("/admin/api/auth/session", s.requireSession(http.HandlerFunc(s.handleSession)))

	// Read API.
	mux.Handle("/admin/api/cluster", s.requireSession(http.HandlerFunc(s.handleCluster)))
	mux.Handle("/admin/api/cells", s.requireSession(http.HandlerFunc(s.handleCells)))
	mux.Handle("/admin/api/cells/", s.requireSession(http.HandlerFunc(s.handleCells)))
	mux.Handle("/admin/api/hosts", s.requireSession(http.HandlerFunc(s.handleHosts)))
	mux.Handle("/admin/api/gateways", s.requireSession(http.HandlerFunc(s.handleGateways)))
	mux.Handle("/admin/api/players", s.requireSession(http.HandlerFunc(s.handlePlayers)))
	mux.Handle("/admin/api/players/", s.requireSession(http.HandlerFunc(s.handlePlayers)))
	mux.Handle("/admin/api/events", s.requireSession(http.HandlerFunc(s.handleEvents)))
	mux.Handle("/admin/api/perf/", s.requireSession(http.HandlerFunc(s.handlePerf)))
	mux.Handle("/admin/api/audit", s.requireSession(http.HandlerFunc(s.handleAudit)))
	mux.Handle("/admin/api/panels", s.requireSession(http.HandlerFunc(s.handlePanels)))
	mux.Handle("/admin/api/logs/categories", s.requireSession(http.HandlerFunc(s.handleLogCategories)))
	mux.Handle("/admin/api/logs/categories/", s.requireSession(http.HandlerFunc(s.handleLogToggle)))

	// Commands.
	mux.Handle("/admin/api/commands", s.requireSession(http.HandlerFunc(s.handleCommandsList)))
	mux.Handle("/admin/api/commands/", s.requireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.handleCommandDescribe(w, r)
		case http.MethodPost:
			s.handleCommandInvoke(w, r)
		default:
			writeJSONError(w, http.StatusMethodNotAllowed, "GET or POST")
		}
	})))

	// Stream.
	mux.Handle("/admin/api/stream", s.requireSession(http.HandlerFunc(s.handleStream)))
}

// staticDist is the SPA filesystem, embedded from pkg/admin/static/dist via
// the //go:embed directive in pkg/admin/static/dist.go. v1 ships a placeholder
// index.html; the real Svelte SPA is built into dist/ by `just admin-build`
// in a follow-up plan.
var staticDist fs.FS = mustSubFS(static.FS, "dist")

func mustSubFS(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
