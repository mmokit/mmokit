package engine

import (
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/coords"
)

// PlayerState represents a player's lifecycle state.
type PlayerState uint8

const (
	StatePending      PlayerState = iota // connected, not yet logged in
	StateActive                          // in-world with an ECS entity
	StateTransferring                    // mid-node-transfer
	StateDisconnected                    // grace period, may reconnect
	stateBuiltinEnd                      // marker for custom state registration
)

// builtinStateNames maps built-in states to their names.
var builtinStateNames = map[PlayerState]string{
	StatePending:      "pending",
	StateActive:       "active",
	StateTransferring: "transferring",
	StateDisconnected: "disconnected",
}

// SessionID uniquely identifies a player session.
type SessionID uint64

// PlayerSession tracks a single player's connection and lifecycle state.
type PlayerSession struct {
	ID             SessionID
	ConnID         uint32      // 0 = no active connection
	Username       string
	State          PlayerState
	Entity         ecs.Entity  // zero-value when no entity exists
	Data           any         // game-specific session data
	PriorState     PlayerState // state before disconnect, for reconnect resume
	DisconnectTime time.Time   // when connection was lost

	// SpawnLocation is the world-space point the gateway resolved for this
	// session's login (or the most recent respawn/teleport). Populated by
	// the gateway before dispatching the PlayerAssignment; read by the
	// game's OnEnter handler via gw.SpawnAtLocation(s.SpawnLocation, ...).
	SpawnLocation coords.Location

	isTransfer bool // true if created via RegisterTransferSession (entity already exists)
}

// StateTransition defines a valid state change with optional guard and action.
type StateTransition struct {
	From   PlayerState
	To     PlayerState
	Guard  func(s *PlayerSession) bool               // optional, can block transition
	Action func(s *PlayerSession, pm *PlayerManager)  // runs during transition (before OnExit)
}

// StateCallbacks are invoked when entering or exiting a state.
type StateCallbacks struct {
	OnEnter func(s *PlayerSession, pm *PlayerManager)
	OnExit  func(s *PlayerSession, pm *PlayerManager)
}
