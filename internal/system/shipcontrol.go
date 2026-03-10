package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
)

// ShipControlSystem converts PlayerInput into velocity and rotation changes.
type ShipControlSystem struct {
	gw     *game.GameWorld
	filter *ecs.Filter4[component.PlayerInput, component.ShipControl, component.Velocity, component.Rotation]
}

func NewShipControlSystem(gw *game.GameWorld) *ShipControlSystem {
	return &ShipControlSystem{gw: gw}
}

func (s *ShipControlSystem) Update(dt float32) {
	if s.filter == nil {
		s.filter = ecs.NewFilter4[component.PlayerInput, component.ShipControl, component.Velocity, component.Rotation](s.gw.ECS)
	}

	query := s.filter.Query()
	for query.Next() {
		input, ship, vel, rot := query.Get()

		// Apply turning
		rot.Angle += input.Turn * ship.TurnRate * dt

		// Apply thrust in facing direction
		if input.Thrust != 0 {
			thrust := input.Thrust * ship.Thrust * dt
			vel.X += float32(math.Cos(float64(rot.Angle))) * thrust
			vel.Y += float32(math.Sin(float64(rot.Angle))) * thrust
		}

		// Clamp to max speed
		speed := float32(math.Sqrt(float64(vel.X*vel.X + vel.Y*vel.Y)))
		if speed > ship.MaxSpeed {
			scale := ship.MaxSpeed / speed
			vel.X *= scale
			vel.Y *= scale
		}

		// Apply drag when not thrusting
		if input.Thrust == 0 {
			vel.X *= 0.98
			vel.Y *= 0.98
		}
	}
}
