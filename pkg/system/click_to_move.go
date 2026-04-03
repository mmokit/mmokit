package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

const defaultMaxSpeed float32 = 300

// ClickToMoveSystem moves entities toward their MoveTarget at MoveParams.MaxSpeed.
// Stops when within ~1 unit of the target. Does nothing when MoveTarget.Active is false.
// Skips Ghost and Replica entities.
type ClickToMoveSystem struct {
	engine.SystemBase
	filter    *ecs.Filter4[component.Position, component.Velocity, component.MoveTarget, component.CellCoord]
	paramsMap *ecs.Map1[component.MoveParams]
}

func (s *ClickToMoveSystem) Init() {
	w := s.ECSWorld()
	s.filter = ecs.NewFilter4[component.Position, component.Velocity, component.MoveTarget, component.CellCoord](w).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
	s.paramsMap = ecs.NewMap1[component.MoveParams](w)
}

func (s *ClickToMoveSystem) Update(dt float32) {
	cellSize := coords.CellSize
	query := s.filter.Query()
	for query.Next() {
		pos, vel, mt, cc := query.Get()

		if !mt.Active {
			continue
		}

		dx := float32(mt.CellX-cc.CellX)*cellSize + mt.X - pos.X
		dy := float32(mt.CellY-cc.CellY)*cellSize + mt.Y - pos.Y
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))

		speed := defaultMaxSpeed
		entity := query.Entity()
		if s.paramsMap.HasAll(entity) {
			if p := s.paramsMap.Get(entity); p.MaxSpeed > 0 {
				speed = p.MaxSpeed
			}
		}

		// If the entity would reach or overshoot the target this tick, snap to it.
		stepDist := speed * dt
		if dist <= stepDist {
			pos.X = mt.X + float32(mt.CellX-cc.CellX)*cellSize
			pos.Y = mt.Y + float32(mt.CellY-cc.CellY)*cellSize
			mt.Active = false
			vel.X = 0
			vel.Y = 0
			continue
		}

		vel.X = (dx / dist) * speed
		vel.Y = (dy / dist) * speed
	}
}

// SetMoveTarget converts world-absolute coordinates to cell-local and activates.
func SetMoveTarget(mt *component.MoveTarget, worldX, worldY float32) {
	cellSize := coords.CellSize
	mt.CellX = int32(math.Floor(float64(worldX / cellSize)))
	mt.CellY = int32(math.Floor(float64(worldY / cellSize)))
	mt.X = worldX - float32(mt.CellX)*cellSize
	mt.Y = worldY - float32(mt.CellY)*cellSize
	mt.Active = true
}

// CancelMoveTarget deactivates movement.
func CancelMoveTarget(mt *component.MoveTarget) {
	mt.Active = false
}
