package game

import (
	"github.com/mmokit/mmokit"
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/examples/space/internal/item"
)

// Bank transfers, LootItem, and LootAll all dispatch via Commands.Defer
// from their input handlers — no per-tick System lives here anymore.
// This file holds the GameWorld methods invoked by those deferred
// closures plus the SendBankContents helper consumed by op_bank and the
// docked-equip path.

// performTransferFor handles a single cargo<->bank transfer, dispatched
// from the InventoryTransfer input handler via Commands.Defer.
// deposit=true is cargo->bank, false is bank->cargo. amount<=0 means
// "transfer as much as possible" (capped by source qty + dest capacity).
func (gw *GameWorld) performTransferFor(connID uint32, itemID uint32, amount int32, deposit bool) {
	sess := gw.Players.ByConnID(connID)
	if sess == nil || sess.Username == "" {
		return
	}
	username := sess.Username
	pdata := gw.PlayerDB.Bind(sess)

	// Docked players: operate on PlayerDB cargo directly.
	if sess.State == StateDocked {
		gw.performDockedTransfer(connID, itemID, amount, deposit, username, pdata)
		return
	}

	entity := mmokit.EntityFromECS(gw.stage, sess.Entity)
	if !entity.Alive() {
		return
	}
	pos := mmokit.Get[mmokit.Position](entity)
	inv := mmokit.Get[gamecomp.Inventory](entity)
	if pos == nil || inv == nil {
		return
	}

	// Collect station positions for nearness check (each call walks the
	// per-cell station list; cheap and avoids per-tick state).
	stationPositions := gw.collectStationPositions()
	sellRange := float64(gw.Config.SellRange)
	sellRange2 := sellRange * sellRange

	if !nearStationPos(pos, stationPositions, sellRange2) {
		gw.sendTransferResult(connID, false, "Not near a station", itemID, 0, deposit)
		return
	}

	if deposit {
		// Cargo -> Bank
		var have int32
		if inv.Items != nil {
			have = inv.Items[itemID]
		}
		if have <= 0 {
			gw.sendTransferResult(connID, false, "No items to deposit", itemID, 0, true)
			return
		}
		qty := have
		if amount > 0 && amount < qty {
			qty = amount
		}
		deposited := pdata.DepositToBank(itemID, qty, gw.Config.BankMaxMass)
		if deposited <= 0 {
			gw.sendTransferResult(connID, false, "Bank is full", itemID, 0, true)
			return
		}
		inv.RemoveItem(itemID, deposited)
		gw.PlayerDB.MarkDirty(username)
		gw.eng.Log.Log(CatEconomyBank, "bank deposit: player=%s item=%d qty=%d bank_mass=%.1f/%.1f",
			username, itemID, deposited, pdata.BankTotalMass(), gw.Config.BankMaxMass)
		gw.sendTransferResult(connID, true, "", itemID, deposited, true)
		gw.SendBankContents(connID, pdata)
	} else {
		// Bank -> Cargo
		var have int32
		if pdata.Bank != nil {
			have = pdata.Bank[itemID]
		}
		if have <= 0 {
			gw.sendTransferResult(connID, false, "No items to withdraw", itemID, 0, false)
			return
		}
		qty := have
		if amount > 0 && amount < qty {
			qty = amount
		}
		massPerUnit := item.MassOf(itemID)
		maxByMass := int32(inv.RemainingMass() / massPerUnit)
		if qty > maxByMass {
			qty = maxByMass
		}
		if qty <= 0 {
			gw.sendTransferResult(connID, false, "Cargo is full", itemID, 0, false)
			return
		}
		withdrawn := pdata.WithdrawFromBank(itemID, qty)
		inv.AddItem(itemID, withdrawn)
		gw.PlayerDB.MarkDirty(username)
		gw.eng.Log.Log(CatEconomyBank, "bank withdraw: player=%s item=%d qty=%d bank_mass=%.1f/%.1f",
			username, itemID, withdrawn, pdata.BankTotalMass(), gw.Config.BankMaxMass)
		gw.sendTransferResult(connID, true, "", itemID, withdrawn, false)
		gw.SendBankContents(connID, pdata)
	}
}

