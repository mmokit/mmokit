package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TransferPayload contains all component data for an entity handoff.
type TransferPayload struct {
	NetworkID  uint32
	EntityType uint8
	ConnID     uint32 // 0 for non-player entities
	Username   string // "" for non-player entities
	SourceTick uint32 // source node's tick counter for dead reckoning

	Position mmokit.Position
	Sector   mmokit.SectorCoord
	Velocity mmokit.Velocity
	Rotation mmokit.Rotation
	Collider mmokit.Collider

	Health        *mmokit.Health
	Shield        *mmokit.Shield
	ShipControl   *gamecomp.ShipControl
	Equipment     *gamecomp.Equipment
	MoveTarget    *mmokit.MoveTarget
	AbilitySet    *gamecomp.AbilitySet
	Minable       *gamecomp.Minable
	Lifetime      *mmokit.Lifetime
	StatusEffects *gamecomp.StatusEffects

	// Target lock state (NetID-based, re-resolved on destination node)
	LockTargetNetID uint32
	LockProgress    float32
	LockLocked      bool

	// Deep-copied inventory
	CargoItems map[uint32]int32
	MaxCargo   float32
}

