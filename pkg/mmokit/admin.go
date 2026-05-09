package mmokit

import (
	"sync"

	"github.com/zenion/mmoserver/pkg/admin"
	"github.com/zenion/mmoserver/pkg/universe"
)

// AdminPanelDef is the mmokit facade alias for admin.PanelDef. Games register
// custom dashboard panels via mmokit.RegisterAdminPanel.
type AdminPanelDef = admin.PanelDef

// AdminConfig is the facade alias for universe.AdminConfig.
type AdminConfig = universe.AdminConfig

// AdminOperatorConfig is the facade alias for universe.AdminOperatorConfig.
type AdminOperatorConfig = universe.AdminOperatorConfig

// adminPanelRegistries maps each *universe.Process to its admin.PanelRegistry,
// allowing games to register panels before the admin Server is built. The
// factory below pulls the same registry into the Server it constructs, so
// game-registered panels appear in /admin/api/panels.
var (
	adminPanelMu  sync.Mutex
	adminPanelMap = map[*universe.Process]*admin.PanelRegistry{}
)

func adminPanelRegistry(c *universe.Process) *admin.PanelRegistry {
	adminPanelMu.Lock()
	defer adminPanelMu.Unlock()
	r, ok := adminPanelMap[c]
	if !ok {
		r = admin.NewPanelRegistry()
		adminPanelMap[c] = r
	}
	return r
}

// RegisterAdminPanel adds a game-defined panel to the dashboard sidebar. The
// dashboard SPA reads /admin/api/panels at boot and renders any registered
// panel reflectively from this metadata. Returns an error on duplicate IDs
// or empty PanelDef.ID.
//
// Call BEFORE the admin Server is built (i.e. before Process.Start). The
// engine-shipped builtins (Cluster, Hosts, Players, etc.) register
// automatically on NewServer; games add to the same registry here.
func RegisterAdminPanel(coord *universe.Process, def AdminPanelDef) error {
	return adminPanelRegistry(coord).Register(def)
}

// DefaultAdminServerFactory returns a factory that constructs the
// engine-shipped admin.Server from a *universe.Process and its config.
// Set on universe.Config.Admin.ServerFactory when wiring a coordinator
// that wants the admin dashboard.
//
// The returned closure satisfies universe.AdminConfig.ServerFactory
// without exposing pkg/admin types to the universe layer (which can't
// import pkg/admin because pkg/admin imports universe).
func DefaultAdminServerFactory() func(*universe.Process) universe.AdminServer {
	return func(c *universe.Process) universe.AdminServer {
		cfg := c.Cfg()
		ac := cfg.Admin
		view := admin.NewLocalClusterView(c)
		ops := make([]admin.OperatorConfig, 0, len(ac.Operators))
		for _, o := range ac.Operators {
			ops = append(ops, admin.OperatorConfig{
				Username:     o.Username,
				PasswordHash: o.PasswordHash,
				Grants:       o.Grants,
			})
		}
		return admin.NewServer(admin.ServerOpts{
			View:         view,
			Registry:     c.CmdRegistry(),
			Dispatcher:   c.CmdDispatcher(),
			SessionStore: admin.NewMemorySessionStore(),
			Panels:       adminPanelRegistry(c),
			Logger:       c.Log,
			Process:      c,
			Config: admin.Config{
				BindAddr:   cfg.AdminListen,
				SessionTTL: ac.SessionTTL,
				LockoutMax: ac.LockoutMaxAttempts,
				LockoutWin: ac.LockoutWindow,
				AuditCap:   ac.AuditCap,
				Operators:  ops,
			},
		})
	}
}
