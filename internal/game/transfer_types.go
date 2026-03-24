package game

import (
	comp "github.com/zenion/mmoserver/pkg/component"
	gamecomp "github.com/zenion/mmoserver/internal/component"
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

	Position comp.Position
	Sector   comp.SectorCoord
	Velocity comp.Velocity
	Rotation comp.Rotation
	Collider comp.Collider

	Health        *comp.Health
	Shield        *comp.Shield
	ShipControl   *gamecomp.ShipControl
	Equipment     *gamecomp.Equipment
	MoveTarget    *comp.MoveTarget
	AbilitySet    *gamecomp.AbilitySet
	Minable       *gamecomp.Minable
	Lifetime      *comp.Lifetime
	StatusEffects *gamecomp.StatusEffects

	// Deep-copied inventory
	CargoItems map[uint32]int32
	MaxCargo   float32
}

// ReplicaSnapshot is a lightweight entity snapshot for border replication.
type ReplicaSnapshot struct {
	NetworkID  uint32
	EntityType uint8
	Position   comp.Position
	Sector     comp.SectorCoord
	Velocity   comp.Velocity
	Rotation   comp.Rotation
	Collider   comp.Collider
	Health     *comp.Health
	Shield     *comp.Shield
	Minable    *gamecomp.Minable
}
