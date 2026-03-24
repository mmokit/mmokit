package system

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
)

// EntityRemover is implemented by any world that can mark entities for removal.
type EntityRemover interface {
	MarkForRemoval(entity ecs.Entity)
}

// LifetimeSystem despawns entities whose Lifetime component has expired.
// Skips Ghost and Replica entities.
type LifetimeSystem struct {
	world   *ecs.World
	remover EntityRemover
	filter  *ecs.Filter1[component.Lifetime]
}

// NewLifetimeSystem creates a lifetime system operating on the given ECS world.
func NewLifetimeSystem(world *ecs.World, remover EntityRemover) *LifetimeSystem {
	return &LifetimeSystem{world: world, remover: remover}
}

func (s *LifetimeSystem) Update(dt float32) {
	if s.filter == nil {
		s.filter = ecs.NewFilter1[component.Lifetime](s.world).
			Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
	}
	query := s.filter.Query()
	for query.Next() {
		lifetime := query.Get()
		lifetime.Remaining -= dt
		if lifetime.Remaining <= 0 {
			s.remover.MarkForRemoval(query.Entity())
		}
	}
}
