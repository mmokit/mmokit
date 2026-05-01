package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"google.golang.org/protobuf/proto"
)

var (
	ErrInvalidTransition     = errors.New("invalid state transition")
	ErrTransitionGuardFailed = errors.New("transition guard returned false")
	ErrSessionNil            = errors.New("session is nil")
	// ErrLoginPending is returned by login handlers to indicate no login message
	// has arrived yet. The session stays pending and the handler is retried next tick.
	ErrLoginPending = errors.New("login pending")
)

// transitionKey uniquely identifies a From->To pair.
type transitionKey struct {
	From, To PlayerState
}

// PlayerManager owns player sessions and enforces lifecycle state transitions.
type PlayerManager struct {
	sessions   map[SessionID]*PlayerSession
	byConnID   map[uint32]*PlayerSession
	byUsername map[string]*PlayerSession

	states         map[PlayerState]string
	stateCallbacks map[PlayerState]*StateCallbacks
	transitions    map[transitionKey]*StateTransition

	gracePeriod   time.Duration
	nextSessionID SessionID
	nextState     PlayerState

	eng *Engine

	onSessionActive       func(username string) // called when player enters Active
	onSessionDisconnected func(username string) // called when player enters Disconnected
	onSessionRemoved      func(username string) // called when session is removed
}

// NewPlayerManager creates a PlayerManager with default built-in states and transitions.
func NewPlayerManager() *PlayerManager {
	pm := &PlayerManager{
		sessions:       make(map[SessionID]*PlayerSession),
		byConnID:       make(map[uint32]*PlayerSession),
		byUsername:     make(map[string]*PlayerSession),
		states:         make(map[PlayerState]string),
		stateCallbacks: make(map[PlayerState]*StateCallbacks),
		transitions:    make(map[transitionKey]*StateTransition),
		nextState:      StateBuiltinEnd,
	}

	for state, name := range builtinStateNames {
		pm.states[state] = name
	}

	defaults := []StateTransition{
		{From: StatePending, To: StateActive},
		{From: StateActive, To: StateTransferring},
		{From: StateActive, To: StateDisconnected},
		{From: StateTransferring, To: StateDisconnected},
		{From: StateDisconnected, To: StateActive},
	}
	for i := range defaults {
		pm.transitions[transitionKey{defaults[i].From, defaults[i].To}] = &defaults[i]
	}

	return pm
}

func (pm *PlayerManager) Engine() *Engine { return pm.eng }

func (pm *PlayerManager) RegisterState(name string) PlayerState {
	for state, n := range pm.states {
		if n == name {
			return state
		}
	}
	state := pm.nextState
	pm.nextState++
	pm.states[state] = name
	return state
}

func (pm *PlayerManager) StateName(state PlayerState) string {
	if name, ok := pm.states[state]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", state)
}

func (pm *PlayerManager) OnState(state PlayerState, cbs StateCallbacks) {
	pm.stateCallbacks[state] = &cbs
}

// StateCallbacks returns the currently-registered callbacks for state, or
// nil if none are registered. Used by code paths that need to compose
// with an existing registration rather than overwrite it (the universe
// layer's createNode-time wiring chains its hooks after any callbacks
// the world factory installed).
func (pm *PlayerManager) StateCallbacks(state PlayerState) *StateCallbacks {
	return pm.stateCallbacks[state]
}

func (pm *PlayerManager) AddTransition(t StateTransition) {
	key := transitionKey{t.From, t.To}
	copied := t
	pm.transitions[key] = &copied
}

func (pm *PlayerManager) AddTransitions(ts []StateTransition) {
	for _, t := range ts {
		pm.AddTransition(t)
	}
}

func (pm *PlayerManager) ByConnID(connID uint32) *PlayerSession {
	return pm.byConnID[connID]
}

func (pm *PlayerManager) ByUsername(username string) *PlayerSession {
	return pm.byUsername[username]
}

func (pm *PlayerManager) BySessionID(id SessionID) *PlayerSession {
	return pm.sessions[id]
}

func (pm *PlayerManager) InState(state PlayerState) []*PlayerSession {
	var result []*PlayerSession
	for _, s := range pm.sessions {
		if s.State == state {
			result = append(result, s)
		}
	}
	return result
}

func (pm *PlayerManager) ForEach(state PlayerState, fn func(s *PlayerSession)) {
	for _, s := range pm.sessions {
		if s.State == state {
			fn(s)
		}
	}
}

// ForEachConnected iterates all sessions that have an active connection (connID != 0)
// and are NOT in StatePending. Pending sessions are excluded because their input is
// consumed by processLogins() which runs earlier in the tick.
func (pm *PlayerManager) ForEachConnected(fn func(s *PlayerSession)) {
	for _, s := range pm.byConnID {
		if s.State != StatePending {
			fn(s)
		}
	}
}

