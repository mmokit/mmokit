package universe

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// ── cell list ────────────────────────────────────────────────────────────────

type cellListArgs struct {
	Live bool `cmd:"named-only,optional,default=false,help=fan out to all hosts for realtime per-cell metrics"`
}

type cellListRow struct {
	Cell     string
	Size     float32
	Depth    int
	Host     string
	Entities int
	Players  int
	Load     float64
	Cooldown string
}

type cellListResult struct {
	Cells []cellListRow `cmd:"table"`
}

// ── cell snapshot (internal fan-out verb) ────────────────────────────────────

type cellSnapshotArgs struct{}

type cellSnapshotRow struct {
	Cell     string
	Host     string
	Entities int
	Players  int
	Replica  int
	Ghost    int
	Load     float64
}

type cellSnapshotResult struct {
	Rows []cellSnapshotRow `cmd:"table"`
}

// ── cell info ────────────────────────────────────────────────────────────────

type cellInfoArgs struct {
	CellID string `cmd:"help=cell ID e.g. 0_0,complete=cells"`
}

type cellInfoResult struct {
	Output string
}

// ── cell split ───────────────────────────────────────────────────────────────

type cellSplitArgs struct {
	CellID string `cmd:"help=cell ID to split,complete=cells"`
}

type cellSplitResult struct {
	Cell string
	OK   bool
}

// ── cell merge ───────────────────────────────────────────────────────────────

type cellMergeArgs struct {
	CellID string `cmd:"help=cell ID (and siblings) to merge,complete=cells"`
}

type cellMergeResult struct {
	Cell   string
	Parent string
	OK     bool
}

// ── cell migrate ─────────────────────────────────────────────────────────────

type cellMigrateArgs struct {
	CellID string `cmd:"help=cell ID to migrate,complete=cells"`
	HostID string `cmd:"help=destination host ID,complete=hosts"`
}

type cellMigrateResult struct {
	Cell     string
	FromHost string
	ToHost   string
	Elapsed  string
}

// ── cell cooldowns ───────────────────────────────────────────────────────────

type cellCooldownsArgs struct{}

type cellCooldownsResult struct {
	Output string
}

// ── cell autosplit ───────────────────────────────────────────────────────────

type cellAutosplitArgs struct {
	Value string `cmd:"optional,help=on or off"`
}

type cellAutosplitResult struct {
	Enabled bool
}

// ── cell automerge ───────────────────────────────────────────────────────────

type cellAutomergeArgs struct {
	Value string `cmd:"optional,help=on or off"`
}

type cellAutomergeResult struct {
	Enabled bool
}

// ── cell config ───────────────────────────────────────────────────────────────

type cellConfigArgs struct{}

type cellConfigResult struct {
	Output string
}

// ── helpers ───────────────────────────────────────────────────────────────────

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// ── registration ─────────────────────────────────────────────────────────────

