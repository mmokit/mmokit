package universe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mmokit/mmokit/pkg/cmdsys"
	"github.com/mmokit/mmokit/pkg/engine"
)

type perfArgs struct {
	// Args captures the full positional tail and is parsed by parsePerfArgs.
	// Recognized forms: "" (all cells), "reset" (reset all),
	// "reset <id>", "cell <id>", "<id>" (drill into one cell).
	Args   string `cmd:"optional,rest,help=[reset] [cell <id> | <id>]"`
	HostID string `cmd:"optional,name=host,help=target host ID,complete=hosts"`
	CellID string `cmd:"optional,name=cell,help=target cell ID,complete=cells"`
}

// parsedPerfArgs is the resolved form after merging positional tail tokens
// with named flags. Target is the requested cell ID in canonical MeshID form
// (or empty for "all cells").
type parsedPerfArgs struct {
	reset  bool
	hostID string
	target string
	err    string // user-facing error; non-empty means "render this and stop"
}

func parsePerfArgs(args perfArgs) parsedPerfArgs {
	out := parsedPerfArgs{hostID: args.HostID, target: args.CellID}
	tokens := strings.Fields(args.Args)
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		switch {
		case t == "reset":
			out.reset = true
		case t == "snapshot":
			// No-op — explicit form of the default action. Accepted because
			// power users may have learned the internal verb name.
		case t == "cell":
			if i+1 >= len(tokens) {
				out.err = "  usage: perf cell <id>   (or: perf <id>)\n"
				return out
			}
			out.target = tokens[i+1]
			i++
		case strings.HasPrefix(t, "cell="):
			out.target = strings.TrimPrefix(t, "cell=")
		default:
			if _, err := ParseCellID(t); err == nil {
				out.target = t
			} else {
				out.err = fmt.Sprintf("  unknown perf argument: %q\n  usage: perf [reset] [cell <id> | <id>]\n", t)
				return out
			}
		}
	}
	if out.target != "" {
		if cid, err := ParseCellID(out.target); err == nil {
			out.target = string(cid.MeshID())
		}
	}
	return out
}

// cellIDMatches reports whether a stored cell-map key matches a requested
// cell ID, tolerating either format ("0_0" or "cell_0_0") on either side.
// Production keys are MeshID; test fixtures sometimes use the bare String form.
func cellIDMatches(stored, requested string) bool {
	if requested == "" {
		return true
	}
	if stored == requested {
		return true
	}
	a, errA := ParseCellID(stored)
	b, errB := ParseCellID(requested)
	return errA == nil && errB == nil && a == b
}

