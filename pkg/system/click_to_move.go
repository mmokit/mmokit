package system

import (
	"math"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/query"
)

const defaultMaxSpeed float32 = 300

// ClickToMoveSystem moves entities toward their MoveTarget at MoveParams.MaxSpeed.
// Stops when within ~1 unit of the target. Does nothing when MoveTarget.Active is false.
// Skips Ghost and Replica entities.
type ClickToMoveSystem struct {
	engine.SystemBase
	entities query.Query[struct {
		Pos    *component.Position
		Vel    *component.Velocity
		MT     *component.MoveTarget
		CC     *component.CellCoord
		Params *component.MoveParams `ecs:"optional"`
	}]
}

func (s *ClickToMoveSystem) Update(dt float32) {
	cellSize := coords.CellSize
	for _, b := range s.entities.Iter {
		if !b.MT.Active {
			continue
		}

		dx := float32(b.MT.CellX-b.CC.CellX)*cellSize + b.MT.LocalX - b.Pos.X
		dy := float32(b.MT.CellY-b.CC.CellY)*cellSize + b.MT.LocalY - b.Pos.Y
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

		speed := defaultMaxSpeed
		if b.Params != nil && b.Params.MaxSpeed > 0 {
			speed = b.Params.MaxSpeed
		}

		stepDist := speed * dt
		if dist <= stepDist {
			b.Pos.X = b.MT.LocalX + float32(b.MT.CellX-b.CC.CellX)*cellSize
			b.Pos.Y = b.MT.LocalY + float32(b.MT.CellY-b.CC.CellY)*cellSize
			b.MT.Active = false
			b.Vel.X = 0
			b.Vel.Y = 0
			continue
		}

		b.Vel.X = (dx / dist) * speed
		b.Vel.Y = (dy / dist) * speed
	}
}
