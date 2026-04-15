package commands

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"

	"github.com/mlange-42/ark/ecs"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type SpawnNPCsArgs struct {
	Username string `cmd:"help=player to spawn NPCs around"`
	Count    int32  `cmd:"help=number of NPCs to spawn"`
	Move     string `cmd:"optional,help=pass --move to give NPCs wander behavior"`
}

type SpawnNPCsResult struct {
	Target  string
	Spawned int32
	Radius  float32
}

func registerSpawnNPCs(reg *cmdsys.Registry, resolver *Resolver) error {
	return reg.Register(cmdsys.Command{
		Verb:        "debug.spawn_npcs",
		Capability:  "debug.spawn_npcs",
		Description: "spawn N NPCs around a player for load testing",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        SpawnNPCsArgs{},
		Result:      SpawnNPCsResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(SpawnNPCsArgs)
			username := strings.ToLower(args.Username)
			count := args.Count
			if count < 1 {
				return nil, fmt.Errorf("count must be >= 1")
			}
			move := strings.Contains(strings.ToLower(args.Move), "move")
			gw := resolver.GameWorldForUser(username)
			if gw == nil {
				return nil, fmt.Errorf("player %q not found on this host", username)
			}
			var result SpawnNPCsResult
			_, err := ExecOnLoop(gw, func() (any, error) {
				sess := gw.Players.ByUsername(username)
				if sess == nil {
					return nil, fmt.Errorf("player %q session not found", username)
				}
				entity := sess.Entity
				if !gw.C.Position.HasAll(entity) {
					return nil, fmt.Errorf("player has no position")
				}
				pos := gw.C.Position.Get(entity)
				wanderMap := ecs.NewMap1[gamecomp.Wander](gw.Engine().ECS)
				radius := gw.Config.AoIRadius * 0.8
				for range count {
					angle := rand.Float64() * 2 * math.Pi
					dist := float32(math.Sqrt(rand.Float64())) * radius
					nx := pos.X + dist*float32(math.Cos(angle))
					ny := pos.Y + dist*float32(math.Sin(angle))
					npcEntity := gw.SpawnNPC(nx, ny)
					if move {
						initAngle := rand.Float32() * 2 * math.Pi
						wanderMap.Add(npcEntity, &gamecomp.Wander{
							Speed:       8,
							Timer:       rand.Float32() * 2,
							Interval:    3,
							TargetAngle: initAngle,
							TurnRate:    1.5,
						})
					}
				}
				result = SpawnNPCsResult{
					Target:  username,
					Spawned: count,
					Radius:  radius,
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
