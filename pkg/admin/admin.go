package admin

import "time"

// Server is the admin HTTP handler set. Construct with NewServer (Task 15);
// mount onto any net/http mux with Mount.
//
// This file is intentionally a stub for the foundation tasks — Task 15
// fleshes out NewServer/Mount and adds the rest of the fields. Earlier
// tasks (cookie/middleware/auth handlers) reference s.sessions, s.cfg,
// s.audit, s.lockout, s.operators, s.panels, s.bus, s.log, s.view,
// s.registry, s.dispatcher; those fields are added incrementally as their
// implementations land.
type Server struct {
	view       any // ClusterView; concrete in Task 15
	registry   any // *cmdsys.Registry
	dispatcher any // *cmdsys.Dispatcher
	sessions   SessionStore
	audit      any // *AuditLog (Task 10)
	lockout    any // *Lockout (Task 10)
	operators  map[string]OperatorConfig
	panels     *PanelRegistry
	bus        *TopicBus
	log        Logger

	cfg Config
}

// Logger is the minimal logger surface admin.Server needs. *logger.Logger
// satisfies it implicitly.
type Logger interface {
	Log(category, format string, args ...any)
}

// Config is the construction-time bundle for NewServer. Task 15 expands.
type Config struct {
	BindAddr   string
	SessionTTL time.Duration
	CookieOpts CookieOpts
	LockoutMax int
	LockoutWin time.Duration
	AuditCap   int
	Operators  []OperatorConfig
}

// OperatorConfig is one entry in the Admin operators list.
type OperatorConfig struct {
	Username     string
	PasswordHash string
	Grants       []string
}