type perfResult struct {
	Output string
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
func registerPerfSnapshotWorker(reg *cmdsys.Registry, coord *Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "perf.snapshot",
		Capability:  "perf",
		Description: "realtime per-cell perf data from this host (internal; fans out via `perf`)",
		Route:       cmdsys.RouteAllHosts,
		Args:        perfSnapshotArgs{},
		Result:      perfSnapshotResult{},
		Hidden:      true,
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(perfSnapshotArgs)

			// Snapshot the cell list and build a cell→host reverse index while
			// holding coord.mu, so we don't race with topology changes.
			coord.mu.RLock()
			cells := make([]*Cell, 0, len(coord.Cells))
			for id, cell := range coord.Cells {
				if !cellIDMatches(string(id), args.CellID) {
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
				// Fast path: if the loop has computed Stats() recently we can
				// reuse the cached result off-loop and skip the RunOnLoop
				// detour entirely. cell.Metrics.Snapshot() (read inside
				// buildPerfCellSnapshotFromStats) is concurrency-safe.
				if cached := cell.Engine.Perf.CachedStats(); cached != nil {
					snap = buildPerfCellSnapshotFromStats(cell, cellHost[cell], *cached)
					rows = append(rows, snap)
					continue
				}
				// Slow path: cache miss or stale. Schedule the recompute
				// on-loop; Stats() repopulates the cache so subsequent polls
				// hit the fast path. When no loop is active (e.g. tests,
				// headless bootstrap), call directly — TickProfile has no
				// concurrent writers at that point.
				if cell.Engine.HasLoopRunning() {
					err := cell.Engine.RunOnLoop(ctx, func() error {
						snap = buildPerfCellSnapshot(cell, cellHost[cell])
						return nil
					})
					// The loop can exit between the check above and the
					// enqueue. Take the direct path rather than failing the
					// verb — returning here dropped every other cell's row
					// because one cell retired mid-poll.
					if errors.Is(err, engine.ErrLoopStopped) {
						snap = buildPerfCellSnapshot(cell, cellHost[cell])
					} else if err != nil {
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

func registerPerfResetWorker(reg *cmdsys.Registry, coord *Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "perf.reset",
		Capability:  "perf",
		Description: "reset perf counters on this host's cells (internal; fans out via `perf reset`)",
		Route:       cmdsys.RouteAllHosts,
		Args:        perfResetArgs{},
		Result:      perfResetResult{},
		Hidden:      true,
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(perfResetArgs)

			coord.mu.RLock()
			cells := make([]*Cell, 0, len(coord.Cells))
			for id, cell := range coord.Cells {
				if !cellIDMatches(string(id), args.CellID) {
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
					// Same race as perf.snapshot: a loop that exited has no
					// concurrent writer left, so reset the profile here.
					if errors.Is(err, engine.ErrLoopStopped) {
						cell.Engine.Perf.Reset()
					} else if err != nil {
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
func registerPerfFrontend(reg *cmdsys.Registry, coord *Process) error {
	disp := coord.dispatcher
	return reg.Register(cmdsys.Command{
		Verb:        "perf",
		Capability:  "perf",
		Description: "show tick timing, entities, network, load (fans out to hosts)",
		Usage:       "perf [reset] [cell <id> | <id>] [--host=<id>] [--cell=<id>]",
		Examples:    []string{"perf", "perf reset", "perf 0_0", "perf --host=host-1", "perf reset 0_0"},
		Route:       cmdsys.RouteLocal,
		Args:        perfArgs{},
		Result:      perfResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(perfArgs)
			parsed := parsePerfArgs(args)
			if parsed.err != "" {
				return perfResult{Output: parsed.err}, nil
			}

			if parsed.reset {
				innerArgs := perfResetArgs{CellID: parsed.target}
				inner, err := disp.InvokeInternal(ctx, env, "perf.reset", innerArgs)
				if err != nil {
					return nil, fmt.Errorf("perf reset: %w", err)
				}
				total, hosts := 0, 0
				var errs []string
				for _, tr := range inner.PerTarget {
					// "local" is the resolver fallback ID — always include it.
					if parsed.hostID != "" && tr.TargetID != "local" && tr.TargetID != parsed.hostID {
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

			innerArgs := perfSnapshotArgs{CellID: parsed.target}
			inner, err := disp.InvokeInternal(ctx, env, "perf.snapshot", innerArgs)
			if err != nil {
				return nil, fmt.Errorf("perf: %w", err)
			}

			var rows []PerfCellSnapshot
			var errs []string
			for _, tr := range inner.PerTarget {
				if parsed.hostID != "" && tr.TargetID != "local" && tr.TargetID != parsed.hostID {
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
					if parsed.hostID != "" && row.HostID != parsed.hostID {
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
		sb.WriteString(renderAggregatedSystems(rows))
		sb.WriteString("\n  Tip: 'perf cell <id>' for one cell's full breakdown\n")
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

// renderAggregatedSystems collapses per-cell system timings into one row per
// system name across all cells. System order matches the cells' execution
// order (top-down) — never sorted by cost — because that's the order the
// loop runs them and the order operators are used to scanning. Empty input →
// empty string. Used as a footer in the multi-cell aggregate view to restore
// the per-system visibility lost when `perf` switched from a single-engine
// command to a fan-out frontend.
func renderAggregatedSystems(rows []PerfCellSnapshot) string {
	type agg struct {
		sumAvg time.Duration
		maxP95 time.Duration
		count  int
	}
	byName := map[string]*agg{}
	var order []string
	for _, r := range rows {
		for _, s := range r.Systems {
			a, ok := byName[s.Name]
			if !ok {
				a = &agg{}
				byName[s.Name] = a
				order = append(order, s.Name)
			}
			a.sumAvg += s.Avg
			if s.P95 > a.maxP95 {
				a.maxP95 = s.P95
			}
			a.count++
		}
	}
	if len(order) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n  Systems (avg across %d cells):\n", len(rows))
	for _, n := range order {
		a := byName[n]
		fmt.Fprintf(&sb, "    %-20s avg %s  max-p95 %s\n",
			n, engine.FmtDuration(a.sumAvg/time.Duration(a.count)), engine.FmtDuration(a.maxP95))
	}
	return sb.String()
}

// registerPerfBuiltins registers perf, perf.snapshot, perf.reset.
// Always registers — even in pure-coordinator mode or when the coord owns no
// local cells. RouteAllHosts fans out; if the resolver returns no remote
// hosts it falls back to local execution.
func registerPerfBuiltins(reg *cmdsys.Registry, coord *Process) error {
	if err := registerPerfSnapshotWorker(reg, coord); err != nil {
		return fmt.Errorf("registerPerfBuiltins snapshot: %w", err)
	}
	if err := registerPerfResetWorker(reg, coord); err != nil {
		return fmt.Errorf("registerPerfBuiltins reset: %w", err)
	}
	if err := registerPerfFrontend(reg, coord); err != nil {
		return fmt.Errorf("registerPerfBuiltins frontend: %w", err)
	}
	return nil
}
