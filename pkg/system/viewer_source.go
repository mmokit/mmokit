package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/engine"
)

// PlayerViewerSource provides active players as viewers for replication.
// It queries players in the specified states, skips ghosts, and returns
// their position as the AoI query center.
type PlayerViewerSource struct {
	world    *ecs.World
	players  *engine.PlayerManager
	posMap   *ecs.Map1[component.Position]
	ghostMap *ecs.Map1[component.Ghost]
	states   []engine.PlayerState
	buf      []ViewerInfo
}

var _ ViewerLiveness = (*PlayerViewerSource)(nil)

// NewPlayerViewerSource creates a ViewerSource that returns players in the given
// states as viewers. Typically includes at least engine.StateActive; additional
// states (e.g. a custom "docking" state) keep those players receiving updates.
func NewPlayerViewerSource(world *ecs.World, players *engine.PlayerManager, states ...engine.PlayerState) *PlayerViewerSource {
	return &PlayerViewerSource{
		world:    world,
		players:  players,
		posMap:   ecs.NewMap1[component.Position](world),
		ghostMap: ecs.NewMap1[component.Ghost](world),
		states:   states,
	}
}

func (s *PlayerViewerSource) ActiveViewers() []ViewerInfo {
	s.buf = s.buf[:0]
	for _, state := range s.states {
		s.players.ForEach(state, func(sess *engine.PlayerSession) {
			if sess.ReplicationSuspended {
				return
			}
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
	}
	return s.buf
}

// ViewerExists reports whether connID still belongs to a session in this
// PlayerManager. A session can temporarily be absent from ActiveViewers while
// it occupies a game-specific lifecycle state; replication uses this signal to
// retain its frame sequence and force a fresh snapshot when it becomes active
// again.
func (s *PlayerViewerSource) ViewerExists(connID uint32) bool {
	return s.players.ByConnID(connID) != nil
}
