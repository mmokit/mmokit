package admin

import (
	"context"
	"fmt"

	"github.com/mmokit/mmokit/pkg/cmdsys"
	"github.com/mmokit/mmokit/pkg/logger"
)

type logSetArgs struct {
	Category string
	Enabled  bool
}

type logSetResult struct {
	Category string
	Enabled  bool
}

// RegisterLogCommands installs cluster-wide log-management verbs on reg.
// log.set toggles a category across every host (Route: RouteAllHosts) —
// each host's local handler runs Enable/Disable against its own *Logger.
func RegisterLogCommands(reg *cmdsys.Registry, log *logger.Logger) error {
	if reg == nil || log == nil {
		return nil
	}
	return reg.Register(cmdsys.Command{
		Verb:        "log.set",
		Capability:  "admin.logs",
		Description: "toggle a log category cluster-wide",
		Route:       cmdsys.RouteAllHosts,
		Args:        logSetArgs{},
		Result:      logSetResult{},
		Handler: func(_ context.Context, _ *cmdsys.Env, raw any) (any, error) {
			a, ok := raw.(logSetArgs)
			if !ok {
				return nil, fmt.Errorf("log.set: bad args type %T", raw)
			}
			if a.Category == "" {
				return nil, fmt.Errorf("log.set: category required")
			}
			if a.Enabled {
				log.Enable(a.Category)
			} else {
				log.Disable(a.Category)
			}
			return logSetResult{Category: a.Category, Enabled: log.IsEnabled(a.Category)}, nil
		},
	})
}
