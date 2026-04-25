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
	"github.com/zenion/mmoserver/pkg/cmdsys"
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
func registerBotCommands(coord *mmokit.Process, reg *cmdsys.Registry) error {
	if err := reg.Register(cmdsys.Command{
		Verb:        "bot.spawn",
		Capability:  "bot.spawn",
		Description: "spawn N bot entities into a cell (routes to the host owning the cell)",
		Route:       cmdsys.RouteSpecificCell,
		Args:        botSpawnArgs{},
		Result:      botSpawnResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
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
			spawned, err := cmdsys.OnLoop(ctx, cell.Engine, func() (int, error) {
				return spawnBotsOnLoop(cell, count), nil
			})
			if err != nil {
				return nil, err
			}
			return botSpawnResult{
				CellID:  cell.ID,
				Spawned: spawned,
				Elapsed: time.Since(start).Truncate(time.Millisecond).String(),
			}, nil
		},
	}); err != nil {
		return fmt.Errorf("bot.spawn: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "bot.clear",
		Capability:  "bot.clear",
		Description: "remove all bot entities (all cells on every host; specify CellID to target one)",
		Route:       cmdsys.RouteAllHosts,
		Args:        botClearArgs{},
		Result:      botClearResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(botClearArgs)
			target := strings.TrimSpace(args.CellID)
			start := time.Now()
			if target == "" {
				cells := snapshotCells(coord)
				cleared := 0
				for _, cell := range cells {
					n, err := cmdsys.OnLoop(ctx, cell.Engine, func() (int, error) {
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
			cleared, err := cmdsys.OnLoop(ctx, cell.Engine, func() (int, error) {
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

	if err := reg.Register(cmdsys.Command{
		Verb:        "bot.list",
		Capability:  "bot.list",
		Description: "show bot count per cell (fan-out across every host)",
		Route:       cmdsys.RouteAllHosts,
		Args:        botListArgs{},
		Result:      botListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			cells := snapshotCells(coord)
			var rows []botCellRow
			for _, cell := range cells {
				n, err := cmdsys.OnLoop(ctx, cell.Engine, func() (int, error) {
					return countBotsOnLoop(cell), nil
				})
				if err != nil {
					return nil, err
				}
				rows = append(rows, botCellRow{Cell: cell.ID, Bots: n})
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
	CellID string `cmd:"optional,help=target cell ID (default: first live cell)"`
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
	// ParseCellID + MeshCellID — matches cell.split / cell.merge / cell.migrate.
	canonical := cellKey
	if parsed, err := mmokit.ParseCellID(cellKey); err == nil {
		canonical = mmokit.MeshCellID(parsed)
	}
	for _, cell := range cells {
		if cell.ID == canonical {
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
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all
}

// spawnBotsOnLoop spawns `count` bot entities into the given cell and returns
// the actual count. MUST be called from the cell's game loop goroutine — e.g.
// from inside a cmdsys handler (which already runs on the loop via ExecOnLoop)
// or from a closure posted to cell.Engine.PendingAdminCmds. Racing with the
// game tick from any other goroutine corrupts the ECS.
func spawnBotsOnLoop(cell *mmokit.Cell, count int) int {
	w := mmokit.WorldOfCell[*World](cell)
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
		e := w.SpawnEntity(
			mmokit.Position{X: x - minX, Y: y - minY},
			mmokit.WithCollider(PlayerRadius),
			mmokit.WithEntityKind(KindPlayer),
			mmokit.WithComponents(),
		)
		name := w.NameMap.Get(e)
		name.Name = fmt.Sprintf("bot_%s_%06d", cell.ID, base+i)
		mt := w.MoveTargetMap.Get(e)
		tx := minX + padX + rng.Float32()*(sizeX-2*padX)
		ty := minY + padY + rng.Float32()*(sizeY-2*padY)
		mmokit.SetMoveTarget(mt, tx, ty)
		spawned++
	}
	return spawned
}

// clearBotsOnLoop removes every entity on the cell whose PlayerName starts
// with "bot_" and returns how many were cleared. MUST be called from the cell's
// game loop goroutine — see spawnBotsOnLoop for the reasoning.
func clearBotsOnLoop(cell *mmokit.Cell) int {
	w := mmokit.WorldOfCell[*World](cell)
	var victims []ecs.Entity
	nameMap := ecs.NewMap1[PlayerName](w.ECSWorld())
	filter := ecs.NewFilter1[PlayerName](w.ECSWorld())
	q := filter.Query()
	for q.Next() {
		name := nameMap.Get(q.Entity())
		if strings.HasPrefix(name.Name, "bot_") {
			victims = append(victims, q.Entity())
		}
	}
	for _, e := range victims {
		w.MarkForRemoval(e)
	}
	return len(victims)
}

// countBotsOnLoop reports how many bot entities currently live on the cell.
// MUST be called from the cell's game loop goroutine.
func countBotsOnLoop(cell *mmokit.Cell) int {
	w := mmokit.WorldOfCell[*World](cell)
	n := 0
	nameMap := ecs.NewMap1[PlayerName](w.ECSWorld())
	filter := ecs.NewFilter1[PlayerName](w.ECSWorld())
	q := filter.Query()
	for q.Next() {
		name := nameMap.Get(q.Entity())
		if strings.HasPrefix(name.Name, "bot_") {
			n++
		}
	}
	return n
}

// spawnBotsInCell schedules spawnBotsOnLoop via engine.RunOnLoop. Safe to
// call from any goroutine — RunOnLoop detects whether the caller is the
// game loop and short-circuits accordingly. Used by the e2e mesh test;
// console command handlers go through cmdsys.OnLoop directly.
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
