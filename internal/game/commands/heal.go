package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type HealArgs struct {
	Username string `cmd:"help=target username"`
}

type HealResult struct {
	Target string
	OK     bool
}

func registerHeal(reg *cmdsys.Registry, resolver *Resolver) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.heal",
		Capability:  "player.heal",
		Description: "restore full HP and shield on a player's entity",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        HealArgs{},
		Result:      HealResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(HealArgs)
			username := strings.ToLower(args.Username)
			gw := resolver.GameWorldForUser(username)
			if gw == nil {
				return nil, fmt.Errorf("player %q not found on this host", username)
			}
			var result HealResult
			_, err := ExecOnLoop(gw, func() (any, error) {
				sess := gw.Players.ByUsername(username)
				if sess == nil {
					return nil, fmt.Errorf("player %q session not found", username)
				}
				entity := sess.Entity
				if gw.C.Health.HasAll(entity) {
					h := gw.C.Health.Get(entity)
					h.Current = h.Max
				}
				if gw.C.Shield.HasAll(entity) {
					s := gw.C.Shield.Get(entity)
					s.Current = s.Max
				}
				result = HealResult{Target: username, OK: true}
				return result, nil
			})
			if err != nil {
				return nil, err
			}
			return result, nil
		},
	})
}
