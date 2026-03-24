package bot

import (
	gamepb "github.com/zenion/mmoserver/gen/go"
)

// EntitySnapshot holds a single entity's state from a world update.
type EntitySnapshot struct {
	ID                uint32
	Type              gamepb.EntityType
	PilotName         string
	X, Y              float32
	VX, VY            float32
	Rotation          float32
	Health, MaxHealth float32
	Shield, MaxShield float32
	Radius            float32
	MiningActive      bool
	MiningTargetID    uint32
	MiningBeamMask    uint32
	ResourceType      gamepb.ResourceType
	ResourceRemaining float32
	CargoItems        []*gamepb.InventoryItem
	LockedByID        uint32
	LockedByProgress  float32
	StatusEffects     []*gamepb.ActiveStatusEffect
}

// OwnState holds state sent only to the owning player via PlayerOwnStateMsg.
type OwnState struct {
	CargoItems   []*gamepb.InventoryItem
	CargoMass    float32
	MaxCargoMass float32
	LockProgress float32
	LockTargetID uint32
	LockedByID       uint32
	LockedByProgress float32
	Cooldowns    []*gamepb.AbilityCooldownState
	Equipment    *gamepb.EquipmentState
}

// WorldState holds a complete snapshot of the visible world for one tick.
type WorldState struct {
	Tick       uint32
	Entities   map[uint32]*EntitySnapshot
	KilledIDs  []uint32
	RemovedIDs []uint32
}

func worldStateFromUpdate(msg *gamepb.WorldUpdateMsg) WorldState {
	ws := WorldState{
		Tick:       msg.Tick,
		Entities:   make(map[uint32]*EntitySnapshot, len(msg.Entities)),
		KilledIDs:  msg.KilledIds,
		RemovedIDs: msg.RemovedIds,
	}
	for _, e := range msg.Entities {
		snap := &EntitySnapshot{
			ID:               e.Id,
			Type:             e.EntityType,
			X:                e.X,
			Y:                e.Y,
			VX:               e.Vx,
			VY:               e.Vy,
			Rotation:         e.Rotation,
			Radius:           e.Radius,
			LockedByID:       e.LockedById,
			LockedByProgress: e.LockedByProgress,
		}

		// Extract type-specific fields from oneof
		switch td := e.TypeData.(type) {
		case *gamepb.EntityState_Ship:
			ship := td.Ship
			snap.PilotName = ship.PilotName
			snap.MiningActive = ship.MiningActive
			snap.MiningTargetID = ship.MiningTargetId
			snap.MiningBeamMask = ship.MiningBeamMask
			if ship.Combat != nil {
				snap.Health = ship.Combat.Health
				snap.MaxHealth = ship.Combat.MaxHealth
				snap.Shield = ship.Combat.Shield
				snap.MaxShield = ship.Combat.MaxShield
				snap.StatusEffects = ship.Combat.StatusEffects
			}
		case *gamepb.EntityState_Npc:
			if td.Npc.Combat != nil {
				snap.Health = td.Npc.Combat.Health
				snap.MaxHealth = td.Npc.Combat.MaxHealth
				snap.Shield = td.Npc.Combat.Shield
				snap.MaxShield = td.Npc.Combat.MaxShield
				snap.StatusEffects = td.Npc.Combat.StatusEffects
			}
		case *gamepb.EntityState_Asteroid:
			snap.ResourceType = td.Asteroid.ResourceType
			snap.ResourceRemaining = td.Asteroid.ResourceRemaining
		case *gamepb.EntityState_LootCrate:
			snap.CargoItems = td.LootCrate.CargoItems
		}

		ws.Entities[e.Id] = snap
	}
	return ws
}

func ownStateFromMsg(msg *gamepb.PlayerOwnStateMsg) OwnState {
	return OwnState{
		CargoItems:       msg.CargoItems,
		CargoMass:        msg.CargoMass,
		MaxCargoMass:     msg.MaxCargoMass,
		LockProgress:     msg.LockProgress,
		LockTargetID:     msg.LockTargetId,
		LockedByID:       msg.BeingLockedById,
		LockedByProgress: msg.BeingLockedByProgress,
		Cooldowns:        msg.AbilityCooldowns,
		Equipment:        msg.Equipment,
	}
}
