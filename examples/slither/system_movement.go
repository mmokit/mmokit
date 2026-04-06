package main

import (
	"math"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// MovementSystem rotates snakes toward their target angle, sets velocity,
// pushes head positions into the body ring buffer, and updates body length
// based on mass. Runs BEFORE PhysicsSystem so the body records the pre-move
// position each tick.
type MovementSystem struct {
	mmokit.SystemBase
	gw       *SlitherWorld
	entities mmokit.Query[struct {
		Pos   *mmokit.Position
		Vel   *mmokit.Velocity
		Rot   *mmokit.Rotation
		State *SnakeState
		Body  *SnakeBody
	}]
}

func (s *MovementSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.entities.Init(s)
}

func (s *MovementSystem) Update(dt float32) {

	cfg := &s.gw.Cfg

	for e, b := range s.entities.All() {
		// Apply input: copy target angle from SnakeInput for player-controlled snakes.
		// Bots set TargetAngle/Boosting directly on SnakeState in BotSystem.
		if s.gw.SnakeInputMap.HasAll(e) && !s.gw.BotMap.HasAll(e) {
			inp := s.gw.SnakeInputMap.Get(e)
			b.State.TargetAngle = inp.TargetAngle
			b.State.Boosting = inp.Boost
		}

		// Smooth rotation toward target angle using shortest-arc
		diff := b.State.TargetAngle - b.Rot.Angle
		// Normalize to [-pi, pi]
		diff = float32(math.Atan2(float64(math.Sin(float64(diff))), float64(math.Cos(float64(diff)))))

		// Turn rate scales inversely with mass — small snakes are more agile.
		// Fourth-root gives a gentle curve: 4x mass = ~71% turn rate, 16x = 50%.
		turnRate := b.State.TurnRate / float32(math.Pow(float64(b.State.Mass/cfg.StartingMass), 0.25))
		maxTurn := turnRate * dt
		if diff > maxTurn {
			diff = maxTurn
		} else if diff < -maxTurn {
			diff = -maxTurn
		}
		b.Rot.Angle += diff

		// Set velocity from angle and speed
		b.Vel.X = float32(math.Cos(float64(b.Rot.Angle))) * b.State.Speed
		b.Vel.Y = float32(math.Sin(float64(b.Rot.Angle))) * b.State.Speed

		// Push current head position into body ring buffer BEFORE physics moves it
		b.Body.PushHead(b.Pos.X, b.Pos.Y)

		// Update body length based on mass
		length := int(b.State.Mass / cfg.MassPerSegment)
		if length < cfg.MinSegments {
			length = cfg.MinSegments
		}
		if length > cfg.MaxSegments {
			length = cfg.MaxSegments
		}
		b.Body.Length = length
	}
}
