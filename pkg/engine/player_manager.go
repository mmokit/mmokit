package engine

import (
	"errors"
	"fmt"
	"time"

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

	onLogin         func(s *PlayerSession, pm *PlayerManager) error
	onLoginRejected func(connID uint32, reason string)

	eng *Engine
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
		nextState:      stateBuiltinEnd,
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

	delete(pm.sessions, s.ID)
	if s.ConnID != 0 {
		delete(pm.byConnID, s.ConnID)
	}
	if s.Username != "" {
		delete(pm.byUsername, s.Username)
	}
}

func (pm *PlayerManager) SetGracePeriod(d time.Duration) {
	pm.gracePeriod = d
}

// SetLoginHandler sets the callback invoked each tick for pending sessions.
// Return nil for successful login (s.Username must be set).
// Return ErrLoginPending if no login message was received yet (retried next tick).
// Return any other error to reject the login (error message sent to client).
func (pm *PlayerManager) SetLoginHandler(fn func(s *PlayerSession, pm *PlayerManager) error) {
	pm.onLogin = fn
}

func (pm *PlayerManager) SetLoginRejectedHandler(fn func(connID uint32, reason string)) {
	pm.onLoginRejected = fn
}

func (pm *PlayerManager) RegisterPendingLogin(connID uint32, username string) {
	s := pm.byConnID[connID]
	if s == nil {
		s = pm.createSession(connID)
	}
	s.Username = username
	s.isTransferLogin = true
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
		ProcessLogins: func() {
			pm.processLogins()
		},
		PostTick: func() {
			pm.expireGracePeriods()
		},
	}
}

func (pm *PlayerManager) processLogins() {
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

		if s.isTransferLogin {
			s.isTransferLogin = false
			// Set state directly — skip OnEnter. The entity is already created
			// by SpawnFromTransfer; firing OnEnter would spawn a duplicate.
			s.State = StateActive
			continue
		}

		if pm.onLogin == nil {
			continue
		}
		if err := pm.onLogin(s, pm); err != nil {
			if errors.Is(err, ErrLoginPending) {
				continue
			}
			if pm.onLoginRejected != nil && s.ConnID != 0 {
				pm.onLoginRejected(s.ConnID, err.Error())
			}
			pm.Remove(s)
			continue
		}

		if s.Username == "" {
			continue
		}

		if existing := pm.byUsername[s.Username]; existing != nil && existing != s && existing.State == StateDisconnected {
			existing.ConnID = s.ConnID
			existing.DisconnectTime = time.Time{}
			pm.byConnID[s.ConnID] = existing

			delete(pm.sessions, s.ID)

			if err := pm.Transition(existing, existing.PriorState); err != nil {
				pm.Remove(existing)
			}
			continue
		}

		if existing := pm.byUsername[s.Username]; existing != nil && existing != s {
			if pm.onLoginRejected != nil && s.ConnID != 0 {
				pm.onLoginRejected(s.ConnID, "Username already connected")
			}
			pm.Remove(s)
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
