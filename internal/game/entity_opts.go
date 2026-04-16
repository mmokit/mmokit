package game

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// BuildEntityOpts creates EntityOpts callbacks that query the game's ECS world.
// The returned callbacks are invoked from console handlers via engine.RunOnLoop, so
// they run on the ECS goroutine and may access the world safely.
func BuildEntityOpts(gw *GameWorld) *engine.EntityOpts {
	return &engine.EntityOpts{
		Summary: func() map[string]int {
			counts := make(map[string]int)
			filter := ecs.NewFilter1[mmokit.EntityKind](gw.Engine().ECS)
			query := filter.Query()
			for query.Next() {
				kind := query.Get()
				if def := gw.Registry.ByType(kind.Type); def != nil {
					counts[def.Name]++
				} else {
					counts["unknown"]++
				}
			}
			return counts
		},
		List: func(typeName string) []engine.EntityInfo {
			filter := ecs.NewFilter3[mmokit.EntityKind, mmokit.NetworkID, mmokit.Position](gw.Engine().ECS)
			query := filter.Query()
			var result []engine.EntityInfo
			for query.Next() {
				kind, nid, pos := query.Get()
				def := gw.Registry.ByType(kind.Type)
				if def == nil {
					continue
				}
				if typeName != "" && def.Name != typeName {
					continue
				}
				info := engine.EntityInfo{
					NetID:  nid.ID,
					NodeID: gw.NodeID(),
					Type:   def.Name,
					X:      pos.X,
					Y:      pos.Y,
				}
				entity := query.Entity()
				if gw.C.CellCoord.HasAll(entity) {
					cell := gw.C.CellCoord.Get(entity)
					info.CellSX = cell.CellX
					info.CellSY = cell.CellY
				}
				result = append(result, info)
			}
			return result
		},
		Get: func(netID uint32) (engine.EntityInfo, bool) {
			entity, ok := gw.NetIDToEntity[netID]
			if !ok || !gw.Engine().ECS.Alive(entity) {
				return engine.EntityInfo{}, false
			}
			info := engine.EntityInfo{NetID: netID, NodeID: gw.NodeID()}
			if gw.C.EntityKind.HasAll(entity) {
				kind := gw.C.EntityKind.Get(entity)
				if def := gw.Registry.ByType(kind.Type); def != nil {
					info.Type = def.Name
				}
			}
			if gw.C.Position.HasAll(entity) {
				pos := gw.C.Position.Get(entity)
				info.X = pos.X
				info.Y = pos.Y
			}
			if gw.C.Velocity.HasAll(entity) {
				vel := gw.C.Velocity.Get(entity)
				info.VX = vel.X
				info.VY = vel.Y
			}
			if gw.C.CellCoord.HasAll(entity) {
				cell := gw.C.CellCoord.Get(entity)
				info.CellSX = cell.CellX
				info.CellSY = cell.CellY
			}
			return info, true
		},
		Remove: func(netID uint32) bool {
			if entity, ok := gw.NetIDToEntity[netID]; ok && gw.Engine().ECS.Alive(entity) {
				gw.Engine().MarkForRemoval(entity)
				return true
			}
			return false
		},
	}
}
