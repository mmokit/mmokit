// Package service — events.go defines the framework-emitted event types
// fired on the per-process service.Bus. Each type is a small POD struct
// with no shared inheritance; service authors subscribe via
// service.Subscribe[T](ctx.Bus, handler).
//
// Phase 1: only SessionEnterEvent + SessionLeaveEvent are published by
// the engine (gateway login/disconnect paths). The remaining types are
// declared here so future phases (auth event extraction in Phase 2,
// player-spawn wiring in a later phase) can land additively.
//
// Wire-stability: type names are registered with the event codec in
// init(); Go-level renames break the wire identity. Same convention as
// pkg/services/auth typed messages.
package service

func init() {
	RegisterEventType[SessionEnterEvent]()
	RegisterEventType[SessionLeaveEvent]()
	RegisterEventType[AuthLoginSucceededEvent]()
	RegisterEventType[AuthLogoutEvent]()
	RegisterEventType[AuthRegisteredEvent]()
	RegisterEventType[PlayerSpawnedEvent]()
	RegisterEventType[PlayerDespawnedEvent]()
}

// SessionEnterEvent fires after a successful auth login + cell dispatch.
// Published by the gateway. Consumed by services that need per-session
// state (chat presence, presence service, achievements, etc).
type SessionEnterEvent struct {
	ConnID    uint32
	UserID    string
	Username  string
	GatewayID string
}

// SessionLeaveEvent fires when a WS connection closes (clean disconnect,
// gateway crash recovery, kick, etc). Published by the gateway.
type SessionLeaveEvent struct {
	ConnID    uint32
	UserID    string // populated when known; empty for unauthenticated drops
	GatewayID string
}

// AuthLoginSucceededEvent fires after a successful AUTH_LOGIN /
// AUTH_REGISTER / AUTH_VALIDATE_TOKEN op. Published by the auth service
// (Phase 2 wiring).
type AuthLoginSucceededEvent struct {
	UserID       string
	Username     string
	SessionToken string // populated on AUTH_LOGIN / AUTH_REGISTER; empty on validate
	ConnID       uint32
	GatewayID    string
}

// AuthLogoutEvent fires after an explicit AUTH_LOGOUT op. NOT fired on
// WS close — that's SessionLeaveEvent. Phase 2 wiring.
type AuthLogoutEvent struct {
	UserID    string
	Username  string
	ConnID    uint32
	GatewayID string
}

// AuthRegisteredEvent fires after a successful AUTH_REGISTER op. Lets
// achievements / starter-pack / welcome-message services run on first
// login. Phase 2 wiring.
type AuthRegisteredEvent struct {
	UserID   string
	Username string
}

// PlayerSpawnedEvent fires after a player's entity is created on its
// authoritative cell. Wired in a later phase (cell host integration).
type PlayerSpawnedEvent struct {
	UserID   string
	Username string
	CellID   string
	NetID    uint32
}

// PlayerDespawnedEvent fires when a player's entity is removed
// (disconnect, transfer, kick). Wired in a later phase.
type PlayerDespawnedEvent struct {
	UserID string
	NetID  uint32
}
