package system

import (
	"math"
	"math/rand"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// WanderSystem steers entities with a Wander component along smoothly
// changing headings, updating both velocity and rotation.
type WanderSystem struct {
	mmokit.SystemBase
	entities mmokit.Query[struct {
		W   *gamecomp.Wander
		Vel *mmokit.Velocity
		Rot *mmokit.Rotation
	}]
}

func (s *WanderSystem) Init() {
	s.entities.Init(s)
}

func (s *WanderSystem) Update(dt float32) {
	for _, b := range s.entities.All() {
		w, vel, rot := b.W, b.Vel, b.Rot

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
