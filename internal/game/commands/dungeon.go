package commands

import (
	"context"
	"fmt"
	"time"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// ── dungeon.list ─────────────────────────────────────────────────────────────

// DungeonListArgs is empty — `dungeon.list` enumerates every dungeon on
// every local cell of the host running the handler. Fan-out is done by
// the dispatcher (Route: RouteAllHosts), mirroring poi.list.
type DungeonListArgs struct{}

// DungeonListResult is one row per chamber across every dungeon. Flat
// shape so the console table renderer can pick it up via `cmd:"table"`
// — the SPA can group on (DungeonNetID, DungeonName) client-side if it
// wants the nested view. Mirrors poi.list's POIListResult shape.
type DungeonListResult struct {
	Rows []DungeonChamberRow `cmd:"table" json:"rows"`
}

// DungeonChamberRow is one chamber's status snapshot with its parent
// dungeon's identity columns included. Role/Status are pre-formatted
// short strings ("Mob"/"Side"/"Term", "Active"/"Cleared"/"CD") so the
// table output is readable without a separate legend.
type DungeonChamberRow struct {
	DungeonNetID uint32  `json:"dungeon_net_id"`
	DungeonName  string  `json:"dungeon_name"`
	CellX        int32   `json:"cell_x"`
	CellY        int32   `json:"cell_y"`
	X            float32 `json:"x"`
	Y            float32 `json:"y"`
	ChamberID    uint16  `json:"chamber_id"`
	Role         string  `json:"role"`
	Status       string  `json:"status"`
	Alive        int     `json:"alive"`
	CooldownLeft string  `json:"cooldown_left"` // "ready" or "2m45s"
}

func registerDungeonList(reg *cmdsys.Registry, coord *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "dungeon.list",
		Capability:  "dungeon.list",
		Description: "list every dungeon on this host with per-chamber status and cooldown remaining",
		// RouteAllHosts: a dungeon's netID could live on any host's cells.
		// Each host scans its own cells; results aggregate at the caller.
		Route:  cmdsys.RouteAllHosts,
		Args:   DungeonListArgs{},
		Result: DungeonListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, _ any) (any, error) {
			cells := make([]*mmokit.Cell, 0, len(coord.Cells))
			for _, c := range coord.Cells {
				cells = append(cells, c)
			}
			var out []DungeonChamberRow
			for _, cell := range cells {
				if cell.Stage == nil || cell.Engine == nil {
					continue
				}
				cellRows, err := mmokit.CmdOnLoop(ctx, cell.Engine, func() ([]DungeonChamberRow, error) {
					return collectDungeonChamberRows(cell), nil
				})
				if err != nil {
					return nil, err
				}
				out = append(out, cellRows...)
			}
			return DungeonListResult{Rows: out}, nil
		},
	})
}

// collectDungeonChamberRows walks gw.DungeonChambers() and emits one
// flat row per chamber across every dungeon on this cell. MUST be called
// on the stage's game-loop goroutine.
func collectDungeonChamberRows(cell *mmokit.Cell) []DungeonChamberRow {
	stage := cell.Stage
	gw := mmokit.State[game.GameWorld](stage)
	if gw == nil {
		return nil
	}
	now := time.Now().UnixNano()
	var rows []DungeonChamberRow
	for dungeonNetID, chambers := range gw.DungeonChambers() {
		e := mmokit.EntityByNetID(stage, dungeonNetID)
		if !e.Alive() {
			continue
		}
		pos := mmokit.Get[mmokit.Position](e)
		dcomp := mmokit.Get[gamecomp.Dungeon](e)
		var px, py float32
		if pos != nil {
			px, py = pos.X, pos.Y
		}
		var name string
		if dcomp != nil {
			name = dcomp.Name
		}
		for _, c := range chambers {
			row := DungeonChamberRow{
				DungeonNetID: dungeonNetID,
				DungeonName:  name,
				CellX:        cell.Cell.X,
				CellY:        cell.Cell.Y,
				X:            px,
				Y:            py,
				ChamberID:    c.ID,
				Role:         roleLabel(c.Role),
				Status:       statusLabel(c.Status),
				Alive:        len(c.AliveNetIDs),
				CooldownLeft: "—",
			}
			switch c.Status {
			case game.ChamberActive:
				row.CooldownLeft = "ready"
			case game.ChamberCooldown:
				if c.ClearedAt > 0 {
					cooldown := game.ChamberCooldownFor(gw.Config, c.Role)
					elapsedSec := float32(now-c.ClearedAt) / 1e9
					remain := cooldown - elapsedSec
					if remain > 0 {
						row.CooldownLeft = formatDur(remain)
					} else {
						row.CooldownLeft = "ready"
					}
				}
			}
			rows = append(rows, row)
		}
	}
	return rows
}

