package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// EconomySystem handles manual loot crate pickup. Bank transfers (deposit/
// withdraw) and LootAll are dispatched via Commands.Defer from the input
// handlers and do not flow through Update.
type EconomySystem struct {
	mmokit.SystemBase
	gw *GameWorld
}

func (s *EconomySystem) Init() {
	s.gw = mmokit.State[GameWorld](s.Stage())
}

func (s *EconomySystem) Update(dt float32) {
	// Bank transfers and LootAll are dispatched via Commands.Defer from
	// their input handlers — no drain here. PendingLootItem still flows
	// through this system (drained below) until it is migrated.
	s.processLootItems()
}

// processTransferFor handles a single cargo<->bank transfer, dispatched
// from the InventoryTransfer input handler via Commands.Defer.
func (gw *GameWorld) processTransferFor(t PendingTransfer) {
	sess := gw.Players.ByConnID(t.ConnID)
	if sess == nil || sess.Username == "" {
		return
	}
	username := sess.Username
	pdata := gw.PlayerDB.GetOrCreate(username)

	// Docked players: operate on PlayerDB cargo directly.
	if sess.State == StateDocked {
		gw.processDockedTransfer(t, username, pdata)
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
		gw.sendTransferResult(t.ConnID, false, "Not near a station", t.ItemID, 0, t.Deposit)
		return
	}

	if t.Deposit {
		// Cargo -> Bank
		var have int32
		if inv.Items != nil {
			have = inv.Items[t.ItemID]
		}
		if have <= 0 {
			gw.sendTransferResult(t.ConnID, false, "No items to deposit", t.ItemID, 0, true)
			return
		}
		amount := have
		if t.Amount > 0 && t.Amount < amount {
			amount = t.Amount
		}
		deposited := pdata.DepositToBank(t.ItemID, amount, gw.Config.BankMaxMass)
		if deposited <= 0 {
			gw.sendTransferResult(t.ConnID, false, "Bank is full", t.ItemID, 0, true)
			return
		}
		inv.RemoveItem(t.ItemID, deposited)
		gw.PlayerDB.MarkDirty(username)
		gw.eng.Log.Log(CatEconomyBank, "bank deposit: player=%s item=%d qty=%d bank_mass=%.1f/%.1f",
			username, t.ItemID, deposited, pdata.BankTotalMass(), gw.Config.BankMaxMass)
		gw.sendTransferResult(t.ConnID, true, "", t.ItemID, deposited, true)
		gw.SendBankContents(t.ConnID, pdata)
	} else {
		// Bank -> Cargo
		var have int32
		if pdata.Bank != nil {
			have = pdata.Bank[t.ItemID]
		}
		if have <= 0 {
			gw.sendTransferResult(t.ConnID, false, "No items to withdraw", t.ItemID, 0, false)
			return
		}
		amount := have
		if t.Amount > 0 && t.Amount < amount {
			amount = t.Amount
		}
		massPerUnit := item.MassOf(t.ItemID)
		maxByMass := int32(inv.RemainingMass() / massPerUnit)
		if amount > maxByMass {
			amount = maxByMass
		}
		if amount <= 0 {
			gw.sendTransferResult(t.ConnID, false, "Cargo is full", t.ItemID, 0, false)
			return
		}
		withdrawn := pdata.WithdrawFromBank(t.ItemID, amount)
		inv.AddItem(t.ItemID, withdrawn)
		gw.PlayerDB.MarkDirty(username)
		gw.eng.Log.Log(CatEconomyBank, "bank withdraw: player=%s item=%d qty=%d bank_mass=%.1f/%.1f",
			username, t.ItemID, withdrawn, pdata.BankTotalMass(), gw.Config.BankMaxMass)
		gw.sendTransferResult(t.ConnID, true, "", t.ItemID, withdrawn, false)
		gw.SendBankContents(t.ConnID, pdata)
	}
}

