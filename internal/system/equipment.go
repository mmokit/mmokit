package system

import (
	"github.com/mlange-42/ark/ecs"
	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/internal/netutil"
)

// EquipmentSystem processes equip/unequip requests and applies stat changes.
type EquipmentSystem struct {
	gw *game.GameWorld
}

func NewEquipmentSystem(gw *game.GameWorld) *EquipmentSystem {
	return &EquipmentSystem{gw: gw}
}

func (s *EquipmentSystem) Update(dt float32) {
	gw := s.gw

	for _, req := range game.Drain[game.PendingEquipRequest](gw.Queue) {
		s.processRequest(req)
	}
}

func (s *EquipmentSystem) processRequest(req game.PendingEquipRequest) {
	gw := s.gw
	entity, ok := gw.Players.Entities[req.ConnID]
	if !ok || !gw.ECS.Alive(entity) {
		return
	}
	if !gw.C.Equipment.HasAll(entity) || !gw.C.Inventory.HasAll(entity) {
		return
	}

	eq := gw.C.Equipment.Get(entity)
	inv := gw.C.Inventory.Get(entity)

	if req.ItemID == 0 {
		s.unequip(req.ConnID, entity, eq, inv, req.Slot)
	} else {
		s.equip(req.ConnID, entity, eq, inv, req.ItemID, req.Slot)
	}
}

func (s *EquipmentSystem) equip(connID uint32, entity ecs.Entity, eq *component.Equipment, inv *component.Inventory, itemID uint32, slot item.EquipSlot) {
	gw := s.gw

	// Validate the item exists in cargo
	have := inv.Items[itemID]
	if have < 1 {
		s.sendResult(connID, false, "Item not in cargo", slot, 0, 0)
		return
	}

	// Validate it's equipment for the right slot
	def := item.Get(itemID)
	if def == nil || def.Category != item.CategoryEquipment || !item.SlotCompatible(def.EquipSlot, slot) {
		s.sendResult(connID, false, "Cannot equip to that slot", slot, 0, 0)
		return
	}

	// Get currently equipped item in this slot
	oldItemID := s.getSlot(eq, slot)

	// If swapping: check that the old item can fit in cargo after removing the new one
	if oldItemID != 0 {
		oldDef := item.Get(oldItemID)
		newMass := inv.TotalMass() - def.MassPerUnit + oldDef.MassPerUnit
		if newMass > inv.MaxMass {
			s.sendResult(connID, false, "Cargo full - cannot swap", slot, oldItemID, 0)
			return
		}
	}

	// Execute the swap
	inv.RemoveItem(itemID, 1)
	if oldItemID != 0 {
		inv.AddItem(oldItemID, 1)
	}
	s.setSlot(eq, slot, itemID)

	// Recalculate passive stats
	gw.ApplyEquipmentStats(entity)

	// Reset cooldowns for affected ability slots
	if gw.C.AbilitySet.HasAll(entity) {
		abilities := gw.C.AbilitySet.Get(entity)
		primary, secondary, hasSec := item.SlotToAbilitySlots(slot)
		abilities.Cooldowns[primary] = def.Equip.Primary.Cooldown
		if hasSec && def.Equip.Secondary != nil {
			abilities.Cooldowns[secondary] = def.Equip.Secondary.Cooldown
		}
	}

	gw.Log.Log(game.CatEquip, "equip: conn=%d slot=%d item=%d (was %d)", connID, slot, itemID, oldItemID)
	s.sendResult(connID, true, "", slot, itemID, oldItemID)
}

func (s *EquipmentSystem) unequip(connID uint32, entity ecs.Entity, eq *component.Equipment, inv *component.Inventory, slot item.EquipSlot) {
	gw := s.gw

	itemID := s.getSlot(eq, slot)
	if itemID == 0 {
		s.sendResult(connID, false, "Slot is empty", slot, 0, 0)
		return
	}

	// Check cargo has room
	def := item.Get(itemID)
	if def != nil && inv.RemainingMass() < def.MassPerUnit {
		s.sendResult(connID, false, "Cargo full", slot, itemID, 0)
		return
	}

	// Move to cargo
	s.setSlot(eq, slot, 0)
	inv.AddItem(itemID, 1)

	// Recalculate passive stats
	gw.ApplyEquipmentStats(entity)

	gw.Log.Log(game.CatEquip, "unequip: conn=%d slot=%d item=%d", connID, slot, itemID)
	s.sendResult(connID, true, "", slot, 0, itemID)
}

func (s *EquipmentSystem) getSlot(eq *component.Equipment, slot item.EquipSlot) uint32 {
	switch slot {
	case item.SlotWeapon1:
		return eq.Weapon1
	case item.SlotWeapon2:
		return eq.Weapon2
	case item.SlotShield:
		return eq.Shield
	case item.SlotThruster:
		return eq.Thruster
	}
	return 0
}

func (s *EquipmentSystem) setSlot(eq *component.Equipment, slot item.EquipSlot, itemID uint32) {
	switch slot {
	case item.SlotWeapon1:
		eq.Weapon1 = itemID
	case item.SlotWeapon2:
		eq.Weapon2 = itemID
	case item.SlotShield:
		eq.Shield = itemID
	case item.SlotThruster:
		eq.Thruster = itemID
	}
}

func (s *EquipmentSystem) sendResult(connID uint32, success bool, reason string, slot item.EquipSlot, equippedID, previousID uint32) {
	data := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_EQUIP_RESULT), &gamepb.EquipResultMsg{
		Success:        success,
		Reason:         reason,
		Slot:           gamepb.EquipSlot(slot),
		EquippedItemId: equippedID,
		PreviousItemId: previousID,
	})
	if data != nil {
		s.gw.ConnMgr.SendReliable(connID, data)
	}
}
