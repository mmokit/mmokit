package admin

import (
	"context"
	"io/fs"
	"net/http"
	"time"

	"github.com/zenion/mmoserver/pkg/admin/static"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/persist"
	"github.com/zenion/mmoserver/pkg/services/auth"
	"github.com/zenion/mmoserver/pkg/universe"
)

// Config is the construction-time bundle for NewServer.
type Config struct {
	BindAddr   string        // for cookie Secure-flag relaxing on loopback
	SessionTTL time.Duration // default 8h
	CookieOpts CookieOpts    // default: Path=/admin, Secure, SameSite=Strict
	LockoutMax int           // default 5
	LockoutWin time.Duration // default 15m
	AuditCap   int           // default 4096
	LogRingCap int           // default 4096
}

// Server is the admin HTTP handler set. Construct with NewServer; mount onto
// any net/http mux with Mount.
type Server struct {
	view       ClusterView
	registry   *cmdsys.Registry
	dispatcher *cmdsys.Dispatcher
	sessions   SessionStore
	audit      *AuditLog
	logRing    *LogRing
	logPump    *logPump
	lockout    *Lockout
	operatorRepo persist.AdminOperatorRepository
	panels     *PanelRegistry
	bus        *TopicBus
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
	// Bus optionally injects a pre-created topic bus. Used by mmokit so game
	// code can publish to admin topics before the Server is constructed. If
	// nil, NewServer creates one internally.
	Bus *TopicBus
	// LogRing optionally injects a pre-created ring. Used by mmokit so
	// game-side wiring and the SSE pump share the same buffer. If nil,
	// NewServer creates one sized by cfg.LogRingCap.
	LogRing *LogRing
	// LocalHostID is the label stamped on log entries that originate in
	// this process (the in-process Hook path). Empty falls back to
	// "local" — but production wiring should pass the resolved process
	// ID (cfg.HostID, or a role-derived fallback like "coordinator").
	LocalHostID string
	Config      Config
	// OperatorRepo persists admin operators. When NewServer detects
	// an empty table (Count == 0), a default admin/admin operator is
	// seeded with grants ["*.*"]. Required — pass an in-memory mock
	// from pkg/persist/persisttest for tests.
	OperatorRepo persist.AdminOperatorRepository
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
	if cfg.LogRingCap == 0 {
		cfg.LogRingCap = 4096
	}
	if cfg.CookieOpts == (CookieOpts{}) {
		cfg.CookieOpts = devLoopbackRelaxed(defaultCookieOpts(), cfg.BindAddr)
	}
	bus := opts.Bus
	if bus == nil {
		bus = NewTopicBus(0)
	}
	ring := opts.LogRing
	if ring == nil {
		ring = NewLogRing(cfg.LogRingCap)
	}
	s := &Server{
		view:       opts.View,
		registry:   opts.Registry,
		dispatcher: opts.Dispatcher,
		sessions:   opts.SessionStore,
		audit:      NewAuditLog(cfg.AuditCap),
		logRing:    ring,
		lockout:    NewLockout(cfg.LockoutMax, cfg.LockoutWin),
		operatorRepo: opts.OperatorRepo,
		panels:     opts.Panels,
		bus:        bus,
		logger:     opts.Logger,
		cfg:        cfg,
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.logPump = newLogPump(s.logRing, bus, opts.LocalHostID)
	go s.logPump.Run(ctx)
	if opts.Logger != nil {
		opts.Logger.AddHook(s.logPump)
	}
	if opts.Process != nil {
		startPublishers(ctx, opts.Process, opts.View, bus)
	}
	if opts.Panels != nil {
		_ = RegisterBuiltinPanels(opts.Panels)
	}
	if opts.Registry != nil && opts.Logger != nil {
		if err := RegisterLogCommands(opts.Registry, opts.Logger); err != nil {
			// Best-effort: a shared registry may already have log.set
			// from a prior NewServer. Logging the failure is enough.
			opts.Logger.Log("admin", "RegisterLogCommands: %v", err)
		}
	}
	// Seed a default admin/admin operator if the table is empty.
	// First-run dev convenience: production deployments rotate via
	// future admin-UI user management.
	if err := seedDefaultOperator(ctx, opts.OperatorRepo, opts.Logger); err != nil {
		if opts.Logger != nil {
			opts.Logger.Log("admin", "seed default operator: %v", err)
		}
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
	mux.Handle("/admin/api/logs/recent", s.requireSession(http.HandlerFunc(s.handleLogsRecent)))
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

// seedDefaultOperator inserts a default admin/admin operator with the
// wildcard grant when the table is empty. Idempotent: a non-zero Count
// short-circuits the insert. Used on first run so the admin dashboard
// is reachable without manual seeding.
//
// Logs a banner with the credentials so operators don't need to grep
// the source — the expectation is rotation in production.
func seedDefaultOperator(ctx context.Context, repo persist.AdminOperatorRepository, log *logger.Logger) error {
	if repo == nil {
		return nil
	}
	n, err := repo.Count(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	hash, err := auth.HashPassword("admin", auth.DefaultArgonParams())
	if err != nil {
		return err
	}
	if err := repo.Create(ctx, &persist.AdminOperator{
		Username:     "admin",
		PasswordHash: hash,
		Grants:       []string{"*.*"},
	}); err != nil {
		return err
	}
	if log != nil {
		log.Log("admin", "seeded default operator: admin / admin (CHANGE IN PRODUCTION)")
	}
	return nil
}
