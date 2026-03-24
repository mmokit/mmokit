package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// ShieldRegenSystem ticks shield regeneration for all entities with a Shield mmokit.
type ShieldRegenSystem struct {
	gw     *game.GameWorld
	filter *ecs.Filter1[mmokit.Shield]
}

func NewShieldRegenSystem(gw *game.GameWorld) *ShieldRegenSystem {
	return &ShieldRegenSystem{gw: gw}
}

func (s *ShieldRegenSystem) Name() string { return "ShieldRegen" }

func (s *ShieldRegenSystem) Update(dt float32) {
	if s.filter == nil {
		s.filter = ecs.NewFilter1[mmokit.Shield](s.gw.ECS).Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
	}
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
