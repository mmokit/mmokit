package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	comp "github.com/zenion/mmoserver/pkg/component"
)

// Components holds all ECS component mappers for direct access by systems.
type Components struct {
	Position         *ecs.Map1[comp.Position]
	Velocity         *ecs.Map1[comp.Velocity]
	Rotation         *ecs.Map1[comp.Rotation]
	Collider         *ecs.Map1[comp.Collider]
	NetworkID        *ecs.Map1[comp.NetworkID]
	EntityKind       *ecs.Map1[comp.EntityKind]
	ShipControl      *ecs.Map1[gamecomp.ShipControl]
	Health           *ecs.Map1[comp.Health]
	Shield           *ecs.Map1[comp.Shield]
	Lifetime         *ecs.Map1[comp.Lifetime]
	Minable          *ecs.Map1[gamecomp.Minable]
	MiningLaser      *ecs.Map1[gamecomp.MiningLaser]
	Inventory        *ecs.Map1[gamecomp.Inventory]
	PlayerConn       *ecs.Map1[comp.PlayerConn]
	PlayerInput      *ecs.Map1[gamecomp.PlayerInput]
	Station          *ecs.Map1[gamecomp.Station]
	LootCrate        *ecs.Map1[gamecomp.LootCrate]
	TargetLock       *ecs.Map1[comp.TargetLock]
	AbilitySet       *ecs.Map1[gamecomp.AbilitySet]
	StatusEffects    *ecs.Map1[gamecomp.StatusEffects]
	MoveTarget       *ecs.Map1[comp.MoveTarget]
	Equipment        *ecs.Map1[gamecomp.Equipment]
	SectorCoord      *ecs.Map1[comp.SectorCoord]
	Ghost            *ecs.Map1[comp.Ghost]
	Replica          *ecs.Map1[comp.Replica]
	TransferCooldown *ecs.Map1[comp.TransferCooldown]

	// ReplicaMapper is used for batch-creating replica entities (6 core components).
	ReplicaMapper *ecs.Map6[comp.Position, comp.Velocity, comp.Rotation, comp.Collider, comp.NetworkID, comp.EntityKind]
}

// NewComponents creates all component mappers for the given ECS world.
func NewComponents(world *ecs.World) *Components {
	return &Components{
		Position:         ecs.NewMap1[comp.Position](world),
		Velocity:         ecs.NewMap1[comp.Velocity](world),
		Rotation:         ecs.NewMap1[comp.Rotation](world),
		Collider:         ecs.NewMap1[comp.Collider](world),
		NetworkID:        ecs.NewMap1[comp.NetworkID](world),
		EntityKind:       ecs.NewMap1[comp.EntityKind](world),
		ShipControl:      ecs.NewMap1[gamecomp.ShipControl](world),
		Health:           ecs.NewMap1[comp.Health](world),
		Shield:           ecs.NewMap1[comp.Shield](world),
		Lifetime:         ecs.NewMap1[comp.Lifetime](world),
		Minable:          ecs.NewMap1[gamecomp.Minable](world),
		MiningLaser:      ecs.NewMap1[gamecomp.MiningLaser](world),
		Inventory:        ecs.NewMap1[gamecomp.Inventory](world),
		PlayerConn:       ecs.NewMap1[comp.PlayerConn](world),
		PlayerInput:      ecs.NewMap1[gamecomp.PlayerInput](world),
		Station:          ecs.NewMap1[gamecomp.Station](world),
		LootCrate:        ecs.NewMap1[gamecomp.LootCrate](world),
		TargetLock:       ecs.NewMap1[comp.TargetLock](world),
		AbilitySet:       ecs.NewMap1[gamecomp.AbilitySet](world),
		StatusEffects:    ecs.NewMap1[gamecomp.StatusEffects](world),
		MoveTarget:       ecs.NewMap1[comp.MoveTarget](world),
		Equipment:        ecs.NewMap1[gamecomp.Equipment](world),
		SectorCoord:      ecs.NewMap1[comp.SectorCoord](world),
		Ghost:            ecs.NewMap1[comp.Ghost](world),
		Replica:          ecs.NewMap1[comp.Replica](world),
		TransferCooldown: ecs.NewMap1[comp.TransferCooldown](world),
		ReplicaMapper:    ecs.NewMap6[comp.Position, comp.Velocity, comp.Rotation, comp.Collider, comp.NetworkID, comp.EntityKind](world),
	}
}
