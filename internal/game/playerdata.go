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
	CellX     int32            `json:"cell_x"`
	CellY     int32            `json:"cell_y"`
	Currencies map[uint32]int64 `json:"currencies,omitempty"`
	Cargo     map[uint32]int32 `json:"cargo,omitempty"`
	Bank      map[uint32]int32 `json:"bank,omitempty"`
	Equipment EquipmentSave    `json:"equipment,omitempty"`
	HasSave   bool             `json:"has_save"`
	CreatedAt time.Time        `json:"created_at"`
	LastLogin time.Time        `json:"last_login"`
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
		// massPerUnit == 0 means weightless (e.g. currency), no mass limit
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

// GetCurrency returns the player's balance of the given currency.
func (pd *PlayerData) GetCurrency(currencyID uint32) int64 {
	if pd.Currencies == nil {
		return 0
	}
	return pd.Currencies[currencyID]
}

// AddCurrency adds an amount to the player's balance of the given currency.
func (pd *PlayerData) AddCurrency(currencyID uint32, amount int64) {
	if pd.Currencies == nil {
		pd.Currencies = make(map[uint32]int64)
	}
	pd.Currencies[currencyID] += amount
}

// SpendCurrency attempts to spend currency. Returns false if insufficient.
func (pd *PlayerData) SpendCurrency(currencyID uint32, amount int64) bool {
	if pd.GetCurrency(currencyID) < amount {
		return false
	}
	pd.Currencies[currencyID] -= amount
	return true
}
