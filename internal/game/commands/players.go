package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type PlayersArgs struct {
	Username string `cmd:"optional,help=single username to filter,complete=players"`
	All      bool   `cmd:"optional,name=all,help=include offline players (requires host/node pane)"`
}

type PlayerRow struct {
	Username  string
	Status    string
	Node      string
	Currency  int64
	Position  string
	LastLogin string
}

type PlayersResult struct {
	Players []PlayerRow `cmd:"table"`
}

func registerPlayers(reg *cmdsys.Registry, resolver *Resolver, playerDB *game.PlayerRepo, coord *mmokit.Coordinator, cfg **game.GameConfig) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.list",
		Capability:  "player.list",
		Description: "list players or show details (--all includes offline)",
		Route:       cmdsys.RouteLocal,
		Args:        PlayersArgs{},
		Result:      PlayersResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(PlayersArgs)
			showAll := args.All
			targetUser := strings.ToLower(strings.TrimSpace(args.Username))

			var curID uint32
			if cfg != nil && *cfg != nil {
				curID = (*cfg).SettlementCurrencyID
			}

			// On pure-coordinator (no playerDB), surface what we CAN know from
			// coord-local state: online users + their owning host. Columns
			// that need DB access (Currency / Position / LastLogin) render
			// blank. --all and single-user lookup still need the DB and are
			// rejected with a hint.
			if playerDB == nil {
				if targetUser != "" {
					return nil, fmt.Errorf("player detail unavailable on coordinator; run from a host/node pane")
				}
				if showAll {
					return nil, fmt.Errorf("--all requires the player DB; run from a host/node pane")
				}
				active := coord.ActiveUsers()
				rows := make([]PlayerRow, 0, len(active))
				for username, nodeID := range active {
					rows = append(rows, PlayerRow{
						Username: username,
						Status:   "online",
						Node:     nodeID,
					})
				}
				return PlayersResult{Players: rows}, nil
			}

			if targetUser != "" {
				pd := playerDB.Get(targetUser)
				if pd == nil {
					return nil, fmt.Errorf("player %q not found in DB", targetUser)
				}
				nodeID := coord.ActiveUserNode(targetUser)
				status := "offline"
				if nodeID != "" {
					status = fmt.Sprintf("online (%s)", nodeID)
				}
				bal := pd.GetCurrency(curID)
				lastLogin := pd.LastLogin.Format("2006-01-02 15:04")
				if pd.LastLogin.IsZero() {
					lastLogin = "never"
				}
				pos := fmt.Sprintf("(%d,%d):(%.0f,%.0f)", pd.CellX, pd.CellY, pd.X, pd.Y)
				row := PlayerRow{
					Username:  pd.Username,
					Status:    status,
					Node:      nodeID,
					Currency:  bal,
					Position:  pos,
					LastLogin: lastLogin,
				}
				return PlayersResult{Players: []PlayerRow{row}}, nil
			}

			active := coord.ActiveUsers()
			var rows []PlayerRow

			if showAll {
				for _, pd := range playerDB.All() {
					nodeID := active[pd.Username]
					status := "offline"
					if nodeID != "" {
						status = "online"
					}
					lastLogin := pd.LastLogin.Format("2006-01-02 15:04")
					if pd.LastLogin.IsZero() {
						lastLogin = "never"
					}
					rows = append(rows, PlayerRow{
						Username:  pd.Username,
						Status:    status,
						Node:      nodeID,
						Currency:  pd.GetCurrency(curID),
						Position:  fmt.Sprintf("(%d,%d):(%.0f,%.0f)", pd.CellX, pd.CellY, pd.X, pd.Y),
						LastLogin: lastLogin,
					})
				}
			} else {
				for username, nodeID := range active {
					pd := playerDB.Get(username)
					if pd == nil {
						continue
					}
					lastLogin := pd.LastLogin.Format("2006-01-02 15:04")
					if pd.LastLogin.IsZero() {
						lastLogin = "never"
					}
					rows = append(rows, PlayerRow{
						Username:  pd.Username,
						Status:    "online",
						Node:      nodeID,
						Currency:  pd.GetCurrency(curID),
						Position:  fmt.Sprintf("(%d,%d):(%.0f,%.0f)", pd.CellX, pd.CellY, pd.X, pd.Y),
						LastLogin: lastLogin,
					})
				}
			}
			return PlayersResult{Players: rows}, nil
		},
	})
}

type PlayerDetailArgs struct {
	Username string `cmd:"help=username,complete=players"`
}

type PlayerDetailResult struct {
	Username  string
	Status    string
	Created   string
	LastLogin string
	Position  string
	Cargo     string
	Bank      string
}

func registerPlayerDetail(reg *cmdsys.Registry, resolver *Resolver, playerDB *game.PlayerRepo, coord *mmokit.Coordinator) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.info",
		Capability:  "player.info",
		Description: "show detailed player info from DB",
		Route:       cmdsys.RouteLocal,
		Args:        PlayerDetailArgs{},
		Result:      PlayerDetailResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			if playerDB == nil {
				return nil, fmt.Errorf("player data unavailable on this role")
			}
			args := raw.(PlayerDetailArgs)
			pd := playerDB.Get(strings.ToLower(args.Username))
			if pd == nil {
				return nil, fmt.Errorf("player %q not found in DB", args.Username)
			}
			nodeID := coord.ActiveUserNode(args.Username)
			status := "offline"
			if nodeID != "" {
				status = fmt.Sprintf("online (%s)", nodeID)
			}
			var cargoParts, bankParts []string
			for id, qty := range pd.Cargo {
				if qty > 0 {
					def := item.Get(id)
					name := fmt.Sprintf("item#%d", id)
					if def != nil {
						name = def.Name
					}
					cargoParts = append(cargoParts, fmt.Sprintf("%s:%d", name, qty))
				}
			}
			for id, qty := range pd.Bank {
				if qty > 0 {
					def := item.Get(id)
					name := fmt.Sprintf("item#%d", id)
					if def != nil {
						name = def.Name
					}
					bankParts = append(bankParts, fmt.Sprintf("%s:%d", name, qty))
				}
			}
			lastLogin := pd.LastLogin.Format(time.RFC3339)
			if pd.LastLogin.IsZero() {
				lastLogin = "never"
			}
			return PlayerDetailResult{
				Username:  pd.Username,
				Status:    status,
				Created:   pd.CreatedAt.Format(time.RFC3339),
				LastLogin: lastLogin,
				Position:  fmt.Sprintf("(%d,%d):(%.0f,%.0f)", pd.CellX, pd.CellY, pd.X, pd.Y),
				Cargo:     strings.Join(cargoParts, " "),
				Bank:      strings.Join(bankParts, " "),
			}, nil
		},
	})
}