func roleLabel(r game.ChamberRole) string {
	switch r {
	case game.ChamberMobPack:
		return "Mob"
	case game.ChamberSideBoss:
		return "Side"
	case game.ChamberTerminal:
		return "Term"
	default:
		return "?"
	}
}

func statusLabel(s game.ChamberStatus) string {
	switch s {
	case game.ChamberActive:
		return "Active"
	case game.ChamberCleared:
		return "Cleared"
	case game.ChamberCooldown:
		return "CD"
	default:
		return "?"
	}
}

func formatDur(sec float32) string {
	if sec <= 0 {
		return "ready"
	}
	mins := int(sec) / 60
	rem := int(sec) % 60
	if mins == 0 {
		return fmt.Sprintf("%ds", rem)
	}
	return fmt.Sprintf("%dm%02ds", mins, rem)
}

// ── dungeon.respawn ──────────────────────────────────────────────────────────

// DungeonRespawnArgs identifies a dungeon by netID and optionally a
// single chamber (0 = all chambers).
type DungeonRespawnArgs struct {
	NetID     uint32 `cmd:"help=dungeon network ID"`
	ChamberID uint16 `cmd:"help=chamber ID (0 = all chambers)"`
}

// DungeonRespawnResult reports how many chambers were respawned and how
// many roster members were despawned + re-spawned in the process.
type DungeonRespawnResult struct {
	Found             bool
	ChambersRespawned int
	RosterDespawned   int
	RosterSpawned     int
}

func registerDungeonRespawn(reg *cmdsys.Registry, coord *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "dungeon.respawn",
		Capability:  "dungeon.respawn",
		Description: "force-respawn a chamber (or all chambers) of a dungeon; bypasses cleared/cooldown",
		Examples:    []string{"dungeon.respawn 12345", "dungeon.respawn 12345 2"},
		// RouteAllHosts: the dungeon's netID could live on any host. Each
		// host scans its own cells; the host that owns the dungeon
		// returns Found=true.
		Route:  cmdsys.RouteAllHosts,
		Args:   DungeonRespawnArgs{},
		Result: DungeonRespawnResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(DungeonRespawnArgs)
			if args.NetID == 0 {
				return nil, fmt.Errorf("dungeon.respawn: netID must be non-zero")
			}
			cells := make([]*mmokit.Cell, 0, len(coord.Cells))
			for _, c := range coord.Cells {
				cells = append(cells, c)
			}
			for _, cell := range cells {
				if cell.Stage == nil || cell.Engine == nil {
					continue
				}
				res, err := mmokit.CmdOnLoop(ctx, cell.Engine, func() (DungeonRespawnResult, error) {
					return respawnDungeonOnLoop(cell.Stage, args.NetID, args.ChamberID), nil
				})
				if err != nil {
					return nil, err
				}
				if res.Found {
					return res, nil
				}
			}
			return DungeonRespawnResult{Found: false}, nil
		},
	})
}

