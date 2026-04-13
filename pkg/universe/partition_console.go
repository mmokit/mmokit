package universe

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/engine"
)

// registerCellCommands registers the "cell" command group on the console.
// The list and info commands are always registered; split/merge/cooldowns/
// autosplit/automerge/config are only added when DynamicPartitioning is enabled.
func (c *Coordinator) registerCellCommands(console *engine.Console) {
	cellGroup := engine.NewCommandGroup("cell", "partitioning", "manage dynamic cell partitioning")

	cellGroup.Add(engine.Command{
		Name:        "list",
		Category:    "partitioning",
		Description: "list all active cells with load info",
		Fn: func(args []string) {
			// Enumerate cells from every available source so the command
			// works in all modes: all-in-one (CellOwner populated locally),
			// coordinator (cells live on remote nodes; HostRegistry carries
			// the ownership), and pre-settle coordinator (fall back to
			// enumerating the configured grid).
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

			// Coordinator pre-settle: show the configured grid even before
			// any cell has been assigned. Nothing to show for node mode
			// (no local grid config beyond what the coordinator provides).
			if len(cells) == 0 && c.cfg.Mode == "coordinator" {
				for sy := uint32(0); sy < c.cfg.CellsY; sy++ {
					for sx := uint32(0); sx < c.cfg.CellsX; sx++ {
						cell := CellID{X: int32(sx), Y: int32(sy)}
						cells = append(cells, cell)
					}
				}
			}

			slices.SortFunc(cells, func(a, b CellID) int {
				if c := cmp.Compare(a.Depth, b.Depth); c != 0 {
					return c
				}
				if c := cmp.Compare(a.X, b.X); c != 0 {
					return c
				}
				return cmp.Compare(a.Y, b.Y)
			})

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("  %-12s %-6s %-5s %-18s %-16s %-10s %-8s %-6s %-10s\n",
				"CELL", "SIZE", "DEPTH", "NODE", "HOST", "ENTITIES", "PLAYERS", "LOAD", "COOLDOWN"))
			sb.WriteString(fmt.Sprintf("  %-12s %-6s %-5s %-18s %-16s %-10s %-8s %-6s %-10s\n",
				"----", "----", "-----", "----", "----", "--------", "-------", "----", "--------"))

			now := time.Now()
			for _, cell := range cells {
				nodeID := c.CellOwner[cell]
				size := cell.Size(c.baseCellSize())
				snap, _ := c.nodeLoad(nodeID)
				cd := "-"
				if c.partState != nil {
					c.partState.mu.Lock()
					if until, ok := c.partState.cooldowns[cell]; ok && now.Before(until) {
						remaining := until.Sub(now).Truncate(time.Second)
						cd = remaining.String()
					}
					c.partState.mu.Unlock()
				}
				// Resolve host: prefer HostRegistry (multi-host mode), fall
				// back to in-process cellToHostMap (all-in-one mode). Both
				// maps are keyed on MeshCellID ("cell_X_Y") format, not
				// CellID.String() ("X_Y") format — the latter silently
				// returns "" on lookup.
				cellKey := MeshCellID(cell)
				hostID := ""
				if c.hostRegistry != nil {
					hostID = c.hostRegistry.HostForCell(cellKey)
				}
				if hostID == "" {
					hostID = c.cellToHostMap[cellKey]
				}
				sb.WriteString(fmt.Sprintf("  %-12s %-6.0f %-5d %-18s %-16s %-10d %-8d %-6.2f %-10s\n",
					cell, size, cell.Depth, nodeID, hostID,
					snap.Entities.Real, snap.Entities.Connected,
					snap.CompositeLoad, cd))
			}
			console.Print(sb.String())
		},
	})

	cellGroup.Add(engine.Command{
		Name:        "info",
		Category:    "partitioning",
		Usage:       "info <cellID>",
		Description: "show detailed info for a cell",
		Fn: func(args []string) {
			if len(args) < 1 {
				console.Print("  usage: cell info <cellID>\n")
				return
			}
			cell, err := ParseCellID(args[0])
			if err != nil {
				console.Printf("  invalid cell ID: %v\n", err)
				return
			}

			// Resolve cell identity from three sources so `cell info`
			// works in all modes: all-in-one (c.CellOwner), coordinator
			// (HostRegistry owns the truth), and pre-settle coordinator
			// (configured grid). nodeID is the in-process cell-ID string
			// used for metrics lookups; hostID is the owning remote host
			// (only meaningful in coordinator mode).
			cellKey := MeshCellID(cell)
			nodeID, existsLocal := c.CellOwner[cell]
			hostID := ""
			if c.hostRegistry != nil {
				hostID = c.hostRegistry.HostForCell(cellKey)
			}
			if hostID == "" {
				hostID = c.cellToHostMap[cellKey]
			}

			// Cell must exist either locally OR on a remote host OR in
			// the configured grid for coordinator mode. Otherwise report
			// not found.
			inGrid := cell.Depth == 0 && cell.X >= 0 && cell.Y >= 0 &&
				uint32(cell.X) < c.cfg.CellsX && uint32(cell.Y) < c.cfg.CellsY
			if !existsLocal && hostID == "" && !inGrid {
				console.Printf("  cell %s does not exist\n", cell)
				return
			}

			snap, _ := c.nodeLoad(nodeID)
			size := cell.Size(c.baseCellSize())
			minX, minY, maxX, maxY := cell.WorldBounds(c.baseCellSize())

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("  Cell:       %s\n", cell))
			if nodeID != "" {
				sb.WriteString(fmt.Sprintf("  Node:       %s\n", nodeID))
			}
			if hostID != "" {
				sb.WriteString(fmt.Sprintf("  Host:       %s\n", hostID))
			}
			sb.WriteString(fmt.Sprintf("  Depth:      %d\n", cell.Depth))
			sb.WriteString(fmt.Sprintf("  Size:       %.0f\n", size))
			sb.WriteString(fmt.Sprintf("  Bounds:     (%.0f, %.0f) - (%.0f, %.0f)\n", minX, minY, maxX, maxY))
			sb.WriteString(fmt.Sprintf("  Entities:   %d real, %d replica, %d ghost\n",
				snap.Entities.Real, snap.Entities.Replica, snap.Entities.Ghost))
			sb.WriteString(fmt.Sprintf("  Players:    %d\n", snap.Entities.Connected))
			sb.WriteString(fmt.Sprintf("  Load:       %.2f\n", snap.CompositeLoad))

			neighbors := c.Topology.Neighbors[cell]
			if len(neighbors) > 0 {
				neighborStrs := make([]string, len(neighbors))
				for i, n := range neighbors {
					neighborStrs[i] = n.String()
				}
				sb.WriteString(fmt.Sprintf("  Neighbors:  %s\n", strings.Join(neighborStrs, ", ")))
			}

			if c.partState != nil && c.partState.onCooldown(cell) {
				sb.WriteString("  Cooldown:   ACTIVE\n")
			}

			if c.cfg.DynamicPartitioning != nil {
				canSplit := size/2 >= c.cfg.DynamicPartitioning.MinCellSize
				sb.WriteString(fmt.Sprintf("  Can split:  %v\n", canSplit))
				if cell.Depth > 0 {
					sb.WriteString(fmt.Sprintf("  Can merge:  true (parent: %s)\n", cell.Parent()))
				}
			}

			console.Print(sb.String())
		},
	})

	// Partition-specific commands: only registered when DynamicPartitioning is enabled.
	if c.partState != nil {
		cellGroup.Add(engine.Command{
			Name:        "split",
			Category:    "partitioning",
			Usage:       "split <cellID>",
			Description: "manually split a cell into 4 sub-cells (bypasses cooldown)",
			Fn: func(args []string) {
				if len(args) < 1 {
					console.Print("  usage: cell split <cellID>\n")
					return
				}
				cell, err := ParseCellID(args[0])
				if err != nil {
					console.Printf("  invalid cell ID: %v\n", err)
					return
				}
				if err := c.SplitCell(cell, true); err != nil {
					console.Printf("  split failed: %v\n", err)
					return
				}
				console.Printf("  split complete: %s -> 4 sub-cells\n", cell)
			},
		})

		cellGroup.Add(engine.Command{
			Name:        "merge",
			Category:    "partitioning",
			Usage:       "merge <cellID>",
			Description: "manually merge a cell and its 3 siblings into parent (bypasses cooldown)",
			Fn: func(args []string) {
				if len(args) < 1 {
					console.Print("  usage: cell merge <cellID>\n")
					return
				}
				cell, err := ParseCellID(args[0])
				if err != nil {
					console.Printf("  invalid cell ID: %v\n", err)
					return
				}
				if err := c.MergeCell(cell, true); err != nil {
					console.Printf("  merge failed: %v\n", err)
					return
				}
				console.Printf("  merge complete: %s + siblings -> %s\n", cell, cell.Parent())
			},
		})

		cellGroup.Add(engine.Command{
			Name:        "cooldowns",
			Category:    "partitioning",
			Description: "show active cooldowns",
			Fn: func(args []string) {
				c.partState.mu.Lock()
				defer c.partState.mu.Unlock()
				if len(c.partState.cooldowns) == 0 {
					console.Print("  no active cooldowns\n")
					return
				}
				var sb strings.Builder
				for cell, until := range c.partState.cooldowns {
					sb.WriteString(fmt.Sprintf("  %s -> %s\n", cell, until.Format("15:04:05")))
				}
				console.Print(sb.String())
			},
		})

		cellGroup.Add(engine.Command{
			Name:        "autosplit",
			Category:    "partitioning",
			Usage:       "autosplit [on|off]",
			Description: "show or toggle automatic cell splitting",
			Fn: func(args []string) {
				pc := c.cfg.DynamicPartitioning
				if len(args) == 0 {
					console.Printf("  autosplit: %s\n", onOff(pc.AutoSplitEnabled))
					return
				}
				switch args[0] {
				case "on":
					pc.AutoSplitEnabled = true
					console.Print("  autosplit enabled\n")
				case "off":
					pc.AutoSplitEnabled = false
					console.Print("  autosplit disabled\n")
				default:
					console.Print("  usage: cell autosplit [on|off]\n")
				}
			},
		})

		cellGroup.Add(engine.Command{
			Name:        "automerge",
			Category:    "partitioning",
			Usage:       "automerge [on|off]",
			Description: "show or toggle automatic cell merging",
			Fn: func(args []string) {
				pc := c.cfg.DynamicPartitioning
				if len(args) == 0 {
					console.Printf("  automerge: %s\n", onOff(pc.AutoMergeEnabled))
					return
				}
				switch args[0] {
				case "on":
					pc.AutoMergeEnabled = true
					console.Print("  automerge enabled\n")
				case "off":
					pc.AutoMergeEnabled = false
					console.Print("  automerge disabled\n")
				default:
					console.Print("  usage: cell automerge [on|off]\n")
				}
			},
		})

		cellGroup.Add(engine.Command{
			Name:        "config",
			Category:    "partitioning",
			Description: "show current partition configuration",
			Fn: func(args []string) {
				pc := c.cfg.DynamicPartitioning
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("  AutoSplitEnabled: %s\n", onOff(pc.AutoSplitEnabled)))
				sb.WriteString(fmt.Sprintf("  AutoMergeEnabled: %s\n", onOff(pc.AutoMergeEnabled)))
				sb.WriteString(fmt.Sprintf("  MinCellSize:      %.0f\n", pc.MinCellSize))
				sb.WriteString(fmt.Sprintf("  SplitThreshold:   %.2f\n", pc.SplitThreshold))
				sb.WriteString(fmt.Sprintf("  MergeThreshold:   %.2f\n", pc.MergeThreshold))
				sb.WriteString(fmt.Sprintf("  SplitSustain:     %s\n", pc.SplitSustain))
				sb.WriteString(fmt.Sprintf("  MergeSustain:     %s\n", pc.MergeSustain))
				sb.WriteString(fmt.Sprintf("  Cooldown:         %s\n", pc.Cooldown))
				sb.WriteString(fmt.Sprintf("  EvalInterval:     %s\n", pc.EvalInterval))
				if pc.MetricFunc != nil {
					sb.WriteString("  MetricFunc:       custom\n")
				} else {
					sb.WriteString("  MetricFunc:       default (tick budget)\n")
				}
				console.Print(sb.String())
			},
		})
	} // end partState != nil

	console.RegisterGroup(cellGroup)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// baseCellSize returns the base cell size from the coordinator config.
func (c *Coordinator) baseCellSize() float32 {
	if c.cfg.CellSize > 0 {
		return c.cfg.CellSize
	}
	return 8192
}
