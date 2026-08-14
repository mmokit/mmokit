package universe

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"

	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/coords"
)

// ── player.tp ───────────────────────────────────────────────────────────────

type playerTpArgs struct {
	Username string  `cmd:"help=target username,complete=players"`
	X        float32 `cmd:"help=destination world X"`
	Y        float32 `cmd:"help=destination world Y"`
}

type playerTpResult struct {
	Username   string
	Status     string // "online" or "offline"
	PrevWorldX float32
	PrevWorldY float32
	NewWorldX  float32
	NewWorldY  float32
}

// ── player.info ──────────────────────────────────────────────────────────────

type playerInfoArgs struct {
	Username string `cmd:"help=username,complete=players"`
}

type playerInfoResult struct {
	Username string
	Status   string // "online" or "offline"
	HostID   string
	CellID   string
	WorldX   float32
	WorldY   float32
}

// ── player.tpto ──────────────────────────────────────────────────────────────

type playerTptoArgs struct {
	Username string `cmd:"help=player to teleport,complete=players"`
	Target   string `cmd:"help=destination username,complete=players"`
}

type playerTptoResult struct {
	Username  string
	Target    string
	NewWorldX float32
	NewWorldY float32
}

// ── player.list ──────────────────────────────────────────────────────────────

type playerListArgs struct {
	All bool `cmd:"named-only,optional,help=include offline players (requires DB)"`
}

type playerListRow struct {
	Username string
	Status   string
	HostID   string
	CellID   string
}

type playerListResult struct {
	Players []playerListRow `cmd:"table"`
}

// ── player.kick ──────────────────────────────────────────────────────────────

type playerKickArgs struct {
	Username string `cmd:"help=target username,complete=players"`
}

type playerKickResult struct {
	Username string
	ConnID   uint32
}

// ── registerPlayerCommands ───────────────────────────────────────────────────

// registerPlayerCommands registers player.tp, player.info, player.tpto,
// player.list, player.kick, and the hidden player.list_offline verb on
// coord.registry. Called from Process.registerAllBuiltins.
func registerPlayerCommands(coord *Process) error {
	reg := coord.registry
	if err := reg.Register(cmdsys.Command{
		Verb:        "player.tp",
		Capability:  "player.tp",
		Description: "teleport a player to a world position (online or offline)",
		Route:       cmdsys.RoutePlayerHomeOrOwner,
		Args:        playerTpArgs{},
		Result:      playerTpResult{},
		Handler:     playerTpHandler(coord),
	}); err != nil {
		return fmt.Errorf("player.tp: %w", err)
	}
	if err := reg.Register(cmdsys.Command{
		Verb:        "player.info",
		Capability:  "player.info",
		Description: "show live or persisted info for a player",
		Route:       cmdsys.RoutePlayerHomeOrOwner,
		Args:        playerInfoArgs{},
		Result:      playerInfoResult{},
		Handler:     playerInfoHandler(coord),
	}); err != nil {
		return fmt.Errorf("player.info: %w", err)
	}
	if err := reg.Register(cmdsys.Command{
		Verb:        "player.tpto",
		Capability:  "player.tpto",
		Description: "teleport a player to another player's position",
		Route:       cmdsys.RoutePlayerHomeOrOwner,
		Args:        playerTptoArgs{},
		Result:      playerTptoResult{},
		Handler:     playerTptoHandler(coord),
	}); err != nil {
		return fmt.Errorf("player.tpto: %w", err)
	}
	if err := reg.Register(cmdsys.Command{
		Verb:        "player.list",
		Capability:  "player.list",
		Description: "list online players; --all includes offline (requires DB)",
		Route:       cmdsys.RouteCoordinator,
		Args:        playerListArgs{},
		Result:      playerListResult{},
		Handler:     playerListHandler(coord),
	}); err != nil {
		return fmt.Errorf("player.list: %w", err)
	}
	if err := reg.Register(cmdsys.Command{
		Verb:        "player.kick",
		Capability:  "player.kick",
		Description: "internal: drop a live WS session on the owning host (use auth.user.kick)",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        playerKickArgs{},
		Result:      playerKickResult{},
		Hidden:      true,
		Handler:     playerKickHandler(coord),
	}); err != nil {
		return fmt.Errorf("player.kick: %w", err)
	}
	// Hidden internal verb used by player.list --all fan-out. RouteSpecificHost
	// so the coordinator can dispatch the offline-list query to the DB-bearing
	// host (which holds the PlayerDataLocator). In single-process `all` mode
	// the coordinator IS the DB host, so the dispatch loops back locally.
	if err := reg.Register(cmdsys.Command{
		Verb:        "player.list_offline",
		Capability:  "player.list_offline",
		Description: "enumerate persisted players from the target host's PlayerDataLocator (internal)",
		Route:       cmdsys.RouteSpecificHost,
		Args:        playerListOfflineArgs{},
		Result:      playerListResult{},
		Hidden:      true,
		Handler:     playerListOfflineHandler(coord),
	}); err != nil {
		return fmt.Errorf("player.list_offline: %w", err)
	}
	return nil
}

