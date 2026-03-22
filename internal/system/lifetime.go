package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
)

// LifetimeSystem despawns entities whose lifetime has expired.
type LifetimeSystem struct {
	gw     *game.GameWorld
	filter *ecs.Filter1[component.Lifetime]
}

func NewLifetimeSystem(gw *game.GameWorld) *LifetimeSystem {
	return &LifetimeSystem{gw: gw}
}

func (s *LifetimeSystem) Update(dt float32) {
	if s.filter == nil {
		s.filter = ecs.NewFilter1[component.Lifetime](s.gw.ECS).Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
	}

	query := s.filter.Query()
	for query.Next() {
		lifetime := query.Get()
		lifetime.Remaining -= dt
		if lifetime.Remaining <= 0 {
			s.gw.MarkForRemoval(query.Entity())
		}
	}
}
