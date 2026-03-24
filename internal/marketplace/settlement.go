package marketplace

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/orderbook"
	"github.com/zenion/mmoserver/pkg/persist"
)

// fluxItemID is the well-known item ID for the Flux currency.
const fluxItemID uint32 = 1

// logCatMarket is the log category used by the marketplace service.
const logCatMarket = "market"

// BankOps provides callbacks for the marketplace to access player banks.
type BankOps struct {
	// GetBankBalance returns qty of an item in a player's bank.
	GetBankBalance func(player string, itemID uint32) int32
	// ModifyBank atomically modifies a player's bank map.
	ModifyBank func(player string, fn func(bank map[uint32]int32))
	// GetFlux returns the player's Flux balance.
	GetFlux func(player string) int64
	// ModifyFlux adds delta to the player's Flux balance.
	ModifyFlux func(player string, delta int64)
	// MarkDirty flags a player for persistence.
	MarkDirty func(player string)
	// SendBankUpdate sends an updated SE_BANK_CONTENTS event to the player's client.
	SendBankUpdate func(player string)
}

// Settlement wraps a generic orderbook.Service and handles game-specific
// bank mutations, tax, escrow, persistence, and notifications.
type Settlement struct {
	ob     *orderbook.Service
	bank   BankOps
	cfg    orderbook.Config
	log    *logger.Logger
	writer *persist.AsyncWriter

	// notify sends a push notification to an online player.
	notify func(username string, code uint32, payload []byte)
}

// NewSettlement creates a new settlement layer wrapping the given orderbook service.
func NewSettlement(
	ob *orderbook.Service,
	bank BankOps,
	cfg orderbook.Config,
	log *logger.Logger,
	writer *persist.AsyncWriter,
	notify func(username string, code uint32, payload []byte),
) *Settlement {
	return &Settlement{
		ob:     ob,
		bank:   bank,
		cfg:    cfg,
		log:    log,
		writer: writer,
		notify: notify,
	}
}

// Browse returns an aggregated order book view for a given station and item.
func (st *Settlement) Browse(stationID, itemID uint32) orderbook.OrderBookView {
	return st.ob.Browse(stationID, itemID)
}

// PlayerOrders returns all active orders for a player.
func (st *Settlement) PlayerOrders(player string) []*orderbook.Order {
	return st.ob.PlayerOrders(player)
}

