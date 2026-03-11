package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"
	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/logger"
)

// EconomySystem handles loot crate pickup, bank transfers (deposit/withdraw),
// and selling bank items for FLUX.
type EconomySystem struct {
	gw            *game.GameWorld
	playerFilter  *ecs.Filter3[component.PlayerInput, component.Position, component.Inventory]
	crateFilter   *ecs.Filter3[component.LootCrate, component.Position, component.Inventory]
	stationFilter *ecs.Filter2[component.Station, component.Position]
}

func NewEconomySystem(gw *game.GameWorld) *EconomySystem {
	return &EconomySystem{gw: gw}
}

type crateInfo struct {
	entity       ecs.Entity
	x, y         float32
	inv          *component.Inventory
	dropperNetID uint32
	immune       bool
}

func (s *EconomySystem) Update(dt float32) {
	gw := s.gw
	if s.playerFilter == nil {
		s.playerFilter = ecs.NewFilter3[component.PlayerInput, component.Position, component.Inventory](gw.ECS)
		s.crateFilter = ecs.NewFilter3[component.LootCrate, component.Position, component.Inventory](gw.ECS)
		s.stationFilter = ecs.NewFilter2[component.Station, component.Position](gw.ECS)
	}

	// Collect station positions
	var stationPositions []component.Position
	stationQuery := s.stationFilter.Query()
	for stationQuery.Next() {
		_, pos := stationQuery.Get()
		stationPositions = append(stationPositions, *pos)
	}

	// Collect crate info and tick down pickup immunity
	var crates []crateInfo
	crateQuery := s.crateFilter.Query()
	for crateQuery.Next() {
		lc, pos, inv := crateQuery.Get()
		if lc.PickupImmunity > 0 {
			lc.PickupImmunity -= dt
		}
		crates = append(crates, crateInfo{
			entity:       crateQuery.Entity(),
			x:            pos.X,
			y:            pos.Y,
			inv:          inv,
			dropperNetID: lc.DropperNetID,
			immune:       lc.PickupImmunity > 0,
		})
	}

	sellRange2 := s.stationRange2()
	pickupRange2 := float64(gw.Config.LootPickupRange) * float64(gw.Config.LootPickupRange)

	playerQuery := s.playerFilter.Query()
	for playerQuery.Next() {
		_, pos, inv := playerQuery.Get()
		entity := playerQuery.Entity()

		// Loot crate pickup
		playerNetID := gw.NetworkIDMap.Get(entity).ID
		for i := range crates {
			c := &crates[i]
			if !gw.ECS.Alive(c.entity) {
				continue
			}

			// Skip if this player dropped the crate and immunity is still active
			if c.immune && c.dropperNetID == playerNetID {
				continue
			}

			dx := float64(pos.X - c.x)
			dy := float64(pos.Y - c.y)
			dist2 := dx*dx + dy*dy
			if dist2 > pickupRange2 {
				continue
			}

			// Transfer items from crate to player respecting mass limit
			var transferred bool
			for itemID, qty := range c.inv.Items {
				if qty <= 0 {
					continue
				}
				added := inv.AddItem(itemID, qty)
				if added > 0 {
					c.inv.RemoveItem(itemID, added)
					transferred = true
				}
				if inv.RemainingMass() <= 0 {
					break
				}
			}

			if transferred {
				if c.inv.IsEmpty() {
					gw.MarkForRemoval(c.entity)
				}
				gw.Log.Log(logger.CatEconomy, "loot pickup: player=%d cargo_mass=%.1f/%.1f",
					playerNetID, inv.TotalMass(), inv.MaxMass)
			}
		}
	}

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
	for _, t := range gw.PendingTransfers {
		entity, ok := gw.PlayerEntities[t.ConnID]
		if !ok || !gw.ECS.Alive(entity) {
			continue
		}
		if !gw.InventoryMap.HasAll(entity) || !gw.PositionMap.HasAll(entity) {
			continue
		}

		pos := gw.PositionMap.Get(entity)
		inv := gw.InventoryMap.Get(entity)
		username := gw.ConnToUsername[t.ConnID]
		pdata := gw.PlayerDB.GetOrCreate(username)

		if !s.nearStation(pos, stationPositions, sellRange2) {
			s.sendTransferResult(t.ConnID, false, "Not near a station", t.ItemID, 0, t.Deposit)
			continue
		}

		if t.Deposit {
			// Cargo -> Bank
			have := float32(0)
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
			gw.Log.Log(logger.CatEconomy, "bank deposit: player=%s item=%d qty=%.1f bank_mass=%.1f/%.1f",
				username, t.ItemID, deposited, pdata.BankTotalMass(), gw.Config.BankMaxMass)
			s.sendTransferResult(t.ConnID, true, "", t.ItemID, deposited, true)
			// Send updated bank contents
			s.sendBankContents(t.ConnID, pdata)
		} else {
			// Bank -> Cargo
			have := float32(0)
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
			// First check how much cargo space we have, then withdraw that much
			massPerUnit := item.MassOf(t.ItemID)
			maxByMass := inv.RemainingMass() / massPerUnit
			if amount > maxByMass {
				amount = float32(math.Floor(float64(maxByMass)))
			}
			if amount <= 0 {
				s.sendTransferResult(t.ConnID, false, "Cargo is full", t.ItemID, 0, false)
				continue
			}
			withdrawn := pdata.WithdrawFromBank(t.ItemID, amount)
			inv.AddItem(t.ItemID, withdrawn)
			gw.PlayerDB.MarkDirty(username)
			gw.Log.Log(logger.CatEconomy, "bank withdraw: player=%s item=%d qty=%.1f bank_mass=%.1f/%.1f",
				username, t.ItemID, withdrawn, pdata.BankTotalMass(), gw.Config.BankMaxMass)
			s.sendTransferResult(t.ConnID, true, "", t.ItemID, withdrawn, false)
			// Send updated bank contents
			s.sendBankContents(t.ConnID, pdata)
		}
	}
}

