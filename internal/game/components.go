package game

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
)

// Components holds all ECS component mappers for direct access by systems.
type Components struct {
	Position         *ecs.Map1[component.Position]
	Velocity         *ecs.Map1[component.Velocity]
	Rotation         *ecs.Map1[component.Rotation]
	Collider         *ecs.Map1[component.Collider]
	NetworkID        *ecs.Map1[component.NetworkID]
	EntityKind       *ecs.Map1[component.EntityKind]
	ShipControl      *ecs.Map1[component.ShipControl]
	Health           *ecs.Map1[component.Health]
	Shield           *ecs.Map1[component.Shield]
	Lifetime         *ecs.Map1[component.Lifetime]
	Minable          *ecs.Map1[component.Minable]
	MiningLaser      *ecs.Map1[component.MiningLaser]
	Inventory        *ecs.Map1[component.Inventory]
	PlayerConn       *ecs.Map1[component.PlayerConn]
	PlayerInput      *ecs.Map1[component.PlayerInput]
	Station          *ecs.Map1[component.Station]
	LootCrate        *ecs.Map1[component.LootCrate]
	TargetLock       *ecs.Map1[component.TargetLock]
	AbilitySet       *ecs.Map1[component.AbilitySet]
	StatusEffects    *ecs.Map1[component.StatusEffects]
	MoveTarget       *ecs.Map1[component.MoveTarget]
	Equipment        *ecs.Map1[component.Equipment]
	SectorCoord      *ecs.Map1[component.SectorCoord]
	Ghost            *ecs.Map1[component.Ghost]
	Replica          *ecs.Map1[component.Replica]
	TransferCooldown *ecs.Map1[component.TransferCooldown]

	// ReplicaMapper is used for batch-creating replica entities (6 core components).
	ReplicaMapper *ecs.Map6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind]
}

// NewComponents creates all component mappers for the given ECS world.
func NewComponents(world *ecs.World) *Components {
	return &Components{
		Position:         ecs.NewMap1[component.Position](world),
		Velocity:         ecs.NewMap1[component.Velocity](world),
		Rotation:         ecs.NewMap1[component.Rotation](world),
		Collider:         ecs.NewMap1[component.Collider](world),
		NetworkID:        ecs.NewMap1[component.NetworkID](world),
		EntityKind:       ecs.NewMap1[component.EntityKind](world),
		ShipControl:      ecs.NewMap1[component.ShipControl](world),
		Health:           ecs.NewMap1[component.Health](world),
		Shield:           ecs.NewMap1[component.Shield](world),
		Lifetime:         ecs.NewMap1[component.Lifetime](world),
		Minable:          ecs.NewMap1[component.Minable](world),
		MiningLaser:      ecs.NewMap1[component.MiningLaser](world),
		Inventory:        ecs.NewMap1[component.Inventory](world),
		PlayerConn:       ecs.NewMap1[component.PlayerConn](world),
		PlayerInput:      ecs.NewMap1[component.PlayerInput](world),
		Station:          ecs.NewMap1[component.Station](world),
		LootCrate:        ecs.NewMap1[component.LootCrate](world),
		TargetLock:       ecs.NewMap1[component.TargetLock](world),
		AbilitySet:       ecs.NewMap1[component.AbilitySet](world),
		StatusEffects:    ecs.NewMap1[component.StatusEffects](world),
		MoveTarget:       ecs.NewMap1[component.MoveTarget](world),
		Equipment:        ecs.NewMap1[component.Equipment](world),
		SectorCoord:      ecs.NewMap1[component.SectorCoord](world),
		Ghost:            ecs.NewMap1[component.Ghost](world),
		Replica:          ecs.NewMap1[component.Replica](world),
		TransferCooldown: ecs.NewMap1[component.TransferCooldown](world),
		ReplicaMapper:    ecs.NewMap6[component.Position, component.Velocity, component.Rotation, component.Collider, component.NetworkID, component.EntityKind](world),
	}
}
