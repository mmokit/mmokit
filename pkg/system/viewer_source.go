package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
)

// PlayerViewerSource provides active players as viewers for replication.
// It queries players in the specified state, skips ghosts, and returns
// their position as the AoI query center.
type PlayerViewerSource struct {
	world    *ecs.World
	players  *engine.PlayerManager
	posMap   *ecs.Map1[component.Position]
	ghostMap *ecs.Map1[component.Ghost]
	state    engine.PlayerState
	buf      []ViewerInfo
}

// NewPlayerViewerSource creates a ViewerSource that returns players in the given
// state as viewers. activeState is typically engine.StateActive from PlayerManager.
func NewPlayerViewerSource(world *ecs.World, players *engine.PlayerManager, activeState engine.PlayerState) *PlayerViewerSource {
	return &PlayerViewerSource{
		world:    world,
		players:  players,
		posMap:   ecs.NewMap1[component.Position](world),
		ghostMap: ecs.NewMap1[component.Ghost](world),
		state:    activeState,
	}
}

func (s *PlayerViewerSource) ActiveViewers() []ViewerInfo {
	s.buf = s.buf[:0]
	s.players.ForEach(s.state, func(sess *engine.PlayerSession) {
		entity := sess.Entity
		if !s.world.Alive(entity) {
			return
		}
		if !s.posMap.HasAll(entity) {
			return
		}
		if s.ghostMap.HasAll(entity) {
			return
		}
		pos := s.posMap.Get(entity)
		s.buf = append(s.buf, ViewerInfo{
			ConnID: sess.ConnID,
			Entity: entity,
			X:      pos.X,
			Y:      pos.Y,
		})
	})
	return s.buf
}
