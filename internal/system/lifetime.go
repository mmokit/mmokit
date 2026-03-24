package system

import (
	"github.com/zenion/mmoserver/internal/game"
	pkgsys "github.com/zenion/mmoserver/pkg/system"
)

// LifetimeSystem wraps the generic engine lifetime system.
type LifetimeSystem struct {
	inner *pkgsys.LifetimeSystem
}

func NewLifetimeSystem(gw *game.GameWorld) *LifetimeSystem {
	return &LifetimeSystem{
		inner: pkgsys.NewLifetimeSystem(gw.ECS, gw),
	}
}

func (s *LifetimeSystem) Update(dt float32) {
	s.inner.Update(dt)
}
