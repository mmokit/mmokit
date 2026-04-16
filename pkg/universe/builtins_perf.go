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
	Load      float64
	TickPct   float64
	EntityPct float64
}

func registerPerfBuiltins(reg *cmdsys.Registry, _ *engine.Console, defaultEng *engine.Engine) error {
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
				err := defaultEng.RunOnLoop(ctx, func() error {
					defaultEng.Perf.Reset()
					return nil
				})
				if err != nil {
					return nil, err
				}
				return perfResult{Output: "  perf counters reset\n"}, nil
			}
			var output string
			err := defaultEng.RunOnLoop(ctx, func() error {
				output = engine.FormatPerfOutput(defaultEng)
				return nil
			})
			if err != nil {
				return nil, err
			}
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
		return fmt.Errorf("registerPerfBuiltins load: %w", err)
	}

	return nil
}

// ── perf.snapshot (internal fan-out verb) ────────────────────────────────────

type perfSnapshotArgs struct {
	// Optional filter — only return this cell's snapshot. Empty = all local cells.
	CellID string `cmd:"optional,help=restrict to this cell ID,complete=cells"`
}

// perfSnapshotResult carries per-cell perf data from one host back to the
// caller. Rows holds one PerfCellSnapshot per local cell.
type perfSnapshotResult struct {
	Rows []PerfCellSnapshot `cmd:"optional"`
}

// registerPerfSnapshotWorker registers perf.snapshot with RouteAllHosts.
// Each host's dispatcher runs the handler locally and returns its cells' data.
// Identical in spirit to cell.snapshot (see builtins_cell.go).
func registerPerfSnapshotWorker(reg *cmdsys.Registry, coord *Coordinator) error {
	return reg.Register(cmdsys.Command{
		Verb:        "perf.snapshot",
		Capability:  "perf",
		Description: "realtime per-cell perf data from this host (internal; fans out via `perf`)",
		Route:       cmdsys.RouteAllHosts,
		Args:        perfSnapshotArgs{},
		Result:      perfSnapshotResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(perfSnapshotArgs)

			// Snapshot the cell list and build a cell→host reverse index while
			// holding coord.mu, so we don't race with topology changes.
			coord.mu.RLock()
			cells := make([]*Cell, 0, len(coord.Cells))
			for id, cell := range coord.Cells {
				if args.CellID != "" && id != args.CellID {
					continue
				}
				cells = append(cells, cell)
			}
			cellHost := map[*Cell]string{}
			for hostID, h := range coord.Hosts {
				h.mu.RLock()
				for _, hc := range h.Cells {
					cellHost[hc] = hostID
				}
				h.mu.RUnlock()
			}
			coord.mu.RUnlock()

			rows := make([]PerfCellSnapshot, 0, len(cells))
			for _, cell := range cells {
				if cell.Engine == nil || cell.Engine.Perf == nil {
					continue
				}
				var snap PerfCellSnapshot
				// When the game loop is running, schedule the read on-loop for
				// thread safety. When no loop is active (e.g. tests, headless
				// bootstrap), call directly — TickProfile has no concurrent
				// writers at that point.
				if cell.Engine.HasLoopRunning() {
					err := cell.Engine.RunOnLoop(ctx, func() error {
						snap = buildPerfCellSnapshot(cell, cellHost[cell])
						return nil
					})
					if err != nil {
						return nil, err
					}
				} else {
					snap = buildPerfCellSnapshot(cell, cellHost[cell])
				}
				rows = append(rows, snap)
			}
			return perfSnapshotResult{Rows: rows}, nil
		},
	})
}
