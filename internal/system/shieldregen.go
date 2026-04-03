package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// ShieldRegenSystem ticks shield regeneration for all entities with a Shield mmokit.
type ShieldRegenSystem struct {
	mmokit.SystemBase
	gw     *game.GameWorld
	filter *ecs.Filter1[mmokit.Shield]
}

func (s *ShieldRegenSystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	s.filter = ecs.NewFilter1[mmokit.Shield](s.ECSWorld()).Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
}

func (s *ShieldRegenSystem) Update(dt float32) {
	query := s.filter.Query()
	for query.Next() {
		shield := query.Get()

		if shield.DamageCooldown > 0 {
			shield.DamageCooldown -= dt
			continue
		}

		if shield.Current < shield.Max {
			shield.Current = min(shield.Current+shield.RegenRate*dt, shield.Max)
		}
	}
}
