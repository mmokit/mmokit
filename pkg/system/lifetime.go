package system

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
)

// LifetimeSystem despawns entities whose Lifetime component has expired.
// Skips Ghost and Replica entities.
type LifetimeSystem struct {
	engine.SystemBase
	filter *ecs.Filter1[component.Lifetime]
}

func (s *LifetimeSystem) Init() {
	s.filter = ecs.NewFilter1[component.Lifetime](s.ECSWorld()).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
}

func (s *LifetimeSystem) Update(dt float32) {
	query := s.filter.Query()
	for query.Next() {
		lifetime := query.Get()
		lifetime.Remaining -= dt
		if lifetime.Remaining <= 0 {
			if s.Engine() != nil {
				s.Engine().MarkForRemoval(query.Entity())
			}
		}
	}
}
