package system

import (
	"math"
	"math/rand"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// WanderSystem steers entities with a Wander component along smoothly
// changing headings, updating both velocity and rotation.
type WanderSystem struct {
	mmokit.SystemBase
	filter *ecs.Filter3[gamecomp.Wander, mmokit.Velocity, mmokit.Rotation]
}

func (s *WanderSystem) Init() {
	s.filter = ecs.NewFilter3[gamecomp.Wander, mmokit.Velocity, mmokit.Rotation](s.ECSWorld()).
		Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
}

func (s *WanderSystem) Update(dt float32) {
	query := s.filter.Query()
	for query.Next() {
		w, vel, rot := query.Get()

		// Pick a new target heading when the timer expires.
		w.Timer -= dt
		if w.Timer <= 0 {
			// Vary interval ±50% so NPCs don't move in lockstep.
			w.Timer = w.Interval * (0.5 + rand.Float32())
			// Steer within ±90° of current heading for natural-looking paths.
			w.TargetAngle = rot.Angle + (rand.Float32()-0.5)*math.Pi
		}

		// Smoothly turn toward target heading.
		diff := normalizeAngle(w.TargetAngle - rot.Angle)
		maxTurn := w.TurnRate * dt
		if diff > maxTurn {
			diff = maxTurn
		} else if diff < -maxTurn {
			diff = -maxTurn
		}
		rot.Angle += diff

		// Drive velocity from current facing angle.
		vel.X = w.Speed * float32(math.Cos(float64(rot.Angle)))
		vel.Y = w.Speed * float32(math.Sin(float64(rot.Angle)))
	}
}
