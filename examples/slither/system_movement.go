package main

import (
	"math"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// MovementSystem rotates snakes toward their target angle, sets velocity,
// pushes head positions into the body ring buffer, and updates body length
// based on mass. Runs BEFORE PhysicsSystem so the body records the pre-move
// position each tick.
type MovementSystem struct {
	mmokit.SystemBase
	gw     *SlitherWorld
	filter *ecs.Filter5[mmokit.Position, mmokit.Velocity, mmokit.Rotation, SnakeState, SnakeBody]
}

func (s *MovementSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.filter = ecs.NewFilter5[mmokit.Position, mmokit.Velocity, mmokit.Rotation, SnakeState, SnakeBody](s.ECSWorld()).
		Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
}

func (s *MovementSystem) Update(dt float32) {

	cfg := &s.gw.Cfg

	query := s.filter.Query()
	for query.Next() {
		pos, vel, rot, state, body := query.Get()

		// Apply input: copy target angle from SnakeInput for player-controlled snakes.
		// Bots set TargetAngle/Boosting directly on SnakeState in BotSystem.
		entity := query.Entity()
		if s.gw.SnakeInputMap.HasAll(entity) && !s.gw.BotMap.HasAll(entity) {
			inp := s.gw.SnakeInputMap.Get(entity)
			state.TargetAngle = inp.TargetAngle
			state.Boosting = inp.Boost
		}

		// Smooth rotation toward target angle using shortest-arc
		diff := state.TargetAngle - rot.Angle
		// Normalize to [-pi, pi]
		diff = float32(math.Atan2(float64(math.Sin(float64(diff))), float64(math.Cos(float64(diff)))))

		// Turn rate scales inversely with mass — small snakes are more agile.
		// Fourth-root gives a gentle curve: 4x mass = ~71% turn rate, 16x = 50%.
		turnRate := state.TurnRate / float32(math.Pow(float64(state.Mass/cfg.StartingMass), 0.25))
		maxTurn := turnRate * dt
		if diff > maxTurn {
			diff = maxTurn
		} else if diff < -maxTurn {
			diff = -maxTurn
		}
		rot.Angle += diff

		// Set velocity from angle and speed
		vel.X = float32(math.Cos(float64(rot.Angle))) * state.Speed
		vel.Y = float32(math.Sin(float64(rot.Angle))) * state.Speed

		// Push current head position into body ring buffer BEFORE physics moves it
		body.PushHead(pos.X, pos.Y)

		// Update body length based on mass
		length := int(state.Mass / cfg.MassPerSegment)
		if length < cfg.MinSegments {
			length = cfg.MinSegments
		}
		if length > cfg.MaxSegments {
			length = cfg.MaxSegments
		}
		body.Length = length
	}
}
