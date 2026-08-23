package system

import (
	"time"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/query"
)

// PhysicsSystem integrates velocity into position each tick, on all three
// axes, and applies gravity to entities that opt into it.
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
//
// Z integrates unconditionally rather than behind a dimension check. That is
// inert in a 2D profile: nothing in pkg/ or in either 2D example writes a
// non-zero Velocity.Z, so the term adds exactly zero, and a profile branch on
// the tick hot path would cost more than the add it guards. Gravity is gated
// on the optional Motion component instead, which no 2D entity carries.
//
// THE MoveWalk CLAMP BELOW IS A DIFFERENT CASE, and it is the one exception to
// "a 2D game's Z is always zero". It writes Pos.Z directly and is gated on
// neither the dimension nor gravity, so a 2D game that spawns
// Motion{Mode: MoveWalk, GroundZ: k} parks its entities at k. Everything
// downstream that treats Z as identically zero in 2D — the spherical area of
// interest and spatial queries from roadmap §7.5 phase 4a — is then measuring
// real distances rather than a no-op. That is not a bug in those tests: at
// k != 0 the sphere is the correct answer and the old cylinder was wrong. It
// is recorded because "2D pays nothing" is an empirical property of the
// current examples, not a structural guarantee.
type PhysicsSystem struct {
	engine.SystemBase
	entities query.Query[struct {
		Pos    *component.Position
		Vel    *component.Velocity
		Motion *component.Motion           `ecs:"optional"`
		Xfer   *component.TransferCooldown `ecs:"optional"`
	}]
}

func (s *PhysicsSystem) Update(dt float32) {
	nowMs := uint64(time.Now().UnixMilli())
	dtMs := uint64(dt * 1000)
	gravity := s.Engine().Config.Gravity

	for _, b := range s.entities.Iter {
		effDt := dt
		if b.Xfer != nil {
			wallSinceMs := nowMs - b.Xfer.ArrivalWallMs
			if wallSinceMs < dtMs {
				effDt = float32(wallSinceMs) / 1000.0
			}
		}

		// Gravity acts on velocity before it is integrated, so a tick's
		// displacement reflects the acceleration applied during that tick.
		if gravity != 0 && b.Motion != nil && b.Motion.Mode != component.MoveFly {
			b.Vel.Z += gravity * effDt
		}

		b.Pos.X += b.Vel.X * effDt
		b.Pos.Y += b.Vel.Y * effDt
		b.Pos.Z += b.Vel.Z * effDt

		if b.Motion != nil && b.Motion.Mode == component.MoveWalk {
			if b.Pos.Z <= b.Motion.GroundZ {
				b.Pos.Z = b.Motion.GroundZ
				// Zero only downward velocity: an entity launched upward on
				// the same tick it was grounded must keep its climb.
				if b.Vel.Z < 0 {
					b.Vel.Z = 0
				}
				b.Motion.Grounded = true
			} else {
				b.Motion.Grounded = false
			}
		}
	}
}
