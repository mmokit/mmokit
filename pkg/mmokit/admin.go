package mmokit

import (
	"sync"
	"time"

	"github.com/zenion/mmoserver/pkg/admin"
	"github.com/zenion/mmoserver/pkg/universe"
)

// AdminPanelDef is the mmokit facade alias for admin.PanelDef. Games register
// custom dashboard panels via mmokit.RegisterAdminPanel.
type AdminPanelDef = admin.PanelDef

// localLogID resolves the label stamped on in-process log entries from
// this process. Prefers an explicit --host-id, falls back to --gateway-id,
// then derives a role-based default ("coordinator", "gateway", "host")
// so the log tail shows a meaningful node name rather than "local".
func localLogID(cfg universe.Config) string {
	if cfg.HostID != "" {
		return cfg.HostID
	}
	if cfg.GatewayID != "" {
		return cfg.GatewayID
	}
	roles, _ := universe.ParseRoles(cfg.Mode)
	switch {
	case roles.Has(universe.RoleCoordinator):
		return "coordinator"
	case roles.Has(universe.RoleHost):
		return "host"
	case roles.Has(universe.RoleGateway):
		return "gateway"
	default:
		return "local"
	}
}

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

// adminBusMap caches the *admin.TopicBus per *universe.Process so game-side
// code can publish to admin topics before the admin Server is constructed.
// DefaultAdminServerFactory pulls the same bus into ServerOpts so the
// SSE multiplexer fans payloads out to subscribers.
var (
	adminBusMu  sync.Mutex
	adminBusMap = map[*universe.Process]*admin.TopicBus{}
)

func adminBus(c *universe.Process) *admin.TopicBus {
	adminBusMu.Lock()
	defer adminBusMu.Unlock()
	b, ok := adminBusMap[c]
	if !ok {
		b = admin.NewTopicBus(0)
		adminBusMap[c] = b
	}
	return b
}

// adminLogRingMap caches the *admin.LogRing per *universe.Process so the
// in-process log pump and the remote-log callback both append to the
// same ring (and the admin Server reads from that same ring).
var (
	adminLogRingMu  sync.Mutex
	adminLogRingMap = map[*universe.Process]*admin.LogRing{}
)

func adminLogRing(c *universe.Process, cap int) *admin.LogRing {
	adminLogRingMu.Lock()
	defer adminLogRingMu.Unlock()
	r, ok := adminLogRingMap[c]
	if !ok {
		r = admin.NewLogRing(cap)
		adminLogRingMap[c] = r
	}
	return r
}

// PublishAdminTopic publishes payload on topic to the admin dashboard's
// SSE multiplexer. Game-registered admin panels subscribe to topics by
// name (PanelDef.Topics) — this is the matching push surface.
//
// No-op when no subscribers are listening. Safe to call from any
// goroutine. The bus is per-Process so test fixtures get isolation.
func PublishAdminTopic(coord *universe.Process, topic string, payload any) {
	adminBus(coord).Publish(topic, payload)
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
		ring := adminLogRing(c, 0)
		bus := adminBus(c)
		server := admin.NewServer(admin.ServerOpts{
			View:         view,
			Registry:     c.CmdRegistry(),
			Dispatcher:   c.CmdDispatcher(),
			SessionStore: admin.NewMemorySessionStore(),
			Panels:       adminPanelRegistry(c),
			Bus:          bus,
			LogRing:      ring,
			LocalHostID:  localLogID(cfg),
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
		// Bridge remote-host LogBatches into the same ring + bus that
		// the in-process logPump feeds — so the admin /logs SSE topic
		// surfaces local + remote lines uniformly.
		c.OnRemoteLogBatch(func(entries []universe.RemoteLogEntry) {
			for _, e := range entries {
				le := admin.LogEntry{
					Host: e.HostID,
					Cat:  e.Cat,
					Msg:  e.Msg,
					T:    time.UnixMilli(e.TimeMs),
				}
				ring.Append(le)
				bus.Publish("logs", le)
			}
		})
		return server
	}
}
