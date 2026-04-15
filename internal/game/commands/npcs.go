package commands

import (
	"context"
	"fmt"

	"github.com/mlange-42/ark/ecs"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type NPCsArgs struct{}

type NPCRow struct {
	NetID    uint32
	Position string
	HP       string
	Shield   string
}

type NPCsResult struct {
	NPCs []NPCRow `cmd:"table"`
}

func registerNPCs(reg *cmdsys.Registry, resolver *Resolver) error {
	return reg.Register(cmdsys.Command{
		Verb:        "debug.npcs",
		Capability:  "debug.npcs",
		Description: "list all NPCs with net IDs across local cells",
		Route:       cmdsys.RouteLocal,
		Args:        NPCsArgs{},
		Result:      NPCsResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			worlds := resolver.AllLocalWorlds()
			if len(worlds) == 0 {
				return nil, fmt.Errorf("no local game worlds available")
			}
			var allRows []NPCRow
			for _, gw := range worlds {
				gwCopy := gw
				result, execErr := ExecOnLoop(gwCopy, func() (any, error) {
					filter := ecs.NewFilter3[mmokit.EntityKind, mmokit.NetworkID, mmokit.Position](gwCopy.Engine().ECS)
					query := filter.Query()
					var rows []NPCRow
					for query.Next() {
						kind, netID, pos := query.Get()
						if kind.Type != gamecomp.TypeNPC {
							continue
						}
						entity := query.Entity()
						var hp, shield string
						if gwCopy.C.Health.HasAll(entity) {
							h := gwCopy.C.Health.Get(entity)
							hp = fmt.Sprintf("%.0f/%.0f", h.Current, h.Max)
						}
						if gwCopy.C.Shield.HasAll(entity) {
							s := gwCopy.C.Shield.Get(entity)
							shield = fmt.Sprintf("%.0f/%.0f", s.Current, s.Max)
						}
						var posStr string
						if gwCopy.C.CellCoord.HasAll(entity) {
							sec := gwCopy.C.CellCoord.Get(entity)
							posStr = fmt.Sprintf("(%d,%d):(%.0f,%.0f)", sec.CellX, sec.CellY, pos.X, pos.Y)
						} else {
							posStr = fmt.Sprintf("%.0f,%.0f", pos.X, pos.Y)
						}
						rows = append(rows, NPCRow{
							NetID:    netID.ID,
							Position: posStr,
							HP:       hp,
							Shield:   shield,
						})
					}
					return rows, nil
				})
				if execErr != nil {
					return nil, execErr
				}
				if rows, ok := result.([]NPCRow); ok {
					allRows = append(allRows, rows...)
				}
			}
			return NPCsResult{NPCs: allRows}, nil
		},
	})
}
