package system

import (
	"github.com/mlange-42/ark/ecs"
	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/internal/netutil"
	"github.com/zenion/mmoserver/pkg/engine"
)

// EconomySystem handles manual loot crate pickup, bank transfers (deposit/withdraw),
// and selling bank items for FLUX.
type EconomySystem struct {
	gw            *game.GameWorld
	stationFilter *ecs.Filter2[component.Station, component.Position]
}

func NewEconomySystem(gw *game.GameWorld) *EconomySystem {
	return &EconomySystem{gw: gw}
}

func (s *EconomySystem) Update(dt float32) {
	gw := s.gw
	if s.stationFilter == nil {
		s.stationFilter = ecs.NewFilter2[component.Station, component.Position](gw.ECS)
	}

	// Collect station positions
	var stationPositions []component.Position
	stationQuery := s.stationFilter.Query()
	for stationQuery.Next() {
		_, pos := stationQuery.Get()
		stationPositions = append(stationPositions, *pos)
	}

	// Process manual loot requests
	s.processLootItems()
	s.processLootAlls()

	sellRange2 := s.stationRange2()

	// Process bank transfers
	s.processTransfers(stationPositions, sellRange2)

	// Process sell requests (bank items → FLUX)
	s.processSells(stationPositions, sellRange2)

	// Process bank view requests
	s.processBankRequests(stationPositions, sellRange2)

	// Process shop buy requests
	s.processShopBuys(stationPositions, sellRange2)
}

func (s *EconomySystem) processTransfers(stationPositions []component.Position, sellRange2 float64) {
	gw := s.gw
	for _, t := range engine.Drain[game.PendingTransfer](gw.Queue) {
		username := gw.Players.Usernames[t.ConnID]
		if username == "" {
			continue
		}
		pdata := gw.PlayerDB.GetOrCreate(username)

		// Docked players: operate on PlayerDB cargo directly
		if gw.Players.Docked[t.ConnID] {
			s.processDockedTransfer(t, username, pdata)
			continue
		}

		entity, ok := gw.Players.Entities[t.ConnID]
		if !ok || !gw.ECS.Alive(entity) {
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
			gw.Log.Log(game.CatEconomy, "bank deposit: player=%s item=%d qty=%d bank_mass=%.1f/%.1f",
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
			gw.Log.Log(game.CatEconomy, "bank withdraw: player=%s item=%d qty=%d bank_mass=%.1f/%.1f",
				username, t.ItemID, withdrawn, pdata.BankTotalMass(), gw.Config.BankMaxMass)
			s.sendTransferResult(t.ConnID, true, "", t.ItemID, withdrawn, false)
			s.sendBankContents(t.ConnID, pdata)
		}
	}
}

// processDockedTransfer handles cargo<->bank transfers for docked players using PlayerDB.
func (s *EconomySystem) processDockedTransfer(t game.PendingTransfer, username string, pdata *game.PlayerData) {
	gw := s.gw
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
		gw.Log.Log(game.CatEconomy, "bank deposit (docked): player=%s item=%d qty=%d", username, t.ItemID, deposited)
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
		gw.Log.Log(game.CatEconomy, "bank withdraw (docked): player=%s item=%d qty=%d", username, t.ItemID, withdrawn)
		s.sendTransferResult(t.ConnID, true, "", t.ItemID, withdrawn, false)
		s.sendBankContents(t.ConnID, pdata)
	}
}

