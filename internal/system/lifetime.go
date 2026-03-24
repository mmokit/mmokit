package system

import (
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// LifetimeSystem wraps the generic engine lifetime system.
type LifetimeSystem struct {
	inner *mmokit.LifetimeSystem
}

func NewLifetimeSystem(gw *game.GameWorld) *LifetimeSystem {
	return &LifetimeSystem{
		inner: mmokit.NewLifetimeSystem(gw.ECS, gw),
	}
}

func (s *LifetimeSystem) Name() string { return "Lifetime" }

func (s *LifetimeSystem) Update(dt float32) {
	s.inner.Update(dt)
}
