package main

import (
	"context"
	"fmt"
	"math/rand"
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
func registerBotCommands(coord *mmokit.Coordinator, reg *cmdsys.Registry) error {
	if err := reg.Register(cmdsys.Command{
		Verb:        "bot.spawn",
		Capability:  "bot.spawn",
		Description: "spawn N bot entities into a cell (default: first live cell)",
		Route:       cmdsys.RouteLocal,
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
				if cellKey == "" {
					return nil, fmt.Errorf("no cells available")
				}
				return nil, fmt.Errorf("unknown cell %q — use `cell list` to see available cells", cellKey)
			}
			start := time.Now()
			spawned := spawnBotsOnLoop(cell, count)
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
		Description: "remove all bot entities (all cells by default, or just one)",
		Route:       cmdsys.RouteLocal,
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
					cleared += clearBotsOnLoop(cell)
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
			cleared := clearBotsOnLoop(cell)
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
		Description: "show bot count per cell",
		Route:       cmdsys.RouteLocal,
		Args:        botListArgs{},
		Result:      botListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			cells := snapshotCells(coord)
			var rows []botCellRow
			for _, cell := range cells {
				n := countBotsOnLoop(cell)
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
func resolveCell(coord *mmokit.Coordinator, cellKey string) *mmokit.Cell {
	cells := snapshotCells(coord)
	if len(cells) == 0 {
		return nil
	}
	if cellKey == "" {
		return cells[0]
	}
	for _, cell := range cells {
		if cell.ID == cellKey {
			return cell
		}
	}
	return nil
}

// snapshotCells returns the current cells sorted by ID.
func snapshotCells(coord *mmokit.Coordinator) []*mmokit.Cell {
	all := make([]*mmokit.Cell, 0, len(coord.Cells))
	for _, c := range coord.Cells {
		all = append(all, c)
	}
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j-1].ID > all[j].ID; j-- {
			all[j-1], all[j] = all[j], all[j-1]
		}
	}
	return all
}

// spawnBotsOnLoop spawns `count` bot entities into the given cell and returns
// the actual count. MUST be called from the cell's game loop goroutine — e.g.
// from inside a cmdsys handler (which already runs on the loop via ExecOnLoop)
// or from a closure posted to cell.Engine.PendingAdminCmds. Racing with the
// game tick from any other goroutine corrupts the ECS.
func spawnBotsOnLoop(cell *mmokit.Cell, count int) int {
	w, ok := cell.World.(*World)
	if !ok || w == nil {
		return 0
	}
	cellSize := mmokit.CellSize()
	minX, minY, maxX, maxY := cell.Cell.WorldBounds(cellSize)
	sizeX := maxX - minX
	sizeY := maxY - minY
	padX := sizeX * 0.1
	padY := sizeY * 0.1

	spawned := 0
	base := int(time.Now().UnixNano() % 1_000_000)
	rng := rand.New(rand.NewSource(int64(base)))
	for i := 0; i < count; i++ {
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
	w, ok := cell.World.(*World)
	if !ok || w == nil {
		return 0
	}
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
	w, ok := cell.World.(*World)
	if !ok || w == nil {
		return 0
	}
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

// spawnBotsInCell is a test-only wrapper that schedules spawnBotsOnLoop via
// PendingAdminCmds. Safe to call from goroutines that aren't the game loop
// (e.g. the e2e mesh test). Blocks with a 5s timeout. Console command handlers
// must NOT use this — they already run on the loop and would deadlock waiting
// for their own closure to drain.
func spawnBotsInCell(cell *mmokit.Cell, count int) int {
	done := make(chan int, 1)
	cell.Engine.PendingAdminCmds <- func() {
		done <- spawnBotsOnLoop(cell, count)
	}
	select {
	case n := <-done:
		return n
	case <-time.After(5 * time.Second):
		return 0
	}
}
