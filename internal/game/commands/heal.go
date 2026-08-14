package commands

import (
	"context"
	"fmt"
	"strings"

	gamecomp "github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/mmokit"
)

type HealArgs struct {
	Username string `cmd:"help=target username,complete=players"`
}

type HealResult struct {
	Target string
	OK     bool
}

func registerHeal(reg *cmdsys.Registry, coord *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.heal",
		Capability:  "player.heal",
		Description: "restore full HP and shield on a player's entity (online only)",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        HealArgs{},
		Result:      HealResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(HealArgs)
			username := strings.ToLower(args.Username)
			target := mmokit.ResolvePlayerTarget(env, username)
			if target.Online == nil || target.Stage == nil {
				return nil, fmt.Errorf("player %q not online on this host", username)
			}
			if gw := gwForStage(coord, target.Stage); gw == nil {
				return nil, fmt.Errorf("player.heal: not a game-world cell")
			}
			return mmokit.CmdOnLoop(ctx, target.Stage.Engine(), func() (HealResult, error) {
				e := mmokit.EntityFromECS(target.Stage, target.Online.Entity)
				if h := mmokit.Get[gamecomp.Health](e); h != nil {
					h.Current = h.Max
				}
				if sh := mmokit.Get[gamecomp.Shield](e); sh != nil {
					sh.Current = sh.Max
				}
				return HealResult{Target: username, OK: true}, nil
			})
		},
	})
}