// collectStationPositions returns the positions of every station entity
// currently in this cell. Walks the ECS each call — cheap relative to
// the per-tick query and keeps the helper stateless.
func (gw *GameWorld) collectStationPositions() []mmokit.Position {
	var out []mmokit.Position
	mmokit.ForEach2(gw.stage, func(_ mmokit.Entity, _ *gamecomp.Station, pos *mmokit.Position) {
		out = append(out, *pos)
	})
	return out
}

// nearStationPos is the pure helper consumed by both the transfer path
// and any future caller that needs station-proximity validation.
func nearStationPos(pos *mmokit.Position, stations []mmokit.Position, range2 float64) bool {
	for _, sp := range stations {
		dx := float64(pos.X - sp.X)
		dy := float64(pos.Y - sp.Y)
		if dx*dx+dy*dy <= range2 {
			return true
		}
	}
	return false
}

// performDockedTransfer handles cargo<->bank transfers for docked
// players, operating on PlayerDB.Cargo / PlayerDB.Bank directly (no ECS
// entity round-trip — docked ships' Inventory is empty until undock
// re-copies pdata.Cargo back in).
func (gw *GameWorld) performDockedTransfer(connID uint32, itemID uint32, amount int32, deposit bool, username string, pdata *PlayerData) {
	if pdata.Cargo == nil {
		pdata.Cargo = make(map[uint32]int32)
	}

	if deposit {
		// pdata.Cargo -> Bank
		have := pdata.Cargo[itemID]
		if have <= 0 {
			gw.sendTransferResult(connID, false, "No items to deposit", itemID, 0, true)
			return
		}
		qty := have
		if amount > 0 && amount < qty {
			qty = amount
		}
		deposited := pdata.DepositToBank(itemID, qty, gw.Config.BankMaxMass)
		if deposited <= 0 {
			gw.sendTransferResult(connID, false, "Bank is full", itemID, 0, true)
			return
		}
		pdata.Cargo[itemID] -= deposited
		if pdata.Cargo[itemID] <= 0 {
			delete(pdata.Cargo, itemID)
		}
		gw.PlayerDB.MarkDirty(username)
		gw.eng.Log.Log(CatEconomyBank, "bank deposit (docked): player=%s item=%d qty=%d", username, itemID, deposited)
		gw.sendTransferResult(connID, true, "", itemID, deposited, true)
		gw.SendBankContents(connID, pdata)
	} else {
		// Bank -> pdata.Cargo
		var have int32
		if pdata.Bank != nil {
			have = pdata.Bank[itemID]
		}
		if have <= 0 {
			gw.sendTransferResult(connID, false, "No items to withdraw", itemID, 0, false)
			return
		}
		qty := have
		if amount > 0 && amount < qty {
			qty = amount
		}
		// Check cargo mass from PlayerDB
		cargoMass := pdata.CargoTotalMass()
		remaining := gw.Config.MaxCargo - cargoMass
		massPerUnit := item.MassOf(itemID)
		if massPerUnit > 0 {
			maxByMass := int32(remaining / massPerUnit)
			if qty > maxByMass {
				qty = maxByMass
			}
		}
		if qty <= 0 {
			gw.sendTransferResult(connID, false, "Cargo is full", itemID, 0, false)
			return
		}
		withdrawn := pdata.WithdrawFromBank(itemID, qty)
		pdata.Cargo[itemID] += withdrawn
		gw.PlayerDB.MarkDirty(username)
		gw.eng.Log.Log(CatEconomyBank, "bank withdraw (docked): player=%s item=%d qty=%d", username, itemID, withdrawn)
		gw.sendTransferResult(connID, true, "", itemID, withdrawn, false)
		gw.SendBankContents(connID, pdata)
	}
}

