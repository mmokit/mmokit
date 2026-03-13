package ops

import "sync"

// PlayerSessions is a thread-safe map of connID → username.
// Updated from the game loop, read by operation router workers.
type PlayerSessions struct {
	mu       sync.RWMutex
	sessions map[uint32]string
}

// NewPlayerSessions creates a new PlayerSessions.
func NewPlayerSessions() *PlayerSessions {
	return &PlayerSessions{
		sessions: make(map[uint32]string),
	}
}

// Set registers a player session. Called from game loop on login.
func (ps *PlayerSessions) Set(connID uint32, username string) {
	ps.mu.Lock()
	ps.sessions[connID] = username
	ps.mu.Unlock()
}

// Remove unregisters a player session. Called from game loop on disconnect.
func (ps *PlayerSessions) Remove(connID uint32) {
	ps.mu.Lock()
	delete(ps.sessions, connID)
	ps.mu.Unlock()
}

// Get returns the username for a connID, or "" if not found.
func (ps *PlayerSessions) Get(connID uint32) string {
	ps.mu.RLock()
	username := ps.sessions[connID]
	ps.mu.RUnlock()
	return username
}
