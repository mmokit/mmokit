package main

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// registerBotCommands wires the `bot` typed commands onto the coordinator's
// cmdsys registry. Called from main.go via coord.OnConsoleReady so the commands
// are available in every interactive run of the 4node-basic binary.
//
// Commands:
//
//	bot.spawn <count> [cellID]   — spawn N bot entities into the given cell
//	bot.clear [cellID]           — remove all bots (all cells, or just one)
//	bot.list                     — show bot counts per cell
func registerBotCommands(coord *mmokit.Process, reg *mmokit.CommandRegistry) error {
	if err := reg.Register(mmokit.Command{
		Verb:        "bot.spawn",
		Capability:  "bot.spawn",
		Description: "spawn N bot entities into a cell (routes to the host owning the cell)",
		Examples:    []string{"bot spawn 30 0_0", "bot spawn 100 cell_0_0"},
		Route:       mmokit.RouteSpecificCell,
		Args:        botSpawnArgs{},
		Result:      botSpawnResult{},
		Handler: func(ctx context.Context, env *mmokit.CommandEnv, raw any) (any, error) {
			args := raw.(botSpawnArgs)
			count, err := strconv.Atoi(strings.TrimSpace(args.Count))
			if err != nil || count <= 0 {
				return nil, fmt.Errorf("invalid count %q: must be a positive integer", args.Count)
			}
			cellKey := strings.TrimSpace(args.CellID)
			cell := resolveCell(coord, cellKey)
			if cell == nil {
				return nil, fmt.Errorf("unknown cell %q — use `cell list` to see available cells", cellKey)
			}
			start := time.Now()
			spawned, err := mmokit.CmdOnLoop(ctx, cell.Engine, func() (int, error) {
				return spawnBotsOnLoop(cell, count), nil
			})
			if err != nil {
				return nil, err
			}
			return botSpawnResult{
				CellID:  string(cell.MeshID),
				Spawned: spawned,
				Elapsed: time.Since(start).Truncate(time.Millisecond).String(),
			}, nil
		},
	}); err != nil {
		return fmt.Errorf("bot.spawn: %w", err)
	}

	if err := reg.Register(mmokit.Command{
		Verb:        "bot.clear",
		Capability:  "bot.clear",
		Description: "remove all bot entities (all cells on every host; specify CellID to target one)",
		Examples:    []string{"bot clear", "bot clear 0_0"},
		Route:       mmokit.RouteAllHosts,
		Args:        botClearArgs{},
		Result:      botClearResult{},
		Handler: func(ctx context.Context, env *mmokit.CommandEnv, raw any) (any, error) {
			args := raw.(botClearArgs)
			target := strings.TrimSpace(args.CellID)
			start := time.Now()
			if target == "" {
				cells := snapshotCells(coord)
				cleared := 0
				for _, cell := range cells {
					n, err := mmokit.CmdOnLoop(ctx, cell.Engine, func() (int, error) {
						return clearBotsOnLoop(cell), nil
					})
					if err != nil {
						return nil, err
					}
					cleared += n
				}
				return botClearResult{
					Cleared: cleared,
					Cells:   len(cells),
					Elapsed: time.Since(start).Truncate(time.Millisecond).String(),
				}, nil
			}
			cell := resolveCell(coord, target)
			if cell == nil {
				return nil, fmt.Errorf("unknown cell %q", target)
			}
			cleared, err := mmokit.CmdOnLoop(ctx, cell.Engine, func() (int, error) {
				return clearBotsOnLoop(cell), nil
			})
			if err != nil {
				return nil, err
			}
			return botClearResult{
				Cleared: cleared,
				Cells:   1,
				Elapsed: time.Since(start).Truncate(time.Millisecond).String(),
			}, nil
		},
	}); err != nil {
		return fmt.Errorf("bot.clear: %w", err)
	}

	if err := reg.Register(mmokit.Command{
		Verb:        "bot.list",
		Capability:  "bot.list",
		Description: "show bot count per cell (fan-out across every host)",
		Route:       mmokit.RouteAllHosts,
		Args:        botListArgs{},
		Result:      botListResult{},
		Handler: func(ctx context.Context, env *mmokit.CommandEnv, raw any) (any, error) {
			cells := snapshotCells(coord)
			var rows []botCellRow
			for _, cell := range cells {
				n, err := mmokit.CmdOnLoop(ctx, cell.Engine, func() (int, error) {
					return countBotsOnLoop(cell), nil
				})
				if err != nil {
					return nil, err
				}
				rows = append(rows, botCellRow{Cell: string(cell.MeshID), Bots: n})
			}
			return botListResult{Cells: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("bot.list: %w", err)
	}

	return nil
}

// ── arg/result types ─────────────────────────────────────────────────────────

type botSpawnArgs struct {
	Count  string `cmd:"help=number of bots to spawn"`
	CellID string `cmd:"help=target cell ID,complete=cells"`
}

type botSpawnResult struct {
	CellID  string
	Spawned int
	Elapsed string
}

type botClearArgs struct {
	CellID string `cmd:"optional,help=cell ID (default: all cells)"`
}

type botClearResult struct {
	Cleared int
	Cells   int
	Elapsed string
}

type botListArgs struct{}

type botCellRow struct {
	Cell string
	Bots int
}

type botListResult struct {
	Cells []botCellRow `cmd:"table"`
}

// ── helpers ──────────────────────────────────────────────────────────────────

// resolveCell looks up a cell by string ID. When cellKey is empty, returns
// the first live cell (sorted lexicographically for determinism). Nil if
// no match.
func resolveCell(coord *mmokit.Process, cellKey string) *mmokit.Cell {
	cells := snapshotCells(coord)
	if len(cells) == 0 {
		return nil
	}
	if cellKey == "" {
		return cells[0]
	}
	// Accept both "0_0" and "cell_0_0" by canonicalizing through
	// ParseCellID + CellID.MeshID — matches cell.split / cell.merge / cell.migrate.
	canonical := cellKey
	if parsed, err := mmokit.ParseCellID(cellKey); err == nil {
		canonical = string(parsed.MeshID())
	}
	for _, cell := range cells {
		if string(cell.MeshID) == canonical {
			return cell
		}
	}
	return nil
}

// snapshotCells returns the current cells sorted by ID.
func snapshotCells(coord *mmokit.Process) []*mmokit.Cell {
	all := make([]*mmokit.Cell, 0, len(coord.Cells))
	for _, c := range coord.Cells {
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].MeshID < all[j].MeshID })
	return all
}

// spawnBotsOnLoop spawns `count` bot entities into the given cell and returns
// the actual count. MUST be called from the cell's game loop goroutine — e.g.
// from inside a cmdsys handler (which already runs on the loop via ExecOnLoop)
// or from a closure posted to cell.Engine.PendingAdminCmds. Racing with the
// game tick from any other goroutine corrupts the ECS.
func spawnBotsOnLoop(cell *mmokit.Cell, count int) int {
	stage := cell.Stage
	cellSize := mmokit.CellSize()
	minX, minY, maxX, maxY := cell.Cell.WorldBounds(cellSize)
	sizeX := maxX - minX
	sizeY := maxY - minY
	padX := sizeX * 0.1
	padY := sizeY * 0.1

	spawned := 0
	base := int(time.Now().UnixNano() % 1_000_000)
	rng := rand.New(rand.NewSource(int64(base)))
	for i := range count {
		x := minX + padX + rng.Float32()*(sizeX-2*padX)
		y := minY + padY + rng.Float32()*(sizeY-2*padY)
		tx := minX + padX + rng.Float32()*(sizeX-2*padX)
		ty := minY + padY + rng.Float32()*(sizeY-2*padY)
		retarget := uint16(rng.Intn(100))
		botName := fmt.Sprintf("bot_%s_%06d", cell.MeshID, base+i)
		stage.SpawnEntity(
			mmokit.Position{X: x - minX, Y: y - minY},
			mmokit.WithCollider(PlayerRadius),
			mmokit.WithEntityKind(KindBot),
			mmokit.Init(func(c *BotComponents) {
				c.Name.Name = botName
				c.MoveTarget.SetTarget(tx, ty)
				// Phase the initial countdown so bots from the same spawn batch
				// don't all retarget on the same tick.
				c.Behavior.TicksUntilRetarget = retarget
			}),
		)
		spawned++
	}
	return spawned
}

// clearBotsOnLoop removes every bot entity on the cell and returns how many
// were cleared. The BotBehavior component is exclusive to KindBot, so the
// filter selects bots cleanly without name-prefix tricks. MUST be called from
// the cell's game loop goroutine — see spawnBotsOnLoop for the reasoning.
func clearBotsOnLoop(cell *mmokit.Cell) int {
	stage := cell.Stage
	var victims []ecs.Entity
	q := ecs.NewFilter1[BotBehavior](stage.ECSWorld()).Query()
	for q.Next() {
		victims = append(victims, q.Entity())
	}
	for _, e := range victims {
		stage.MarkForRemoval(e)
	}
	return len(victims)
}

// countBotsOnLoop reports how many bot entities currently live on the cell.
// MUST be called from the cell's game loop goroutine.
func countBotsOnLoop(cell *mmokit.Cell) int {
	stage := cell.Stage
	n := 0
	q := ecs.NewFilter1[BotBehavior](stage.ECSWorld()).Query()
	for q.Next() {
		n++
	}
	return n
}

// spawnBotsInCell schedules spawnBotsOnLoop via engine.RunOnLoop. Safe to
// call from any goroutine — RunOnLoop detects whether the caller is the
// game loop and short-circuits accordingly. Used by the e2e mesh test;
// console command handlers go through mmokit.CmdOnLoop directly.
func spawnBotsInCell(cell *mmokit.Cell, count int) int {
	var n int
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := cell.Engine.RunOnLoop(ctx, func() error {
		n = spawnBotsOnLoop(cell, count)
		return nil
	})
	if err != nil {
		return 0
	}
	return n
}
