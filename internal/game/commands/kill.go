package commands

import (
	"context"
	"fmt"
	"strings"

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
			gw := gwForStage(coord, target.Stage)
			if gw == nil {
				return nil, fmt.Errorf("player.kill: not a game-world cell")
			}
			return mmokit.CmdOnLoop(ctx, target.Stage.Engine(), func() (KillResult, error) {
				gw.MarkPlayerDeath(target.Online.Entity, 0)
				return KillResult{Target: username, OK: true}, nil
			})
		},
	})
}
