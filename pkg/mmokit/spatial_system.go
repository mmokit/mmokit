package mmokit

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/query"
	"github.com/mmokit/mmokit/pkg/spatial"
	"github.com/mmokit/mmokit/pkg/universe"
)

// SpatialHooks provides optional per-tick callbacks for game-specific spatial logic.
type SpatialHooks struct {
	PreTick  func()
	OnEntity func(entity ecs.Entity, entry spatial.Entry)
	PostTick func()
}

// SpatialSystem updates the spatial hash grid each tick by querying all entities
// with Position + Collider + NetworkID. Rotation is read if present.
type SpatialSystem struct {
	SystemBase
	grid     *spatial.HashGrid
	entities query.Query[struct {
		Pos *component.Position
		Col *component.Collider
		Net *component.NetworkID
		Rot *component.Rotation `ecs:"optional"`
	}]
	hooks    SpatialHooks
	initHook func(stage *universe.Stage) SpatialHooks
}

// SetInitHook sets a function that runs during Init to produce per-tick hooks.
func (s *SpatialSystem) SetInitHook(fn func(stage *universe.Stage) SpatialHooks) {
	s.initHook = fn
}

func (s *SpatialSystem) Init() {
	s.entities.With(query.IncludeAll())
	s.grid = s.Stage().SpatialGrid()
	if s.initHook != nil {
		s.hooks = s.initHook(s.Stage())
	}
}

func (s *SpatialSystem) Update(dt float32) {
	if s.hooks.PreTick != nil {
		s.hooks.PreTick()
	}

	for e, b := range s.entities.Iter {
		entry := spatial.Entry{
			Entity: e,
			X:      b.Pos.X,
			Y:      b.Pos.Y,
			Radius: b.Col.Radius,
			Width:  b.Col.Width,
			Height: b.Col.Height,
			Layer:  b.Col.Layer,
			Shape:  b.Col.Shape,
		}
		if b.Rot != nil {
			entry.Rotation = b.Rot.Angle
		}

		if s.grid.IsRegistered(e) {
			s.grid.Update(entry)
		} else {
			s.grid.Register(entry)
		}

		if s.hooks.OnEntity != nil {
			s.hooks.OnEntity(e, entry)
		}
	}

	if s.hooks.PostTick != nil {
		s.hooks.PostTick()
	}
}
