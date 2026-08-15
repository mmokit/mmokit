package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/mmokit/mmokit"
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/examples/space/internal/game"
	"github.com/mmokit/mmokit/pkg/cmdsys"
)

// ── poi.list ─────────────────────────────────────────────────────────────────

// POIListArgs is empty — `poi.list` enumerates every POI on every local
// cell of the host running the handler. Fan-out is done by the
// dispatcher (Route: RouteAllHosts).
type POIListArgs struct{}

// POIListResult carries one row per POI live on this host.
type POIListResult struct {
	POIs []POIRow `cmd:"table"`
}

// POIRow is the per-POI row returned by poi.list. Fields are flat so the
// `cmd:"table"` renderer prints them as a table; nested rosters would
// break that contract.
type POIRow struct {
	NetID        uint32
	Type         uint8
	Status       uint8
	CellX        int32
	CellY        int32
	X            float32
	Y            float32
	CooldownLeft string // "ready" or a duration string like "2m45s"
	RosterAlive  int
	RosterTotal  int
}

func registerPOIList(reg *cmdsys.Registry, coord *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "poi.list",
		Capability:  "poi.list",
		Description: "list every POI on this host with its status, cooldown, and roster count",
		// RouteAllHosts: dispatcher fans out — each host's handler
		// enumerates only that host's local cells. Mirrors `bot.list`
		// and `entity.list`.
		Route:  cmdsys.RouteAllHosts,
		Args:   POIListArgs{},
		Result: POIListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, _ any) (any, error) {
			cells := make([]*mmokit.Cell, 0, len(coord.Cells))
			for _, c := range coord.Cells {
				cells = append(cells, c)
			}
			var rows []POIRow
			for _, cell := range cells {
				if cell.Stage == nil || cell.Engine == nil {
					continue
				}
				cellRows, err := mmokit.CmdOnLoop(ctx, cell.Engine, func() ([]POIRow, error) {
					return collectPOIRows(cell.Stage), nil
				})
				if err != nil {
					return nil, err
				}
				rows = append(rows, cellRows...)
			}
			return POIListResult{POIs: rows}, nil
		},
	})
}

// collectPOIRows scans the stage for POI entities and builds one row
// per POI. MUST be called on the stage's game-loop goroutine — uses
// raw ark filter iteration with the world unlocked.
func collectPOIRows(stage *mmokit.Stage) []POIRow {
	gw := mmokit.State[game.GameWorld](stage)
	if gw == nil {
		return nil
	}
	var rows []POIRow
	mmokit.ForEach2(stage, func(e mmokit.Entity, p *gamecomp.POI, pos *mmokit.Position) {
		nid := e.NetID()
		alive := len(gw.PoiRosters(nid))
		roster := game.RosterForIdx(p.RosterDefIdx)
		total := 0
		for _, m := range roster.Members {
			total += m.Count
		}
		cooldownLeft := "ready"
		if p.Status == gamecomp.POIStatusCooldown || p.Status == gamecomp.POIStatusCleared {
			cooldownSec := gw.POICooldownSec(p.Tier)
			elapsed := time.Since(time.Unix(0, p.ClearedAt))
			remain := time.Duration(cooldownSec)*time.Second - elapsed
			if remain > 0 {
				cooldownLeft = remain.Round(time.Second).String()
			}
		}
		rows = append(rows, POIRow{
			NetID:        nid,
			Type:         p.Type,
			Status:       p.Status,
			CellX:        gw.RootCell.CellX,
			CellY:        gw.RootCell.CellY,
			X:            pos.X,
			Y:            pos.Y,
			CooldownLeft: cooldownLeft,
			RosterAlive:  alive,
			RosterTotal:  total,
		})
	})
	return rows
}

// ── poi.clear ────────────────────────────────────────────────────────────────

// POIClearArgs identifies the POI to force-clear by its network ID.
type POIClearArgs struct {
	NetID uint32 `cmd:"help=POI network ID"`
}

// POIClearResult reports whether the kill actually landed.
type POIClearResult struct {
	Cleared      bool
	RosterKilled int
}