// collectStationPositions returns the positions of every station entity
// currently in this cell. Walks the ECS each call — cheap relative to
// the per-tick query and keeps the helper stateless.
func (gw *GameWorld) collectStationPositions() []mmokit.Position {
	var out []mmokit.Position
	mmokit.ForEach2[gamecomp.Station, mmokit.Position](gw.stage, func(_ mmokit.Entity, _ *gamecomp.Station, pos *mmokit.Position) {
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

// processDockedTransfer handles cargo<->bank transfers for docked players using PlayerDB.
func (gw *GameWorld) processDockedTransfer(t PendingTransfer, username string, pdata *PlayerData) {
	if pdata.Cargo == nil {
		pdata.Cargo = make(map[uint32]int32)
	}

	if t.Deposit {
		// pdata.Cargo -> Bank
		have := pdata.Cargo[t.ItemID]
		if have <= 0 {
			gw.sendTransferResult(t.ConnID, false, "No items to deposit", t.ItemID, 0, true)
			return
		}
		amount := have
		if t.Amount > 0 && t.Amount < amount {
			amount = t.Amount
		}
		deposited := pdata.DepositToBank(t.ItemID, amount, gw.Config.BankMaxMass)
		if deposited <= 0 {
			gw.sendTransferResult(t.ConnID, false, "Bank is full", t.ItemID, 0, true)
			return
		}
		pdata.Cargo[t.ItemID] -= deposited
		if pdata.Cargo[t.ItemID] <= 0 {
			delete(pdata.Cargo, t.ItemID)
		}
		gw.PlayerDB.MarkDirty(username)
		gw.eng.Log.Log(CatEconomyBank, "bank deposit (docked): player=%s item=%d qty=%d", username, t.ItemID, deposited)
		gw.sendTransferResult(t.ConnID, true, "", t.ItemID, deposited, true)
		gw.SendBankContents(t.ConnID, pdata)
	} else {
		// Bank -> pdata.Cargo
		var have int32
		if pdata.Bank != nil {
			have = pdata.Bank[t.ItemID]
		}
		if have <= 0 {
			gw.sendTransferResult(t.ConnID, false, "No items to withdraw", t.ItemID, 0, false)
			return
		}
		amount := have
		if t.Amount > 0 && t.Amount < amount {
			amount = t.Amount
		}
		// Check cargo mass from PlayerDB
		cargoMass := pdata.CargoTotalMass()
		remaining := gw.Config.MaxCargo - cargoMass
		massPerUnit := item.MassOf(t.ItemID)
		if massPerUnit > 0 {
			maxByMass := int32(remaining / massPerUnit)
			if amount > maxByMass {
				amount = maxByMass
			}
		}
		if amount <= 0 {
			gw.sendTransferResult(t.ConnID, false, "Cargo is full", t.ItemID, 0, false)
			return
		}
		withdrawn := pdata.WithdrawFromBank(t.ItemID, amount)
		pdata.Cargo[t.ItemID] += withdrawn
		gw.PlayerDB.MarkDirty(username)
		gw.eng.Log.Log(CatEconomyBank, "bank withdraw (docked): player=%s item=%d qty=%d", username, t.ItemID, withdrawn)
		gw.sendTransferResult(t.ConnID, true, "", t.ItemID, withdrawn, false)
		gw.SendBankContents(t.ConnID, pdata)
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
// Used by EconomySystem and EquipmentSystem (docked equip changes touch
// pdata.Cargo, so the client needs the refreshed view).
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

func (s *EconomySystem) processLootItems() {
	gw := s.gw
	pickupRange2 := float64(gw.Config.LootPickupRange) * float64(gw.Config.LootPickupRange)

	for _, req := range mmokit.Drain[PendingLootItem](gw.Queue) {
		sess := gw.Players.ByConnID(req.ConnID)
		if sess == nil || sess.State != mmokit.StateActive {
			continue
		}
		entity := mmokit.EntityFromECS(gw.stage, sess.Entity)
		if !entity.Alive() {
			continue
		}
		playerPos := mmokit.Get[mmokit.Position](entity)
		playerInv := mmokit.Get[gamecomp.Inventory](entity)
		if playerPos == nil || playerInv == nil {
			continue
		}

		crateE := mmokit.EntityByNetID(gw.stage, req.CrateNetID)
		if !crateE.Alive() || !mmokit.Has[gamecomp.LootCrate](crateE) {
			continue
		}

		cratePos := mmokit.Get[mmokit.Position](crateE)
		if cratePos == nil {
			continue
		}
		dx := float64(playerPos.X - cratePos.X)
		dy := float64(playerPos.Y - cratePos.Y)
		if dx*dx+dy*dy > pickupRange2 {
			continue
		}

		crateInv := mmokit.Get[gamecomp.Inventory](crateE)
		if crateInv == nil {
			continue
		}
		qty := crateInv.Items[req.ItemID]
		if qty <= 0 {
			continue
		}

		added := playerInv.AddItem(req.ItemID, qty)
		if added > 0 {
			crateInv.RemoveItem(req.ItemID, added)
			gw.eng.Log.Log(CatEconomyLoot, "loot pickup: player=%d item=%d qty=%d cargo_mass=%.1f/%.1f",
				entity.NetID(), req.ItemID, added, playerInv.TotalMass(), playerInv.MaxMass)
		}

		if crateInv.IsEmpty() {
			gw.MarkForRemoval(crateE.Handle())
		}
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

	for itemID, qty := range crateInv.Items {
		if qty <= 0 {
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
