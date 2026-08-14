package commands

import (
	"context"
	"fmt"
	"strings"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/mmokit"
)

type DamageArgs struct {
	Username string  `cmd:"help=target username,complete=players"`
	Amount   float32 `cmd:"help=damage amount"`
}

type DamageResult struct {
	Target string
	Dealt  float32
	HP     string
}

func registerDamage(reg *cmdsys.Registry, coord *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.damage",
		Capability:  "player.damage",
		Description: "deal damage to a player's entity (online only)",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        DamageArgs{},
		Result:      DamageResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(DamageArgs)
			username := strings.ToLower(args.Username)
			target := mmokit.ResolvePlayerTarget(env, username)
			if target.Online == nil || target.Stage == nil {
				return nil, fmt.Errorf("player %q not online on this host", username)
			}
			gw := gwForStage(coord, target.Stage)
			if gw == nil {
				return nil, fmt.Errorf("player.damage: not a game-world cell")
			}
			return mmokit.CmdOnLoop(ctx, target.Stage.Engine(), func() (DamageResult, error) {
				e := mmokit.EntityFromECS(target.Stage, target.Online.Entity)
				h := mmokit.Get[gamecomp.Health](e)
				if h == nil {
					return DamageResult{}, fmt.Errorf("entity has no health")
				}
				dealt := gw.ApplyDamage(e, args.Amount, 0)
				return DamageResult{
					Target: username,
					Dealt:  dealt,
					HP:     fmt.Sprintf("%.0f/%.0f", h.Current, h.Max),
				}, nil
			})
		},
	})
}
