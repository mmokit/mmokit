package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// StatusEffectSystem ticks down status effects and applies per-tick effects (e.g. Ion Burn DoT).
type StatusEffectSystem struct {
	mmokit.SystemBase[*GameWorld]
	entities mmokit.Query[struct {
		SE *gamecomp.StatusEffects
	}]
}

func (s *StatusEffectSystem) Update(dt float32) {
	gw := s.World()

	for e, b := range s.entities.Iter {
		se := b.SE
		entity := mmokit.EntityFromECS(gw.Stage, e)

		// Apply per-tick effects before ticking down durations
		for i := uint8(0); i < se.Count; i++ {
			eff := &se.Effects[i]
			switch eff.Type {
			case gamecomp.StatusIonBurn:
				source := mmokit.EntityFromECS(gw.Stage, eff.Source)
				gw.ApplyDamage(entity, eff.Value*dt, source.NetID())
			case gamecomp.StatusShieldRegen:
				if shield := mmokit.Get[gamecomp.Shield](entity); shield != nil {
					shield.Current = min(shield.Current+eff.Value*dt, shield.Max)
				}
			}
		}

		se.TickDown(dt)
	}
}
