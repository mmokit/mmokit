package main

import (
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// DecaySystem gradually reduces snake mass over time. Snakes that fall
// below MinMass die of starvation (queued as a KillInfo with no killer).
type DecaySystem struct {
	mmokit.SystemBase
	gw       *SlitherWorld
	entities mmokit.Query[struct {
		State *SnakeState
		NetID *mmokit.NetworkID
		Pos   *mmokit.Position
	}]
}

func (s *DecaySystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.entities.Init(s)
}

func (s *DecaySystem) Update(dt float32) {

	cfg := &s.gw.Cfg
	var kills []KillInfo

	for e, b := range s.entities.All() {
		b.State.Mass -= cfg.DecayRate * dt

		if b.State.Mass < cfg.MinMass {
			kills = append(kills, KillInfo{
				Victim:    e,
				VictimNet: b.NetID.ID,
				Mass:      b.State.Mass,
				PosX:      b.Pos.X,
				PosY:      b.Pos.Y,
				HasKiller: false,
			})
			s.gw.Engine().Log.Log(CatSnakeDeath, "snake starved: netID=%d mass=%.2f pos=(%.0f,%.0f)",
				b.NetID.ID, b.State.Mass, b.Pos.X, b.Pos.Y)
		}
	}

	// Append starvation deaths to the pending kills list (processed after iteration)
	s.gw.PendingKills = append(s.gw.PendingKills, kills...)
}
