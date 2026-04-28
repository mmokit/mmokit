package universe

import (
	"sync"

	"github.com/zenion/mmoserver/pkg/engine"
)

// sessionResolver maps a PlayerSession to its owning cell. Populated by
// createNode at session-active time; consulted by mmokit's input invoker
// thunk to build a *Player.
//
// Sessions are uniquely owned by exactly one cell at a time. Cross-cell
// transfers update the mapping in the transfer-receive path.
var (
	sessionMu       sync.RWMutex
	sessionToStage  = make(map[*engine.PlayerSession]*Stage)
	sessionToEngine = make(map[*engine.PlayerSession]*engine.Engine)
)

// RegisterSessionStage records a session→cell binding. Idempotent.
func RegisterSessionStage(sess *engine.PlayerSession, stage *Stage, eng *engine.Engine) {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	sessionToStage[sess] = stage
	sessionToEngine[sess] = eng
}

// UnregisterSessionStage drops the binding (e.g. on disconnect).
func UnregisterSessionStage(sess *engine.PlayerSession) {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	delete(sessionToStage, sess)
	delete(sessionToEngine, sess)
}

// StageForSession returns the cell stage for a session, or nil.
func StageForSession(sess *engine.PlayerSession) *Stage {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return sessionToStage[sess]
}

// EngineForSession returns the engine for a session, or nil.
func EngineForSession(sess *engine.PlayerSession) *engine.Engine {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return sessionToEngine[sess]
}
