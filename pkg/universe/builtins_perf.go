package universe

import (
	"context"
	"fmt"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/engine"
)

type perfArgs struct {
	Sub string `cmd:"optional,help=subcommand: reset"`
}

type perfResult struct {
	Output string
}

type loadArgs struct{}

type loadResult struct {
	Load     float64
	TickPct  float64
	EntityPct float64
}

func registerPerfBuiltins(reg *cmdsys.Registry, console *engine.Console, defaultEng *engine.Engine) error {
	if err := reg.Register(cmdsys.Command{
		Verb:        "perf",
		Capability:  "perf",
		Description: "show tick timing, entities, network, load",
		Route:       cmdsys.RouteLocal,
		Args:        perfArgs{},
		Result:      perfResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(perfArgs)
			if args.Sub == "reset" {
				output := console.ExecOnGameLoop(func() string {
					defaultEng.Perf.Reset()
					return "  perf counters reset\n"
				})
				return perfResult{Output: output}, nil
			}
			output := console.ExecOnGameLoop(func() string {
				return engine.FormatPerfOutput(defaultEng)
			})
			return perfResult{Output: output}, nil
		},
	}); err != nil {
		return fmt.Errorf("registerPerfBuiltins perf: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "load",
		Capability:  "load",
		Description: "show composite load score",
		Route:       cmdsys.RouteLocal,
		Args:        loadArgs{},
		Result:      loadResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			output := console.ExecOnGameLoop(func() string {
				if defaultEng.Metrics == nil {
					return "  metrics not wired\n"
				}
				snap := defaultEng.Metrics.Snapshot()
				tickBudget := time.Duration(1000/defaultEng.Config.TickRate) * time.Millisecond
				return fmt.Sprintf("  load: %.2f (tick=%.1f%% entity=%.1f%%)\n",
					snap.CompositeLoad,
					float64(snap.Tick.AvgDuration)/float64(tickBudget)*100,
					float64(snap.Entities.Real)/1000.0*100,
				)
			})
			return perfResult{Output: output}, nil
		},
	}); err != nil {
		return fmt.Errorf("registerPerfBuiltins load: %w", err)
	}

	return nil
}