func (s *EconomySystem) processSells(stationPositions []component.Position, sellRange2 float64) {
	gw := s.gw
	for _, req := range engine.Drain[game.PendingSellRequest](gw.Queue) {
		username := gw.Players.Usernames[req.ConnID]
		if username == "" {
			continue
		}
		pdata := gw.PlayerDB.GetOrCreate(username)

		// Docked players skip entity/proximity check
		if !gw.Players.Docked[req.ConnID] {
			entity, ok := gw.Players.Entities[req.ConnID]
			if !ok || !gw.ECS.Alive(entity) {
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

		// Withdraw from bank and convert to FLUX
		withdrawn := pdata.WithdrawFromBank(req.ItemID, amount)
		fluxEarned := int64(float64(withdrawn) * def.SellPrice)
		pdata.AddFlux(fluxEarned)
		gw.PlayerDB.MarkDirty(username)

		gw.Log.Log(game.CatEconomy, "bank sell: player=%s item=%d qty=%d flux_earned=%d total_flux=%d",
			username, req.ItemID, withdrawn, fluxEarned, pdata.Flux)

		s.sendTransferResult(req.ConnID, true, "", req.ItemID, withdrawn, false)
		s.sendBankContents(req.ConnID, pdata)
	}
}

func (s *EconomySystem) processBankRequests(stationPositions []component.Position, sellRange2 float64) {
	gw := s.gw
	for _, req := range engine.Drain[game.PendingBankRequest](gw.Queue) {
		username := gw.Players.Usernames[req.ConnID]
		if username == "" {
			continue
		}

		// Docked players skip entity/proximity check
		if !gw.Players.Docked[req.ConnID] {
			entity, ok := gw.Players.Entities[req.ConnID]
			if !ok || !gw.ECS.Alive(entity) {
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

		pdata := gw.PlayerDB.GetOrCreate(username)
		s.sendBankContents(req.ConnID, pdata)
	}
}

func (s *EconomySystem) stationRange2() float64 {
	r := float64(s.gw.Config.SellRange)
	return r * r
}

func (s *EconomySystem) nearStation(pos *component.Position, stations []component.Position, range2 float64) bool {
	for _, sp := range stations {
		dx := float64(pos.X - sp.X)
		dy := float64(pos.Y - sp.Y)
		if dx*dx+dy*dy <= range2 {
			return true
		}
	}
	return false
}

func (s *EconomySystem) processShopBuys(stationPositions []component.Position, sellRange2 float64) {
	gw := s.gw
	for _, req := range engine.Drain[game.PendingShopBuy](gw.Queue) {
		username := gw.Players.Usernames[req.ConnID]
		if username == "" {
			continue
		}

		isDocked := gw.Players.Docked[req.ConnID]

		// Non-docked players need entity + proximity check
		if !isDocked {
			entity, ok := gw.Players.Entities[req.ConnID]
			if !ok || !gw.ECS.Alive(entity) {
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

		// Check FLUX balance
		if pdata.Flux < totalCost {
			s.sendTransferResult(req.ConnID, false, "Not enough FLUX", req.ItemID, 0, false)
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
			entity := gw.Players.Entities[req.ConnID]
			inv := gw.C.Inventory.Get(entity)
			if inv.RemainingMass() < massNeeded {
				s.sendTransferResult(req.ConnID, false, "Cargo is full", req.ItemID, 0, false)
				continue
			}
		}

		// Deduct FLUX and add item to cargo
		pdata.SpendFlux(totalCost)

		if isDocked {
			if pdata.Cargo == nil {
				pdata.Cargo = make(map[uint32]int32)
			}
			pdata.Cargo[req.ItemID] += qty
		} else {
			entity := gw.Players.Entities[req.ConnID]
			inv := gw.C.Inventory.Get(entity)
			inv.AddItem(req.ItemID, qty)
		}
		gw.PlayerDB.MarkDirty(username)

		gw.Log.Log(game.CatEconomy, "shop buy: player=%s item=%d qty=%d cost=%d flux_remaining=%d",
			username, req.ItemID, qty, totalCost, pdata.Flux)

		s.sendTransferResult(req.ConnID, true, "", req.ItemID, qty, false)
		s.sendBankContents(req.ConnID, pdata)
	}
}

func (s *EconomySystem) sendTransferResult(connID uint32, success bool, reason string, itemID uint32, qty int32, deposit bool) {
	data := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_TRANSFER_RESULT), &gamepb.TransferResultMsg{
		Success:  success,
		Reason:   reason,
		ItemId:   itemID,
		Quantity: qty,
		Deposit:  deposit,
	})
	if data != nil {
		s.gw.ConnMgr.SendReliable(connID, data)
	}
}

func (s *EconomySystem) sendBankContents(connID uint32, pdata *game.PlayerData) {
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
	data := netutil.MakeEvent(uint32(gamepb.ServerEventCode_SE_BANK_CONTENTS), &gamepb.BankContentsMsg{
		Items:        items,
		TotalMass:    pdata.BankTotalMass(),
		MaxMass:      s.gw.Config.BankMaxMass,
		CargoItems:   cargoItems,
		CargoMass:    pdata.CargoTotalMass(),
		MaxCargoMass: s.gw.Config.MaxCargo,
		FluxBalance:  pdata.Flux,
	})
	if data != nil {
		s.gw.ConnMgr.SendReliable(connID, data)
	}
}

func (s *EconomySystem) processLootItems() {
	gw := s.gw
	pickupRange2 := float64(gw.Config.LootPickupRange) * float64(gw.Config.LootPickupRange)

	for _, req := range engine.Drain[game.PendingLootItem](gw.Queue) {
		entity, ok := gw.Players.Entities[req.ConnID]
		if !ok || !gw.ECS.Alive(entity) {
			continue
		}
		if !gw.C.Position.HasAll(entity) || !gw.C.Inventory.HasAll(entity) {
			continue
		}

		crateEntity, ok := gw.NetIDToEntity[req.CrateNetID]
		if !ok || !gw.ECS.Alive(crateEntity) {
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
			gw.Log.Log(game.CatEconomy, "loot pickup: player=%d item=%d qty=%d cargo_mass=%.1f/%.1f",
				playerNetID, req.ItemID, added, playerInv.TotalMass(), playerInv.MaxMass)
		}

		if crateInv.IsEmpty() {
			gw.MarkForRemoval(crateEntity)
		}
	}
}

func (s *EconomySystem) processLootAlls() {
	gw := s.gw
	pickupRange2 := float64(gw.Config.LootPickupRange) * float64(gw.Config.LootPickupRange)

	for _, req := range engine.Drain[game.PendingLootAll](gw.Queue) {
		entity, ok := gw.Players.Entities[req.ConnID]
		if !ok || !gw.ECS.Alive(entity) {
			continue
		}
		if !gw.C.Position.HasAll(entity) || !gw.C.Inventory.HasAll(entity) {
			continue
		}

		crateEntity, ok := gw.NetIDToEntity[req.CrateNetID]
		if !ok || !gw.ECS.Alive(crateEntity) {
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
				gw.Log.Log(game.CatEconomy, "loot pickup: player=%d item=%d qty=%d cargo_mass=%.1f/%.1f",
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
