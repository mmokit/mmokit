package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// StatusEffectSystem ticks down status effects and applies per-tick effects (e.g. Ion Burn DoT).
type StatusEffectSystem struct {
	mmokit.SystemBase
	gw       *GameWorld
	entities mmokit.Query[struct {
		SE *gamecomp.StatusEffects
	}]
}

func (s *StatusEffectSystem) Init() {
	s.gw = gwFromSystem(s.SystemBase)
	s.entities.Init(s)
}

func (s *StatusEffectSystem) Update(dt float32) {
	gw := s.gw

	for e, b := range s.entities.All() {
		se := b.SE

		// Apply per-tick effects before ticking down durations
		for i := uint8(0); i < se.Count; i++ {
			eff := &se.Effects[i]
			switch eff.Type {
			case gamecomp.StatusIonBurn:
				sourceNetID := uint32(0)
				if gw.ECS.Alive(eff.Source) && gw.C.NetworkID.HasAll(eff.Source) {
					sourceNetID = gw.C.NetworkID.Get(eff.Source).ID
				}
				gw.ApplyDamage(e, eff.Value*dt, sourceNetID)
			case gamecomp.StatusShieldRegen:
				if gw.C.Shield.HasAll(e) {
					shield := gw.C.Shield.Get(e)
					shield.Current = min(shield.Current+eff.Value*dt, shield.Max)
				}
			}
		}

		se.TickDown(dt)
	}
}
