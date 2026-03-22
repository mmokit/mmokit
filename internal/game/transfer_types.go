package game

import "github.com/zenion/mmoserver/internal/component"

// TransferPayload contains all component data for an entity handoff.
type TransferPayload struct {
	NetworkID  uint32
	EntityType uint8
	ConnID     uint32 // 0 for non-player entities
	Username   string // "" for non-player entities
	SourceTick uint32 // source node's tick counter for dead reckoning

	Position component.Position
	Sector   component.SectorCoord
	Velocity component.Velocity
	Rotation component.Rotation
	Collider component.Collider

	Health        *component.Health
	Shield        *component.Shield
	ShipControl   *component.ShipControl
	Equipment     *component.Equipment
	MoveTarget    *component.MoveTarget
	AbilitySet    *component.AbilitySet
	Minable       *component.Minable
	Lifetime      *component.Lifetime
	StatusEffects *component.StatusEffects

	// Deep-copied inventory
	CargoItems map[uint32]int32
	MaxCargo   float32
}

// ArrivalConfirmMsg confirms entity arrived on destination node.
type ArrivalConfirmMsg struct {
	NetworkID uint32
	ConnID    uint32 // non-zero for player entities
}

// ReplicaSnapshot is a lightweight entity snapshot for border replication.
type ReplicaSnapshot struct {
	NetworkID  uint32
	EntityType uint8
	Position   component.Position
	Sector     component.SectorCoord
	Velocity   component.Velocity
	Rotation   component.Rotation
	Collider   component.Collider
	Health     *component.Health
	Shield     *component.Shield
	Minable    *component.Minable
}

// ChatRelay relays chat messages across nodes.
type ChatRelay struct {
	Username string
	Text     string
}

// RespawnTransfer requests a player respawn on another node.
type RespawnTransfer struct {
	ConnID   uint32
	Username string
}
