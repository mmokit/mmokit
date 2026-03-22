package game

import (
	"time"

	"github.com/zenion/mmoserver/internal/item"
)

// EquipmentSave holds the item IDs of equipped gear for persistence.
type EquipmentSave struct {
	Weapon1  uint32 `json:"weapon1,omitempty"`
	Weapon2  uint32 `json:"weapon2,omitempty"`
	Shield   uint32 `json:"shield,omitempty"`
	Thruster uint32 `json:"thruster,omitempty"`
}

// IsZero returns true if no equipment is saved.
func (e EquipmentSave) IsZero() bool {
	return e.Weapon1 == 0 && e.Weapon2 == 0 && e.Shield == 0 && e.Thruster == 0
}

// PlayerData holds persistent player state that survives disconnect/death.
type PlayerData struct {
	Username  string           `json:"username"`
	X         float32          `json:"x"`
	Y         float32          `json:"y"`
	SectorX   int32            `json:"sector_x"`
	SectorY   int32            `json:"sector_y"`
	Flux      int64            `json:"flux,omitempty"`
	Cargo     map[uint32]int32 `json:"cargo,omitempty"`
	Bank      map[uint32]int32 `json:"bank,omitempty"`
	Equipment EquipmentSave    `json:"equipment,omitempty"`
	HasSave   bool             `json:"has_save"`
	CreatedAt time.Time        `json:"created_at"`
	LastLogin time.Time        `json:"last_login"`
}

// MigrateItemIDs shifts old item IDs {1,2,3,4} → {2,3,4,5} in a map.
// Detects old format by checking: has keys in 1-4 range and no key 5.
func migrateItemIDs(items map[uint32]int32) map[uint32]int32 {
	if items == nil {
		return nil
	}
	// Check if migration is needed: has old-range keys and no key 5
	hasOldRange := false
	for id := range items {
		if id >= 1 && id <= 4 {
			hasOldRange = true
			break
		}
	}
	if !hasOldRange {
		return items
	}
	if _, has5 := items[5]; has5 {
		return items // already migrated or has new-format data
	}

	migrated := make(map[uint32]int32, len(items))
	for id, qty := range items {
		if id >= 1 && id <= 4 {
			migrated[id+1] = qty // shift 1→2, 2→3, 3→4, 4→5
		} else {
			migrated[id] = qty
		}
	}
	return migrated
}

// MigrateCargoIDs shifts old cargo item IDs from {1,2,3,4} to {2,3,4,5}.
func (pd *PlayerData) MigrateCargoIDs() {
	pd.Cargo = migrateItemIDs(pd.Cargo)
}

// MigrateBankIDs shifts old bank item IDs from {1,2,3,4} to {2,3,4,5}.
func (pd *PlayerData) MigrateBankIDs() {
	pd.Bank = migrateItemIDs(pd.Bank)
}

// DepositToBank moves items from an external source into the bank.
// Returns the amount actually deposited, clamped by bankMaxMass.
func (pd *PlayerData) DepositToBank(itemID uint32, amount int32, bankMaxMass float32) int32 {
	if amount <= 0 {
		return 0
	}
	if pd.Bank == nil {
		pd.Bank = make(map[uint32]int32)
	}
	if bankMaxMass > 0 {
		currentMass := pd.BankTotalMass()
		remaining := bankMaxMass - currentMass
		massPerUnit := item.MassOf(itemID)
		if massPerUnit > 0 {
			maxByMass := int32(remaining / massPerUnit)
			if amount > maxByMass {
				amount = maxByMass
			}
		}
		// massPerUnit == 0 means weightless (e.g. FLUX), no mass limit
	}
	if amount <= 0 {
		return 0
	}
	pd.Bank[itemID] += amount
	return amount
}

// WithdrawFromBank removes items from the bank.
// Returns the amount actually withdrawn.
func (pd *PlayerData) WithdrawFromBank(itemID uint32, amount int32) int32 {
	if amount <= 0 || pd.Bank == nil {
		return 0
	}
	have := pd.Bank[itemID]
	if have <= 0 {
		return 0
	}
	if amount > have {
		amount = have
	}
	pd.Bank[itemID] -= amount
	if pd.Bank[itemID] <= 0 {
		delete(pd.Bank, itemID)
	}
	return amount
}

// BankTotalMass returns the total mass of all items in the bank.
func (pd *PlayerData) BankTotalMass() float32 {
	var total float32
	for id, qty := range pd.Bank {
		total += float32(qty) * item.MassOf(id)
	}
	return total
}

// CargoTotalMass returns the total mass of all items in cargo.
func (pd *PlayerData) CargoTotalMass() float32 {
	var total float32
	for id, qty := range pd.Cargo {
		total += float32(qty) * item.MassOf(id)
	}
	return total
}

// AddFlux adds flux to the player's balance.
func (pd *PlayerData) AddFlux(amount int64) { pd.Flux += amount }

// SpendFlux attempts to spend flux. Returns false if insufficient.
func (pd *PlayerData) SpendFlux(amount int64) bool {
	if pd.Flux < amount {
		return false
	}
	pd.Flux -= amount
	return true
}
