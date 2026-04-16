package universe

import (
	"context"
	"fmt"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/engine"
)

type loadArgs struct{}

type loadResult struct {
	Load      float64
	TickPct   float64
	EntityPct float64
}

// registerLoadBuiltins registers the `load` command.
func registerLoadBuiltins(reg *cmdsys.Registry, defaultEng *engine.Engine) error {
	if err := reg.Register(cmdsys.Command{
		Verb:        "load",
		Capability:  "load",
		Description: "show composite load score",
		Route:       cmdsys.RouteLocal,
		Args:        loadArgs{},
		Result:      loadResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			var output string
			err := defaultEng.RunOnLoop(ctx, func() error {
				if defaultEng.Metrics == nil {
					output = "  metrics not wired\n"
					return nil
				}
				snap := defaultEng.Metrics.Snapshot()
				tickBudget := time.Duration(1000/defaultEng.Config.TickRate) * time.Millisecond
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
