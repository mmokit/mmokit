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
	world     *ecs.World
	players   *engine.PlayerManager
	posMap    *ecs.Map1[component.Position]
	ghostMap  *ecs.Map1[component.Ghost]
	shadowMap *ecs.Map1[component.Shadow]
	states    []engine.PlayerState
	buf       []ViewerInfo
}

// NewPlayerViewerSource creates a ViewerSource that returns players in the given
// states as viewers. Typically includes at least engine.StateActive; additional
// states (e.g. a custom "docking" state) keep those players receiving updates.
func NewPlayerViewerSource(world *ecs.World, players *engine.PlayerManager, states ...engine.PlayerState) *PlayerViewerSource {
	return &PlayerViewerSource{
		world:     world,
		players:   players,
		posMap:    ecs.NewMap1[component.Position](world),
		ghostMap:  ecs.NewMap1[component.Ghost](world),
		shadowMap: ecs.NewMap1[component.Shadow](world),
		states:    states,
	}
}

func (s *PlayerViewerSource) ActiveViewers() []ViewerInfo {
	s.buf = s.buf[:0]
	for _, state := range s.states {
		s.players.ForEach(state, func(sess *engine.PlayerSession) {
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
			// Skip players whose entity is a pre-authority Shadow —
			// during a handoff warmup window, the destination cell
			// pre-creates the session but the entity is still a
			// Shadow. The source cell holds authority and emits
			// frames for this viewer. Including the Shadow here
			// would cause both source and destination to send
			// alternating frames, producing a ~3-4 tick visual
			// oscillation on the client. Skipped until PromoteShadow
			// removes the Shadow component at Commit time.
			if s.shadowMap.HasAll(entity) {
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