// PlaceSellOrder validates bank balance, runs matching, settles fills, escrows remainder.
func (st *Settlement) PlaceSellOrder(player string, stationID, itemID uint32, price int64, qty int32) (*orderbook.PlaceResult, error) {
	if itemID == fluxItemID {
		return nil, fmt.Errorf("Flux cannot be traded on the marketplace")
	}

	// Verify seller has enough items in bank
	bankBal := st.bank.GetBankBalance(player, itemID)
	if bankBal < qty {
		return nil, fmt.Errorf("insufficient bank balance: have %d, need %d", bankBal, qty)
	}

	matches, resting, err := st.ob.PlaceSellOrder(player, stationID, itemID, price, qty)
	if err != nil {
		return nil, err
	}

	result := &orderbook.PlaceResult{}
	var totalFlux int64

	// Settle each match
	for _, m := range matches {
		fluxEarned := int64(float64(m.Quantity) * float64(m.Price) * (1.0 - st.cfg.TaxPct))
		totalFlux += fluxEarned

		st.bank.ModifyFlux(player, fluxEarned)
		st.bank.MarkDirty(player)

		st.bank.ModifyBank(m.BuyerID, func(bank map[uint32]int32) {
			bank[itemID] += m.Quantity
		})
		st.bank.MarkDirty(m.BuyerID)

		trade := &orderbook.Trade{
			ID: st.ob.AllocID(), ItemID: itemID, LocationID: stationID,
			Price: m.Price, Quantity: m.Quantity,
			Buyer: m.BuyerID, Seller: player, Timestamp: time.Now().Unix(),
		}
		st.persistTrade(trade)

		st.log.Log(logCatMarket, "trade: seller=%s buyer=%s item=%d qty=%d price=%d",
			player, m.BuyerID, itemID, m.Quantity, m.Price)

		st.sendTradeNotification(m.BuyerID, m.BuyOrderID, itemID, m.Quantity, m.Price, false, int64(m.Quantity))

		// Persist the partially filled buy order
		buyOrder := st.ob.GetOrder(m.BuyOrderID)
		if buyOrder != nil {
			st.persistOrder(buyOrder)
		} else {
			// Fully consumed buy order — delete from persistence
			st.deletePersistOrder(m.BuyOrderID)
		}

		result.FilledQty += m.Quantity
	}

	// Withdraw sold items from seller bank
	if soldQty := result.FilledQty; soldQty > 0 {
		st.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[itemID] -= soldQty
			if bank[itemID] <= 0 {
				delete(bank, itemID)
			}
		})
		st.bank.MarkDirty(player)
	}

	if result.FilledQty > 0 {
		result.AvgPrice = totalFlux / int64(result.FilledQty)
		result.TotalCost = totalFlux
	}

	// Escrow remaining items for resting sell order
	if resting != nil {
		st.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[itemID] -= resting.Quantity
			if bank[itemID] <= 0 {
				delete(bank, itemID)
			}
		})
		st.bank.MarkDirty(player)
		st.persistOrder(resting)
		result.OrderID = resting.ID

		st.log.Log(logCatMarket, "sell order placed: player=%s id=%d item=%d qty=%d price=%d",
			player, resting.ID, itemID, resting.Quantity, price)
	}

	st.bank.SendBankUpdate(player)
	return result, nil
}

// PlaceBuyOrder validates flux balance, runs matching, settles fills, escrows flux for remainder.
func (st *Settlement) PlaceBuyOrder(player string, stationID, itemID uint32, price int64, qty int32) (*orderbook.PlaceResult, error) {
	if itemID == fluxItemID {
		return nil, fmt.Errorf("Flux cannot be traded on the marketplace")
	}

	maxCost := price * int64(qty)
	fluxBal := st.bank.GetFlux(player)
	if fluxBal < maxCost {
		return nil, fmt.Errorf("insufficient Flux: have %d, need %d", fluxBal, maxCost)
	}

	matches, resting, err := st.ob.PlaceBuyOrder(player, stationID, itemID, price, qty)
	if err != nil {
		return nil, err
	}

	result := &orderbook.PlaceResult{}
	var totalCost int64

	for _, m := range matches {
		tradeCost := m.Price * int64(m.Quantity)
		totalCost += tradeCost

		st.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[itemID] += m.Quantity
		})
		st.bank.ModifyFlux(player, -tradeCost)
		st.bank.MarkDirty(player)

		sellerFlux := int64(float64(m.Quantity) * float64(m.Price) * (1.0 - st.cfg.TaxPct))
		st.bank.ModifyFlux(m.SellerID, sellerFlux)
		st.bank.MarkDirty(m.SellerID)

		trade := &orderbook.Trade{
			ID: st.ob.AllocID(), ItemID: itemID, LocationID: stationID,
			Price: m.Price, Quantity: m.Quantity,
			Buyer: player, Seller: m.SellerID, Timestamp: time.Now().Unix(),
		}
		st.persistTrade(trade)

		st.log.Log(logCatMarket, "trade: buyer=%s seller=%s item=%d qty=%d price=%d",
			player, m.SellerID, itemID, m.Quantity, m.Price)

		st.sendTradeNotification(m.SellerID, m.SellOrderID, itemID, m.Quantity, m.Price, true, sellerFlux)

		// Persist the partially filled sell order
		sellOrder := st.ob.GetOrder(m.SellOrderID)
		if sellOrder != nil {
			st.persistOrder(sellOrder)
		} else {
			st.deletePersistOrder(m.SellOrderID)
		}

		result.FilledQty += m.Quantity
	}

	if result.FilledQty > 0 {
		result.AvgPrice = totalCost / int64(result.FilledQty)
		result.TotalCost = totalCost
	}

	// Escrow flux for resting buy order
	if resting != nil {
		escrowCost := price * int64(resting.Quantity)
		st.bank.ModifyFlux(player, -escrowCost)
		st.bank.MarkDirty(player)
		st.persistOrder(resting)
		result.OrderID = resting.ID

		st.log.Log(logCatMarket, "buy order placed: player=%s id=%d item=%d qty=%d price=%d",
			player, resting.ID, itemID, resting.Quantity, price)
	}

	st.bank.SendBankUpdate(player)
	return result, nil
}

