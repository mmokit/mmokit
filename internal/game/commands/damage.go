package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type DamageArgs struct {
	Username string  `cmd:"help=target username or netID"`
	Amount   float32 `cmd:"help=damage amount"`
}

type DamageResult struct {
	Target string
	Dealt  float32
	HP     string
}

func registerDamage(reg *cmdsys.Registry, resolver *Resolver) error {
	return reg.Register(cmdsys.Command{
		Verb:        "entity.damage",
		Capability:  "entity.damage",
		Description: "deal damage to a player's entity",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        DamageArgs{},
		Result:      DamageResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(DamageArgs)
			username := strings.ToLower(args.Username)
			gw := resolver.GameWorldForUser(username)
			if gw == nil {
				// Try entity by netID
				netID, err := strconv.ParseUint(username, 10, 32)
				if err != nil {
					return nil, fmt.Errorf("player %q not found on this host", username)
				}
				var result DamageResult
				var execErr error
				_, execErr = ExecOnLoop(gw, func() (any, error) {
					return nil, fmt.Errorf("entity %d: host routing not available in netID path", uint32(netID))
				})
				return result, execErr
			}
			var result DamageResult
			var execErr error
			_, execErr = ExecOnLoop(gw, func() (any, error) {
				sess := gw.Players.ByUsername(username)
				if sess == nil {
					return nil, fmt.Errorf("player %q session not found", username)
				}
				entity := sess.Entity
				if !gw.C.Health.HasAll(entity) {
					return nil, fmt.Errorf("entity has no health component")
				}
				dealt := gw.ApplyDamage(entity, args.Amount, 0)
				h := gw.C.Health.Get(entity)
				result = DamageResult{
					Target: username,
					Dealt:  dealt,
					HP:     fmt.Sprintf("%.0f/%.0f", h.Current, h.Max),
				}
				return result, nil
			})
			if execErr != nil {
				return nil, execErr
			}
			return result, nil
		},
	})
}
