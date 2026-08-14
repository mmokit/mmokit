package game

import (
	"github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/internal/item"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// Equip/unequip is a set of GameWorld methods dispatched directly from
// the Equip input handler via stage.Commands().Defer — no per-tick
// system shell. Helpers are grouped here because they only serve the
// equip flow.

// performEquipFor is the entry point dispatched by the Equip input
// handler via Commands.Defer. Routes by player state. itemID=0 means
// "unequip the slot". targetBank only matters while docked: when true,
// equip pulls from bank and the swapped-out item returns to bank;
// unequip deposits to bank instead of cargo.
func (gw *GameWorld) performEquipFor(connID uint32, itemID uint32, slot item.EquipSlot, targetBank bool) {
	sess := gw.Players.ByConnID(connID)
	if sess == nil {
		return
	}
	switch sess.State {
	case mmokit.StateActive:
		gw.performActiveEquip(sess, itemID, slot)
	case StateDocked:
		gw.performDockedEquip(sess, itemID, slot, targetBank)
	}
}

// performActiveEquip handles in-flight equip/unequip via the ECS
// Inventory + Equipment components on the ship entity.
func (gw *GameWorld) performActiveEquip(sess *mmokit.PlayerSession, itemID uint32, slot item.EquipSlot) {
	entity := mmokit.EntityFromECS(gw.stage, sess.Entity)
	if !entity.Alive() {
		return
	}
	eq := mmokit.Get[component.Equipment](entity)
	inv := mmokit.Get[component.Inventory](entity)
	if eq == nil || inv == nil {
		return
	}

	if itemID == 0 {
		gw.unequip(sess.ConnID, entity, eq, inv, slot)
	} else {
		gw.equip(sess.ConnID, entity, eq, inv, itemID, slot)
	}
}

// performDockedEquip handles equip/unequip while docked, working
// against pdata.Cargo and pdata.Equipment (the canonical state while
// docked) and mirroring into the ECS Equipment component so the entity
// stays consistent. pdata.Cargo is the only authoritative cargo store
// while docked — the entity's Inventory map is empty until undock
// copies pdata.Cargo back in (see lifecycle.processUndocks).
//
// targetBank routes to the bank-flavored variant: equip pulls from
// pdata.Bank (and any swapped-out item returns to pdata.Bank); unequip
// deposits to pdata.Bank instead of pdata.Cargo. Lets the player swap
// gear directly between loadout and bank without round-tripping through
// cargo.
func (gw *GameWorld) performDockedEquip(sess *mmokit.PlayerSession, itemID uint32, slot item.EquipSlot, targetBank bool) {
	pdata := gw.PlayerDB.Bind(sess)
	if pdata.Cargo == nil {
		pdata.Cargo = make(map[uint32]int32)
	}
	if pdata.Bank == nil {
		pdata.Bank = make(map[uint32]int32)
	}

	switch {
	case itemID == 0 && targetBank:
		gw.unequipDockedToBank(sess, pdata, slot)
	case itemID == 0:
		gw.unequipDocked(sess, pdata, slot)
	case targetBank:
		gw.equipDockedFromBank(sess, pdata, itemID, slot)
	default:
		gw.equipDocked(sess, pdata, itemID, slot)
	}
}

func (gw *GameWorld) equipDocked(sess *mmokit.PlayerSession, pdata *PlayerData, itemID uint32, slot item.EquipSlot) {
	connID := sess.ConnID

	// Validate: item is in cargo
	if pdata.Cargo[itemID] < 1 {
		gw.sendEquipResult(connID, false, "Item not in cargo", slot, 0, 0)
		return
	}

	def := item.Get(itemID)
	if def == nil || def.Category != item.CategoryEquipment || !item.SlotCompatible(def.EquipSlot, slot) {
		gw.sendEquipResult(connID, false, "Cannot equip to that slot", slot, 0, 0)
		return
	}

	oldItemID := equipmentSaveSlot(&pdata.Equipment, slot)
	if oldItemID != 0 {
		oldDef := item.Get(oldItemID)
		// Mass = current cargo - newItem (going onto ship) + oldItem (coming off into cargo)
		newMass := pdata.CargoTotalMass() - def.MassPerUnit + oldDef.MassPerUnit
		if newMass > gw.Config.MaxCargo {
			gw.sendEquipResult(connID, false, "Cargo full - cannot swap", slot, oldItemID, 0)
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
	if entity := mmokit.EntityFromECS(gw.stage, sess.Entity); entity.Alive() {
		if eq := mmokit.Get[component.Equipment](entity); eq != nil {
			setEquipmentSlot(eq, slot, itemID)
			gw.ApplyEquipmentStats(entity)
		}
	}

	gw.PlayerDB.MarkDirty(sess.Username)

	gw.eng.Log.Log(CatPlayerEquip, "equip (docked): conn=%d slot=%d item=%d (was %d)", connID, slot, itemID, oldItemID)
	gw.sendEquipResult(connID, true, "", slot, itemID, oldItemID)
	gw.SendBankContents(sess.ConnID, pdata)
}

// equipDockedFromBank pulls itemID out of pdata.Bank, places it in slot,
// and routes any swapped-out item back to pdata.Bank. Mass check: bank
// gains (oldItem - newItem) when swapping, must fit BankMaxMass.
func (gw *GameWorld) equipDockedFromBank(sess *mmokit.PlayerSession, pdata *PlayerData, itemID uint32, slot item.EquipSlot) {
	connID := sess.ConnID

	if pdata.Bank[itemID] < 1 {
		gw.sendEquipResult(connID, false, "Item not in bank", slot, 0, 0)
		return
	}

	def := item.Get(itemID)
	if def == nil || def.Category != item.CategoryEquipment || !item.SlotCompatible(def.EquipSlot, slot) {
		gw.sendEquipResult(connID, false, "Cannot equip to that slot", slot, 0, 0)
		return
	}

	oldItemID := equipmentSaveSlot(&pdata.Equipment, slot)
	if oldItemID != 0 {
		oldDef := item.Get(oldItemID)
		// New: bank loses newItem (going onto ship), gains oldItem (off ship).
		newBankMass := pdata.BankTotalMass() - def.MassPerUnit + oldDef.MassPerUnit
		if gw.Config.BankMaxMass > 0 && newBankMass > gw.Config.BankMaxMass {
			gw.sendEquipResult(connID, false, "Bank full - cannot swap", slot, oldItemID, 0)
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

	if entity := mmokit.EntityFromECS(gw.stage, sess.Entity); entity.Alive() {
		if eq := mmokit.Get[component.Equipment](entity); eq != nil {
			setEquipmentSlot(eq, slot, itemID)
			gw.ApplyEquipmentStats(entity)
		}
	}

	gw.PlayerDB.MarkDirty(sess.Username)
	gw.eng.Log.Log(CatPlayerEquip, "equip from bank: conn=%d slot=%d item=%d (was %d)", connID, slot, itemID, oldItemID)
	gw.sendEquipResult(connID, true, "", slot, itemID, oldItemID)
	gw.SendBankContents(connID, pdata)
}

// unequipDockedToBank moves the slot's item directly to pdata.Bank
// instead of pdata.Cargo. Mass check: bank gains def.MassPerUnit, must
// fit BankMaxMass.
func (gw *GameWorld) unequipDockedToBank(sess *mmokit.PlayerSession, pdata *PlayerData, slot item.EquipSlot) {
	connID := sess.ConnID

	itemID := equipmentSaveSlot(&pdata.Equipment, slot)
	if itemID == 0 {
		gw.sendEquipResult(connID, false, "Slot is empty", slot, 0, 0)
		return
	}

	def := item.Get(itemID)
	if def != nil && gw.Config.BankMaxMass > 0 {
		newBankMass := pdata.BankTotalMass() + def.MassPerUnit
		if newBankMass > gw.Config.BankMaxMass {
			gw.sendEquipResult(connID, false, "Bank full", slot, itemID, 0)
			return
		}
	}

	setEquipmentSaveSlot(&pdata.Equipment, slot, 0)
	pdata.Bank[itemID]++

	if entity := mmokit.EntityFromECS(gw.stage, sess.Entity); entity.Alive() {
		if eq := mmokit.Get[component.Equipment](entity); eq != nil {
			setEquipmentSlot(eq, slot, 0)
			gw.ApplyEquipmentStats(entity)
		}
	}

	gw.PlayerDB.MarkDirty(sess.Username)
	gw.eng.Log.Log(CatPlayerEquip, "unequip to bank: conn=%d slot=%d item=%d", connID, slot, itemID)
	gw.sendEquipResult(connID, true, "", slot, 0, itemID)
	gw.SendBankContents(connID, pdata)
}

func (gw *GameWorld) unequipDocked(sess *mmokit.PlayerSession, pdata *PlayerData, slot item.EquipSlot) {
	connID := sess.ConnID

	itemID := equipmentSaveSlot(&pdata.Equipment, slot)
	if itemID == 0 {
		gw.sendEquipResult(connID, false, "Slot is empty", slot, 0, 0)
		return
	}

	def := item.Get(itemID)
	if def != nil {
		newMass := pdata.CargoTotalMass() + def.MassPerUnit
		if newMass > gw.Config.MaxCargo {
			gw.sendEquipResult(connID, false, "Cargo full", slot, itemID, 0)
			return
		}
	}

	setEquipmentSaveSlot(&pdata.Equipment, slot, 0)
	pdata.Cargo[itemID]++

	if entity := mmokit.EntityFromECS(gw.stage, sess.Entity); entity.Alive() {
		if eq := mmokit.Get[component.Equipment](entity); eq != nil {
			setEquipmentSlot(eq, slot, 0)
			gw.ApplyEquipmentStats(entity)
		}
	}

	gw.PlayerDB.MarkDirty(sess.Username)

	gw.eng.Log.Log(CatPlayerEquip, "unequip (docked): conn=%d slot=%d item=%d", connID, slot, itemID)
	gw.sendEquipResult(connID, true, "", slot, 0, itemID)
	gw.SendBankContents(sess.ConnID, pdata)
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

func (gw *GameWorld) equip(connID uint32, entity mmokit.Entity, eq *component.Equipment, inv *component.Inventory, itemID uint32, slot item.EquipSlot) {
	// Validate the item exists in cargo
	have := inv.Items[itemID]
	if have < 1 {
		gw.sendEquipResult(connID, false, "Item not in cargo", slot, 0, 0)
		return
	}

	// Validate it's equipment for the right slot
	def := item.Get(itemID)
	if def == nil || def.Category != item.CategoryEquipment || !item.SlotCompatible(def.EquipSlot, slot) {
		gw.sendEquipResult(connID, false, "Cannot equip to that slot", slot, 0, 0)
		return
	}

	// Get currently equipped item in this slot
	oldItemID := equipmentSlot(eq, slot)

	// If swapping: check that the old item can fit in cargo after removing the new one
	if oldItemID != 0 {
		oldDef := item.Get(oldItemID)
		newMass := inv.TotalMass() - def.MassPerUnit + oldDef.MassPerUnit
		if newMass > inv.MaxMass {
			gw.sendEquipResult(connID, false, "Cargo full - cannot swap", slot, oldItemID, 0)
			return
		}
	}

	// Execute the swap
	inv.RemoveItem(itemID, 1)
	if oldItemID != 0 {
		inv.AddItem(oldItemID, 1)
	}
	setEquipmentSlot(eq, slot, itemID)

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
	gw.sendEquipResult(connID, true, "", slot, itemID, oldItemID)
}

func (gw *GameWorld) unequip(connID uint32, entity mmokit.Entity, eq *component.Equipment, inv *component.Inventory, slot item.EquipSlot) {
	itemID := equipmentSlot(eq, slot)
	if itemID == 0 {
		gw.sendEquipResult(connID, false, "Slot is empty", slot, 0, 0)
		return
	}

	// Check cargo has room
	def := item.Get(itemID)
	if def != nil && inv.RemainingMass() < def.MassPerUnit {
		gw.sendEquipResult(connID, false, "Cargo full", slot, itemID, 0)
		return
	}

	// Move to cargo
	setEquipmentSlot(eq, slot, 0)
	inv.AddItem(itemID, 1)

	// Recalculate passive stats
	gw.ApplyEquipmentStats(entity)

	gw.eng.Log.Log(CatPlayerEquip, "unequip: conn=%d slot=%d item=%d", connID, slot, itemID)
	gw.sendEquipResult(connID, true, "", slot, 0, itemID)
}

// equipmentSlot reads itemID from an ECS Equipment slot.
func equipmentSlot(eq *component.Equipment, slot item.EquipSlot) uint32 {
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

// setEquipmentSlot writes itemID into an ECS Equipment slot.
func setEquipmentSlot(eq *component.Equipment, slot item.EquipSlot, itemID uint32) {
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

func (gw *GameWorld) sendEquipResult(connID uint32, success bool, reason string, slot item.EquipSlot, equippedID, previousID uint32) {
	mmokit.SendEvent(gw.stage, connID, &EquipResult{
		Success:        success,
		Reason:         reason,
		Slot:           uint32(slot),
		EquippedItemID: equippedID,
		PreviousItemID: previousID,
	})
}
