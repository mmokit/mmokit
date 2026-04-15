package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type TpArgs struct {
	Username string  `cmd:"help=target username"`
	X        float32 `cmd:"help=local X coordinate"`
	Y        float32 `cmd:"help=local Y coordinate"`
}

type TpResult struct {
	Target   string
	Position string
}

func registerTp(reg *cmdsys.Registry, resolver *Resolver) error {
	return reg.Register(cmdsys.Command{
		Verb:        "entity.tp",
		Capability:  "entity.tp",
		Description: "teleport a player to local cell coordinates",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        TpArgs{},
		Result:      TpResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(TpArgs)
			username := strings.ToLower(args.Username)
			gw := resolver.GameWorldForUser(username)
			if gw == nil {
				return nil, fmt.Errorf("player %q not found on this host", username)
			}
			var result TpResult
			_, err := ExecOnLoop(gw, func() (any, error) {
				sess := gw.Players.ByUsername(username)
				if sess == nil {
					return nil, fmt.Errorf("player %q session not found", username)
				}
				entity := sess.Entity
				if !gw.C.Position.HasAll(entity) {
					return nil, fmt.Errorf("entity has no position")
				}
				pos := gw.C.Position.Get(entity)
				pos.X = args.X
				pos.Y = args.Y
				if gw.C.Velocity.HasAll(entity) {
					vel := gw.C.Velocity.Get(entity)
					vel.X = 0
					vel.Y = 0
				}
				if gw.C.MoveTarget.HasAll(entity) {
					gw.C.MoveTarget.Get(entity).Active = false
				}
				var sx, sy int32
				if gw.C.CellCoord.HasAll(entity) {
					sec := gw.C.CellCoord.Get(entity)
					sx, sy = sec.CellX, sec.CellY
				}
				result = TpResult{
					Target:   username,
					Position: fmt.Sprintf("(%d,%d):(%.0f,%.0f)", sx, sy, args.X, args.Y),
				}
				return result, nil
			})
			if err != nil {
				return nil, err
			}
			return result, nil
		},
	})
}
