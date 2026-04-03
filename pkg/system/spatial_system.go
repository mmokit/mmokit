package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// SpatialHooks provides optional per-tick callbacks for game-specific spatial logic.
type SpatialHooks struct {
	PreTick  func()                                    // called before the core entity loop
	OnEntity func(entity ecs.Entity, entry spatial.Entry) // called after each entity is registered/updated
	PostTick func()                                    // called after the core entity loop
}

// SpatialSystem updates the spatial hash grid each tick by querying all entities
// with Position + Collider + NetworkID. Rotation is read if present. Games can
// provide hooks for per-tick and per-entity custom logic via SetInitHook.
type SpatialSystem struct {
	engine.SystemBase
	grid     *spatial.HashGrid
	filter   *ecs.Filter3[component.Position, component.Collider, component.NetworkID]
	rotMap   *ecs.Map1[component.Rotation]
	hooks    SpatialHooks
	initHook func(gw any) SpatialHooks
}

// SetInitHook sets a function that runs during Init to produce per-tick hooks.
// Called by the mmokit.NewSpatialSystem factory — not intended for direct use.
func (s *SpatialSystem) SetInitHook(fn func(gw any) SpatialHooks) {
	s.initHook = fn
}

func (s *SpatialSystem) Init() {
	w := s.ECSWorld()
	s.filter = ecs.NewFilter3[component.Position, component.Collider, component.NetworkID](w)
	s.rotMap = ecs.NewMap1[component.Rotation](w)

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

	query := s.filter.Query()
	for query.Next() {
		pos, col, _ := query.Get()
		entity := query.Entity()

		entry := spatial.Entry{
			Entity: entity,
			X:      pos.X,
			Y:      pos.Y,
			Radius: col.Radius,
			Width:  col.Width,
			Height: col.Height,
			Layer:  col.Layer,
			Shape:  col.Shape,
		}
		if s.rotMap.HasAll(entity) {
			entry.Rotation = s.rotMap.Get(entity).Angle
		}

		if s.grid.IsRegistered(entity) {
			s.grid.Update(entry)
		} else {
			s.grid.Register(entry)
		}

		if s.hooks.OnEntity != nil {
			s.hooks.OnEntity(entity, entry)
		}
	}

	if s.hooks.PostTick != nil {
		s.hooks.PostTick()
	}
}
