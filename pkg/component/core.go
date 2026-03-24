// Package component defines generic ECS components for a 2D authoritative
// game engine. Game-specific components should be defined in the game's own
// package; these are the shared building blocks that any 2D multiplayer game
// needs.
package component

import "github.com/mlange-42/ark/ecs"

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

// Health represents hit points.
type Health struct {
	Current float32
	Max     float32
}

// Shield represents shield points with regeneration.
type Shield struct {
	Current        float32
	Max            float32
	RegenRate      float32
	RegenDelay     float32 // seconds after damage before regen starts
	DamageCooldown float32 // time remaining before regen resumes
}

// Lifetime tracks remaining time before despawn.
type Lifetime struct {
	Remaining float32 // seconds
}

// PlayerConn links a player entity to its network connection.
type PlayerConn struct {
	ConnID uint32
}

// SectorCoord identifies which sector an entity belongs to.
type SectorCoord struct {
	SX, SY int32
}

// Ghost marks an entity mid-transfer. Visible in AoI but not mutated by game systems.
type Ghost struct {
	TTL        int    // ticks remaining before auto-removal
	DestNodeID string // which node the entity transferred to
}

// Replica is a read-only copy of an entity from a neighboring node.
// Participates in spatial grid and AoI queries but is never mutated.
type Replica struct {
	SourceNodeID string
	SourceNetID  uint32
	TTL          int // ticks remaining before expiry (reset on refresh)
}

// TransferCooldown prevents rapid re-transfers after arriving on a new node.
type TransferCooldown struct {
	Remaining int // ticks remaining
}

// MoveTarget holds a click-to-move destination.
type MoveTarget struct {
	X, Y   float32 // destination local coordinates within target sector
	SX, SY int32   // sector of the destination
	Active bool    // whether entity is moving to destination
}

// TargetLock holds lock-on state for targeting another entity.
type TargetLock struct {
	TargetEntity ecs.Entity // entity being locked/locked onto
	TargetNetID  uint32     // network ID of target
	Progress     float32    // 0.0 to 1.0 (1.0 = locked)
	LockTime     float32    // seconds required to achieve full lock
	Range        float32    // max lock range
	Locked       bool       // true when Progress >= 1.0
}
