package game

import (
	"github.com/zenion/mmoserver/internal/component"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// Re-export generic message types from pkg/universe.
type (
	ArrivalConfirmMsg = pkguniverse.ArrivalConfirmMsg
	ChatRelay         = pkguniverse.ChatRelay
	RespawnTransfer   = pkguniverse.RespawnTransfer
)

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
