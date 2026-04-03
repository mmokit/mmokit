package main

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// BoostSystem handles snake boosting: increases speed while consuming mass,
// and periodically drops food pellets at the tail.
type BoostSystem struct {
	mmokit.SystemBase
	gw     *SlitherWorld
	filter *ecs.Filter2[SnakeState, SnakeBody]
	tick   uint32
}

func (s *BoostSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.filter = ecs.NewFilter2[SnakeState, SnakeBody](s.ECSWorld()).
		Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
}

type boostFoodDrop struct {
	x, y, value float32
}

func (s *BoostSystem) Update(dt float32) {
	cfg := &s.gw.Cfg
	s.tick++

	// Collect food drops during iteration, spawn after
	var drops []boostFoodDrop

	query := s.filter.Query()
	for query.Next() {
		state, body := query.Get()

		if state.Boosting && state.Mass > cfg.MinMass+cfg.BoostMassCost {
			state.Speed = cfg.BoostSpeed

			// Deduct mass (cost is per second, so multiply by dt)
			state.Mass -= cfg.BoostMassCost * dt

			// Every 4th tick, drop a food pellet at the tail
			if s.tick%4 == 0 && body.Length > 0 {
				tail := body.GetSegment(body.Length - 1)
				drops = append(drops, boostFoodDrop{tail.X, tail.Y, cfg.BoostFoodValue})
			}
		} else {
			state.Speed = cfg.BaseSpeed
		}
	}

	// Spawn food after query is done
	for _, d := range drops {
		s.gw.SpawnDeathFood(d.x, d.y, d.value)
		s.gw.Engine().Log.Log(CatSnakeBoost, "boost food dropped: pos=(%.0f,%.0f) value=%.2f",
			d.x, d.y, d.value)
	}
}
