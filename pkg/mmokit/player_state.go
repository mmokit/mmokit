package mmokit

import (
	"github.com/zenion/mmoserver/pkg/component"
)

// PlayerStateOf returns the current PlayerState for the given Entity,
// resolving via the entity's PlayerConn → session lookup. Returns the
// zero-value PlayerState if the entity has no PlayerConn, isn't bound to a
// stage, or the session is not found.
//
// Use in HandleClient handlers as the first-line state filter:
//
//	mmokit.HandleClient(p, func(player mmokit.Entity, msg *Dock) {
//	    if mmokit.PlayerStateOf(player) != mmokit.StateActive { return }
//	    ...
//	})
func PlayerStateOf(e Entity) PlayerState {
	conn := Get[component.PlayerConn](e)
	if conn == nil {
		return PlayerState(0)
	}
	stage := e.Stage()
	if stage == nil {
		return PlayerState(0)
	}
	eng := stage.Engine()
	if eng == nil || eng.Players == nil {
		return PlayerState(0)
	}
	s := eng.Players.ByConnID(conn.ConnID)
	if s == nil {
		return PlayerState(0)
	}
	return s.State
}
