package system

import (
	"time"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/query"
)

// PhysicsSystem integrates velocity into position each tick.
// Skips Ghost and Replica entities.
//
// Just-transferred entities (carrying TransferCooldown) use a partial dt
// scaled to actual wall time elapsed since arrival on this cell, capped
// at the full tick dt. This prevents the destination's first physics
// step from advancing the player by 50 ms of simulation during (say) a
// 1 ms wall-time window between source's last frame and destination's
// first — which the client would otherwise render as a 50×-velocity
// spike at the handoff boundary. After wall-time catches up past one
// full dt, normal dt applies automatically.
type PhysicsSystem struct {
	engine.SystemBase
	entities query.Query[struct {
		Pos  *component.Position
		Vel  *component.Velocity
		Xfer *component.TransferCooldown `ecs:"optional"`
	}]
}

func (s *PhysicsSystem) Update(dt float32) {
	nowMs := uint64(time.Now().UnixMilli())
	dtMs := uint64(dt * 1000)
	for _, b := range s.entities.Iter {
		effDt := dt
		if b.Xfer != nil {
			wallSinceMs := nowMs - b.Xfer.ArrivalWallMs
			if wallSinceMs < dtMs {
				effDt = float32(wallSinceMs) / 1000.0
			}
		}
		b.Pos.X += b.Vel.X * effDt
		b.Pos.Y += b.Vel.Y * effDt
	}
}