func (gw *GameWorld) sendTransferResult(connID uint32, success bool, reason string, itemID uint32, qty int32, deposit bool) {
	mmokit.SendEvent(gw.stage, connID, &TransferResult{
		Success:  success,
		Reason:   reason,
		ItemID:   itemID,
		Quantity: qty,
		Deposit:  deposit,
	})
}

// SendBankContents emits a typed BankContents event to one connection,
// snapshotting the player's bank, docked cargo, and currency balances.
// Used by the bank-transfer path and the docked equip path (docked equip
// changes touch pdata.Cargo, so the client needs the refreshed view).
func (gw *GameWorld) SendBankContents(connID uint32, pdata *PlayerData) {
	var items []InventoryItem
	for id, qty := range pdata.Bank {
		if qty > 0 {
			items = append(items, InventoryItem{ItemID: id, Quantity: qty})
		}
	}
	var cargoItems []InventoryItem
	for id, qty := range pdata.Cargo {
		if qty > 0 {
			cargoItems = append(cargoItems, InventoryItem{ItemID: id, Quantity: qty})
		}
	}
	var currencies []CurrencyBalance
	for curID, bal := range pdata.Currencies {
		if bal != 0 {
			currencies = append(currencies, CurrencyBalance{CurrencyID: curID, Balance: bal})
		}
	}
	mmokit.SendEvent(gw.stage, connID, &BankContents{
		Items:        items,
		TotalMass:    pdata.BankTotalMass(),
		MaxMass:      gw.Config.BankMaxMass,
		CargoItems:   cargoItems,
		CargoMass:    pdata.CargoTotalMass(),
		MaxCargoMass: gw.Config.MaxCargo,
		Currencies:   currencies,
	})
}

// performLootItemFor processes a single LootItem request. Dispatched
// via Commands.Defer from the LootItem input handler; runs at the next
// per-system flush boundary with the ECS world unlocked.
func (gw *GameWorld) performLootItemFor(connID uint32, crateNetID uint32, itemID uint32) {
	pickupRange2 := float64(gw.Config.LootPickupRange) * float64(gw.Config.LootPickupRange)

	sess := gw.Players.ByConnID(connID)
	if sess == nil || sess.State != mmokit.StateActive {
		return
	}
	entity := mmokit.EntityFromECS(gw.stage, sess.Entity)
	if !entity.Alive() {
		return
	}
	playerPos := mmokit.Get[mmokit.Position](entity)
	playerInv := mmokit.Get[gamecomp.Inventory](entity)
	if playerPos == nil || playerInv == nil {
		return
	}

	crateE := mmokit.EntityByNetID(gw.stage, crateNetID)
	if !crateE.Alive() || !mmokit.Has[gamecomp.LootCrate](crateE) {
		return
	}

	cratePos := mmokit.Get[mmokit.Position](crateE)
	if cratePos == nil {
		return
	}
	dx := float64(playerPos.X - cratePos.X)
	dy := float64(playerPos.Y - cratePos.Y)
	if dx*dx+dy*dy > pickupRange2 {
		return
	}

	crateInv := mmokit.Get[gamecomp.Inventory](crateE)
	if crateInv == nil {
		return
	}
	qty := crateInv.Items[itemID]
	if qty <= 0 {
		return
	}

	if item.IsCurrency(itemID) {
		// Currencies route to PlayerData.Currencies, not cargo inventory.
		if sess.Username != "" {
			pdata := gw.PlayerDB.Bind(sess)
			pdata.AddCurrency(itemID, int64(qty))
			gw.PlayerDB.MarkDirtyByUserID(pdata.UserID)
			mmokit.SendEvent(gw.stage, connID, &CurrencyUpdate{
				CurrencyID: itemID,
				Balance:    pdata.GetCurrency(itemID),
				Earned:     int64(qty),
			})
		}
		crateInv.RemoveItem(itemID, qty)
		gw.eng.Log.Log(CatEconomyLoot, "loot pickup: player=%d currency=%d qty=%d",
			entity.NetID(), itemID, qty)
	} else {
		added := playerInv.AddItem(itemID, qty)
		if added > 0 {
			crateInv.RemoveItem(itemID, added)
			gw.eng.Log.Log(CatEconomyLoot, "loot pickup: player=%d item=%d qty=%d cargo_mass=%.1f/%.1f",
				entity.NetID(), itemID, added, playerInv.TotalMass(), playerInv.MaxMass)
		}
	}

	if crateInv.IsEmpty() {
		gw.MarkForRemoval(crateE.Handle())
	}
}

