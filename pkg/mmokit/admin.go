package mmokit

import (
	"github.com/zenion/mmoserver/pkg/admin"
	"github.com/zenion/mmoserver/pkg/universe"
)

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
			Panels:       admin.NewPanelRegistry(),
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
