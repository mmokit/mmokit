package game

import (
	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// FinishTransferSpawn adds entity-type-specific components and defaults
// after core + game components have been applied.
func (gw *GameWorld) FinishTransferSpawn(entity ecs.Entity, frame *mmokit.TransferFrame) {
	switch frame.EntityType {
	case gamecomp.TypeShip:
		// Override collider to match game config
		if gw.C.Collider.HasAll(entity) {
			col := gw.C.Collider.Get(entity)
			br := boundingRadius(gw.Config.ShipWidth, gw.Config.ShipHeight)
			col.Radius = br
			col.Width = gw.Config.ShipWidth
			col.Height = gw.Config.ShipHeight
			col.Layer = gamecomp.LayerPlayer
			col.Shape = mmokit.ShapeRect
		}
		// Add components not in transfer but always needed for ships
		if !gw.C.PlayerInput.HasAll(entity) {
			gw.C.PlayerInput.Add(entity, &gamecomp.PlayerInput{})
		}
		if !gw.C.MiningLaser.HasAll(entity) {
			gw.C.MiningLaser.Add(entity, &gamecomp.MiningLaser{})
		}
		// Apply defaults for optional components that weren't transferred
		if !gw.C.Health.HasAll(entity) {
			gw.C.Health.Add(entity, &gamecomp.Health{Current: gw.Config.ShipHealth, Max: gw.Config.ShipHealth})
		}
		if !gw.C.Shield.HasAll(entity) {
			gw.C.Shield.Add(entity, &gamecomp.Shield{Current: gw.Config.ShipShield, Max: gw.Config.ShipShield, RegenRate: gw.Config.ShieldRegenRate, RegenDelay: gw.Config.ShieldRegenDelay})
		}
		if !gw.C.ShipControl.HasAll(entity) {
			gw.C.ShipControl.Add(entity, &gamecomp.ShipControl{Thrust: gw.Config.ShipThrust, TurnRate: gw.Config.ShipTurnRate, MaxSpeed: gw.Config.MaxSpeed})
		}
		if !gw.C.AbilitySet.HasAll(entity) {
			gw.C.AbilitySet.Add(entity, &gamecomp.AbilitySet{})
		}
		if !gw.C.StatusEffects.HasAll(entity) {
			gw.C.StatusEffects.Add(entity, &gamecomp.StatusEffects{})
		}
		if !gw.C.MoveTarget.HasAll(entity) {
			gw.C.MoveTarget.Add(entity, &mmokit.MoveTarget{})
		}
		if !gw.C.Inventory.HasAll(entity) {
			gw.C.Inventory.Add(entity, &gamecomp.Inventory{MaxMass: gw.Config.MaxCargo})
		}
		if !gw.C.Equipment.HasAll(entity) {
			gw.C.Equipment.Add(entity, &gamecomp.Equipment{})
		}
		gw.ApplyEquipmentStats(entity)

		// TargetLock: restore from component if transferred, else add default
		if !gw.C.TargetLock.HasAll(entity) {
			gw.C.TargetLock.Add(entity, &gamecomp.TargetLock{
				LockTime: gw.Config.LockOnTime,
				Range:    gw.Config.LockOnRange,
			})
		}

	case gamecomp.TypeNPC:
		// Override collider
		if gw.C.Collider.HasAll(entity) {
			col := gw.C.Collider.Get(entity)
			col.Radius = boundingRadius(gw.Config.NpcWidth, gw.Config.NpcHeight)
			col.Width = gw.Config.NpcWidth
			col.Height = gw.Config.NpcHeight
			col.Layer = gamecomp.LayerPlayer
			col.Shape = mmokit.ShapeRect
		}
		// Defaults
		if !gw.C.Health.HasAll(entity) {
			gw.C.Health.Add(entity, &gamecomp.Health{Current: gw.Config.NpcHealth, Max: gw.Config.NpcHealth})
		}
		if !gw.C.Shield.HasAll(entity) {
			gw.C.Shield.Add(entity, &gamecomp.Shield{Current: gw.Config.NpcShield, Max: gw.Config.NpcShield, RegenRate: gw.Config.NpcShieldRegenRate, RegenDelay: gw.Config.NpcShieldRegenDelay})
		}
		if !gw.C.StatusEffects.HasAll(entity) {
			gw.C.StatusEffects.Add(entity, &gamecomp.StatusEffects{})
		}

	case gamecomp.TypeLootCrate:
		if !gw.C.Lifetime.HasAll(entity) {
			gw.C.Lifetime.Add(entity, &mmokit.Lifetime{Remaining: gw.Config.LootCrateLifetime})
		}
		if !gw.C.Inventory.HasAll(entity) {
			gw.C.Inventory.Add(entity, &gamecomp.Inventory{})
		}
		gw.C.LootCrate.Add(entity, &gamecomp.LootCrate{})

	case gamecomp.TypeAsteroid:
		// No special setup needed beyond applied components
	}
}

// WireTransferPlayer handles post-transfer player session wiring.
// Called from the universe adapter after SpawnFromTransferCore + FinishTransferSpawn.
// Does NOT call reconnectPlayer — that sends SE_PLAYER_SPAWNED which clears client entities
// and causes a visual blink. The adapter sends SE_CELL_CHANGE instead.
func (gw *GameWorld) WireTransferPlayer(entity ecs.Entity, s *mmokit.PlayerSession) {
	s.Entity = entity
	gw.updatePlayerCompletions()
}