func (s *EconomySystem) processSells(stationPositions []component.Position, sellRange2 float64) {
	gw := s.gw
	for _, req := range gw.PendingSellRequests {
		entity, ok := gw.PlayerEntities[req.ConnID]
		if !ok || !gw.ECS.Alive(entity) {
			continue
		}
		if !gw.PositionMap.HasAll(entity) {
			continue
		}

		pos := gw.PositionMap.Get(entity)
		username := gw.ConnToUsername[req.ConnID]
		pdata := gw.PlayerDB.GetOrCreate(username)

		if !s.nearStation(pos, stationPositions, sellRange2) {
			s.sendTransferResult(req.ConnID, false, "Not near a station", req.ItemID, 0, false)
			continue
		}

		// Validate item is sellable
		def := item.Get(req.ItemID)
		if def == nil || def.SellPrice <= 0 {
			s.sendTransferResult(req.ConnID, false, "Item cannot be sold", req.ItemID, 0, false)
			continue
		}

		// Check bank has the item
		have := float32(0)
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
		fluxEarned := float32(float64(withdrawn) * def.SellPrice)
		if pdata.Bank == nil {
			pdata.Bank = make(map[uint32]float32)
		}
		pdata.Bank[item.FluxItemID] += fluxEarned
		gw.PlayerDB.MarkDirty(username)

		gw.Log.Log(logger.CatEconomy, "bank sell: player=%s item=%d qty=%.1f flux_earned=%.1f total_flux=%.1f",
			username, req.ItemID, withdrawn, fluxEarned, pdata.Bank[item.FluxItemID])

		s.sendTransferResult(req.ConnID, true, "", req.ItemID, withdrawn, false)
		s.sendBankContents(req.ConnID, pdata)
	}
}