// InstantSell sells items at market price (matches against buy book, no resting order).
func (st *Settlement) InstantSell(player string, stationID, itemID uint32, qty int32) (*orderbook.PlaceResult, error) {
	if itemID == fluxItemID {
		return nil, fmt.Errorf("Flux cannot be traded on the marketplace")
	}

	bankBal := st.bank.GetBankBalance(player, itemID)
	if bankBal < qty {
		return nil, fmt.Errorf("insufficient bank balance: have %d, need %d", bankBal, qty)
	}

	matches, err := st.ob.InstantSell(player, stationID, itemID, qty)
	if err != nil {
		return nil, err
	}

	result := &orderbook.PlaceResult{}
	var totalFlux int64

	for _, m := range matches {
		fluxEarned := int64(float64(m.Quantity) * float64(m.Price) * (1.0 - st.cfg.TaxPct))
		totalFlux += fluxEarned

		st.bank.ModifyFlux(player, fluxEarned)
		st.bank.MarkDirty(player)

		st.bank.ModifyBank(m.BuyerID, func(bank map[uint32]int32) {
			bank[itemID] += m.Quantity
		})
		st.bank.MarkDirty(m.BuyerID)

		trade := &orderbook.Trade{
			ID: st.ob.AllocID(), ItemID: itemID, LocationID: stationID,
			Price: m.Price, Quantity: m.Quantity,
			Buyer: m.BuyerID, Seller: player, Timestamp: time.Now().Unix(),
		}
		st.persistTrade(trade)

		st.log.Log(logCatMarket, "instant sell: seller=%s buyer=%s item=%d qty=%d price=%d",
			player, m.BuyerID, itemID, m.Quantity, m.Price)
		st.sendTradeNotification(m.BuyerID, m.BuyOrderID, itemID, m.Quantity, m.Price, false, int64(m.Quantity))

		buyOrder := st.ob.GetOrder(m.BuyOrderID)
		if buyOrder != nil {
			st.persistOrder(buyOrder)
		} else {
			st.deletePersistOrder(m.BuyOrderID)
		}

		result.FilledQty += m.Quantity
	}

	if result.FilledQty > 0 {
		soldQty := result.FilledQty
		st.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[itemID] -= soldQty
			if bank[itemID] <= 0 {
				delete(bank, itemID)
			}
		})
		st.bank.MarkDirty(player)
		result.AvgPrice = totalFlux / int64(result.FilledQty)
		result.TotalCost = totalFlux
	}

	st.bank.SendBankUpdate(player)
	return result, nil
}

