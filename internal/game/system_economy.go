package game

import (
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// EconomySystem handles manual loot crate pickup, bank transfers (deposit/withdraw),
// and selling bank items for currency.
type EconomySystem struct {
	mmokit.SystemBase[*GameWorld]
	stations mmokit.Query[struct {
		Station *gamecomp.Station
		Pos     *mmokit.Position
	}]
}

func (s *EconomySystem) Update(dt float32) {
	// Collect station positions
	var stationPositions []mmokit.Position
	for _, b := range s.stations.Iter {
		stationPositions = append(stationPositions, *b.Pos)
	}

	// Process manual loot requests
	s.processLootItems()
	s.processLootAlls()

	sellRange2 := s.stationRange2()

	// Process bank transfers
	s.processTransfers(stationPositions, sellRange2)

	// Process sell requests (bank items → currency)
	s.processSells(stationPositions, sellRange2)

	// Process bank view requests
	s.processBankRequests(stationPositions, sellRange2)

	// Process shop buy requests
	s.processShopBuys(stationPositions, sellRange2)
}

func (s *EconomySystem) processTransfers(stationPositions []mmokit.Position, sellRange2 float64) {
	gw := s.World()
	for _, t := range mmokit.Drain[PendingTransfer](gw.Queue) {
		sess := gw.Players.ByConnID(t.ConnID)
		if sess == nil || sess.Username == "" {
			continue
		}
		username := sess.Username
		pdata := gw.PlayerDB.GetOrCreate(username)

		// Docked players: operate on PlayerDB cargo directly
		if sess.State == StateDocked {
			s.processDockedTransfer(t, username, pdata)
			continue
		}

		entity := sess.Entity
		if !gw.eng.ECS.Alive(entity) {
			continue
		}
		if !gw.C.Inventory.HasAll(entity) || !gw.C.Position.HasAll(entity) {
			continue
		}

		pos := gw.C.Position.Get(entity)
		inv := gw.C.Inventory.Get(entity)

		if !s.nearStation(pos, stationPositions, sellRange2) {
			s.sendTransferResult(t.ConnID, false, "Not near a station", t.ItemID, 0, t.Deposit)
			continue
		}

		if t.Deposit {
			// Cargo -> Bank
			var have int32
			if inv.Items != nil {
				have = inv.Items[t.ItemID]
			}
			if have <= 0 {
				s.sendTransferResult(t.ConnID, false, "No items to deposit", t.ItemID, 0, true)
				continue
			}
			amount := have
			if t.Amount > 0 && t.Amount < amount {
				amount = t.Amount
			}
			deposited := pdata.DepositToBank(t.ItemID, amount, gw.Config.BankMaxMass)
			if deposited <= 0 {
				s.sendTransferResult(t.ConnID, false, "Bank is full", t.ItemID, 0, true)
				continue
			}
			inv.RemoveItem(t.ItemID, deposited)
			gw.PlayerDB.MarkDirty(username)
			gw.eng.Log.Log(CatEconomyBank, "bank deposit: player=%s item=%d qty=%d bank_mass=%.1f/%.1f",
				username, t.ItemID, deposited, pdata.BankTotalMass(), gw.Config.BankMaxMass)
			s.sendTransferResult(t.ConnID, true, "", t.ItemID, deposited, true)
			s.sendBankContents(t.ConnID, pdata)
		} else {
			// Bank -> Cargo
			var have int32
			if pdata.Bank != nil {
				have = pdata.Bank[t.ItemID]
			}
			if have <= 0 {
				s.sendTransferResult(t.ConnID, false, "No items to withdraw", t.ItemID, 0, false)
				continue
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
				s.sendTransferResult(t.ConnID, false, "Cargo is full", t.ItemID, 0, false)
				continue
			}
			withdrawn := pdata.WithdrawFromBank(t.ItemID, amount)
			inv.AddItem(t.ItemID, withdrawn)
			gw.PlayerDB.MarkDirty(username)
			gw.eng.Log.Log(CatEconomyBank, "bank withdraw: player=%s item=%d qty=%d bank_mass=%.1f/%.1f",
				username, t.ItemID, withdrawn, pdata.BankTotalMass(), gw.Config.BankMaxMass)
			s.sendTransferResult(t.ConnID, true, "", t.ItemID, withdrawn, false)
			s.sendBankContents(t.ConnID, pdata)
		}
	}
}

// processDockedTransfer handles cargo<->bank transfers for docked players using PlayerDB.
func (s *EconomySystem) processDockedTransfer(t PendingTransfer, username string, pdata *PlayerData) {
	gw := s.World()
	if pdata.Cargo == nil {
		pdata.Cargo = make(map[uint32]int32)
	}

	if t.Deposit {
		// pdata.Cargo -> Bank
		have := pdata.Cargo[t.ItemID]
		if have <= 0 {
			s.sendTransferResult(t.ConnID, false, "No items to deposit", t.ItemID, 0, true)
			return
		}
		amount := have
		if t.Amount > 0 && t.Amount < amount {
			amount = t.Amount
		}
		deposited := pdata.DepositToBank(t.ItemID, amount, gw.Config.BankMaxMass)
		if deposited <= 0 {
			s.sendTransferResult(t.ConnID, false, "Bank is full", t.ItemID, 0, true)
			return
		}
		pdata.Cargo[t.ItemID] -= deposited
		if pdata.Cargo[t.ItemID] <= 0 {
			delete(pdata.Cargo, t.ItemID)
		}
		gw.PlayerDB.MarkDirty(username)
		gw.eng.Log.Log(CatEconomyBank, "bank deposit (docked): player=%s item=%d qty=%d", username, t.ItemID, deposited)
		s.sendTransferResult(t.ConnID, true, "", t.ItemID, deposited, true)
		s.sendBankContents(t.ConnID, pdata)
	} else {
		// Bank -> pdata.Cargo
		var have int32
		if pdata.Bank != nil {
			have = pdata.Bank[t.ItemID]
		}
		if have <= 0 {
			s.sendTransferResult(t.ConnID, false, "No items to withdraw", t.ItemID, 0, false)
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
			s.sendTransferResult(t.ConnID, false, "Cargo is full", t.ItemID, 0, false)
			return
		}
		withdrawn := pdata.WithdrawFromBank(t.ItemID, amount)
		pdata.Cargo[t.ItemID] += withdrawn
		gw.PlayerDB.MarkDirty(username)
		gw.eng.Log.Log(CatEconomyBank, "bank withdraw (docked): player=%s item=%d qty=%d", username, t.ItemID, withdrawn)
		s.sendTransferResult(t.ConnID, true, "", t.ItemID, withdrawn, false)
		s.sendBankContents(t.ConnID, pdata)
	}
}

func (s *EconomySystem) processSells(stationPositions []mmokit.Position, sellRange2 float64) {
	gw := s.World()
	for _, req := range mmokit.Drain[PendingSellRequest](gw.Queue) {
		sess := gw.Players.ByConnID(req.ConnID)
		if sess == nil || sess.Username == "" {
			continue
		}
		username := sess.Username
		pdata := gw.PlayerDB.GetOrCreate(username)

		// Docked players skip entity/proximity check
		if sess.State != StateDocked {
			entity := sess.Entity
			if !gw.eng.ECS.Alive(entity) {
				continue
			}
			if !gw.C.Position.HasAll(entity) {
				continue
			}
			pos := gw.C.Position.Get(entity)
			if !s.nearStation(pos, stationPositions, sellRange2) {
				s.sendTransferResult(req.ConnID, false, "Not near a station", req.ItemID, 0, false)
				continue
			}
		}

		// Validate item is sellable
		def := item.Get(req.ItemID)
		if def == nil || def.SellPrice <= 0 {
			s.sendTransferResult(req.ConnID, false, "Item cannot be sold", req.ItemID, 0, false)
			continue
		}

		// Check bank has the item
		var have int32
		if pdata.Bank != nil {
			have = pdata.Bank[req.ItemID]
		}
		if have <= 0 {
			s.sendTransferResult(req.ConnID, false, "No items to sell", req.ItemID, 0, false)
			continue
		}

		// Determine amount to sell
		amount := have
		if req.Amount > 0 && req.Amount < amount {
			amount = req.Amount
		}

		// Withdraw from bank and convert to currency
		withdrawn := pdata.WithdrawFromBank(req.ItemID, amount)
		fluxEarned := int64(float64(withdrawn) * def.SellPrice)
		settleCur := gw.Config.SettlementCurrencyID
		pdata.AddCurrency(settleCur, fluxEarned)
		gw.PlayerDB.MarkDirty(username)

		gw.eng.Log.Log(CatEconomyShop, "bank sell: player=%s item=%d qty=%d earned=%d balance=%d",
			username, req.ItemID, withdrawn, fluxEarned, pdata.GetCurrency(settleCur))

		s.sendTransferResult(req.ConnID, true, "", req.ItemID, withdrawn, false)
		s.sendBankContents(req.ConnID, pdata)
	}
}

func (s *EconomySystem) processBankRequests(stationPositions []mmokit.Position, sellRange2 float64) {
	gw := s.World()
	for _, req := range mmokit.Drain[PendingBankRequest](gw.Queue) {
		sess := gw.Players.ByConnID(req.ConnID)
		if sess == nil || sess.Username == "" {
			continue
		}

		// Docked players skip entity/proximity check
		if sess.State != StateDocked {
			entity := sess.Entity
			if !gw.eng.ECS.Alive(entity) {
				continue
			}
			if !gw.C.Position.HasAll(entity) {
				continue
			}
			pos := gw.C.Position.Get(entity)
			if !s.nearStation(pos, stationPositions, sellRange2) {
				continue
			}
		}

		pdata := gw.PlayerDB.GetOrCreate(sess.Username)
		s.sendBankContents(req.ConnID, pdata)
	}
}

func (s *EconomySystem) stationRange2() float64 {
	r := float64(s.World().Config.SellRange)
	return r * r
}

func (s *EconomySystem) nearStation(pos *mmokit.Position, stations []mmokit.Position, range2 float64) bool {
	for _, sp := range stations {
		dx := float64(pos.X - sp.X)
		dy := float64(pos.Y - sp.Y)
		if dx*dx+dy*dy <= range2 {
			return true
		}
	}
	return false
}

func (s *EconomySystem) processShopBuys(stationPositions []mmokit.Position, sellRange2 float64) {
	gw := s.World()
	for _, req := range mmokit.Drain[PendingShopBuy](gw.Queue) {
		sess := gw.Players.ByConnID(req.ConnID)
		if sess == nil || sess.Username == "" {
			continue
		}
		username := sess.Username

		isDocked := sess.State == StateDocked

		// Non-docked players need entity + proximity check
		if !isDocked {
			entity := sess.Entity
			if !gw.eng.ECS.Alive(entity) {
				continue
			}
			if !gw.C.Position.HasAll(entity) || !gw.C.Inventory.HasAll(entity) {
				continue
			}
			pos := gw.C.Position.Get(entity)
			if !s.nearStation(pos, stationPositions, sellRange2) {
				s.sendTransferResult(req.ConnID, false, "Not near a station", req.ItemID, 0, false)
				continue
			}
		}

		def := item.Get(req.ItemID)
		if def == nil || def.BuyPrice <= 0 {
			s.sendTransferResult(req.ConnID, false, "Item not available for purchase", req.ItemID, 0, false)
			continue
		}

		pdata := gw.PlayerDB.GetOrCreate(username)

		qty := int32(req.Qty)
		if qty <= 0 {
			qty = 1
		}
		totalCost := int64(def.BuyPrice) * int64(qty)

		// Check currency balance
		settleCur := gw.Config.SettlementCurrencyID
		if pdata.GetCurrency(settleCur) < totalCost {
			s.sendTransferResult(req.ConnID, false, "Not enough currency", req.ItemID, 0, false)
			continue
		}

		// Check cargo space
		massNeeded := def.MassPerUnit * float32(qty)
		if isDocked {
			remaining := gw.Config.MaxCargo - pdata.CargoTotalMass()
			if remaining < massNeeded {
				s.sendTransferResult(req.ConnID, false, "Cargo is full", req.ItemID, 0, false)
				continue
			}
		} else {
			entity := sess.Entity
			inv := gw.C.Inventory.Get(entity)
			if inv.RemainingMass() < massNeeded {
				s.sendTransferResult(req.ConnID, false, "Cargo is full", req.ItemID, 0, false)
				continue
			}
		}

		// Deduct currency and add item to cargo
		pdata.SpendCurrency(settleCur, totalCost)

		if isDocked {
			if pdata.Cargo == nil {
				pdata.Cargo = make(map[uint32]int32)
			}
			pdata.Cargo[req.ItemID] += qty
		} else {
			entity := sess.Entity
			inv := gw.C.Inventory.Get(entity)
			inv.AddItem(req.ItemID, qty)
		}
		gw.PlayerDB.MarkDirty(username)

		gw.eng.Log.Log(CatEconomyShop, "shop buy: player=%s item=%d qty=%d cost=%d balance=%d",
			username, req.ItemID, qty, totalCost, pdata.GetCurrency(settleCur))

		s.sendTransferResult(req.ConnID, true, "", req.ItemID, qty, false)
		s.sendBankContents(req.ConnID, pdata)
	}
}

func (s *EconomySystem) sendTransferResult(connID uint32, success bool, reason string, itemID uint32, qty int32, deposit bool) {
	gw := s.World()
	gw.ServerEvents().Send(gw.eng.ConnMgr, connID, uint32(gamepb.GameServerEventCode_GSE_TRANSFER_RESULT), &gamepb.TransferResultMsg{
		Success:  success,
		Reason:   reason,
		ItemId:   itemID,
		Quantity: qty,
		Deposit:  deposit,
	})
}

func (s *EconomySystem) sendBankContents(connID uint32, pdata *PlayerData) {
	var items []*gamepb.InventoryItem
	for id, qty := range pdata.Bank {
		if qty > 0 {
			items = append(items, &gamepb.InventoryItem{ItemId: id, Quantity: qty})
		}
	}
	var cargoItems []*gamepb.InventoryItem
	for id, qty := range pdata.Cargo {
		if qty > 0 {
			cargoItems = append(cargoItems, &gamepb.InventoryItem{ItemId: id, Quantity: qty})
		}
	}
	var currencies []*gamepb.CurrencyBalance
	for curID, bal := range pdata.Currencies {
		if bal != 0 {
			currencies = append(currencies, &gamepb.CurrencyBalance{CurrencyId: curID, Balance: bal})
		}
	}
	gw := s.World()
	gw.ServerEvents().Send(gw.eng.ConnMgr, connID, uint32(gamepb.GameServerEventCode_GSE_BANK_CONTENTS), &gamepb.BankContentsMsg{
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
	gw := s.World()
	pickupRange2 := float64(gw.Config.LootPickupRange) * float64(gw.Config.LootPickupRange)

	for _, req := range mmokit.Drain[PendingLootItem](gw.Queue) {
		sess := gw.Players.ByConnID(req.ConnID)
		if sess == nil || sess.State != mmokit.StateActive {
			continue
		}
		entity := sess.Entity
		if !gw.eng.ECS.Alive(entity) {
			continue
		}
		if !gw.C.Position.HasAll(entity) || !gw.C.Inventory.HasAll(entity) {
			continue
		}

		crateEntity, ok := gw.NetIDToEntity[req.CrateNetID]
		if !ok || !gw.eng.ECS.Alive(crateEntity) {
			continue
		}
		if !gw.C.LootCrate.HasAll(crateEntity) {
			continue
		}

		playerPos := gw.C.Position.Get(entity)
		cratePos := gw.C.Position.Get(crateEntity)
		dx := float64(playerPos.X - cratePos.X)
		dy := float64(playerPos.Y - cratePos.Y)
		if dx*dx+dy*dy > pickupRange2 {
			continue
		}

		crateInv := gw.C.Inventory.Get(crateEntity)
		qty := crateInv.Items[req.ItemID]
		if qty <= 0 {
			continue
		}

		playerInv := gw.C.Inventory.Get(entity)
		added := playerInv.AddItem(req.ItemID, qty)
		if added > 0 {
			crateInv.RemoveItem(req.ItemID, added)
			playerNetID := gw.C.NetworkID.Get(entity).ID
			gw.eng.Log.Log(CatEconomyLoot, "loot pickup: player=%d item=%d qty=%d cargo_mass=%.1f/%.1f",
				playerNetID, req.ItemID, added, playerInv.TotalMass(), playerInv.MaxMass)
		}

		if crateInv.IsEmpty() {
			gw.MarkForRemoval(crateEntity)
		}
	}
}

func (s *EconomySystem) processLootAlls() {
	gw := s.World()
	pickupRange2 := float64(gw.Config.LootPickupRange) * float64(gw.Config.LootPickupRange)

	for _, req := range mmokit.Drain[PendingLootAll](gw.Queue) {
		sess := gw.Players.ByConnID(req.ConnID)
		if sess == nil || sess.State != mmokit.StateActive {
			continue
		}
		entity := sess.Entity
		if !gw.eng.ECS.Alive(entity) {
			continue
		}
		if !gw.C.Position.HasAll(entity) || !gw.C.Inventory.HasAll(entity) {
			continue
		}

		crateEntity, ok := gw.NetIDToEntity[req.CrateNetID]
		if !ok || !gw.eng.ECS.Alive(crateEntity) {
			continue
		}
		if !gw.C.LootCrate.HasAll(crateEntity) {
			continue
		}

		playerPos := gw.C.Position.Get(entity)
		cratePos := gw.C.Position.Get(crateEntity)
		dx := float64(playerPos.X - cratePos.X)
		dy := float64(playerPos.Y - cratePos.Y)
		if dx*dx+dy*dy > pickupRange2 {
			continue
		}

		crateInv := gw.C.Inventory.Get(crateEntity)
		playerInv := gw.C.Inventory.Get(entity)
		playerNetID := gw.C.NetworkID.Get(entity).ID

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
			gw.MarkForRemoval(crateEntity)
		}
	}
}
