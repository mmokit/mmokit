package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/query"
	"github.com/zenion/mmoserver/pkg/spatial"
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
	engine.SystemBase
	grid     *spatial.HashGrid
	entities query.Query[struct {
		Pos *component.Position
		Col *component.Collider
		Net *component.NetworkID
		Rot *component.Rotation `ecs:"optional"`
	}]
	hooks    SpatialHooks
	initHook func(gw any) SpatialHooks
}

// SetInitHook sets a function that runs during Init to produce per-tick hooks.
func (s *SpatialSystem) SetInitHook(fn func(gw any) SpatialHooks) {
	s.initHook = fn
}

func (s *SpatialSystem) Init() {
	s.entities.Init(s, query.IncludeAll())

	if sp, ok := s.GameWorld().(interface{ SpatialGrid() *spatial.HashGrid }); ok {
		s.grid = sp.SpatialGrid()
	}
	if s.initHook != nil {
		s.hooks = s.initHook(s.GameWorld())
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
