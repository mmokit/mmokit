package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
)

// DirectionMoveSystem moves entities in the direction of their DirectionInput
// at MoveParams.MaxSpeed. Sets velocity to zero when input is inactive.
// Skips Ghost and Replica entities.
type DirectionMoveSystem struct {
	engine.SystemBase
	filter    *ecs.Filter3[component.Position, component.Velocity, component.DirectionInput]
	paramsMap *ecs.Map1[component.MoveParams]
}

func (s *DirectionMoveSystem) Init() {
	w := s.ECSWorld()
	s.filter = ecs.NewFilter3[component.Position, component.Velocity, component.DirectionInput](w).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
	s.paramsMap = ecs.NewMap1[component.MoveParams](w)
}

func (s *DirectionMoveSystem) Update(dt float32) {
	query := s.filter.Query()
	for query.Next() {
		_, vel, di := query.Get()

		if !di.Active {
			vel.X = 0
			vel.Y = 0
			continue
		}

		speed := defaultMaxSpeed
		entity := query.Entity()
		if s.paramsMap.HasAll(entity) {
			if p := s.paramsMap.Get(entity); p.MaxSpeed > 0 {
				speed = p.MaxSpeed
			}
		}

		mag := float32(math.Sqrt(float64(di.X*di.X + di.Y*di.Y)))
		if mag < 0.001 {
			vel.X = 0
			vel.Y = 0
			continue
		}

		vel.X = (di.X / mag) * speed
		vel.Y = (di.Y / mag) * speed
	}
}