func registerCellBuiltins(reg *cmdsys.Registry, coord *Process) error {
	c := coord

	// cell.snapshot — internal fan-out verb. RouteAllHosts so `cell.list --live`
	// can dispatch it and every host returns its own local cells' LoadSnapshots.
	// Safe to call directly too (it's read-only).
	if err := reg.Register(cmdsys.Command{
		Verb:        "cell.snapshot",
		Capability:  "cell.snapshot",
		Description: "realtime per-cell metrics from this host (internal; fans out via cell.list --live)",
		Route:       cmdsys.RouteAllHosts,
		Args:        cellSnapshotArgs{},
		Result:      cellSnapshotResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			c.mu.RLock()
			cells := make([]*Cell, 0, len(c.Cells))
			for _, cell := range c.Cells {
				cells = append(cells, cell)
			}
			c.mu.RUnlock()
			rows := make([]cellSnapshotRow, 0, len(cells))
			for _, cell := range cells {
				if cell.Metrics == nil {
					continue
				}
				snap := cell.Metrics.Snapshot()
				hostID := ""
				c.mu.RLock()
				for id, h := range c.Hosts {
					for _, hc := range h.Cells {
						if hc == cell {
							hostID = id
							break
						}
					}
					if hostID != "" {
						break
					}
				}
				c.mu.RUnlock()
				rows = append(rows, cellSnapshotRow{
					Cell:     cell.ID,
					Host:     hostID,
					Entities: int(snap.Entities.Real),
					Players:  int(snap.Entities.Connected),
					Replica:  int(snap.Entities.Replica),
					Ghost:    int(snap.Entities.Ghost),
					Load:     snap.CompositeLoad,
				})
			}
			return cellSnapshotResult{Rows: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("cell.snapshot: %w", err)
	}

	// cell.list
	if err := reg.Register(cmdsys.Command{
		Verb:        "cell.list",
		Capability:  "cell.list",
		Description: "list all active cells with load info (add --live for realtime per-host fan-out)",
		Route:       cmdsys.RouteLocal,
		Args:        cellListArgs{},
		Result:      cellListResult{},
		Usage:       "cell list [--live]",
		Examples: []string{
			"cell list",
			"cell list --live",
		},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(cellListArgs)
			// --live: dispatch cell.snapshot to all hosts and merge the results
			// into the coord's ownership view. Replaces the (possibly stale)
			// coord-local allCellLoads snapshots with fresh per-host data.
			var liveByCell map[string]cellSnapshotRow
			if args.Live && c.dispatcher != nil {
				inner, err := c.dispatcher.InvokeInternal(ctx, env, "cell.snapshot", cellSnapshotArgs{})
				if err != nil {
					return nil, fmt.Errorf("cell.list --live: %w", err)
				}
				liveByCell = make(map[string]cellSnapshotRow)
				for _, tr := range inner.PerTarget {
					if !tr.OK {
						continue
					}
					res, ok := tr.Result.(cellSnapshotResult)
					if !ok {
						continue
					}
					for _, row := range res.Rows {
						if row.Host == "" {
							row.Host = tr.TargetID
						}
						liveByCell[row.Cell] = row
					}
				}
			}

			cells := make([]CellID, 0)
			seen := make(map[CellID]bool)
			for cell := range c.CellOwner {
				if !seen[cell] {
					cells = append(cells, cell)
					seen[cell] = true
				}
			}
			if c.hostRegistry != nil {
				for _, host := range c.hostRegistry.LiveHosts() {
					for cellIDStr := range host.OwnedCells {
						cell, err := ParseCellID(cellIDStr)
						if err != nil {
							continue
						}
						if !seen[cell] {
							cells = append(cells, cell)
							seen[cell] = true
						}
					}
				}
			}
			if len(cells) == 0 && c.roles.Has(RoleCoordinator) && !c.roles.Has(RoleHost) {
				for sy := uint32(0); sy < c.cfg.CellsY; sy++ {
					for sx := uint32(0); sx < c.cfg.CellsX; sx++ {
						cell := CellID{X: int32(sx), Y: int32(sy)}
						cells = append(cells, cell)
					}
				}
			}
			slices.SortFunc(cells, func(a, b CellID) int {
				if v := cmp.Compare(a.Depth, b.Depth); v != 0 {
					return v
				}
				if v := cmp.Compare(a.X, b.X); v != 0 {
					return v
				}
				return cmp.Compare(a.Y, b.Y)
			})
			now := time.Now()
			var rows []cellListRow
			for _, cell := range cells {
				nodeID := c.CellOwner[cell]
				size := cell.Size(c.baseCellSize())
				snap, _ := c.cellLoad(nodeID)
				cd := "-"
				if c.partState != nil {
					c.partState.mu.Lock()
					if until, ok := c.partState.cooldowns[cell]; ok && now.Before(until) {
						remaining := until.Sub(now).Truncate(time.Second)
						cd = remaining.String()
					}
					c.partState.mu.Unlock()
				}
				cellKey := MeshCellID(cell)
				hostID, _ := c.Control.OwnerOf(cellKey)
				entities := int(snap.Entities.Real)
				players := int(snap.Entities.Connected)
				load := snap.CompositeLoad
				// --live: overwrite coord-cached metrics with fresh per-host data.
				if live, ok := liveByCell[cell.String()]; ok {
					entities = live.Entities
					players = live.Players
					load = live.Load
					if hostID == "" {
						hostID = live.Host
					}
				}
				rows = append(rows, cellListRow{
					Cell:     cell.String(),
					Size:     size,
					Depth:    int(cell.Depth),
					Host:     hostID,
					Entities: entities,
					Players:  players,
					Load:     load,
					Cooldown: cd,
				})
			}
			return cellListResult{Cells: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("cell.list: %w", err)
	}

	// cell.info
	if err := reg.Register(cmdsys.Command{
		Verb:        "cell.info",
		Capability:  "cell.info",
		Description: "show detailed info for a cell",
		Route:       cmdsys.RouteLocal,
		Args:        cellInfoArgs{},
		Result:      cellInfoResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(cellInfoArgs)
			cell, err := ParseCellID(args.CellID)
			if err != nil {
				return nil, fmt.Errorf("invalid cell ID: %w", err)
			}
			cellKey := MeshCellID(cell)
			nodeID, existsLocal := c.CellOwner[cell]
			hostID, _ := c.Control.OwnerOf(cellKey)
			inGrid := cell.Depth == 0 && cell.X >= 0 && cell.Y >= 0 &&
				uint32(cell.X) < c.cfg.CellsX && uint32(cell.Y) < c.cfg.CellsY
			if !existsLocal && hostID == "" && !inGrid {
				return nil, fmt.Errorf("cell %s does not exist", cell)
			}
			snap, _ := c.cellLoad(nodeID)
			size := cell.Size(c.baseCellSize())
			minX, minY, maxX, maxY := cell.WorldBounds(c.baseCellSize())
			var sb strings.Builder
			fmt.Fprintf(&sb, "  Cell:       %s\n", cell)
			if nodeID != "" {
				fmt.Fprintf(&sb, "  Node:       %s\n", nodeID)
			}
			if hostID != "" {
				fmt.Fprintf(&sb, "  Host:       %s\n", hostID)
			}
			fmt.Fprintf(&sb, "  Depth:      %d\n", cell.Depth)
			fmt.Fprintf(&sb, "  Size:       %.0f\n", size)
			fmt.Fprintf(&sb, "  Bounds:     (%.0f, %.0f) - (%.0f, %.0f)\n", minX, minY, maxX, maxY)
			fmt.Fprintf(&sb, "  Entities:   %d real, %d replica, %d ghost\n",
				snap.Entities.Real, snap.Entities.Replica, snap.Entities.Ghost)
			fmt.Fprintf(&sb, "  Players:    %d\n", snap.Entities.Connected)
			fmt.Fprintf(&sb, "  Load:       %.2f\n", snap.CompositeLoad)
			neighbors := c.Control.Topology.Neighbors[cell]
			if len(neighbors) > 0 {
				neighborStrs := make([]string, len(neighbors))
				for i, n := range neighbors {
					neighborStrs[i] = n.String()
				}
				fmt.Fprintf(&sb, "  Neighbors:  %s\n", strings.Join(neighborStrs, ", "))
			}
			if c.partState != nil && c.partState.onCooldown(cell) {
				fmt.Fprintf(&sb, "  Cooldown:   ACTIVE\n")
			}
			if c.cfg.DynamicPartitioning != nil {
				canSplit := size/2 >= c.cfg.DynamicPartitioning.MinCellSize
				fmt.Fprintf(&sb, "  Can split:  %v\n", canSplit)
				if cell.Depth > 0 {
					fmt.Fprintf(&sb, "  Can merge:  true (parent: %s)\n", cell.Parent())
				}
			}
			return cellInfoResult{Output: sb.String()}, nil
		},
	}); err != nil {
		return fmt.Errorf("cell.info: %w", err)
	}

	// Partition commands — always registered. Handlers that need partState
	// check at invocation time and return a clear error if partitioning is
	// disabled. This avoids the old pattern where registering in
	// New (before Build sets up partState) silently dropped them.

	if err := reg.Register(cmdsys.Command{
		Verb:        "cell.split",
		Capability:  "cell.split",
		Description: "manually split a cell into 4 sub-cells (bypasses cooldown)",
		Usage:       "cell split <cellID>",
		Examples:    []string{"cell split 0_0", "cell split 0_0/0"},
		Route:       cmdsys.RouteLocal,
		Args:        cellSplitArgs{},
		Result:      cellSplitResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(cellSplitArgs)
			cell, err := ParseCellID(args.CellID)
			if err != nil {
				return nil, fmt.Errorf("invalid cell ID: %w", err)
			}
			if err := c.SplitCell(cell, true); err != nil {
				return nil, fmt.Errorf("split failed: %w", err)
			}
			return cellSplitResult{Cell: cell.String(), OK: true}, nil
		},
	}); err != nil {
		return fmt.Errorf("cell.split: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "cell.merge",
		Capability:  "cell.merge",
		Description: "manually merge a cell and its 3 siblings into parent (bypasses cooldown)",
		Usage:       "cell merge <cellID>",
		Examples:    []string{"cell merge 0_0", "cell merge 0_0/0"},
		Route:       cmdsys.RouteLocal,
		Args:        cellMergeArgs{},
		Result:      cellMergeResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(cellMergeArgs)
			cell, err := ParseCellID(args.CellID)
			if err != nil {
				return nil, fmt.Errorf("invalid cell ID: %w", err)
			}
			if err := c.MergeCell(cell, true); err != nil {
				return nil, fmt.Errorf("merge failed: %w", err)
			}
			return cellMergeResult{Cell: cell.String(), Parent: cell.Parent().String(), OK: true}, nil
		},
	}); err != nil {
		return fmt.Errorf("cell.merge: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "cell.migrate",
		Capability:  "cell.migrate",
		Description: "move a cell to another host via the transfer orchestrator",
		Usage:       "cell migrate <cellID> <hostID>",
		Examples:    []string{"cell migrate 0_0 host-2"},
		Route:       cmdsys.RouteLocal,
		Args:        cellMigrateArgs{},
		Result:      cellMigrateResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(cellMigrateArgs)
			cell, err := ParseCellID(args.CellID)
			if err != nil {
				return nil, fmt.Errorf("invalid cell ID: %w", err)
			}
			destHost := args.HostID
			if c.orchestrator == nil {
				return nil, fmt.Errorf("orchestrator not initialized")
			}
			known := false
			if c.hostRegistry != nil {
				for _, h := range c.hostRegistry.LiveHosts() {
					if h.ID == destHost {
						known = true
						break
					}
				}
			}
			if !known {
				c.mu.RLock()
				_, ok := c.Hosts[destHost]
				c.mu.RUnlock()
				if ok {
					known = true
				}
			}
			if !known {
				return nil, fmt.Errorf("unknown host %q", destHost)
			}
			cellKey := MeshCellID(cell)
			srcHost, _ := c.Control.OwnerOf(cellKey)
			if srcHost == "" {
				return nil, fmt.Errorf("cell %s not in topology", cell)
			}
			if srcHost == destHost {
				return nil, fmt.Errorf("cell %s already on host %q", cell, destHost)
			}
			start := time.Now()
			req, err := c.orchestrator.BeginMigrate(cell, destHost)
			if err != nil {
				return nil, fmt.Errorf("migrate failed: %w", err)
			}
			select {
			case <-req.Done:
			case <-time.After(defaultTransferTimeout + 2*time.Second):
				return nil, fmt.Errorf("migrate timed out waiting for req=%d", req.ID)
			}
			elapsed := time.Since(start)
			if req.Result != nil {
				return nil, fmt.Errorf("migrate failed after %s: %v", elapsed.Truncate(time.Millisecond), req.Result)
			}
			return cellMigrateResult{
				Cell:     cell.String(),
				FromHost: srcHost,
				ToHost:   destHost,
				Elapsed:  elapsed.Truncate(time.Millisecond).String(),
			}, nil
		},
	}); err != nil {
		return fmt.Errorf("cell.migrate: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "cell.cooldowns",
		Capability:  "cell.cooldowns",
		Description: "show active cell cooldowns",
		Route:       cmdsys.RouteLocal,
		Args:        cellCooldownsArgs{},
		Result:      cellCooldownsResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			if c.partState == nil {
				return cellCooldownsResult{Output: "  dynamic partitioning not enabled\n"}, nil
			}
			c.partState.mu.Lock()
			defer c.partState.mu.Unlock()
			if len(c.partState.cooldowns) == 0 {
				return cellCooldownsResult{Output: "  no active cooldowns\n"}, nil
			}
			var sb strings.Builder
			for cell, until := range c.partState.cooldowns {
				fmt.Fprintf(&sb, "  %s -> %s\n", cell, until.Format("15:04:05"))
			}
			return cellCooldownsResult{Output: sb.String()}, nil
		},
	}); err != nil {
		return fmt.Errorf("cell.cooldowns: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "cell.autosplit",
		Capability:  "cell.autosplit",
		Description: "show or toggle automatic cell splitting",
		Route:       cmdsys.RouteLocal,
		Args:        cellAutosplitArgs{},
		Result:      cellAutosplitResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			if c.cfg.DynamicPartitioning == nil {
				return nil, fmt.Errorf("dynamic partitioning not enabled")
			}
			args := raw.(cellAutosplitArgs)
			pc := c.cfg.DynamicPartitioning
			switch strings.TrimSpace(args.Value) {
			case "on":
				pc.AutoSplitEnabled = true
			case "off":
				pc.AutoSplitEnabled = false
			case "":
			default:
				return nil, fmt.Errorf("usage: cell autosplit [on|off]")
			}
			return cellAutosplitResult{Enabled: pc.AutoSplitEnabled}, nil
		},
	}); err != nil {
		return fmt.Errorf("cell.autosplit: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "cell.automerge",
		Capability:  "cell.automerge",
		Description: "show or toggle automatic cell merging",
		Route:       cmdsys.RouteLocal,
		Args:        cellAutomergeArgs{},
		Result:      cellAutomergeResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			if c.cfg.DynamicPartitioning == nil {
				return nil, fmt.Errorf("dynamic partitioning not enabled")
			}
			args := raw.(cellAutomergeArgs)
			pc := c.cfg.DynamicPartitioning
			switch strings.TrimSpace(args.Value) {
			case "on":
				pc.AutoMergeEnabled = true
			case "off":
				pc.AutoMergeEnabled = false
			case "":
			default:
				return nil, fmt.Errorf("usage: cell automerge [on|off]")
			}
			return cellAutomergeResult{Enabled: pc.AutoMergeEnabled}, nil
		},
	}); err != nil {
		return fmt.Errorf("cell.automerge: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "cell.config",
		Capability:  "cell.config",
		Description: "show current partition configuration",
		Route:       cmdsys.RouteLocal,
		Args:        cellConfigArgs{},
		Result:      cellConfigResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			pc := c.cfg.DynamicPartitioning
			if pc == nil {
				return cellConfigResult{Output: "  dynamic partitioning not enabled\n"}, nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "  AutoSplitEnabled: %s\n", onOff(pc.AutoSplitEnabled))
			fmt.Fprintf(&sb, "  AutoMergeEnabled: %s\n", onOff(pc.AutoMergeEnabled))
			fmt.Fprintf(&sb, "  MinCellSize:      %.0f\n", pc.MinCellSize)
			fmt.Fprintf(&sb, "  SplitThreshold:   %.2f\n", pc.SplitThreshold)
			fmt.Fprintf(&sb, "  MergeThreshold:   %.2f\n", pc.MergeThreshold)
			fmt.Fprintf(&sb, "  SplitSustain:     %s\n", pc.SplitSustain)
			fmt.Fprintf(&sb, "  MergeSustain:     %s\n", pc.MergeSustain)
			fmt.Fprintf(&sb, "  Cooldown:         %s\n", pc.Cooldown)
			fmt.Fprintf(&sb, "  EvalInterval:     %s\n", pc.EvalInterval)
			if pc.MetricFunc != nil {
				sb.WriteString("  MetricFunc:       custom\n")
			} else {
				sb.WriteString("  MetricFunc:       default (tick budget)\n")
			}
			return cellConfigResult{Output: sb.String()}, nil
		},
	}); err != nil {
		return fmt.Errorf("cell.config: %w", err)
	}

	return nil
}
