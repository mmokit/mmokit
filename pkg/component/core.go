// Package component defines generic ECS components for a 2D authoritative
// game engine. Game-specific components should be defined in the game's own
// package; these are the shared building blocks that any 2D multiplayer game
// needs.
package component

import "math"

// defaultCellSize is the cell size used by MoveTarget.SetTarget. It is
// initialized to coords.CellSize by pkg/system at init() time. Tests use
// SetTargetWithCellSize directly to avoid depending on the global.
var defaultCellSize float32 = 1000

// SetDefaultCellSize wires the cell size used by MoveTarget.SetTarget.
// Called once by pkg/system.init(); games never call this.
func SetDefaultCellSize(size float32) { defaultCellSize = size }

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
// Epoch increments on each authority transfer and is used by receivers
// to drop stale frames from a previous owner.
type NetworkID struct {
	ID    uint32
	Epoch uint32
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

// Ghost marks an entity mid-transfer. Pure marker — no state. Any entity
// tagged Ghost is removed on the next TickGhosts pass; transfer state lives
// in the handoff protocol, not on the component.
type Ghost struct{}

// Replica is a read-only copy of an entity from a neighboring cell.
// Participates in spatial grid and AoI queries but is never mutated
// locally — position/velocity/components are refreshed solely by
// upsertBorderReplica applying inbound border frames.
//
// ProducedAtMs is the cluster-clock stamp from the authoritative
// source's most recent frame for this netID. It travels opaquely
// through this cell's outbound replication so downstream clients see
// one coherent timeline regardless of how many cells relayed the
// entity's state.
type Replica struct {
	SourceCellID string
	SourceNetID  uint32
	TTL          int    // ticks remaining before expiry (reset on refresh)
	ProducedAtMs uint64 // authoritative producer's ClusterClock.TickTime at emit (tick-aligned)
}

// TransferCooldown prevents rapid re-transfers after arriving on a new node.
// ArrivalWallMs is the wall-clock instant of arrival; PhysicsSystem reads
// it to scale the first post-transfer tick's dt down to the true elapsed
// wall-time since arrival. Without that scaling, a transfer that commits
// (say) 1 ms before the destination's next tick produces a full 50 ms of
// simulation in 1 ms of wire-stamped server_time, which clients interpret
// as a ~50×-velocity spike at the handoff boundary.
type TransferCooldown struct {
	Remaining     int    // ticks remaining
	ArrivalWallMs uint64 // wall-clock ms at arrival (time.Now().UnixMilli())
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
//
// LocalX/LocalY are cell-local coordinates within (CellX, CellY). Use
// SetTarget(worldX, worldY) to convert from world-absolute input. Sequence is
// an optional client-supplied counter used by games that ack movement.
type MoveTarget struct {
	LocalX, LocalY float32 // destination local coordinates within target cell
	CellX, CellY   int32   // cell of the destination
	Active         bool    // whether entity is moving to destination
	Sequence       uint32  // optional: client-supplied input sequence number
}

// SetTarget converts world-absolute coordinates to cell-local using the
// engine's default cell size (coords.CellSize) and activates the move.
// Use SetTargetWithCellSize for custom cell sizes (rare; tests only).
func (mt *MoveTarget) SetTarget(worldX, worldY float32) {
	mt.SetTargetWithCellSize(worldX, worldY, defaultCellSize)
}

// SetTargetWithCellSize converts world-absolute coordinates to cell-local
// using the given cell size and activates the move.
func (mt *MoveTarget) SetTargetWithCellSize(worldX, worldY, cellSize float32) {
	mt.CellX = int32(math.Floor(float64(worldX / cellSize)))
	mt.CellY = int32(math.Floor(float64(worldY / cellSize)))
	mt.LocalX = worldX - float32(mt.CellX)*cellSize
	mt.LocalY = worldY - float32(mt.CellY)*cellSize
	mt.Active = true
}

// Cancel deactivates movement. Other fields are untouched so the
// destination is preserved if the caller wants to resume.
func (mt *MoveTarget) Cancel() {
	mt.Active = false
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

