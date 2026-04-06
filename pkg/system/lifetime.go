package system

import (
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/query"
)

// LifetimeSystem despawns entities whose Lifetime component has expired.
// Skips Ghost and Replica entities.
type LifetimeSystem struct {
	engine.SystemBase
	entities query.Query[struct {
		Lt *component.Lifetime
	}]
}

func (s *LifetimeSystem) Init() {
	s.entities.Init(s)
}

func (s *LifetimeSystem) Update(dt float32) {
	for e, b := range s.entities.All() {
		b.Lt.Remaining -= dt
		if b.Lt.Remaining <= 0 {
			if s.Engine() != nil {
				s.Engine().MarkForRemoval(e)
			}
		}
	}
}