// respawnDungeonOnLoop is the per-stage worker for dungeon.respawn.
// Returns Found=false when the dungeon isn't on this stage so the
// dispatcher can keep scanning.
func respawnDungeonOnLoop(stage *mmokit.Stage, netID uint32, chamberID uint16) DungeonRespawnResult {
	gw := mmokit.State[game.GameWorld](stage)
	if gw == nil {
		return DungeonRespawnResult{}
	}
	chambers, ok := gw.DungeonChambers()[netID]
	if !ok {
		return DungeonRespawnResult{}
	}
	res := DungeonRespawnResult{Found: true}
	for _, c := range chambers {
		if chamberID != 0 && c.ID != chamberID {
			continue
		}
		// Despawn currently-alive roster (admin force — skip the chest /
		// cooldown phases entirely).
		for _, id := range c.AliveNetIDs {
			e := mmokit.EntityByNetID(stage, id)
			if !e.Alive() {
				continue
			}
			mmokit.Despawn(e)
			res.RosterDespawned++
		}
		c.AliveNetIDs = nil
		c.RespawnCount++
		// Re-roll the roster def from a fresh seed so the respawn isn't
		// deterministic on every admin click.
		rngU64 := uint64(netID) ^ uint64(c.ID+1)*0xbf58476d1ce4e5b9 ^ uint64(c.RespawnCount)
		c.RosterDefIdx = game.RosterIdxForRole(c.Role, rngU64)
		c.Status = game.ChamberActive
		c.ClearedAt = 0
		before := len(c.AliveNetIDs)
		gw.SpawnChamberRoster(netID, c)
		res.RosterSpawned += len(c.AliveNetIDs) - before
		res.ChambersRespawned++
	}
	return res
}

// ── dungeon.regenerate ───────────────────────────────────────────────────────

// DungeonRegenerateArgs identifies the dungeon to tear down and
// re-procgen. Debug-only — the layout normally never re-rolls per spec.
type DungeonRegenerateArgs struct {
	NetID uint32 `cmd:"help=dungeon network ID"`
}

// DungeonRegenerateResult reports the new netID and the seed used.
type DungeonRegenerateResult struct {
	Found    bool
	OldNetID uint32
	NewNetID uint32
	Seed     uint64
}

func registerDungeonRegenerate(reg *cmdsys.Registry, coord *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "dungeon.regenerate",
		Capability:  "dungeon.regenerate",
		Description: "debug-only: tear down a dungeon (including its wall colliders) and re-procgen with a fresh seed at the same position",
		Examples:    []string{"dungeon.regenerate 12345"},
		Route:       cmdsys.RouteAllHosts,
		Args:        DungeonRegenerateArgs{},
		Result:      DungeonRegenerateResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(DungeonRegenerateArgs)
			if args.NetID == 0 {
				return nil, fmt.Errorf("dungeon.regenerate: netID must be non-zero")
			}
			cells := make([]*mmokit.Cell, 0, len(coord.Cells))
			for _, c := range coord.Cells {
				cells = append(cells, c)
			}
			for _, cell := range cells {
				if cell.Stage == nil || cell.Engine == nil {
					continue
				}
				res, err := mmokit.CmdOnLoop(ctx, cell.Engine, func() (DungeonRegenerateResult, error) {
					return regenerateDungeonOnLoop(cell.Stage, args.NetID), nil
				})
				if err != nil {
					return nil, err
				}
				if res.Found {
					return res, nil
				}
			}
			return DungeonRegenerateResult{Found: false}, nil
		},
	})
}

