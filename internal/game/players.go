package game

import "github.com/mlange-42/ark/ecs"

// PlayerTracker manages all player-connection state.
// Fields are exported for direct access by systems.
type PlayerTracker struct {
	// Entities maps connID → ECS entity for active (in-world) players.
	Entities map[uint32]ecs.Entity
	// Usernames maps connID → username for all logged-in connections.
	Usernames map[uint32]string
	// Dead tracks connIDs of players awaiting respawn.
	Dead map[uint32]bool
	// Docked tracks connIDs of players currently docked at a station.
	Docked map[uint32]bool
	// Docking tracks connIDs of players mid-docking animation.
	Docking map[uint32]*DockingState
	// PendingConnections tracks connIDs that connected but haven't logged in.
	PendingConnections map[uint32]bool
	// PendingLogins tracks connIDs with login requests to process (from transfers).
	PendingLogins map[uint32]string
}

// NewPlayerTracker creates a PlayerTracker with initialized maps.
func NewPlayerTracker() *PlayerTracker {
	return &PlayerTracker{
		Entities:           make(map[uint32]ecs.Entity),
		Usernames:          make(map[uint32]string),
		Dead:               make(map[uint32]bool),
		Docked:             make(map[uint32]bool),
		Docking:            make(map[uint32]*DockingState),
		PendingConnections: make(map[uint32]bool),
		PendingLogins:      make(map[uint32]string),
	}
}

// Remove cleans up all state for a disconnecting player.
func (p *PlayerTracker) Remove(connID uint32) {
	delete(p.Entities, connID)
	delete(p.Usernames, connID)
	delete(p.Dead, connID)
	delete(p.Docked, connID)
	delete(p.Docking, connID)
	delete(p.PendingConnections, connID)
	delete(p.PendingLogins, connID)
}

// UsernameInUse returns true if the given username is already connected.
func (p *PlayerTracker) UsernameInUse(username string) bool {
	for _, u := range p.Usernames {
		if u == username {
			return true
		}
	}
	return false
}
