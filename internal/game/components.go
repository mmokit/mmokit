package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// Components holds all ECS component mappers for direct access by systems.
type Components struct {
	Position         *ecs.Map1[mmokit.Position]
	Velocity         *ecs.Map1[mmokit.Velocity]
	Rotation         *ecs.Map1[mmokit.Rotation]
	Collider         *ecs.Map1[mmokit.Collider]
	NetworkID        *ecs.Map1[mmokit.NetworkID]
	EntityKind       *ecs.Map1[mmokit.EntityKind]
	ShipControl      *ecs.Map1[gamecomp.ShipControl]
	Health           *ecs.Map1[mmokit.Health]
	Shield           *ecs.Map1[mmokit.Shield]
	Lifetime         *ecs.Map1[mmokit.Lifetime]
	Minable          *ecs.Map1[gamecomp.Minable]
	MiningLaser      *ecs.Map1[gamecomp.MiningLaser]
	Inventory        *ecs.Map1[gamecomp.Inventory]
	PlayerConn       *ecs.Map1[mmokit.PlayerConn]
	PlayerInput      *ecs.Map1[gamecomp.PlayerInput]
	Station          *ecs.Map1[gamecomp.Station]
	LootCrate        *ecs.Map1[gamecomp.LootCrate]
	TargetLock       *ecs.Map1[mmokit.TargetLock]
	AbilitySet       *ecs.Map1[gamecomp.AbilitySet]
	StatusEffects    *ecs.Map1[gamecomp.StatusEffects]
	MoveTarget       *ecs.Map1[mmokit.MoveTarget]
	Equipment        *ecs.Map1[gamecomp.Equipment]
	SectorCoord      *ecs.Map1[mmokit.SectorCoord]
	Ghost            *ecs.Map1[mmokit.Ghost]
	Replica          *ecs.Map1[mmokit.Replica]
	TransferCooldown *ecs.Map1[mmokit.TransferCooldown]

	// ReplicaMapper is used for batch-creating replica entities (6 core components).
	ReplicaMapper *ecs.Map6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind]
}

// NewComponents creates all component mappers for the given ECS world.
func NewComponents(world *ecs.World) *Components {
	return &Components{
		Position:         ecs.NewMap1[mmokit.Position](world),
		Velocity:         ecs.NewMap1[mmokit.Velocity](world),
		Rotation:         ecs.NewMap1[mmokit.Rotation](world),
		Collider:         ecs.NewMap1[mmokit.Collider](world),
		NetworkID:        ecs.NewMap1[mmokit.NetworkID](world),
		EntityKind:       ecs.NewMap1[mmokit.EntityKind](world),
		ShipControl:      ecs.NewMap1[gamecomp.ShipControl](world),
		Health:           ecs.NewMap1[mmokit.Health](world),
		Shield:           ecs.NewMap1[mmokit.Shield](world),
		Lifetime:         ecs.NewMap1[mmokit.Lifetime](world),
		Minable:          ecs.NewMap1[gamecomp.Minable](world),
		MiningLaser:      ecs.NewMap1[gamecomp.MiningLaser](world),
		Inventory:        ecs.NewMap1[gamecomp.Inventory](world),
		PlayerConn:       ecs.NewMap1[mmokit.PlayerConn](world),
		PlayerInput:      ecs.NewMap1[gamecomp.PlayerInput](world),
		Station:          ecs.NewMap1[gamecomp.Station](world),
		LootCrate:        ecs.NewMap1[gamecomp.LootCrate](world),
		TargetLock:       ecs.NewMap1[mmokit.TargetLock](world),
		AbilitySet:       ecs.NewMap1[gamecomp.AbilitySet](world),
		StatusEffects:    ecs.NewMap1[gamecomp.StatusEffects](world),
		MoveTarget:       ecs.NewMap1[mmokit.MoveTarget](world),
		Equipment:        ecs.NewMap1[gamecomp.Equipment](world),
		SectorCoord:      ecs.NewMap1[mmokit.SectorCoord](world),
		Ghost:            ecs.NewMap1[mmokit.Ghost](world),
		Replica:          ecs.NewMap1[mmokit.Replica](world),
		TransferCooldown: ecs.NewMap1[mmokit.TransferCooldown](world),
		ReplicaMapper:    ecs.NewMap6[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.Collider, mmokit.NetworkID, mmokit.EntityKind](world),
	}
}
