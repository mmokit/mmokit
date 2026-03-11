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
	ResourceType      gamepb.ResourceType
	ResourceRemaining float32
	CargoItems        []*gamepb.InventoryItem
	CargoMass         float32
	MaxCargoMass      float32
	LockProgress      float32
	LockTargetID      uint32
	LockedByID        uint32
	LockedByProgress  float32
	StatusEffects     []*gamepb.ActiveStatusEffect
	Cooldowns         []*gamepb.AbilityCooldownState
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
		ws.Entities[e.Id] = &EntitySnapshot{
			ID:                e.Id,
			Type:              e.EntityType,
			PilotName:         e.PilotName,
			X:                 e.X,
			Y:                 e.Y,
			VX:                e.Vx,
			VY:                e.Vy,
			Rotation:          e.Rotation,
			Health:            e.Health,
			MaxHealth:         e.MaxHealth,
			Shield:            e.Shield,
			MaxShield:         e.MaxShield,
			Radius:            e.Radius,
			MiningActive:      e.MiningActive,
			MiningTargetID:    e.MiningTargetId,
			ResourceType:      e.ResourceType,
			ResourceRemaining: e.ResourceRemaining,
			CargoItems:        e.CargoItems,
			CargoMass:         e.CargoMass,
			MaxCargoMass:      e.MaxCargoMass,
			LockProgress:      e.LockProgress,
			LockTargetID:      e.LockTargetId,
			LockedByID:        e.LockedById,
			LockedByProgress:  e.LockedByProgress,
			StatusEffects:     e.StatusEffects,
			Cooldowns:         e.AbilityCooldowns,
		}
	}
	return ws
}
