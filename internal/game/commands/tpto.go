package commands

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type TpToArgs struct {
	Username string `cmd:"help=player to teleport"`
	Target   string `cmd:"help=destination username or net ID"`
}

type TpToResult struct {
	Source      string
	Destination string
	Position    string
}

func registerTpTo(reg *cmdsys.Registry, resolver *Resolver) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.tpto",
		Capability:  "player.tpto",
		Description: "teleport a player near another player or entity (by username or net ID)",
		// TODO C5: route via RouteEntityOwner after adding an entity-name resolver
		Route:  cmdsys.RoutePlayerOwner,
		Args:   TpToArgs{},
		Result: TpToResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(TpToArgs)
			username := strings.ToLower(args.Username)
			targetArg := args.Target
			gw := resolver.GameWorldForUser(username)
			if gw == nil {
				return nil, fmt.Errorf("player %q not found on this host", username)
			}
			var result TpToResult
			_, err := ExecOnLoop(gw, func() (any, error) {
				sess := gw.Players.ByUsername(username)
				if sess == nil {
					return nil, fmt.Errorf("player %q session not found", username)
				}
				srcEntity := sess.Entity

				// Resolve destination: try username first, then net ID
				var dstEntity ecs.Entity
				var ok bool
				if dstSess := gw.Players.ByUsername(strings.ToLower(targetArg)); dstSess != nil && gw.Engine().ECS.Alive(dstSess.Entity) {
					dstEntity = dstSess.Entity
					ok = true
				}
				if !ok {
					dstEntity, ok = resolveEntityByNetID(gw, targetArg)
				}
				if !ok {
					return nil, fmt.Errorf("destination %q not found on same node", targetArg)
				}
				if !gw.C.Position.HasAll(srcEntity) || !gw.C.Position.HasAll(dstEntity) {
					return nil, fmt.Errorf("entity missing position")
				}
				dstPos := gw.C.Position.Get(dstEntity)
				angle := rand.Float64() * 2 * math.Pi
				offsetDist := float32(150)
				nx := dstPos.X + offsetDist*float32(math.Cos(angle))
				ny := dstPos.Y + offsetDist*float32(math.Sin(angle))
				srcPos := gw.C.Position.Get(srcEntity)
				srcPos.X = nx
				srcPos.Y = ny
				if gw.C.CellCoord.HasAll(srcEntity) && gw.C.CellCoord.HasAll(dstEntity) {
					srcSec := gw.C.CellCoord.Get(srcEntity)
					dstSec := gw.C.CellCoord.Get(dstEntity)
					srcSec.CellX = dstSec.CellX
					srcSec.CellY = dstSec.CellY
				}
				if gw.C.Velocity.HasAll(srcEntity) {
					vel := gw.C.Velocity.Get(srcEntity)
					vel.X = 0
					vel.Y = 0
				}
				if gw.C.MoveTarget.HasAll(srcEntity) {
					gw.C.MoveTarget.Get(srcEntity).Active = false
				}
				var sx, sy int32
				if gw.C.CellCoord.HasAll(srcEntity) {
					sec := gw.C.CellCoord.Get(srcEntity)
					sx, sy = sec.CellX, sec.CellY
				}
				result = TpToResult{
					Source:      username,
					Destination: targetArg,
					Position:    fmt.Sprintf("(%d,%d):(%.0f,%.0f)", sx, sy, nx, ny),
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

// resolveEntityByNetID finds any entity by network ID string.
func resolveEntityByNetID(gw *game.GameWorld, input string) (ecs.Entity, bool) {
	id, err := strconv.ParseUint(input, 10, 32)
	if err != nil {
		return ecs.Entity{}, false
	}
	netID := uint32(id)
	if entity, exists := gw.NetIDToEntity[netID]; exists && gw.Engine().ECS.Alive(entity) {
		return entity, true
	}
	return ecs.Entity{}, false
}