// regenerateDungeonOnLoop tears down a dungeon's chamber state, wall
// colliders, and dungeon marker entity, then re-procgens at the same
// position with a fresh time-based seed. Wall netIDs are tracked at
// spawn time (see GameWorld.dungeonWalls) so the spatial grid doesn't
// accumulate orphan colliders across regenerates.
func regenerateDungeonOnLoop(stage *mmokit.Stage, netID uint32) DungeonRegenerateResult {
	gw := mmokit.State[game.GameWorld](stage)
	if gw == nil {
		return DungeonRegenerateResult{}
	}
	chambers, ok := gw.DungeonChambers()[netID]
	if !ok {
		return DungeonRegenerateResult{}
	}
	e := mmokit.EntityByNetID(stage, netID)
	if !e.Alive() {
		return DungeonRegenerateResult{}
	}
	pos := mmokit.Get[mmokit.Position](e)
	if pos == nil {
		return DungeonRegenerateResult{Found: true, OldNetID: netID}
	}
	centerX, centerY := pos.X, pos.Y

	// Despawn all live roster NPCs across every chamber.
	for _, c := range chambers {
		for _, id := range c.AliveNetIDs {
			re := mmokit.EntityByNetID(stage, id)
			if !re.Alive() {
				continue
			}
			mmokit.Despawn(re)
		}
	}
	// Despawn the dungeon's wall colliders so they don't accumulate
	// across regenerates (each procgen run spawns dozens; without this
	// cleanup the spatial grid bloats and AoI/collision queries slow).
	for _, wallNetID := range gw.DungeonWallNetIDs(netID) {
		we := mmokit.EntityByNetID(stage, wallNetID)
		if !we.Alive() {
			continue
		}
		mmokit.Despawn(we)
	}
	// Drop the chamber tracking + NavGrid + wall-netID entries for the
	// old dungeon.
	gw.RemoveDungeon(netID)
	// Despawn the dungeon marker entity.
	mmokit.Despawn(e)

	seed := uint64(time.Now().UnixNano())
	newNetID := gw.SpawnDungeonFromGraph(centerX, centerY, seed)
	return DungeonRegenerateResult{
		Found:    true,
		OldNetID: netID,
		NewNetID: newNetID,
		Seed:     seed,
	}
}

// ── dungeon.spawn ────────────────────────────────────────────────────────────

// DungeonSpawnArgs targets a cell by its (CellX, CellY) coordinate. The
// handler resolves the cell, computes its center in world coordinates,
// and procgens a dungeon there using a seed derived from the cell.
type DungeonSpawnArgs struct {
	CellX int32 `cmd:"help=destination cell X coordinate"`
	CellY int32 `cmd:"help=destination cell Y coordinate"`
}

// DungeonSpawnResult reports the assigned netID, the resolved cell,
// and the seed used.
type DungeonSpawnResult struct {
	NetID  uint32
	CellID string
	Seed   uint64
}

func registerDungeonSpawn(reg *cmdsys.Registry, coord *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "dungeon.spawn",
		Capability:  "dungeon.spawn",
		Description: "spawn a procgen dungeon at the center of (CellX, CellY); test/debug only",
		Examples:    []string{"dungeon.spawn 1 0"},
		// RouteAllHosts: the destination cell could be on any host. Each
		// host checks if it owns the cell and spawns if so.
		Route:  cmdsys.RouteAllHosts,
		Args:   DungeonSpawnArgs{},
		Result: DungeonSpawnResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(DungeonSpawnArgs)
			// Resolve the cell via its center coordinates.
			cellSize := mmokit.CellSize()
			worldX := float32(args.CellX)*cellSize + cellSize/2
			worldY := float32(args.CellY)*cellSize + cellSize/2
			cellID := coord.CellAtPosition(worldX, worldY)
			if cellID == "" {
				return nil, fmt.Errorf("dungeon.spawn: no cell owns (%d,%d)", args.CellX, args.CellY)
			}
			cell := coord.CellByID(mmokit.MeshCellID(cellID))
			if cell == nil || cell.Stage == nil {
				// Not on this host — let another host's handler answer.
				return DungeonSpawnResult{}, nil
			}
			gw := gwForStage(coord, cell.Stage)
			if gw == nil {
				return nil, fmt.Errorf("dungeon.spawn: not a game-world cell")
			}
			// Cell-local center.
			localX := cellSize / 2
			localY := cellSize / 2
			seed := game.DungeonSeed(args.CellX, args.CellY)
			return mmokit.CmdOnLoop(ctx, cell.Engine, func() (DungeonSpawnResult, error) {
				// Prevent double-spawn: if any dungeon already exists in
				// this cell's chamber map, refuse rather than silently
				// stacking layouts on top of one another.
				if len(gw.DungeonChambers()) > 0 {
					return DungeonSpawnResult{}, fmt.Errorf("dungeon.spawn: cell %q already has a dungeon (use dungeon.regenerate to replace)", cellID)
				}
				netID := gw.SpawnDungeonFromGraph(localX, localY, seed)
				return DungeonSpawnResult{
					NetID:  netID,
					CellID: cellID,
					Seed:   seed,
				}, nil
			})
		},
	})
}
