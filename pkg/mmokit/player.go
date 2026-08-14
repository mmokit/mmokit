package mmokit

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/universe"
)

// Player is the friendly facade that input handlers receive. It wraps a
// PlayerSession and exposes identity, communication, and component access
// without ever surfacing ecs.Entity, ecs.World, or ecs.Map* to game code.
//
// Player instances are owned by the engine and reused across handler
// invocations within a session's lifetime. Do NOT retain a *Player or any
// pointer derived from it (e.g. component pointers from GetComponent[T])
// past the end of the handler that received it.
type Player struct {
	stage *universe.Stage
	eng   *engine.Engine
	sess  *engine.PlayerSession
}

// newPlayer constructs a Player. Internal — built by the input dispatcher.
func newPlayer(stage *universe.Stage, eng *engine.Engine, sess *engine.PlayerSession) *Player {
	return &Player{stage: stage, eng: eng, sess: sess}
}

// Username returns the player's lowercase, validated username.
func (p *Player) Username() string { return p.sess.Username }

// ConnID returns the gateway-local connection ID for this session.
// Stable for the lifetime of a single connection.
func (p *Player) ConnID() uint32 { return p.sess.ConnID }

// State returns the player's current lifecycle state.
func (p *Player) State() engine.PlayerState { return p.sess.State }

// NetID returns the player entity's visible network identifier, or 0 if
// the session has no live entity.
func (p *Player) NetID() uint32 {
	if p.eng == nil || p.sess.Entity == (ecs.Entity{}) {
		return 0
	}
	if !p.eng.ECS.Alive(p.sess.Entity) {
		return 0
	}
	netMap := ecs.NewMap1[component.NetworkID](p.eng.ECS)
	if !netMap.HasAll(p.sess.Entity) {
		return 0
	}
	return netMap.Get(p.sess.Entity).ID
}

// TransitionState requests an engine-validated state machine transition.
// Returns the underlying engine error (e.g. illegal transition) without
// mutation if the transition is rejected.
func (p *Player) TransitionState(to engine.PlayerState) error {
	return p.eng.Players.Transition(p.sess, to)
}

// Stage returns the cell-level stage. Escape hatch for handlers that
// need cell-scoped operations (rare).
func (p *Player) Stage() *universe.Stage { return p.stage }

// Engine returns the engine handle. Escape hatch for handlers that need
// engine-level operations (rare).
func (p *Player) Engine() *engine.Engine { return p.eng }

// Session returns the underlying engine.PlayerSession. Escape hatch
// only — prefer the high-level methods.
func (p *Player) Session() *engine.PlayerSession { return p.sess }

// GetComponent returns a typed pointer to component T on the player's
// entity, or nil if the entity is dead or T is not attached. Component
// pointers are valid only for the duration of the handler call —
// retain at your peril (the underlying storage may be relocated by Ark
// on archetype changes).
//
//	pos := mmokit.GetComponent[mmokit.Position](p)
//	if pos != nil { /* ... */ }
func GetComponent[T any](p *Player) *T {
	if p == nil || p.eng == nil || p.sess.Entity == (ecs.Entity{}) {
		return nil
	}
	if !p.eng.ECS.Alive(p.sess.Entity) {
		return nil
	}
	m := ecs.NewMap1[T](p.eng.ECS)
	if !m.HasAll(p.sess.Entity) {
		return nil
	}
	return m.Get(p.sess.Entity)
}

// HasComponent returns true if T is attached to the player's entity.
func HasComponent[T any](p *Player) bool {
	if p == nil || p.eng == nil || p.sess.Entity == (ecs.Entity{}) {
		return false
	}
	if !p.eng.ECS.Alive(p.sess.Entity) {
		return false
	}
	m := ecs.NewMap1[T](p.eng.ECS)
	return m.HasAll(p.sess.Entity)
}
