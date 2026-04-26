package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// ShieldRegenSystem ticks shield regeneration for all entities with a Shield mmokit.
type ShieldRegenSystem struct {
	mmokit.SystemBase[*GameWorld]
	gw       *GameWorld
	entities mmokit.Query[struct {
		Shield *gamecomp.Shield
	}]
}

func (s *ShieldRegenSystem) Init() {
	s.gw = gwFromSystem(s.SystemBase)
	s.entities.Init(s)
}

func (s *ShieldRegenSystem) Update(dt float32) {
	for _, b := range s.entities.Iter {
		shield := b.Shield

		if shield.DamageCooldown > 0 {
			shield.DamageCooldown -= dt
			continue
		}

		if shield.Current < shield.Max {
			shield.Current = min(shield.Current+shield.RegenRate*dt, shield.Max)
		}
	}
}