func (pm *PlayerManager) Count(state PlayerState) int {
	n := 0
	for _, s := range pm.sessions {
		if s.State == state {
			n++
		}
	}
	return n
}

func (pm *PlayerManager) ConnectedCount() int {
	return len(pm.byConnID)
}

// Transition moves a session to a new state.
// Order: Guard -> Action -> OnExit(old) -> state update -> OnEnter(new).
func (pm *PlayerManager) Transition(s *PlayerSession, to PlayerState) error {
	if s == nil {
		return ErrSessionNil
	}

	key := transitionKey{s.State, to}
	trans, ok := pm.transitions[key]
	if !ok {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, pm.StateName(s.State), pm.StateName(to))
	}

	if trans.Guard != nil && !trans.Guard(s) {
		return fmt.Errorf("%w: %s -> %s", ErrTransitionGuardFailed, pm.StateName(s.State), pm.StateName(to))
	}

	if trans.Action != nil {
		trans.Action(s, pm)
	}

	if cbs, ok := pm.stateCallbacks[s.State]; ok && cbs.OnExit != nil {
		cbs.OnExit(s, pm)
	}

	s.State = to

	if cbs, ok := pm.stateCallbacks[to]; ok && cbs.OnEnter != nil {
		cbs.OnEnter(s, pm)
	}

	if to == StateActive && pm.onSessionActive != nil && s.Username != "" {
		pm.onSessionActive(s.Username)
	}
	if to == StateDisconnected && pm.onSessionDisconnected != nil && s.Username != "" {
		pm.onSessionDisconnected(s.Username)
	}

	return nil
}

func (pm *PlayerManager) createSession(connID uint32) *PlayerSession {
	pm.nextSessionID++
	s := &PlayerSession{
		ID:     pm.nextSessionID,
		ConnID: connID,
		State:  StatePending,
	}
	pm.sessions[s.ID] = s
	if connID != 0 {
		pm.byConnID[connID] = s
	}
	return s
}

func (pm *PlayerManager) Remove(s *PlayerSession) {
	if s == nil {
		return
	}

	if cbs, ok := pm.stateCallbacks[s.State]; ok && cbs.OnExit != nil {
		cbs.OnExit(s, pm)
	}

	if pm.onSessionRemoved != nil && s.Username != "" {
		pm.onSessionRemoved(s.Username)
	}

	delete(pm.sessions, s.ID)
	if s.ConnID != 0 {
		delete(pm.byConnID, s.ConnID)
	}
	if s.Username != "" {
		delete(pm.byUsername, s.Username)
	}
}

// AllSessions returns all sessions (for inspection during splits).
func (pm *PlayerManager) AllSessions() []*PlayerSession {
	result := make([]*PlayerSession, 0, len(pm.sessions))
	for _, s := range pm.sessions {
		result = append(result, s)
	}
	return result
}

// RegisterSessionTransfer creates a session in a specific state (by name).
// Used during cell splits for entity-less sessions (docked, dead players).
//
// data is the opaque game-state blob serialized by SessionTransfer; the
// engine no longer carries it on PlayerSession itself — game code is
// responsible for stashing per-session state via mmokit-registered
// state factories or its own side maps.
func (pm *PlayerManager) RegisterSessionTransfer(connID uint32, username string, stateName string, data any) {
	_ = data // game responsibility; see method doc
	s := pm.byConnID[connID]
	if s == nil {
		s = pm.createSession(connID)
	}
	s.Username = username
	pm.byUsername[username] = s

	// Find the state by name and set directly (skip transition/callbacks)
	for state, name := range pm.states {
		if name == stateName {
			s.State = state
			if pm.onSessionActive != nil && s.Username != "" {
				pm.onSessionActive(s.Username)
			}
			return
		}
	}
	// Fallback: set to Active if state name not found
	s.State = StateActive
	if pm.onSessionActive != nil && s.Username != "" {
		pm.onSessionActive(s.Username)
	}
}

func (pm *PlayerManager) SetGracePeriod(d time.Duration) {
	pm.gracePeriod = d
}


// SetSessionCallbacks sets coordinator-level session tracking callbacks.
// These are called during state transitions and session removal.
func (pm *PlayerManager) SetSessionCallbacks(
	onActive func(username string),
	onDisconnected func(username string),
	onRemoved func(username string),
) {
	pm.onSessionActive = onActive
	pm.onSessionDisconnected = onDisconnected
	pm.onSessionRemoved = onRemoved
}

// ReconnectSession re-activates a disconnected session with a new connection ID.
// Called by the coordinator when routing a reconnecting player to the correct node.
func (pm *PlayerManager) ReconnectSession(s *PlayerSession) {
	pm.byConnID[s.ConnID] = s
	if err := pm.Transition(s, s.PriorState); err != nil {
		pm.Remove(s)
	}
}

