package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type KickArgs struct {
	Username string `cmd:"help=target username,complete=players"`
}

type KickResult struct {
	Target string
	ConnID uint32
	Note   string
}

func registerKick(reg *cmdsys.Registry, resolver *Resolver) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.kick",
		Capability:  "player.kick",
		Description: "force disconnect a player",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        KickArgs{},
		Result:      KickResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(KickArgs)
			username := strings.ToLower(args.Username)
			gw := resolver.GameWorldForUser(username)
			if gw == nil {
				return nil, fmt.Errorf("player %q not found on this host", username)
			}
			var result KickResult
			_, err := ExecOnLoop(gw, func() (any, error) {
				sess := gw.Players.ByUsername(username)
				if sess == nil {
					return nil, fmt.Errorf("player %q session not found", username)
				}
				connID := sess.ConnID
				gw.Players.Remove(sess)
				if remover, ok := gw.Engine().ConnMgr.(interface{ Remove(uint32) }); ok {
					remover.Remove(connID)
					result = KickResult{Target: username, ConnID: connID}
				} else {
					result = KickResult{Target: username, ConnID: connID,
						Note: "session removed locally (virtual conn manager — socket not closed)"}
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