// playerListOfflineArgs carries the HostID for RouteSpecificHost
// dispatch. The hidden player.list_offline verb is fanned out from
// player.list --all on the coordinator; HostID is the lex-first
// DB-bearing host returned by PickDBHost.
type playerListOfflineArgs struct {
	HostID string `cmd:"help=target host with the PlayerDataLocator"`
}

// DisconnectActiveUser closes any live WebSocket session for username by
// dispatching the hidden player.kick verb to the host owning the player. No-op
// (returns 0, nil) when the user is not currently online. Used by admin paths
// like auth.user.kick to combine token revocation with a real WS disconnect —
// neither alone is a complete boot.
func (c *Process) DisconnectActiveUser(ctx context.Context, env *cmdsys.Env, username string) (int, error) {
	if c.ActiveUserHost(username) == "" {
		return 0, nil
	}
	res, err := c.dispatcher.InvokeInternal(ctx, env, "player.kick", playerKickArgs{Username: username})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, tr := range res.PerTarget {
		if tr.OK {
			n++
		}
	}
	return n, nil
}

// ── handlers ─────────────────────────────────────────────────────────────────

func playerTpHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(playerTpArgs)
		args.Username = strings.ToLower(args.Username)

		target := ResolvePlayerTarget(env, args.Username)

		if target.Online != nil && target.Stage != nil {
			result, err := runOnCell(ctx, findCellForStage(coord, target.Stage), func() (playerTpResult, error) {
				cid := target.Stage.Cell()
				var prevWorldX, prevWorldY float32
				posMap := target.Stage.PositionMap()
				if posMap.HasAll(target.Online.Entity) {
					pos := posMap.Get(target.Online.Entity)
					prevWorldX, prevWorldY = localToWorld(cid.X, cid.Y, pos.X, pos.Y)
				}
				if err := target.Stage.MoveEntityTo(
					target.Online.Entity, args.X, args.Y,
					MoveBypassCooldown(),
					MoveAsPlayer(target.Online.ConnID, target.Online.Username),
				); err != nil {
					return playerTpResult{}, fmt.Errorf("player.tp: %w", err)
				}
				return playerTpResult{
					Username:   args.Username,
					Status:     "online",
					PrevWorldX: prevWorldX,
					PrevWorldY: prevWorldY,
					NewWorldX:  args.X,
					NewWorldY:  args.Y,
				}, nil
			})
			return result, err
		}

		if target.Offline != nil {
			cellX, cellY, localX, localY := worldToLocal(args.X, args.Y)
			prevWorldX := float32(target.Offline.GetCellX())*coords.CellSize + target.Offline.GetX()
			prevWorldY := float32(target.Offline.GetCellY())*coords.CellSize + target.Offline.GetY()
			target.Offline.SetCell(cellX, cellY)
			target.Offline.SetPosition(localX, localY)
			target.DirtyMark()
			return playerTpResult{
				Username:   args.Username,
				Status:     "offline",
				PrevWorldX: prevWorldX,
				PrevWorldY: prevWorldY,
				NewWorldX:  args.X,
				NewWorldY:  args.Y,
			}, nil
		}

		return nil, fmt.Errorf("player %q not found", args.Username)
	}
}

func playerInfoHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(playerInfoArgs)
		args.Username = strings.ToLower(args.Username)

		target := ResolvePlayerTarget(env, args.Username)

		if target.Online != nil && target.Stage != nil {
			cell := findCellForStage(coord, target.Stage)
			result, err := runOnCell(ctx, cell, func() (playerInfoResult, error) {
				cid := target.Stage.Cell()
				var worldX, worldY float32
				posMap := target.Stage.PositionMap()
				if posMap.HasAll(target.Online.Entity) {
					pos := posMap.Get(target.Online.Entity)
					worldX, worldY = localToWorld(cid.X, cid.Y, pos.X, pos.Y)
				}
				hostID := localHostIDFor(coord, cell)
				return playerInfoResult{
					Username: args.Username,
					Status:   "online",
					HostID:   hostID,
					CellID:   string(target.Stage.CellID()),
					WorldX:   worldX,
					WorldY:   worldY,
				}, nil
			})
			return result, err
		}

		if target.Offline != nil {
			worldX := float32(target.Offline.GetCellX())*coords.CellSize + target.Offline.GetX()
			worldY := float32(target.Offline.GetCellY())*coords.CellSize + target.Offline.GetY()
			return playerInfoResult{
				Username: args.Username,
				Status:   "offline",
				WorldX:   worldX,
				WorldY:   worldY,
			}, nil
		}

		return nil, fmt.Errorf("player %q not found", args.Username)
	}
}

func playerTptoHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(playerTptoArgs)
		args.Username = strings.ToLower(args.Username)
		args.Target = strings.ToLower(args.Target)

		infoRes, err := coord.dispatcher.InvokeInternal(ctx, env, "player.info", playerInfoArgs{Username: args.Target})
		if err != nil {
			return nil, fmt.Errorf("player.tpto: resolving target %q: %w", args.Target, err)
		}

		var pi playerInfoResult
		for _, tr := range infoRes.PerTarget {
			if tr.OK {
				if r, ok := tr.Result.(playerInfoResult); ok {
					pi = r
					break
				}
			}
		}
		if pi.WorldX == 0 && pi.WorldY == 0 {
			return nil, fmt.Errorf("player.tpto: target %q has no resolvable position", args.Target)
		}

		const offsetDist = 150.0
		angle := rand.Float64() * 2 * math.Pi
		nx := pi.WorldX + float32(math.Cos(angle)*offsetDist)
		ny := pi.WorldY + float32(math.Sin(angle)*offsetDist)

		_, err = coord.dispatcher.InvokeInternal(ctx, env, "player.tp", playerTpArgs{
			Username: args.Username,
			X:        nx,
			Y:        ny,
		})
		if err != nil {
			return nil, fmt.Errorf("player.tpto: tp %q: %w", args.Username, err)
		}

		return playerTptoResult{
			Username:  args.Username,
			Target:    args.Target,
			NewWorldX: nx,
			NewWorldY: ny,
		}, nil
	}
}

func playerListHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(playerListArgs)

		online := coord.ActiveUsers()
		rows := make(map[string]playerListRow, len(online))
		for username, hostID := range online {
			rows[username] = playerListRow{
				Username: username,
				Status:   "online",
				HostID:   hostID,
			}
		}

		if args.All {
			dbHost := coord.PickDBHost()
			if dbHost == "" {
				return nil, fmt.Errorf("player.list --all requires a DB-bearing host (none available)")
			}
			offlineRes, err := coord.dispatcher.InvokeInternal(ctx, env, "player.list_offline",
				playerListOfflineArgs{HostID: dbHost})
			if err != nil {
				return nil, fmt.Errorf("player.list --all: %w", err)
			}
			for _, tr := range offlineRes.PerTarget {
				if !tr.OK {
					continue
				}
				r, ok := tr.Result.(playerListResult)
				if !ok {
					continue
				}
				for _, row := range r.Players {
					if _, alreadyOnline := rows[row.Username]; !alreadyOnline {
						rows[row.Username] = row
					}
				}
			}
		}

		result := make([]playerListRow, 0, len(rows))
		for _, row := range rows {
			result = append(result, row)
		}
		return playerListResult{Players: result}, nil
	}
}

func playerListOfflineHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		coord.mu.RLock()
		loc := coord.playerDataLocator
		coord.mu.RUnlock()

		if loc == nil {
			return playerListResult{}, nil
		}

		all := loc.ListOffline()
		rows := make([]playerListRow, 0, len(all))
		for _, pda := range all {
			rows = append(rows, playerListRow{
				Username: pda.GetUsername(),
				Status:   "offline",
			})
		}
		return playerListResult{Players: rows}, nil
	}
}

func playerKickHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(playerKickArgs)
		args.Username = strings.ToLower(args.Username)

		target := ResolvePlayerTarget(env, args.Username)
		if target.Online == nil {
			return nil, fmt.Errorf("player %q is not online on this host", args.Username)
		}

		cell := findCellForStage(coord, target.Stage)
		if cell == nil {
			return nil, fmt.Errorf("player.kick: no cell found for player %q", args.Username)
		}

		connID := target.Online.ConnID
		sess := target.Online

		result, err := runOnCell(ctx, cell, func() (playerKickResult, error) {
			cell.Engine.Players.Remove(sess)
			return playerKickResult{
				Username: args.Username,
				ConnID:   connID,
			}, nil
		})
		if err != nil {
			return nil, err
		}
		// Close the underlying connection after the session is removed from
		// the game loop. coord.ConnMgr is the real *net.ConnManager which
		// exposes Remove; the engine-side ConnSender interface does not.
		if connID != 0 && coord.ConnMgr != nil {
			coord.ConnMgr.Remove(connID)
		}
		return result, nil
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// findCellForStage finds the *Cell in coord that contains the given stage.
// Returns nil if not found.
func findCellForStage(coord *Process, stage *Stage) *Cell {
	coord.mu.RLock()
	defer coord.mu.RUnlock()
	for _, c := range coord.Cells {
		if c.Stage == stage {
			return c
		}
	}
	return nil
}