// InstantBuy buys items at market price (matches against sell book, no resting order).
// Buying is limited by the player's Flux balance.
func (st *Settlement) InstantBuy(player string, stationID, itemID uint32, qty int32) (*orderbook.PlaceResult, error) {
	if itemID == fluxItemID {
		return nil, fmt.Errorf("Flux cannot be traded on the marketplace")
	}

	// Get current flux balance for budget limiting
	fluxBal := st.bank.GetFlux(player)

	matches, err := st.ob.InstantBuy(player, stationID, itemID, qty, fluxBal)
	if err != nil {
		return nil, err
	}

	result := &orderbook.PlaceResult{}
	var totalCost int64

	for _, m := range matches {
		tradeCost := m.Price * int64(m.Quantity)
		totalCost += tradeCost

		st.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[itemID] += m.Quantity
		})
		st.bank.ModifyFlux(player, -tradeCost)
		st.bank.MarkDirty(player)

		sellerFlux := int64(float64(m.Quantity) * float64(m.Price) * (1.0 - st.cfg.TaxPct))
		st.bank.ModifyFlux(m.SellerID, sellerFlux)
		st.bank.MarkDirty(m.SellerID)

		trade := &orderbook.Trade{
			ID: st.ob.AllocID(), ItemID: itemID, LocationID: stationID,
			Price: m.Price, Quantity: m.Quantity,
			Buyer: player, Seller: m.SellerID, Timestamp: time.Now().Unix(),
		}
		st.persistTrade(trade)

		st.log.Log(logCatMarket, "instant buy: buyer=%s seller=%s item=%d qty=%d price=%d",
			player, m.SellerID, itemID, m.Quantity, m.Price)
		st.sendTradeNotification(m.SellerID, m.SellOrderID, itemID, m.Quantity, m.Price, true, sellerFlux)

		sellOrder := st.ob.GetOrder(m.SellOrderID)
		if sellOrder != nil {
			st.persistOrder(sellOrder)
		} else {
			st.deletePersistOrder(m.SellOrderID)
		}

		result.FilledQty += m.Quantity
	}

	if result.FilledQty > 0 {
		result.AvgPrice = totalCost / int64(result.FilledQty)
		result.TotalCost = totalCost
	}

	st.bank.SendBankUpdate(player)
	return result, nil
}

// CancelOrder cancels an active order and returns escrowed assets to the player's bank.
func (st *Settlement) CancelOrder(player string, orderID uint64) error {
	order, err := st.ob.CancelOrder(player, orderID)
	if err != nil {
		return err
	}

	st.deletePersistOrder(orderID)

	if order.Side == orderbook.SideSell {
		st.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[order.ItemID] += order.Quantity
		})
	} else {
		refund := order.Price * int64(order.Quantity)
		st.bank.ModifyFlux(player, refund)
	}
	st.bank.MarkDirty(player)

	st.log.Log(logCatMarket, "order cancelled: player=%s id=%d side=%d item=%d qty=%d",
		player, orderID, order.Side, order.ItemID, order.Quantity)

	st.bank.SendBankUpdate(player)
	return nil
}

// ExpireOrders removes all expired orders and returns escrowed assets. Call periodically.
func (st *Settlement) ExpireOrders() {
	expired := st.ob.ExpireOrders()
	for _, order := range expired {
		st.deletePersistOrder(order.ID)

		if order.Side == orderbook.SideSell {
			st.bank.ModifyBank(order.Player, func(bank map[uint32]int32) {
				bank[order.ItemID] += order.Quantity
			})
		} else {
			refund := order.Price * int64(order.Quantity)
			st.bank.ModifyFlux(order.Player, refund)
		}
		st.bank.MarkDirty(order.Player)

		st.log.Log(logCatMarket, "order expired: player=%s id=%d item=%d qty=%d",
			order.Player, order.ID, order.ItemID, order.Quantity)
	}
}

// InsertLoadedOrder adds an order loaded from persistence into the in-memory books.
func (st *Settlement) InsertLoadedOrder(order *orderbook.Order) {
	st.ob.InsertLoadedOrder(order)
}

func (st *Settlement) sendTradeNotification(username string, orderID uint64, itemID uint32, qty int32, price int64, youSold bool, fluxChange int64) {
	if st.notify == nil {
		return
	}
	notif := &gamepb.MarketTradeNotification{
		OrderId:    orderID,
		ItemId:     itemID,
		FilledQty:  qty,
		Price:      price,
		YouSold:    youSold,
		FluxChange: fluxChange,
	}
	data, err := proto.Marshal(notif)
	if err != nil {
		return
	}
	st.notify(username, uint32(gamepb.OperationCode_OP_MARKET_INSTANT_TRADE), data)
}