// performLootAllFor processes a single LootAll request. Dispatched via
// Commands.Defer from the LootAll input handler; runs at the next per-system
// flush boundary with the ECS world unlocked.
func (gw *GameWorld) performLootAllFor(connID uint32, crateNetID uint32) {
	pickupRange2 := float64(gw.Config.LootPickupRange) * float64(gw.Config.LootPickupRange)

	sess := gw.Players.ByConnID(connID)
	if sess == nil || sess.State != mmokit.StateActive {
		return
	}
	entity := mmokit.EntityFromECS(gw.stage, sess.Entity)
	if !entity.Alive() {
		return
	}
	playerPos := mmokit.Get[mmokit.Position](entity)
	playerInv := mmokit.Get[gamecomp.Inventory](entity)
	if playerPos == nil || playerInv == nil {
		return
	}

	crateE := mmokit.EntityByNetID(gw.stage, crateNetID)
	if !crateE.Alive() || !mmokit.Has[gamecomp.LootCrate](crateE) {
		return
	}

	cratePos := mmokit.Get[mmokit.Position](crateE)
	crateInv := mmokit.Get[gamecomp.Inventory](crateE)
	if cratePos == nil || crateInv == nil {
		return
	}
	dx := float64(playerPos.X - cratePos.X)
	dy := float64(playerPos.Y - cratePos.Y)
	if dx*dx+dy*dy > pickupRange2 {
		return
	}

	playerNetID := entity.NetID()

	// Currencies don't go through the cargo inventory — they route into
	// PlayerData.Currencies. Bind once and reuse for any currency items.
	var pdata *PlayerData
	if sess.Username != "" {
		pdata = gw.PlayerDB.Bind(sess)
	}

	for itemID, qty := range crateInv.Items {
		if qty <= 0 {
			continue
		}
		if item.IsCurrency(itemID) {
			// Currencies route to PlayerData.Currencies; the inventory's
			// mass system doesn't apply (MassPerUnit=0 for currencies).
			if pdata != nil {
				pdata.AddCurrency(itemID, int64(qty))
				gw.PlayerDB.MarkDirtyByUserID(pdata.UserID)
				mmokit.SendEvent(gw.stage, connID, &CurrencyUpdate{
					CurrencyID: itemID,
					Balance:    pdata.GetCurrency(itemID),
					Earned:     int64(qty),
				})
			}
			crateInv.RemoveItem(itemID, qty)
			gw.eng.Log.Log(CatEconomyLoot, "loot pickup: player=%d currency=%d qty=%d",
				playerNetID, itemID, qty)
			continue
		}
		added := playerInv.AddItem(itemID, qty)
		if added > 0 {
			crateInv.RemoveItem(itemID, added)
			gw.eng.Log.Log(CatEconomyLoot, "loot pickup: player=%d item=%d qty=%d cargo_mass=%.1f/%.1f",
				playerNetID, itemID, added, playerInv.TotalMass(), playerInv.MaxMass)
		}
		if playerInv.RemainingMass() <= 0 {
			break
		}
	}

	if crateInv.IsEmpty() {
		gw.MarkForRemoval(crateE.Handle())
	}
}
