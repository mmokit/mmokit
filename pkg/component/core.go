// Package component defines generic ECS components for a 2D authoritative
// game engine. Game-specific components should be defined in the game's own
// package; these are the shared building blocks that any 2D multiplayer game
// needs.
package component

// Position in world space.
type Position struct {
	X, Y float32
}

// Velocity in world units per second.
type Velocity struct {
	X, Y float32
}

// Rotation facing direction.
type Rotation struct {
	Angle float32 // radians
}

// Collider defines a collision shape (circle or oriented rectangle).
// For circles, use Radius. For rects, use Width (forward) and Height (side).
// Radius is also used as the bounding radius for broad-phase checks on rects.
type Collider struct {
	Radius float32 // circle radius, or bounding radius for rects
	Width  float32 // rect extent along local X (forward axis)
	Height float32 // rect extent along local Y (side axis)
	Layer  uint8
	Shape  uint8
}

// NetworkID is a stable identifier sent to clients.
type NetworkID struct {
	ID uint32
}

// EntityKind identifies the type of entity for the client.
type EntityKind struct {
	Type uint8
}

// Lifetime tracks remaining time before despawn.
type Lifetime struct {
	Remaining float32 // seconds
}

// PlayerConn links a player entity to its network connection.
type PlayerConn struct {
	ConnID uint32
}

// CellCoord identifies which cell an entity belongs to.
type CellCoord struct {
	CellX, CellY int32
}

// Ghost marks an entity mid-transfer. Visible in AoI but not mutated by game systems.
type Ghost struct {
	TTL        int    // ticks remaining before auto-removal
	DestNodeID string // which node the entity transferred to
	Confirmed  bool   // true after arrival confirm; ghost stays until replica replaces it
}

// Replica is a read-only copy of an entity from a neighboring node.
// Participates in spatial grid and AoI queries but is never mutated.
type Replica struct {
	SourceNodeID    string
	SourceNetID     uint32
	TTL             int  // ticks remaining before expiry (reset on refresh)
	UpdatedThisTick bool // set by ApplyReplicas, cleared each tick start
}

// TransferCooldown prevents rapid re-transfers after arriving on a new node.
type TransferCooldown struct {
	Remaining int // ticks remaining
}

// Proxy is a lightweight representation of an entity on a neighboring node.
// Unlike Replica (full component copy), a Proxy carries only position, velocity,
// bounding radius, and entity type — enough for spatial queries and collision
// broad-phase. Promoted to a full Replica on demand when a player's AoI or
// collision detection requires full state.
//
// Systems that exclude Ghost/Replica should also exclude Proxy.
type Proxy struct {
	SourceNodeID    string
	SourceNetID     uint32
	EntityType      uint8
	BoundingRadius  float32
	VelX, VelY      float32 // for dead-reckoning between updates
	TTL             int
	UpdatedThisTick bool
	Promoted        bool // true once detail has been requested
}

// Dormant marks an entity as sleeping. Dormant entities are excluded from
// border scanning (no proxy summaries sent), game system updates, and client
// replication. They wake when a viewer (local or proxy from a neighbor) enters
// proximity on the authoritative node.
//
// Systems that should skip dormant entities add Without(ecs.C[Dormant]()) to
// their filters.
type Dormant struct{}

// MoveTarget holds a click-to-move destination.
type MoveTarget struct {
	X, Y         float32 // destination local coordinates within target cell
	CellX, CellY int32   // cell of the destination
	Active       bool    // whether entity is moving to destination
}

// MoveParams holds per-entity movement configuration.
// Optional — movement systems use their defaults if this component is absent.
type MoveParams struct {
	MaxSpeed float32 // units/sec; 0 means use system default
}

// DirectionInput holds WASD/joystick direction state.
// Set by the game's input handler each tick.
type DirectionInput struct {
	X, Y   float32 // direction vector (normalized by client)
	Active bool    // currently holding a direction key
}

