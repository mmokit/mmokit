package universe

import (
	"context"
	"fmt"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type loadArgs struct{}

type loadResult struct {
	Load      float64
	TickPct   float64
	EntityPct float64
}

// registerLoadBuiltins registers the `load` command.
// The engine is resolved lazily at invocation time from coord.Cells so that
// registration can happen before Build() populates the cell map.
func registerLoadBuiltins(reg *cmdsys.Registry, coord *Process) error {
	if err := reg.Register(cmdsys.Command{
		Verb:        "load",
		Capability:  "load",
		Description: "show composite load score",
		Route:       cmdsys.RouteLocal,
		Args:        loadArgs{},
		Result:      loadResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			coord.mu.RLock()
			var resolvedCell *Cell
			for _, cell := range coord.Cells {
				resolvedCell = cell
				break
			}
			coord.mu.RUnlock()

			if resolvedCell == nil || resolvedCell.Engine == nil {
				return perfResult{Output: "  no local cells\n"}, nil
			}
			eng := resolvedCell.Engine

			var output string
			err := eng.RunOnLoop(ctx, func() error {
				if eng.Metrics == nil {
					output = "  metrics not wired\n"
					return nil
				}
				snap := eng.Metrics.Snapshot()
				tickBudget := time.Duration(1000/eng.Config.TickRate) * time.Millisecond
				output = fmt.Sprintf("  load: %.2f (tick=%.1f%% entity=%.1f%%)\n",
					snap.CompositeLoad,
					float64(snap.Tick.AvgDuration)/float64(tickBudget)*100,
					float64(snap.Entities.Real)/1000.0*100,
				)
				return nil
			})
			if err != nil {
				return nil, err
			}
			return perfResult{Output: output}, nil
		},
	}); err != nil {
		return fmt.Errorf("registerLoadBuiltins load: %w", err)
	}
	return nil
}
