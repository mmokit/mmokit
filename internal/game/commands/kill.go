package commands

import (
	"context"
	"fmt"
	"strings"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type KillArgs struct {
	Username string `cmd:"help=target username,complete=players"`
}

type KillResult struct {
	Target string
	OK     bool
}

func registerKill(reg *cmdsys.Registry, coord *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.kill",
		Capability:  "player.kill",
		Description: "instantly kill a player's entity (online only)",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        KillArgs{},
		Result:      KillResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(KillArgs)
			username := strings.ToLower(args.Username)
			target := mmokit.ResolvePlayerTarget(env, username)
			if target.Online == nil || target.Stage == nil {
				return nil, fmt.Errorf("player %q not online on this host", username)
			}
			if gwForStage(coord, target.Stage) == nil {
				return nil, fmt.Errorf("player.kill: not a game-world cell")
			}
			return mmokit.CmdOnLoop(ctx, target.Stage.Engine(), func() (KillResult, error) {
				// /kill: zero the player's Health; the death observer will
				// fire Killed next tick and run the cleanup path.
				e := mmokit.EntityFromECS(target.Stage, target.Online.Entity)
				if !e.Alive() {
					return KillResult{Target: username, OK: false}, fmt.Errorf("player.kill: target entity not alive")
				}
				h := mmokit.Get[gamecomp.Health](e)
				if h == nil {
					return KillResult{Target: username, OK: false}, fmt.Errorf("player.kill: target has no Health")
				}
				h.Current = 0
				h.LastDamagedByNetID = 0 // admin-killed: unattributed
				return KillResult{Target: username, OK: true}, nil
			})
		},
	})
}
