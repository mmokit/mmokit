package main

import (
	"log"
	"math/rand"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type foodMappers struct {
	base *ecs.Map5[mmokit.Position, mmokit.NetworkID, mmokit.EntityKind, mmokit.Collider, mmokit.CellCoord]
}

func newFoodMappers(w *ecs.World) *foodMappers {
	return &foodMappers{
		base: ecs.NewMap5[mmokit.Position, mmokit.NetworkID, mmokit.EntityKind, mmokit.Collider, mmokit.CellCoord](w),
	}
}

// SpawnFood creates a natural food pellet.
func (gw *SlitherWorld) SpawnFood(x, y, value float32, colorIdx uint8) ecs.Entity {
	netID := gw.Engine().NextNetID()
	entity := gw.foodMappers.base.NewEntity(
		&mmokit.Position{X: x, Y: y},
		&mmokit.NetworkID{ID: netID},
		&mmokit.EntityKind{Type: KindNaturalFood},
		&mmokit.Collider{Radius: 5, Layer: LayerFood},
		&mmokit.CellCoord{CellX: gw.Cell().X, CellY: gw.Cell().Y},
	)
	gw.FoodMap.Add(entity, &Food{Value: value, ColorIdx: colorIdx})
	gw.FoodCount++
	return entity
}

// SpawnDeathFood creates food from a dead snake (brighter, slightly larger).
func (gw *SlitherWorld) SpawnDeathFood(x, y, value float32) ecs.Entity {
	netID := gw.Engine().NextNetID()
	entity := gw.foodMappers.base.NewEntity(
		&mmokit.Position{X: x, Y: y},
		&mmokit.NetworkID{ID: netID},
		&mmokit.EntityKind{Type: KindDeathFood},
		&mmokit.Collider{Radius: 7, Layer: LayerFood},
		&mmokit.CellCoord{CellX: gw.Cell().X, CellY: gw.Cell().Y},
	)
	gw.FoodMap.Add(entity, &Food{Value: value, ColorIdx: uint8(rand.Intn(8))})
	gw.FoodCount++
	return entity
}

// SpawnRandomFood creates food at a random position within the cell.
// Natural food varies in size: mostly small, occasionally medium/large.
func (gw *SlitherWorld) SpawnRandomFood() {
	cellSize := mmokit.CellSize()
	x := rand.Float32() * cellSize
	y := rand.Float32() * cellSize
	colorIdx := uint8(rand.Intn(8))
	// Vary value: 70% small (0.5-1.0), 25% medium (1.0-2.0), 5% large (2.0-4.0)
	r := rand.Float32()
	var value float32
	if r < 0.70 {
		value = 0.5 + rand.Float32()*0.5
	} else if r < 0.95 {
		value = 1.0 + rand.Float32()*1.0
	} else {
		value = 2.0 + rand.Float32()*2.0
	}
	gw.SpawnFood(x, y, value, colorIdx)
}

// SpawnInitialFood populates the node with the target food count.
func (gw *SlitherWorld) SpawnInitialFood() {
	for i := 0; i < gw.Cfg.FoodPerNode; i++ {
		gw.SpawnRandomFood()
	}
	log.Printf("[%s] spawned %d initial food", gw.CellID(), gw.Cfg.FoodPerNode)
}