// RegisterTransferSession creates a pending session for an entity transfer.
// The entity is already created by SpawnFromTransfer; processPendingSessions
// sets state to Active directly (skipping OnEnter to avoid duplicate spawn).
func (pm *PlayerManager) RegisterTransferSession(connID uint32, username string) {
	s := pm.byConnID[connID]
	if s == nil {
		s = pm.createSession(connID)
	}
	s.Username = username
	s.isTransfer = true
	pm.byUsername[username] = s
}

// RegisterPlayer creates a pending session for a coordinator-assigned player
// or respawn transfer. Fires the normal OnEnter callback (SpawnPlayer) when
// transitioning to Active.
func (pm *PlayerManager) RegisterPlayer(connID uint32, username string) {
	s := pm.byConnID[connID]
	if s == nil {
		s = pm.createSession(connID)
	}
	s.Username = username
	pm.byUsername[username] = s
}

func (pm *PlayerManager) sendServerConfig(connID uint32) {
	msg := &enginepb.ServerConfigMsg{
		TickRate: uint32(pm.eng.Config.TickRate),
	}
	inner, err := proto.Marshal(msg)
	if err != nil {
		return
	}
	evt := &enginepb.ServerEvent{
		Code: uint32(enginepb.ServerEventCode_SE_SERVER_CONFIG),
		Data: inner,
	}
	evtData, err := proto.Marshal(evt)
	if err != nil {
		return
	}
	frame := make([]byte, 1+len(evtData))
	frame[0] = 0x00 // event channel
	copy(frame[1:], evtData)
	pm.eng.ConnMgr.Send(connID, frame)
}

func (pm *PlayerManager) hooks() Hooks {
	return Hooks{
		OnConnect: func(connID uint32) {
			pm.createSession(connID)
			pm.sendServerConfig(connID)
		},
		OnDisconnect: func(connID uint32) {
			s := pm.byConnID[connID]
			if s == nil {
				return
			}
			if s.State == StatePending {
				pm.Remove(s)
				return
			}
			// Always transition through Disconnected so transition Actions
			// and StateDisconnected.OnExit run correctly for cleanup.
			s.PriorState = s.State
			s.DisconnectTime = time.Now()
			if err := pm.Transition(s, StateDisconnected); err != nil {
				pm.Remove(s)
				return
			}
			if pm.gracePeriod > 0 {
				// Keep session alive for reconnection
				s.ConnID = 0
				delete(pm.byConnID, connID)
			} else {
				// Immediate cleanup
				pm.Remove(s)
			}
		},
		PostTick: func() {
			pm.expireGracePeriods()
		},
	}
}

func (pm *PlayerManager) processPendingSessions() {
	var pending []*PlayerSession
	for _, s := range pm.sessions {
		if s.State == StatePending {
			pending = append(pending, s)
		}
	}

	for _, s := range pending {
		if _, ok := pm.sessions[s.ID]; !ok {
			continue
		}

		if s.isTransfer {
			// Hard-cut handoff defers the entity spawn to the commit-tick
			// PostSystems pass (drainPendingPromotes). In the window between
			// MsgHandoff receive and that spawn, sess.Entity is still the
			// zero value. Activating the session prematurely would let
			// InputRouter.ForEachConnected pick it up in the next Systems
			// phase, invoke handlers with ctx.Entity == zero, and any
			// handler calling ECS.HasAll/Get on it would panic with
			// "can't check components of a dead entity" (Ark treats the
			// zero handle as dead). Stay Pending until the entity exists.
			if s.Entity == (ecs.Entity{}) || !pm.eng.ECS.Alive(s.Entity) {
				continue
			}
			s.isTransfer = false
			// Set state directly — skip OnEnter. The entity is already created
			// by SpawnFromTransfer; firing OnEnter would spawn a duplicate.
			s.State = StateActive
			// Still notify coordinator so activeUsers tracks the new node.
			if pm.onSessionActive != nil && s.Username != "" {
				pm.onSessionActive(s.Username)
			}
			continue
		}

		// Sessions with username set (from coordinator or respawn transfer)
		if s.Username == "" {
			continue
		}

		pm.byUsername[s.Username] = s
		if err := pm.Transition(s, StateActive); err != nil {
			pm.Remove(s)
		}
	}
}

func (pm *PlayerManager) expireGracePeriods() {
	if pm.gracePeriod <= 0 {
		return
	}
	now := time.Now()
	var expired []*PlayerSession
	for _, s := range pm.sessions {
		if s.State == StateDisconnected && now.Sub(s.DisconnectTime) > pm.gracePeriod {
			expired = append(expired, s)
		}
	}
	for _, s := range expired {
		pm.Remove(s)
	}
}