func registerPOIClear(reg *cmdsys.Registry, coord *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "poi.clear",
		Capability:  "poi.clear",
		Description: "force-clear a POI (debug): zero the HP of every roster member so POISystem fires the bounty crate + cooldown",
		Examples:    []string{"poi.clear 12345"},
		// RouteAllHosts: a POI's netID could live on any host's cells.
		// Each host scans its own cells; the host that owns the POI
		// returns Cleared=true, the others return Cleared=false.
		Route:  cmdsys.RouteAllHosts,
		Args:   POIClearArgs{},
		Result: POIClearResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(POIClearArgs)
			if args.NetID == 0 {
				return nil, fmt.Errorf("poi.clear: netID must be non-zero")
			}
			cells := make([]*mmokit.Cell, 0, len(coord.Cells))
			for _, c := range coord.Cells {
				cells = append(cells, c)
			}
			for _, cell := range cells {
				if cell.Stage == nil || cell.Engine == nil {
					continue
				}
				res, err := mmokit.CmdOnLoop(ctx, cell.Engine, func() (POIClearResult, error) {
					return clearPOIOnLoop(cell.Stage, args.NetID), nil
				})
				if err != nil {
					return nil, err
				}
				if res.Cleared {
					return res, nil
				}
			}
			return POIClearResult{Cleared: false}, nil
		},
	})
}

// clearPOIOnLoop zeroes the Health of every live roster member of the
// POI with the given netID. The POISystem's next tick will observe the
// roster as empty and fire onClear (loot crate + cooldown transition).
// Returns Cleared=false if the POI is not on this stage.
func clearPOIOnLoop(stage *mmokit.Stage, netID uint32) POIClearResult {
	gw := mmokit.State[game.GameWorld](stage)
	if gw == nil {
		return POIClearResult{}
	}
	e := mmokit.EntityByNetID(stage, netID)
	if !e.Alive() {
		return POIClearResult{}
	}
	if !mmokit.Has[gamecomp.POI](e) {
		return POIClearResult{}
	}
	killed := 0
	for _, nid := range gw.PoiRosters(netID) {
		re := mmokit.EntityByNetID(stage, nid)
		if !re.Alive() {
			continue
		}
		h := mmokit.Get[gamecomp.Health](re)
		if h == nil || h.Current <= 0 {
			continue
		}
		h.Current = 0
		killed++
	}
	return POIClearResult{Cleared: true, RosterKilled: killed}
}

// ── poi.spawn ────────────────────────────────────────────────────────────────

// POISpawnArgs takes world coordinates (matching npc.spawn / entity.spawn).
// The handler resolves which cell owns (X,Y) and converts to cell-local
// coords before calling gw.SpawnPOI.
type POISpawnArgs struct {
	X         float32 `cmd:"help=world X coordinate"`
	Y         float32 `cmd:"help=world Y coordinate"`
	RosterIdx uint16  `cmd:"help=roster def index (0 = Starter Arena)"`
}

// POISpawnResult reports the assigned netID and resolved cell.
type POISpawnResult struct {
	NetID  uint32
	CellID string
}

func registerPOISpawn(reg *cmdsys.Registry, coord *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "poi.spawn",
		Capability:  "poi.spawn",
		Description: "manually spawn a combat POI at world (X,Y) with the given roster (no procgen)",
		Examples:    []string{"poi.spawn 2000 2000 0"},
		// RouteLocal: the handler resolves the destination cell from
		// (X,Y) via coord.CellAtPosition. Mirrors npc.spawn.
		Route:  cmdsys.RouteLocal,
		Args:   POISpawnArgs{},
		Result: POISpawnResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(POISpawnArgs)
			cellID := coord.CellAtPosition(args.X, args.Y)
			if cellID == "" {
				return nil, fmt.Errorf("poi.spawn: no cell owns world position (%g,%g)", args.X, args.Y)
			}
			cell := coord.CellByID(mmokit.MeshCellID(cellID))
			if cell == nil || cell.Stage == nil {
				return nil, fmt.Errorf("poi.spawn: cell %q not local to this host", cellID)
			}
			gw := gwForStage(coord, cell.Stage)
			if gw == nil {
				return nil, fmt.Errorf("poi.spawn: not a game-world cell")
			}

			// Convert world coords to cell-local — gw.SpawnPOI takes
			// local coords (matches the procgen path in spawnPOIs).
			minX, minY, _, _ := cell.CellID().WorldBounds(mmokit.CellSize())
			localX := args.X - minX
			localY := args.Y - minY

			// Derive tier from world distance so operator-spawned POIs
			// match the gradient at their position — same rule the procgen
			// path uses (per-POI tier, not cell-tier).
			cellCoord := mmokit.CellCoord{CellX: int32(cell.CellID().X), CellY: int32(cell.CellID().Y)}
			stationCell := gw.Config.StationCell
			tier := game.TierForDist(game.DistFromStation(cellCoord, localX, localY, stationCell))

			return mmokit.CmdOnLoop(ctx, cell.Engine, func() (POISpawnResult, error) {
				netID := gw.SpawnPOIWithRoster(localX, localY, gamecomp.POITypeCombat, args.RosterIdx, tier)
				return POISpawnResult{NetID: netID, CellID: cellID}, nil
			})
		},
	})
}
