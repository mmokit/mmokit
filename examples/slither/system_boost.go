package main

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// BoostSystem handles snake boosting: increases speed while consuming mass,
// and periodically drops food pellets at the tail.
type BoostSystem struct {
	mmokit.SystemBase
	gw       *SlitherWorld
	entities mmokit.Query[struct {
		State *SnakeState
		Body  *SnakeBody
	}]
	tick uint32
}

func (s *BoostSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.entities.Init(s)
}

type boostFoodDrop struct {
	x, y, value float32
}

func (s *BoostSystem) Update(dt float32) {
	cfg := &s.gw.Cfg
	s.tick++

	// Collect food drops during iteration, spawn after
	var drops []boostFoodDrop

	for _, b := range s.entities.All() {
		if b.State.Boosting && b.State.Mass > cfg.MinMass+cfg.BoostMassCost {
			b.State.Speed = cfg.BoostSpeed

			// Deduct mass (cost is per second, so multiply by dt)
			b.State.Mass -= cfg.BoostMassCost * dt

			// Every 4th tick, drop a food pellet at the tail
			if s.tick%4 == 0 && b.Body.Length > 0 {
				tail := b.Body.GetSegment(b.Body.Length - 1)
				drops = append(drops, boostFoodDrop{tail.X, tail.Y, cfg.BoostFoodValue})
			}
		} else {
			b.State.Speed = cfg.BaseSpeed
		}
	}

	// Spawn food after query is done
	for _, d := range drops {
		s.gw.SpawnDeathFood(d.x, d.y, d.value)
		s.gw.Engine().Log.Log(CatSnakeBoost, "boost food dropped: pos=(%.0f,%.0f) value=%.2f",
			d.x, d.y, d.value)
	}
}
