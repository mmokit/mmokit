package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type KillArgs struct {
	Username string `cmd:"help=target username"`
}

type KillResult struct {
	Target string
	OK     bool
}

func registerKill(reg *cmdsys.Registry, resolver *Resolver) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.kill",
		Capability:  "player.kill",
		Description: "instantly kill a player's entity",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        KillArgs{},
		Result:      KillResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(KillArgs)
			username := strings.ToLower(args.Username)
			gw := resolver.GameWorldForUser(username)
			if gw == nil {
				return nil, fmt.Errorf("player %q not found on this host", username)
			}
			var result KillResult
			_, err := ExecOnLoop(gw, func() (any, error) {
				sess := gw.Players.ByUsername(username)
				if sess == nil {
					return nil, fmt.Errorf("player %q session not found", username)
				}
				gw.MarkPlayerDeath(sess.Entity, 0)
				result = KillResult{Target: username, OK: true}
				return result, nil
			})
			if err != nil {
				return nil, err
			}
			return result, nil
		},
	})
}
