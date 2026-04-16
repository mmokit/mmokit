package universe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/engine"
)

type perfArgs struct {
	Sub    string `cmd:"optional,help=subcommand: reset"`
	HostID string `cmd:"optional,name=host,help=target host ID,complete=hosts"`
	CellID string `cmd:"optional,name=cell,help=target cell ID,complete=cells"`
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

// registerLoadBuiltins registers the `load` command. Temporary home until
// Task 8 moves it to builtins_load.go.
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

// ── perf.reset (internal fan-out verb) ───────────────────────────────────────

type perfResetArgs struct {
	// Optional filter — only reset this cell. Empty = all local cells.
	CellID string `cmd:"optional,help=restrict to this cell ID,complete=cells"`
}

type perfResetResult struct {
	CellsReset int
}

func registerPerfResetWorker(reg *cmdsys.Registry, coord *Coordinator) error {
	return reg.Register(cmdsys.Command{
		Verb:        "perf.reset",
		Capability:  "perf",
		Description: "reset perf counters on this host's cells (internal; fans out via `perf reset`)",
		Route:       cmdsys.RouteAllHosts,
		Args:        perfResetArgs{},
		Result:      perfResetResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(perfResetArgs)

			coord.mu.RLock()
			cells := make([]*Cell, 0, len(coord.Cells))
			for id, cell := range coord.Cells {
				if args.CellID != "" && id != args.CellID {
					continue
				}
				cells = append(cells, cell)
			}
			coord.mu.RUnlock()

			count := 0
			for _, cell := range cells {
				if cell.Engine == nil || cell.Engine.Perf == nil {
					continue
				}
				// Mirror the RunOnLoop gate from perf.snapshot: when a game
				// loop is running, mutate the profile on-loop; otherwise
				// (tests, headless bootstrap) call Reset directly.
				if cell.Engine.HasLoopRunning() {
					err := cell.Engine.RunOnLoop(ctx, func() error {
						cell.Engine.Perf.Reset()
						return nil
					})
					if err != nil {
						return nil, err
					}
				} else {
					cell.Engine.Perf.Reset()
				}
				count++
			}
			return perfResetResult{CellsReset: count}, nil
		},
	})
}

// ── perf (frontend) ──────────────────────────────────────────────────────────

// registerPerfFrontend registers the user-facing `perf` verb. It dispatches
// through InvokeInternal to perf.snapshot (or perf.reset), post-filters by
// HostID/CellID, and renders the aggregated rows as text.
func registerPerfFrontend(reg *cmdsys.Registry, disp *cmdsys.Dispatcher, coord *Coordinator) error {
	return reg.Register(cmdsys.Command{
		Verb:        "perf",
		Capability:  "perf",
		Description: "show tick timing, entities, network, load (fans out to hosts)",
		Route:       cmdsys.RouteLocal,
		Args:        perfArgs{},
		Result:      perfResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(perfArgs)

			if args.Sub == "reset" {
				innerArgs := perfResetArgs{CellID: args.CellID}
				inner, err := disp.InvokeInternal(ctx, env, "perf.reset", innerArgs)
				if err != nil {
					return nil, fmt.Errorf("perf reset: %w", err)
				}
				total, hosts := 0, 0
				var errs []string
				for _, tr := range inner.PerTarget {
					// "local" is the resolver fallback ID — always include it.
					if args.HostID != "" && tr.TargetID != "local" && tr.TargetID != args.HostID {
						continue
					}
					if !tr.OK {
						errs = append(errs, fmt.Sprintf("%s: %s", tr.TargetID, tr.Error))
						continue
					}
					r, _ := tr.Result.(perfResetResult)
					total += r.CellsReset
					hosts++
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "  perf counters reset: %d cells across %d host(s)\n", total, hosts)
				for _, e := range errs {
					fmt.Fprintf(&sb, "  error: %s\n", e)
				}
				return perfResult{Output: sb.String()}, nil
			}

			innerArgs := perfSnapshotArgs{CellID: args.CellID}
			inner, err := disp.InvokeInternal(ctx, env, "perf.snapshot", innerArgs)
			if err != nil {
				return nil, fmt.Errorf("perf: %w", err)
			}

			var rows []PerfCellSnapshot
			var errs []string
			for _, tr := range inner.PerTarget {
				if args.HostID != "" && tr.TargetID != "local" && tr.TargetID != args.HostID {
					continue
				}
				if !tr.OK {
					errs = append(errs, fmt.Sprintf("%s: %s", tr.TargetID, tr.Error))
					continue
				}
				r, ok := tr.Result.(perfSnapshotResult)
				if !ok {
					continue
				}
				for _, row := range r.Rows {
					if row.HostID == "" {
						row.HostID = tr.TargetID
					}
					// Row-level host filter: when the target is "local", rows
					// carry their actual HostID (set by the worker). Narrow now.
					if args.HostID != "" && row.HostID != args.HostID {
						continue
					}
					rows = append(rows, row)
				}
			}

			return perfResult{Output: renderPerfRows(rows, errs)}, nil
		},
	})
}

// renderPerfRows picks detail vs. aggregate formatting based on row count.
// Detail mode (single row): full per-system + entities/network block.
// Aggregate mode: one summary line per row + a total footer.
func renderPerfRows(rows []PerfCellSnapshot, errs []string) string {
	var sb strings.Builder
	if len(rows) == 0 {
		sb.WriteString("  no cells reporting\n")
	} else if len(rows) == 1 {
		r := rows[0]
		fmt.Fprintf(&sb, "  Host: %s  Cell: %s\n", r.HostID, r.CellID)
		sb.WriteString(engine.FormatPerfSnapshotText(r.toText()))
	} else {
		fmt.Fprintf(&sb, "  %-14s %-8s %7s %7s %9s %5s\n",
			"HOST", "CELL", "avg", "p95", "entities", "load")
		totalEntities := 0
		for _, r := range rows {
			fmt.Fprintf(&sb, "  %-14s %-8s %7s %7s %9d %5.2f\n",
				r.HostID, r.CellID,
				fmtDurShort(r.Tick.Avg), fmtDurShort(r.Tick.P95),
				r.Entities.Real, r.Load)
			totalEntities += r.Entities.Real
		}
		fmt.Fprintf(&sb, "  TOTAL: %d cells, %d entities\n", len(rows), totalEntities)
	}
	for _, e := range errs {
		fmt.Fprintf(&sb, "  error: %s\n", e)
	}
	return sb.String()
}

// fmtDurShort renders a duration as `12.3ms` (always ms, 1 decimal) for tables.
func fmtDurShort(d time.Duration) string {
	return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
}

// registerPerfBuiltins registers perf, perf.snapshot, perf.reset.
// Always registers — even in pure-coordinator mode or when the coord owns no
// local cells. RouteAllHosts fans out; if the resolver returns no remote
// hosts it falls back to local execution.
func registerPerfBuiltins(reg *cmdsys.Registry, disp *cmdsys.Dispatcher, coord *Coordinator) error {
	if err := registerPerfSnapshotWorker(reg, coord); err != nil {
		return fmt.Errorf("registerPerfBuiltins snapshot: %w", err)
	}
	if err := registerPerfResetWorker(reg, coord); err != nil {
		return fmt.Errorf("registerPerfBuiltins reset: %w", err)
	}
	if err := registerPerfFrontend(reg, disp, coord); err != nil {
		return fmt.Errorf("registerPerfBuiltins frontend: %w", err)
	}
	return nil
}
