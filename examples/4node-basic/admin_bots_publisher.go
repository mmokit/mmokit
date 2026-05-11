package main

import (
	"context"
	"time"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// BotRow is one row of the "bots" admin topic. The PanelHost auto-derives
// columns from the row keys (JSON tags), so any rename here is reflected
// in the table header on the next publish.
type BotRow struct {
	CellID string `json:"cellId"`
	Count  int    `json:"count"`
}

// startBotsPublisher runs a 1Hz goroutine that publishes the per-cell
// bot count to the admin "bots" topic. Local cells only — cross-host
// enumeration is deferred to a future bot.count cmdsys verb that fans
// out via RouteAllHosts.
func startBotsPublisher(ctx context.Context, coord *mmokit.Process) {
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		// Publish once immediately so the panel doesn't show "waiting
		// for topic…" for a full second after page open.
		mmokit.PublishAdminTopic(coord, "bots", collectBotRows(ctx, coord))
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				mmokit.PublishAdminTopic(coord, "bots", collectBotRows(ctx, coord))
			}
		}
	}()
}

// collectBotRows snapshots the local cell set and asks each one's game
// loop for its current bot count. Cells transferred away during the
// snapshot just drop out — best-effort consistency is fine for a 1Hz
// telemetry topic.
func collectBotRows(ctx context.Context, coord *mmokit.Process) []BotRow {
	cells := snapshotCells(coord)
	rows := make([]BotRow, 0, len(cells))
	for _, cell := range cells {
		n, err := mmokit.CmdOnLoop(ctx, cell.Engine, func() (int, error) {
			return countBotsOnLoop(cell), nil
		})
		if err != nil {
			continue
		}
		rows = append(rows, BotRow{
			CellID: string(cell.MeshID),
			Count:  n,
		})
	}
	return rows
}
