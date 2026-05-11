package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// BotRow is one row of the "bots" admin topic. The PanelHost auto-derives
// columns from the row keys (JSON tags), so any rename here is reflected
// in the table header on the next publish.
type BotRow struct {
	CellID string `json:"cellId"`
	Count  int    `json:"count"`
}

// startBotsPublisher runs a 1Hz goroutine that publishes per-cell bot
// counts to the admin "bots" topic. Admin SSE subscribers live on the
// coordinator's TopicBus, so the publisher only makes sense on a
// coordinator-role process — host-only processes have their own
// subscriber-less bus.
func startBotsPublisher(ctx context.Context, coord *mmokit.Process) {
	if !coord.Roles().Has(mmokit.RoleCoordinator) {
		return
	}
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
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

// collectBotRows fans out to every host via the existing bot.list
// cmdsys verb (Route: RouteAllHosts) and merges the per-cell counts.
// Re-uses the same accounting as the console `bot list` command —
// nothing in this publisher knows about the host topology directly.
func collectBotRows(ctx context.Context, coord *mmokit.Process) []BotRow {
	invokeCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	caller := cmdsys.Caller{
		ID:     "admin:bots-publisher",
		Source: cmdsys.SourceConsole,
		Grants: []cmdsys.Grant{{Pattern: "*", Allow: true}},
	}
	res, err := coord.CmdDispatcher().Invoke(invokeCtx, caller, "bot.list", nil)
	if err != nil {
		return nil
	}
	var rows []BotRow
	for _, t := range res.PerTarget {
		if !t.OK {
			continue
		}
		// Local results arrive typed; remote (MeshControl-routed) results
		// arrive as a map decoded from JSON. JSON round-trip handles both.
		b, err := json.Marshal(t.Result)
		if err != nil {
			continue
		}
		var lr botListResult
		if err := json.Unmarshal(b, &lr); err != nil {
			continue
		}
		for _, c := range lr.Cells {
			rows = append(rows, BotRow{CellID: c.Cell, Count: c.Bots})
		}
	}
	return rows
}
