package system

import (
	"math"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/query"
)

// DirectionMoveSystem moves entities in the direction of their DirectionInput
// at MoveParams.MaxSpeed. Sets velocity to zero when input is inactive.
// Skips Ghost and Replica entities.
type DirectionMoveSystem struct {
	engine.SystemBase[any]
	entities query.Query[struct {
		Pos    *component.Position
		Vel    *component.Velocity
		DI     *component.DirectionInput
		Params *component.MoveParams `ecs:"optional"`
	}]
}

func (s *DirectionMoveSystem) Update(dt float32) {
	for _, b := range s.entities.Iter {
		if !b.DI.Active {
			b.Vel.X = 0
			b.Vel.Y = 0
			continue
		}

		speed := defaultMaxSpeed
		if b.Params != nil && b.Params.MaxSpeed > 0 {
			speed = b.Params.MaxSpeed
		}

		mag := float32(math.Sqrt(float64(b.DI.X*b.DI.X + b.DI.Y*b.DI.Y)))
		if mag < 0.001 {
			b.Vel.X = 0
			b.Vel.Y = 0
			continue
		}

		b.Vel.X = (b.DI.X / mag) * speed
		b.Vel.Y = (b.DI.Y / mag) * speed
	}
}
