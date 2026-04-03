package main

import "github.com/zenion/mmoserver/pkg/mmokit"

// FoodSpawnSystem maintains the target food count by spawning new pellets.
type FoodSpawnSystem struct {
	mmokit.SystemBase
	gw *SlitherWorld
}

func (s *FoodSpawnSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
}

func (s *FoodSpawnSystem) Update(dt float32) {
	gw := s.gw
	deficit := gw.Cfg.FoodPerNode - gw.FoodCount
	if deficit <= 0 {
		return
	}

	// Spawn up to 10 per tick to avoid flooding the ECS.
	batch := deficit
	if batch > 10 {
		batch = 10
	}

	for i := 0; i < batch; i++ {
		gw.SpawnRandomFood()
	}

	gw.Engine().Log.Log(CatGameFood, "spawned %d food (count=%d target=%d)", batch, gw.FoodCount, gw.Cfg.FoodPerNode)
}
