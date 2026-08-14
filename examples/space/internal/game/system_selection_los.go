package game

import (
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/pkg/mmokit"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// SelectionLOSSystem clears a player's Selection when LOS to the selected
// entity has been continuously blocked by a LayerStatic collider for
// LockLosLossBreakSec. Mirrors the NPC AI LOS-loss latch on the player
// side: Selection-driven abilities (mining beams, future lock-on shots)
// stop the moment the selection clears, so a player whose target ducks
// behind a wall loses the lock on the same cadence the AI loses theirs.
//
// Time is tracked locally via elapsedSec (monotonic dt accumulation) to
// match the convention NPCAISystem uses — only the deltas matter and the
// stage doesn't expose a game-time accessor.
type SelectionLOSSystem struct {
	mmokit.SystemBase
	gw         *GameWorld
	elapsedSec float32
	entities   mmokit.Query[struct {
		Sel *gamecomp.Selection
		Pos *mmokit.Position
	}]
}

func (s *SelectionLOSSystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
}

func (s *SelectionLOSSystem) Update(dt float32) {
	gw := s.gw
	s.elapsedSec += dt
	now := s.elapsedSec
	cutoff := gw.Config.LockLosLossBreakSec

	for selfE, b := range s.entities.Iter {
		sel, pos := b.Sel, b.Pos

		if sel.EntityNetID == 0 {
			sel.LOSLostAt = 0
			continue
		}

		target := mmokit.EntityByNetID(gw.stage, sel.EntityNetID)
		if !target.Alive() {
			sel.EntityNetID = 0
			sel.LOSLostAt = 0
			continue
		}
		tpos := mmokit.Get[mmokit.Position](target)
		if tpos == nil {
			sel.EntityNetID = 0
			sel.LOSLostAt = 0
			continue
		}

		if hasLOSOnGrid(gw.Spatial,
			spatial.Vec2{X: pos.X, Y: pos.Y},
			spatial.Vec2{X: tpos.X, Y: tpos.Y},
			selfE) {
			sel.LOSLostAt = 0
			continue
		}

		// LOS is blocked — start or extend the latch.
		if sel.LOSLostAt == 0 {
			sel.LOSLostAt = now
			continue
		}
		if now-sel.LOSLostAt >= cutoff {
			sel.EntityNetID = 0
			sel.LOSLostAt = 0
		}
	}
}
