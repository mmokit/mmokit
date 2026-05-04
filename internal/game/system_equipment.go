package game

import (
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// EquipmentSystem processes equip/unequip requests and applies stat changes.
type EquipmentSystem struct {
	mmokit.SystemBase[*GameWorld]
}

func (s *EquipmentSystem) Update(dt float32) {
	gw := s.World()

	for _, req := range mmokit.Drain[PendingEquipRequest](gw.Queue) {
		s.processRequest(req)
	}
}

func (s *EquipmentSystem) processRequest(req PendingEquipRequest) {
	gw := s.World()
	sess := gw.Players.ByConnID(req.ConnID)
	if sess == nil {
		return
	}
	switch sess.State {
	case mmokit.StateActive:
		s.processActiveRequest(sess, req)
	case StateDocked:
		s.processDockedRequest(sess, req)
	}
}

// processActiveRequest handles in-flight equip/unequip via the ECS
// Inventory + Equipment components on the ship entity.
func (s *EquipmentSystem) processActiveRequest(sess *mmokit.PlayerSession, req PendingEquipRequest) {
	gw := s.World()
	entity := mmokit.EntityFromECS(gw.Stage, sess.Entity)
	if !entity.Alive() {
		return
	}
	eq := mmokit.Get[component.Equipment](entity)
	inv := mmokit.Get[component.Inventory](entity)
	if eq == nil || inv == nil {
		return
	}

	if req.ItemID == 0 {
		s.unequip(req.ConnID, entity, eq, inv, req.Slot)
	} else {
		s.equip(req.ConnID, entity, eq, inv, req.ItemID, req.Slot)
	}
}

// processDockedRequest handles equip/unequip while docked, working
// against pdata.Cargo and pdata.Equipment (the canonical state while
// docked) and mirroring into the ECS Equipment component so the entity
// stays consistent. pdata.Cargo is the only authoritative cargo store
// while docked — the entity's Inventory map is empty until undock
// copies pdata.Cargo back in (see lifecycle.processUndocks).
//
// req.TargetBank routes to the bank-flavored variant: equip pulls from
// pdata.Bank (and any swapped-out item returns to pdata.Bank); unequip
// deposits to pdata.Bank instead of pdata.Cargo. Lets the player swap
// gear directly between loadout and bank without round-tripping through
// cargo.
func (s *EquipmentSystem) processDockedRequest(sess *mmokit.PlayerSession, req PendingEquipRequest) {
	gw := s.World()
	pdata := gw.PlayerDB.GetOrCreate(sess.Username)
	if pdata.Cargo == nil {
		pdata.Cargo = make(map[uint32]int32)
	}
	if pdata.Bank == nil {
		pdata.Bank = make(map[uint32]int32)
	}

	switch {
	case req.ItemID == 0 && req.TargetBank:
		s.unequipDockedToBank(sess, pdata, req.Slot)
	case req.ItemID == 0:
		s.unequipDocked(sess, pdata, req.Slot)
	case req.TargetBank:
		s.equipDockedFromBank(sess, pdata, req.ItemID, req.Slot)
	default:
		s.equipDocked(sess, pdata, req.ItemID, req.Slot)
	}
}

func (s *EquipmentSystem) equipDocked(sess *mmokit.PlayerSession, pdata *PlayerData, itemID uint32, slot item.EquipSlot) {
	gw := s.World()
	connID := sess.ConnID

	// Validate: item is in cargo
	if pdata.Cargo[itemID] < 1 {
		s.sendResult(connID, false, "Item not in cargo", slot, 0, 0)
		return
	}

	def := item.Get(itemID)
	if def == nil || def.Category != item.CategoryEquipment || !item.SlotCompatible(def.EquipSlot, slot) {
		s.sendResult(connID, false, "Cannot equip to that slot", slot, 0, 0)
		return
	}

	oldItemID := equipmentSaveSlot(&pdata.Equipment, slot)
	if oldItemID != 0 {
		oldDef := item.Get(oldItemID)
		// Mass = current cargo - newItem (going onto ship) + oldItem (coming off into cargo)
		newMass := pdata.CargoTotalMass() - def.MassPerUnit + oldDef.MassPerUnit
		if newMass > gw.Config.MaxCargo {
			s.sendResult(connID, false, "Cargo full - cannot swap", slot, oldItemID, 0)
			return
		}
	}

	// Move item out of cargo into slot
	pdata.Cargo[itemID]--
	if pdata.Cargo[itemID] <= 0 {
		delete(pdata.Cargo, itemID)
	}
	if oldItemID != 0 {
		pdata.Cargo[oldItemID]++
	}
	setEquipmentSaveSlot(&pdata.Equipment, slot, itemID)

	// Mirror into the ECS Equipment component (the dormant entity still
	// holds it; ApplyEquipmentStats reads from there at undock time).
	if entity := mmokit.EntityFromECS(gw.Stage, sess.Entity); entity.Alive() {
		if eq := mmokit.Get[component.Equipment](entity); eq != nil {
			s.setSlot(eq, slot, itemID)
			gw.ApplyEquipmentStats(entity)
		}
	}

	gw.PlayerDB.MarkDirty(sess.Username)

	gw.eng.Log.Log(CatPlayerEquip, "equip (docked): conn=%d slot=%d item=%d (was %d)", connID, slot, itemID, oldItemID)
	s.sendResult(connID, true, "", slot, itemID, oldItemID)
	s.sendBankContents(sess.ConnID, pdata)
}

// equipDockedFromBank pulls itemID out of pdata.Bank, places it in slot,
// and routes any swapped-out item back to pdata.Bank. Mass check: bank
// gains (oldItem - newItem) when swapping, must fit BankMaxMass.
func (s *EquipmentSystem) equipDockedFromBank(sess *mmokit.PlayerSession, pdata *PlayerData, itemID uint32, slot item.EquipSlot) {
	gw := s.World()
	connID := sess.ConnID

	if pdata.Bank[itemID] < 1 {
		s.sendResult(connID, false, "Item not in bank", slot, 0, 0)
		return
	}

	def := item.Get(itemID)
	if def == nil || def.Category != item.CategoryEquipment || !item.SlotCompatible(def.EquipSlot, slot) {
		s.sendResult(connID, false, "Cannot equip to that slot", slot, 0, 0)
		return
	}

	oldItemID := equipmentSaveSlot(&pdata.Equipment, slot)
	if oldItemID != 0 {
		oldDef := item.Get(oldItemID)
		// New: bank loses newItem (going onto ship), gains oldItem (off ship).
		newBankMass := pdata.BankTotalMass() - def.MassPerUnit + oldDef.MassPerUnit
		if gw.Config.BankMaxMass > 0 && newBankMass > gw.Config.BankMaxMass {
			s.sendResult(connID, false, "Bank full - cannot swap", slot, oldItemID, 0)
			return
		}
	}

	pdata.Bank[itemID]--
	if pdata.Bank[itemID] <= 0 {
		delete(pdata.Bank, itemID)
	}
	if oldItemID != 0 {
		pdata.Bank[oldItemID]++
	}
	setEquipmentSaveSlot(&pdata.Equipment, slot, itemID)

	if entity := mmokit.EntityFromECS(gw.Stage, sess.Entity); entity.Alive() {
		if eq := mmokit.Get[component.Equipment](entity); eq != nil {
			s.setSlot(eq, slot, itemID)
			gw.ApplyEquipmentStats(entity)
		}
	}

	gw.PlayerDB.MarkDirty(sess.Username)
	gw.eng.Log.Log(CatPlayerEquip, "equip from bank: conn=%d slot=%d item=%d (was %d)", connID, slot, itemID, oldItemID)
	s.sendResult(connID, true, "", slot, itemID, oldItemID)
	s.sendBankContents(connID, pdata)
}

// unequipDockedToBank moves the slot's item directly to pdata.Bank
// instead of pdata.Cargo. Mass check: bank gains def.MassPerUnit, must
// fit BankMaxMass.
func (s *EquipmentSystem) unequipDockedToBank(sess *mmokit.PlayerSession, pdata *PlayerData, slot item.EquipSlot) {
	gw := s.World()
	connID := sess.ConnID

	itemID := equipmentSaveSlot(&pdata.Equipment, slot)
	if itemID == 0 {
		s.sendResult(connID, false, "Slot is empty", slot, 0, 0)
		return
	}

	def := item.Get(itemID)
	if def != nil && gw.Config.BankMaxMass > 0 {
		newBankMass := pdata.BankTotalMass() + def.MassPerUnit
		if newBankMass > gw.Config.BankMaxMass {
			s.sendResult(connID, false, "Bank full", slot, itemID, 0)
			return
		}
	}

	setEquipmentSaveSlot(&pdata.Equipment, slot, 0)
	pdata.Bank[itemID]++

	if entity := mmokit.EntityFromECS(gw.Stage, sess.Entity); entity.Alive() {
		if eq := mmokit.Get[component.Equipment](entity); eq != nil {
			s.setSlot(eq, slot, 0)
			gw.ApplyEquipmentStats(entity)
		}
	}

	gw.PlayerDB.MarkDirty(sess.Username)
	gw.eng.Log.Log(CatPlayerEquip, "unequip to bank: conn=%d slot=%d item=%d", connID, slot, itemID)
	s.sendResult(connID, true, "", slot, 0, itemID)
	s.sendBankContents(connID, pdata)
}

func (s *EquipmentSystem) unequipDocked(sess *mmokit.PlayerSession, pdata *PlayerData, slot item.EquipSlot) {
	gw := s.World()
	connID := sess.ConnID

	itemID := equipmentSaveSlot(&pdata.Equipment, slot)
	if itemID == 0 {
		s.sendResult(connID, false, "Slot is empty", slot, 0, 0)
		return
	}

	def := item.Get(itemID)
	if def != nil {
		newMass := pdata.CargoTotalMass() + def.MassPerUnit
		if newMass > gw.Config.MaxCargo {
			s.sendResult(connID, false, "Cargo full", slot, itemID, 0)
			return
		}
	}

	setEquipmentSaveSlot(&pdata.Equipment, slot, 0)
	pdata.Cargo[itemID]++

	if entity := mmokit.EntityFromECS(gw.Stage, sess.Entity); entity.Alive() {
		if eq := mmokit.Get[component.Equipment](entity); eq != nil {
			s.setSlot(eq, slot, 0)
			gw.ApplyEquipmentStats(entity)
		}
	}

	gw.PlayerDB.MarkDirty(sess.Username)

	gw.eng.Log.Log(CatPlayerEquip, "unequip (docked): conn=%d slot=%d item=%d", connID, slot, itemID)
	s.sendResult(connID, true, "", slot, 0, itemID)
	s.sendBankContents(sess.ConnID, pdata)
}

// sendBankContents notifies the client that bank/cargo state has changed,
// reusing EconomySystem's sender via the GameWorld's wired Settlement
// machinery. Goes through the same GSE_BANK_CONTENTS path the bank UI
// already listens for.
func (s *EquipmentSystem) sendBankContents(connID uint32, pdata *PlayerData) {
	gw := s.World()
	gw.SendBankContents(connID, pdata)
}

// equipmentSaveSlot reads the slot value from a pdata.Equipment.
func equipmentSaveSlot(eq *EquipmentSave, slot item.EquipSlot) uint32 {
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

// setEquipmentSaveSlot writes the slot value into a pdata.Equipment.
func setEquipmentSaveSlot(eq *EquipmentSave, slot item.EquipSlot, itemID uint32) {
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

func (s *EquipmentSystem) equip(connID uint32, entity mmokit.Entity, eq *component.Equipment, inv *component.Inventory, itemID uint32, slot item.EquipSlot) {
	gw := s.World()

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
	if abilities := mmokit.Get[component.AbilitySet](entity); abilities != nil {
		primary, secondary, hasSec := item.SlotToAbilitySlots(slot)
		abilities.Cooldowns[primary] = def.Equip.Primary.Cooldown
		if hasSec && def.Equip.Secondary != nil {
			abilities.Cooldowns[secondary] = def.Equip.Secondary.Cooldown
		}
	}

	gw.eng.Log.Log(CatPlayerEquip, "equip: conn=%d slot=%d item=%d (was %d)", connID, slot, itemID, oldItemID)
	s.sendResult(connID, true, "", slot, itemID, oldItemID)
}

func (s *EquipmentSystem) unequip(connID uint32, entity mmokit.Entity, eq *component.Equipment, inv *component.Inventory, slot item.EquipSlot) {
	gw := s.World()

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

	gw.eng.Log.Log(CatPlayerEquip, "unequip: conn=%d slot=%d item=%d", connID, slot, itemID)
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
	gw := s.World()
	gw.ServerEvents().Send(gw.eng.ConnMgr, connID, uint32(gamepb.GameServerEventCode_GSE_EQUIP_RESULT), &gamepb.EquipResultMsg{
		Success:        success,
		Reason:         reason,
		Slot:           gamepb.EquipSlot(slot),
		EquippedItemId: equippedID,
		PreviousItemId: previousID,
	})
}
