package main

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// DecaySystem gradually reduces snake mass over time. Snakes that fall
// below MinMass die of starvation (queued as a KillInfo with no killer).
type DecaySystem struct {
	mmokit.SystemBase
	gw     *SlitherWorld
	filter *ecs.Filter3[SnakeState, mmokit.NetworkID, mmokit.Position]
}

func (s *DecaySystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.filter = ecs.NewFilter3[SnakeState, mmokit.NetworkID, mmokit.Position](s.ECSWorld()).
		Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
}

func (s *DecaySystem) Update(dt float32) {

	cfg := &s.gw.Cfg
	var kills []KillInfo

	query := s.filter.Query()
	for query.Next() {
		state, netID, pos := query.Get()

		state.Mass -= cfg.DecayRate * dt

		if state.Mass < cfg.MinMass {
			entity := query.Entity()
			kills = append(kills, KillInfo{
				Victim:    entity,
				VictimNet: netID.ID,
				Mass:      state.Mass,
				PosX:      pos.X,
				PosY:      pos.Y,
				HasKiller: false,
			})
			s.gw.Engine().Log.Log(CatSnakeDeath, "snake starved: netID=%d mass=%.2f pos=(%.0f,%.0f)",
				netID.ID, state.Mass, pos.X, pos.Y)
		}
	}

	// Append starvation deaths to the pending kills list (processed after iteration)
	s.gw.PendingKills = append(s.gw.PendingKills, kills...)
}
