package system

import (
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/query"
)

// LifetimeSystem despawns entities whose Lifetime component has expired.
// Skips Ghost and Replica entities.
type LifetimeSystem struct {
	engine.SystemBase
	entities query.Query[struct {
		Lt *component.Lifetime
	}]
}

func (s *LifetimeSystem) Update(dt float32) {
	for e, b := range s.entities.Iter {
		b.Lt.Remaining -= dt
		if b.Lt.Remaining <= 0 {
			if s.Engine() != nil {
				s.Engine().MarkForRemoval(e)
			}
		}
	}
}
