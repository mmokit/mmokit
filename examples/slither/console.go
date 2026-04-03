package main

import (
	"math/rand"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// buildEntityRegistry creates the entity registry mapping kind IDs to names.
func buildEntityRegistry(gw *SlitherWorld) *mmokit.EntityRegistry {
	reg := mmokit.NewEntityRegistry()

	reg.Register(mmokit.EntityDef{
		Name:       "player",
		EntityType: KindPlayerSnake,
	})
	reg.Register(mmokit.EntityDef{
		Name:       "bot",
		EntityType: KindBotSnake,
		Spawnable:  true,
		Spawn: func(x, y float32) {
			gw.SpawnBotSnake(x, y)
		},
	})
	reg.Register(mmokit.EntityDef{
		Name:       "food",
		EntityType: KindNaturalFood,
		Spawnable:  true,
		Spawn: func(x, y float32) {
			gw.SpawnFood(x, y, 0.5+rand.Float32()*1.5, uint8(rand.Intn(8)))
		},
	})
	reg.Register(mmokit.EntityDef{
		Name:       "deathfood",
		EntityType: KindDeathFood,
		Spawnable:  true,
		Spawn: func(x, y float32) {
			gw.SpawnDeathFood(x, y, 1.0+rand.Float32()*2.0)
		},
	})

	return reg
}

// buildEntityOpts creates EntityOpts callbacks for the console entity commands.
func buildEntityOpts(gw *SlitherWorld, registry *mmokit.EntityRegistry) *mmokit.EntityOpts {
	return &mmokit.EntityOpts{
		Summary: func() map[string]int {
			counts := make(map[string]int)
			filter := ecs.NewFilter1[mmokit.EntityKind](gw.ECSWorld())
			query := filter.Query()
			for query.Next() {
				kind := query.Get()
				def := registry.ByType(kind.Type)
				if def != nil {
					counts[def.Name]++
				} else {
					counts["unknown"]++
				}
			}
			return counts
		},
		List: func(typeName string) []mmokit.EntityInfo {
			filter := ecs.NewFilter3[mmokit.EntityKind, mmokit.NetworkID, mmokit.Position](gw.ECSWorld())
			query := filter.Query()
			var result []mmokit.EntityInfo
			for query.Next() {
				kind, nid, pos := query.Get()
				def := registry.ByType(kind.Type)
				if def == nil {
					continue
				}
				if typeName != "" && def.Name != typeName {
					continue
				}
				entity := query.Entity()
				info := mmokit.EntityInfo{
					NetID:  nid.ID,
					NodeID: gw.NodeID(),
					Type:   def.Name,
					X:      pos.X,
					Y:      pos.Y,
				}
				if gw.CellCoordMap().HasAll(entity) {
					cell := gw.CellCoordMap().Get(entity)
					info.CellSX = cell.CellX
					info.CellSY = cell.CellY
				}
				result = append(result, info)
			}
			return result
		},
		Get: func(netID uint32) (mmokit.EntityInfo, bool) {
			// Look up entity by netID via filter scan
			filter := ecs.NewFilter3[mmokit.NetworkID, mmokit.EntityKind, mmokit.Position](gw.ECSWorld())
			query := filter.Query()
			for query.Next() {
				nid, kind, pos := query.Get()
				if nid.ID != netID {
					continue
				}
				entity := query.Entity()
				query.Close()
				info := mmokit.EntityInfo{
					NetID:  netID,
					NodeID: gw.NodeID(),
					X:      pos.X,
					Y:      pos.Y,
				}
				if def := registry.ByType(kind.Type); def != nil {
					info.Type = def.Name
				}
				if gw.VelocityMap().HasAll(entity) {
					vel := gw.VelocityMap().Get(entity)
					info.VX = vel.X
					info.VY = vel.Y
				}
				if gw.CellCoordMap().HasAll(entity) {
					cell := gw.CellCoordMap().Get(entity)
					info.CellSX = cell.CellX
					info.CellSY = cell.CellY
				}
				return info, true
			}
			return mmokit.EntityInfo{}, false
		},
		Remove: func(netID uint32) bool {
			filter := ecs.NewFilter1[mmokit.NetworkID](gw.ECSWorld())
			query := filter.Query()
			for query.Next() {
				nid := query.Get()
				if nid.ID != netID {
					continue
				}
				entity := query.Entity()
				query.Close()
				gw.MarkForRemoval(entity)
				return true
			}
			return false
		},
	}
}