func (s *EconomySystem) processBankRequests(stationPositions []component.Position, sellRange2 float64) {
	gw := s.gw
	for _, req := range gw.PendingBankRequests {
		entity, ok := gw.PlayerEntities[req.ConnID]
		if !ok || !gw.ECS.Alive(entity) {
			continue
		}
		if !gw.PositionMap.HasAll(entity) {
			continue
		}

		pos := gw.PositionMap.Get(entity)
		if !s.nearStation(pos, stationPositions, sellRange2) {
			continue
		}

		username := gw.ConnToUsername[req.ConnID]
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
	for _, req := range gw.PendingShopBuys {
		entity, ok := gw.PlayerEntities[req.ConnID]
		if !ok || !gw.ECS.Alive(entity) {
			continue
		}
		if !gw.PositionMap.HasAll(entity) || !gw.InventoryMap.HasAll(entity) {
			continue
		}

		pos := gw.PositionMap.Get(entity)
		if !s.nearStation(pos, stationPositions, sellRange2) {
			s.sendTransferResult(req.ConnID, false, "Not near a station", req.ItemID, 0, false)
			continue
		}

		def := item.Get(req.ItemID)
		if def == nil || def.BuyPrice <= 0 {
			s.sendTransferResult(req.ConnID, false, "Item not available for purchase", req.ItemID, 0, false)
			continue
		}

		username := gw.ConnToUsername[req.ConnID]
		pdata := gw.PlayerDB.GetOrCreate(username)

		qty := float32(req.Qty)
		if qty <= 0 {
			qty = 1
		}
		totalCost := float32(def.BuyPrice) * qty

		// Check FLUX balance in bank
		flux := float32(0)
		if pdata.Bank != nil {
			flux = pdata.Bank[item.FluxItemID]
		}
		if flux < totalCost {
			s.sendTransferResult(req.ConnID, false, "Not enough FLUX", req.ItemID, 0, false)
			continue
		}

		// Check cargo space
		inv := gw.InventoryMap.Get(entity)
		massNeeded := def.MassPerUnit * qty
		if inv.RemainingMass() < massNeeded {
			s.sendTransferResult(req.ConnID, false, "Cargo is full", req.ItemID, 0, false)
			continue
		}

		// Deduct FLUX and add item to cargo
		if pdata.Bank == nil {
			pdata.Bank = make(map[uint32]float32)
		}
		pdata.Bank[item.FluxItemID] -= totalCost
		if pdata.Bank[item.FluxItemID] <= 0 {
			delete(pdata.Bank, item.FluxItemID)
		}
		inv.AddItem(req.ItemID, qty)
		gw.PlayerDB.MarkDirty(username)

		gw.Log.Log(logger.CatEconomy, "shop buy: player=%s item=%d qty=%.0f cost=%.1f flux_remaining=%.1f",
			username, req.ItemID, qty, totalCost, pdata.Bank[item.FluxItemID])

		s.sendTransferResult(req.ConnID, true, "", req.ItemID, qty, false)
		s.sendBankContents(req.ConnID, pdata)
	}
}

func (s *EconomySystem) sendTransferResult(connID uint32, success bool, reason string, itemID uint32, qty float32, deposit bool) {
	msg := &gamepb.ServerMessage{
		Msg: &gamepb.ServerMessage_TransferResult{
			TransferResult: &gamepb.TransferResultMsg{
				Success:  success,
				Reason:   reason,
				ItemId:   itemID,
				Quantity: qty,
				Deposit:  deposit,
			},
		},
	}
	if data, err := proto.Marshal(msg); err == nil {
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
	msg := &gamepb.ServerMessage{
		Msg: &gamepb.ServerMessage_BankContents{
			BankContents: &gamepb.BankContentsMsg{
				Items:     items,
				TotalMass: pdata.BankTotalMass(),
				MaxMass:   s.gw.Config.BankMaxMass,
			},
		},
	}
	if data, err := proto.Marshal(msg); err == nil {
		s.gw.ConnMgr.SendReliable(connID, data)
	}
}
