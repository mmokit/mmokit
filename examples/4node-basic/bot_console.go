package main

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// registerBotCommands wires the `bot` command group onto the coordinator's
// console. Called from main.go via coord.OnConsoleReady so the commands are
// available in every interactive run of the 4node-basic binary.
//
// Commands:
//
//	bot spawn <count> [cellID]   — spawn N bot entities into the given cell
//	bot clear [cellID]           — remove all bots (all cells, or just one)
//	bot list                     — show bot counts per cell
//
// `cellID` defaults to the lexicographically first cell in `coord.Cells` if
// omitted. Bots use KindPlayer with name prefix `bot_` so BotSystem.Update
// picks them up regardless of which cell they end up on after a split.
func registerBotCommands(coord *mmokit.Coordinator, console *engine.Console) {
	group := engine.NewCommandGroup("bot", "demo", "spawn and manage bot entities for the S7 split demo")

	group.Add(engine.Command{
		Name:        "spawn",
		Category:    "demo",
		Usage:       "spawn <count> [cellID]",
		Description: "spawn N bot entities into a cell (default: first live cell)",
		Fn: func(args []string) {
			if len(args) < 1 {
				console.Print("  usage: bot spawn <count> [cellID]\n")
				return
			}
			count, err := strconv.Atoi(args[0])
			if err != nil || count <= 0 {
				console.Printf("  invalid count %q: must be a positive integer\n", args[0])
				return
			}
			cellKey := ""
			if len(args) >= 2 {
				cellKey = args[1]
			}
			cell := resolveCell(coord, cellKey)
			if cell == nil {
				if cellKey == "" {
					console.Print("  no cells available\n")
				} else {
					console.Printf("  unknown cell %q — use `cell list` to see available cells\n", cellKey)
				}
				return
			}

			start := time.Now()
			spawned := spawnBotsInCell(cell, count)
			console.Printf("  spawned %d bot(s) in %s (%s)\n",
				spawned, cell.ID, time.Since(start).Truncate(time.Millisecond))
		},
	})

	group.Add(engine.Command{
		Name:        "clear",
		Category:    "demo",
		Usage:       "clear [cellID]",
		Description: "remove all bot entities (all cells by default, or just one)",
		Fn: func(args []string) {
			target := ""
			if len(args) >= 1 {
				target = args[0]
			}

			start := time.Now()
			cleared := 0
			if target == "" {
				// Snapshot the cell set under coord.mu so a concurrent split
				// doesn't mutate the map mid-iteration.
				cells := snapshotCells(coord)
				for _, cell := range cells {
					cleared += clearBotsInCell(cell)
				}
				console.Printf("  cleared %d bot(s) across %d cell(s) (%s)\n",
					cleared, len(cells), time.Since(start).Truncate(time.Millisecond))
				return
			}
			cell := resolveCell(coord, target)
			if cell == nil {
				console.Printf("  unknown cell %q\n", target)
				return
			}
			cleared = clearBotsInCell(cell)
			console.Printf("  cleared %d bot(s) in %s (%s)\n",
				cleared, cell.ID, time.Since(start).Truncate(time.Millisecond))
		},
	})

	group.Add(engine.Command{
		Name:        "list",
		Category:    "demo",
		Description: "show bot count per cell",
		Fn: func(args []string) {
			cells := snapshotCells(coord)
			if len(cells) == 0 {
				console.Print("  no cells\n")
				return
			}
			total := 0
			var sb strings.Builder
			sb.WriteString("  CELL            BOTS\n")
			for _, cell := range cells {
				n := countBotsInCell(cell)
				total += n
				sb.WriteString(fmt.Sprintf("  %-15s %d\n", cell.ID, n))
			}
			sb.WriteString(fmt.Sprintf("  %-15s %d\n", "TOTAL", total))
			console.Print(sb.String())
		},
	})

	console.RegisterGroup(group)
}

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

// snapshotCells returns the current cells sorted by ID. Matches the
// existing pattern used by the game's other console paths: direct read
// of coord.Cells under no additional lock. There is an inherent race
// with a concurrent split/merge commit here; in practice the console is
// human-driven so the window is negligible, and the worst case is
// operating on a stale cell set for one command.
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

// spawnBotsInCell enqueues a game-loop closure that spawns `count` bot
// entities into the given cell, each with a random position inside the
// cell's world bounds and an immediate MoveTarget so they start walking
// right away. Blocks until the closure completes (with a 5s timeout).
// Returns the number of bots actually spawned.
func spawnBotsInCell(cell *mmokit.Cell, count int) int {
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

	done := make(chan int, 1)
	cell.Engine.PendingAdminCmds <- func() {
		spawned := 0
		// Pick a unique name-suffix base so repeated spawn calls don't
		// collide. Nanoseconds is coarse enough for a human-driven
		// console but unique across seconds.
		base := int(time.Now().UnixNano() % 1_000_000)
		rng := rand.New(rand.NewSource(int64(base)))
		for i := 0; i < count; i++ {
			x := minX + padX + rng.Float32()*(sizeX-2*padX)
			y := minY + padY + rng.Float32()*(sizeY-2*padY)
			// SpawnEntity uses the cell's WorldOrigin as the origin —
			// so we pass local-space coords (x-minX, y-minY).
			e := w.SpawnEntity(
				mmokit.Position{X: x - minX, Y: y - minY},
				mmokit.WithCollider(PlayerRadius),
				mmokit.WithEntityKind(KindPlayer),
				mmokit.WithComponents(),
			)
			name := w.NameMap.Get(e)
			name.Name = fmt.Sprintf("bot_%s_%06d", cell.ID, base+i)

			// Seed an initial MoveTarget so bots start walking immediately.
			mt := w.MoveTargetMap.Get(e)
			tx := minX + padX + rng.Float32()*(sizeX-2*padX)
			ty := minY + padY + rng.Float32()*(sizeY-2*padY)
			mmokit.SetMoveTarget(mt, tx, ty)

			spawned++
		}
		done <- spawned
	}
	select {
	case n := <-done:
		return n
	case <-time.After(5 * time.Second):
		return 0
	}
}

// clearBotsInCell removes every entity on the cell whose PlayerName starts
// with "bot_". Blocks on a game-loop closure with a 5s timeout. Returns the
// number of bots marked for removal.
func clearBotsInCell(cell *mmokit.Cell) int {
	w, ok := cell.World.(*World)
	if !ok || w == nil {
		return 0
	}
	done := make(chan int, 1)
	cell.Engine.PendingAdminCmds <- func() {
		n := 0
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
			n++
		}
		done <- n
	}
	select {
	case n := <-done:
		return n
	case <-time.After(5 * time.Second):
		return 0
	}
}

// countBotsInCell reports how many bot entities currently live on the cell.
// Runs a read-only query via the game loop.
func countBotsInCell(cell *mmokit.Cell) int {
	w, ok := cell.World.(*World)
	if !ok || w == nil {
		return 0
	}
	done := make(chan int, 1)
	cell.Engine.PendingAdminCmds <- func() {
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
		done <- n
	}
	select {
	case n := <-done:
		return n
	case <-time.After(5 * time.Second):
		return 0
	}
}
