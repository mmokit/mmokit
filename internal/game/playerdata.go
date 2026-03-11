package game

import (
	"encoding/json"
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
	Username  string             `json:"username"`
	X         float32            `json:"x"`
	Y         float32            `json:"y"`
	Cargo     map[uint32]float32 `json:"cargo,omitempty"`
	Bank      map[uint32]float32 `json:"bank,omitempty"`
	Equipment EquipmentSave      `json:"equipment,omitempty"`
	Resources [4]float32         `json:"resources"` // deprecated: kept for migration
	HasSave   bool               `json:"has_save"`
	CreatedAt time.Time          `json:"created_at"`
	LastLogin time.Time          `json:"last_login"`
}

// MigrateResources converts old Resources [4]float32 to Cargo map if needed.
func (pd *PlayerData) MigrateResources() {
	if pd.Cargo != nil {
		return
	}
	var hasResources bool
	for _, r := range pd.Resources {
		if r > 0 {
			hasResources = true
			break
		}
	}
	if !hasResources {
		return
	}
	pd.Cargo = make(map[uint32]float32)
	for i, amt := range pd.Resources {
		if amt > 0 {
			pd.Cargo[item.ResourceItemID(uint8(i))] = amt
		}
	}
	pd.Resources = [4]float32{}
}

// MigrateFlux converts old "flux" JSON field into Bank[FluxItemID].
// Must be called with the raw JSON bytes before the Flux field was removed.
func (pd *PlayerData) MigrateFlux(rawJSON []byte) {
	var legacy struct {
		Flux float64 `json:"flux"`
	}
	if err := json.Unmarshal(rawJSON, &legacy); err != nil || legacy.Flux <= 0 {
		return
	}
	if pd.Bank == nil {
		pd.Bank = make(map[uint32]float32)
	}
	pd.Bank[item.FluxItemID] += float32(legacy.Flux)
}

// MigrateItemIDs shifts old item IDs {1,2,3,4} → {2,3,4,5} in a map.
// Detects old format by checking: has keys in 1-4 range and no key 5.
func migrateItemIDs(items map[uint32]float32) map[uint32]float32 {
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

	migrated := make(map[uint32]float32, len(items))
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
func (pd *PlayerData) DepositToBank(itemID uint32, amount float32, bankMaxMass float32) float32 {
	if amount <= 0 {
		return 0
	}
	if pd.Bank == nil {
		pd.Bank = make(map[uint32]float32)
	}
	if bankMaxMass > 0 {
		currentMass := pd.BankTotalMass()
		remaining := bankMaxMass - currentMass
		massPerUnit := item.MassOf(itemID)
		if massPerUnit > 0 {
			maxByMass := remaining / massPerUnit
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
func (pd *PlayerData) WithdrawFromBank(itemID uint32, amount float32) float32 {
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
		total += qty * item.MassOf(id)
	}
	return total
}
