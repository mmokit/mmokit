package marketplace

import (
	"context"
	"fmt"
	"time"

	gamepersist "github.com/zenion/mmokit/internal/persist"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// logCatMarket is the log category used by the marketplace service.
const logCatMarket = "economy:market"

// BankOps provides callbacks for the marketplace to access player banks.
type BankOps struct {
	// GetBankBalance returns qty of an item in a player's bank.
	GetBankBalance func(player string, itemID uint32) int32
	// ModifyBank atomically modifies a player's bank map.
	ModifyBank func(player string, fn func(bank map[uint32]int32))
	// GetCurrency returns the player's balance of a currency.
	GetCurrency func(player string, currencyID uint32) int64
	// ModifyCurrency adds delta to a player's currency balance.
	ModifyCurrency func(player string, currencyID uint32, delta int64)
	// MarkDirty flags a player for persistence.
	MarkDirty func(player string)
	// SendBankUpdate sends an updated SE_BANK_CONTENTS event to the player's client.
	SendBankUpdate func(player string)
}

// Settlement wraps a generic orderbook.Service and handles game-specific
// bank mutations, tax, escrow, persistence, and notifications.
type Settlement struct {
	ob         *mmokit.OrderBookService
	bank       BankOps
	cfg        mmokit.OrderBookConfig
	currencyID uint32 // settlement currency item ID
	log        *mmokit.Logger
	market     gamepersist.MarketRepository

	// notify sends a typed trade-fill notification to an online player.
	// Wired by the host process to mmokit.SendEvent against the cell stage
	// for the player's connection.
	notify func(username string, msg *MarketTradeNotification)
}

// NewSettlement creates a new settlement layer wrapping the given orderbook service.
func NewSettlement(
	ob *mmokit.OrderBookService,
	bank BankOps,
	cfg mmokit.OrderBookConfig,
	currencyID uint32,
	log *mmokit.Logger,
	market gamepersist.MarketRepository,
	notify func(username string, msg *MarketTradeNotification),
) *Settlement {
	return &Settlement{
		ob:         ob,
		bank:       bank,
		cfg:        cfg,
		currencyID: currencyID,
		log:        log,
		market:     market,
		notify:     notify,
	}
}

// Browse returns an aggregated order book view for a given station and item.
func (st *Settlement) Browse(stationID, itemID uint32) mmokit.OrderBookView {
	return st.ob.Browse(stationID, itemID)
}

// PlayerOrders returns all active orders for a player.
func (st *Settlement) PlayerOrders(player string) []*mmokit.Order {
	return st.ob.PlayerOrders(player)
}

// PlaceSellOrder validates bank balance, runs matching, settles fills, escrows remainder.
func (st *Settlement) PlaceSellOrder(player string, stationID, itemID uint32, price int64, qty int32) (*mmokit.PlaceResult, error) {
	if itemID == st.currencyID {
		return nil, fmt.Errorf("settlement currency cannot be traded on the marketplace")
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

	result := &mmokit.PlaceResult{}
	var totalFlux int64

	// Settle each match
	for _, m := range matches {
		fluxEarned := int64(float64(m.Quantity) * float64(m.Price) * (1.0 - st.cfg.TaxPct))
		totalFlux += fluxEarned

		st.bank.ModifyCurrency(player, st.currencyID, fluxEarned)
		st.bank.MarkDirty(player)

		st.bank.ModifyBank(m.BuyerID, func(bank map[uint32]int32) {
			bank[itemID] += m.Quantity
		})
		st.bank.MarkDirty(m.BuyerID)

		trade := &mmokit.Trade{
			ItemID: itemID, LocationID: stationID,
			Price: m.Price, Quantity: m.Quantity,
			Buyer: m.BuyerID, Seller: player, Timestamp: time.Now().Unix(),
		}
		if err := st.market.RecordTrade(context.Background(), tradeToRecord(trade)); err != nil {
			st.log.Log(logCatMarket, "record trade failed: %v", err)
		}

		st.log.Log(logCatMarket, "trade: seller=%s buyer=%s item=%d qty=%d price=%d",
			player, m.BuyerID, itemID, m.Quantity, m.Price)

		st.sendTradeNotification(m.BuyerID, m.BuyOrderID, itemID, m.Quantity, m.Price, false, int64(m.Quantity))

		// Persist the partially filled buy order
		buyOrder := st.ob.GetOrder(m.BuyOrderID)
		if buyOrder != nil {
			if err := st.market.UpdateQuantity(context.Background(), buyOrder.ID, buyOrder.Quantity); err != nil {
				st.log.Log(logCatMarket, "update order %d quantity failed: %v", buyOrder.ID, err)
			}
		} else {
			// Fully consumed buy order — delete from persistence
			if err := st.market.DeleteOrder(context.Background(), m.BuyOrderID); err != nil {
				st.log.Log(logCatMarket, "delete order %d failed: %v", m.BuyOrderID, err)
			}
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
		if err := st.market.PlaceOrder(context.Background(), orderToRecord(resting)); err != nil {
			st.log.Log(logCatMarket, "place order %d failed: %v", resting.ID, err)
		}
		result.OrderID = resting.ID

		st.log.Log(logCatMarket, "sell order placed: player=%s id=%d item=%d qty=%d price=%d",
			player, resting.ID, itemID, resting.Quantity, price)
	}

	st.bank.SendBankUpdate(player)
	return result, nil
}

// PlaceBuyOrder validates currency balance, runs matching, settles fills, escrows currency for remainder.
func (st *Settlement) PlaceBuyOrder(player string, stationID, itemID uint32, price int64, qty int32) (*mmokit.PlaceResult, error) {
	if itemID == st.currencyID {
		return nil, fmt.Errorf("settlement currency cannot be traded on the marketplace")
	}

	maxCost := price * int64(qty)
	fluxBal := st.bank.GetCurrency(player, st.currencyID)
	if fluxBal < maxCost {
		return nil, fmt.Errorf("insufficient currency: have %d, need %d", fluxBal, maxCost)
	}

	matches, resting, err := st.ob.PlaceBuyOrder(player, stationID, itemID, price, qty)
	if err != nil {
		return nil, err
	}

	result := &mmokit.PlaceResult{}
	var totalCost int64

	for _, m := range matches {
		tradeCost := m.Price * int64(m.Quantity)
		totalCost += tradeCost

		st.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[itemID] += m.Quantity
		})
		st.bank.ModifyCurrency(player, st.currencyID, -tradeCost)
		st.bank.MarkDirty(player)

		sellerFlux := int64(float64(m.Quantity) * float64(m.Price) * (1.0 - st.cfg.TaxPct))
		st.bank.ModifyCurrency(m.SellerID, st.currencyID, sellerFlux)
		st.bank.MarkDirty(m.SellerID)

		trade := &mmokit.Trade{
			ItemID: itemID, LocationID: stationID,
			Price: m.Price, Quantity: m.Quantity,
			Buyer: player, Seller: m.SellerID, Timestamp: time.Now().Unix(),
		}
		if err := st.market.RecordTrade(context.Background(), tradeToRecord(trade)); err != nil {
			st.log.Log(logCatMarket, "record trade failed: %v", err)
		}

		st.log.Log(logCatMarket, "trade: buyer=%s seller=%s item=%d qty=%d price=%d",
			player, m.SellerID, itemID, m.Quantity, m.Price)

		st.sendTradeNotification(m.SellerID, m.SellOrderID, itemID, m.Quantity, m.Price, true, sellerFlux)

		// Persist the partially filled sell order
		sellOrder := st.ob.GetOrder(m.SellOrderID)
		if sellOrder != nil {
			if err := st.market.UpdateQuantity(context.Background(), sellOrder.ID, sellOrder.Quantity); err != nil {
				st.log.Log(logCatMarket, "update order %d quantity failed: %v", sellOrder.ID, err)
			}
		} else {
			if err := st.market.DeleteOrder(context.Background(), m.SellOrderID); err != nil {
				st.log.Log(logCatMarket, "delete order %d failed: %v", m.SellOrderID, err)
			}
		}

		result.FilledQty += m.Quantity
	}

	if result.FilledQty > 0 {
		result.AvgPrice = totalCost / int64(result.FilledQty)
		result.TotalCost = totalCost
	}

	// Escrow currency for resting buy order
	if resting != nil {
		escrowCost := price * int64(resting.Quantity)
		st.bank.ModifyCurrency(player, st.currencyID, -escrowCost)
		st.bank.MarkDirty(player)
		if err := st.market.PlaceOrder(context.Background(), orderToRecord(resting)); err != nil {
			st.log.Log(logCatMarket, "place order %d failed: %v", resting.ID, err)
		}
		result.OrderID = resting.ID

		st.log.Log(logCatMarket, "buy order placed: player=%s id=%d item=%d qty=%d price=%d",
			player, resting.ID, itemID, resting.Quantity, price)
	}

	st.bank.SendBankUpdate(player)
	return result, nil
}

// InstantSell sells items at market price (matches against buy book, no resting order).
func (st *Settlement) InstantSell(player string, stationID, itemID uint32, qty int32) (*mmokit.PlaceResult, error) {
	if itemID == st.currencyID {
		return nil, fmt.Errorf("settlement currency cannot be traded on the marketplace")
	}

	bankBal := st.bank.GetBankBalance(player, itemID)
	if bankBal < qty {
		return nil, fmt.Errorf("insufficient bank balance: have %d, need %d", bankBal, qty)
	}

	matches, err := st.ob.InstantSell(player, stationID, itemID, qty)
	if err != nil {
		return nil, err
	}

	result := &mmokit.PlaceResult{}
	var totalFlux int64

	for _, m := range matches {
		fluxEarned := int64(float64(m.Quantity) * float64(m.Price) * (1.0 - st.cfg.TaxPct))
		totalFlux += fluxEarned

		st.bank.ModifyCurrency(player, st.currencyID, fluxEarned)
		st.bank.MarkDirty(player)

		st.bank.ModifyBank(m.BuyerID, func(bank map[uint32]int32) {
			bank[itemID] += m.Quantity
		})
		st.bank.MarkDirty(m.BuyerID)

		trade := &mmokit.Trade{
			ItemID: itemID, LocationID: stationID,
			Price: m.Price, Quantity: m.Quantity,
			Buyer: m.BuyerID, Seller: player, Timestamp: time.Now().Unix(),
		}
		if err := st.market.RecordTrade(context.Background(), tradeToRecord(trade)); err != nil {
			st.log.Log(logCatMarket, "record trade failed: %v", err)
		}

		st.log.Log(logCatMarket, "instant sell: seller=%s buyer=%s item=%d qty=%d price=%d",
			player, m.BuyerID, itemID, m.Quantity, m.Price)
		st.sendTradeNotification(m.BuyerID, m.BuyOrderID, itemID, m.Quantity, m.Price, false, int64(m.Quantity))

		buyOrder := st.ob.GetOrder(m.BuyOrderID)
		if buyOrder != nil {
			if err := st.market.UpdateQuantity(context.Background(), buyOrder.ID, buyOrder.Quantity); err != nil {
				st.log.Log(logCatMarket, "update order %d quantity failed: %v", buyOrder.ID, err)
			}
		} else {
			if err := st.market.DeleteOrder(context.Background(), m.BuyOrderID); err != nil {
				st.log.Log(logCatMarket, "delete order %d failed: %v", m.BuyOrderID, err)
			}
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
// Buying is limited by the player's currency balance.
func (st *Settlement) InstantBuy(player string, stationID, itemID uint32, qty int32) (*mmokit.PlaceResult, error) {
	if itemID == st.currencyID {
		return nil, fmt.Errorf("settlement currency cannot be traded on the marketplace")
	}

	// Get current currency balance for budget limiting
	fluxBal := st.bank.GetCurrency(player, st.currencyID)

	matches, err := st.ob.InstantBuy(player, stationID, itemID, qty, fluxBal)
	if err != nil {
		return nil, err
	}

	result := &mmokit.PlaceResult{}
	var totalCost int64

	for _, m := range matches {
		tradeCost := m.Price * int64(m.Quantity)
		totalCost += tradeCost

		st.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[itemID] += m.Quantity
		})
		st.bank.ModifyCurrency(player, st.currencyID, -tradeCost)
		st.bank.MarkDirty(player)

		sellerFlux := int64(float64(m.Quantity) * float64(m.Price) * (1.0 - st.cfg.TaxPct))
		st.bank.ModifyCurrency(m.SellerID, st.currencyID, sellerFlux)
		st.bank.MarkDirty(m.SellerID)

		trade := &mmokit.Trade{
			ItemID: itemID, LocationID: stationID,
			Price: m.Price, Quantity: m.Quantity,
			Buyer: player, Seller: m.SellerID, Timestamp: time.Now().Unix(),
		}
		if err := st.market.RecordTrade(context.Background(), tradeToRecord(trade)); err != nil {
			st.log.Log(logCatMarket, "record trade failed: %v", err)
		}

		st.log.Log(logCatMarket, "instant buy: buyer=%s seller=%s item=%d qty=%d price=%d",
			player, m.SellerID, itemID, m.Quantity, m.Price)
		st.sendTradeNotification(m.SellerID, m.SellOrderID, itemID, m.Quantity, m.Price, true, sellerFlux)

		sellOrder := st.ob.GetOrder(m.SellOrderID)
		if sellOrder != nil {
			if err := st.market.UpdateQuantity(context.Background(), sellOrder.ID, sellOrder.Quantity); err != nil {
				st.log.Log(logCatMarket, "update order %d quantity failed: %v", sellOrder.ID, err)
			}
		} else {
			if err := st.market.DeleteOrder(context.Background(), m.SellOrderID); err != nil {
				st.log.Log(logCatMarket, "delete order %d failed: %v", m.SellOrderID, err)
			}
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

	if err := st.market.DeleteOrder(context.Background(), orderID); err != nil {
		st.log.Log(logCatMarket, "delete order %d failed: %v", orderID, err)
	}

	if order.Side == mmokit.SideSell {
		st.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[order.ItemID] += order.Quantity
		})
	} else {
		refund := order.Price * int64(order.Quantity)
		st.bank.ModifyCurrency(player, st.currencyID, refund)
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
		if err := st.market.DeleteOrder(context.Background(), order.ID); err != nil {
			st.log.Log(logCatMarket, "delete expired order %d failed: %v", order.ID, err)
		}

		if order.Side == mmokit.SideSell {
			st.bank.ModifyBank(order.Owner, func(bank map[uint32]int32) {
				bank[order.ItemID] += order.Quantity
			})
		} else {
			refund := order.Price * int64(order.Quantity)
			st.bank.ModifyCurrency(order.Owner, st.currencyID, refund)
		}
		st.bank.MarkDirty(order.Owner)

		st.log.Log(logCatMarket, "order expired: player=%s id=%d item=%d qty=%d",
			order.Owner, order.ID, order.ItemID, order.Quantity)
	}
}

// InsertLoadedOrder adds an order loaded from persistence into the in-memory books.
func (st *Settlement) InsertLoadedOrder(order *mmokit.Order) {
	st.ob.InsertLoadedOrder(order)
}

// LoadAll reads all active orders from the repository and rebuilds
// the in-memory order books. Also seeds the orderbook's NextID
// counter from LoadMaxOrderID so subsequent allocations don't
// collide with persisted IDs. Call during startup before processing
// any requests.
func (st *Settlement) LoadAll(ctx context.Context) error {
	maxID, err := st.market.LoadMaxOrderID(ctx)
	if err != nil {
		return fmt.Errorf("load max order id: %w", err)
	}
	if maxID > 0 {
		st.ob.SetNextID(maxID + 1)
	}

	count := 0
	err = st.market.LoadActiveOrders(ctx, func(rec *gamepersist.OrderRecord) error {
		st.ob.InsertLoadedOrder(recordToOrder(rec))
		count++
		return nil
	})
	if err != nil {
		return fmt.Errorf("load market orders: %w", err)
	}
	if count > 0 {
		st.log.Log(logCatMarket, "loaded %d orders", count)
	}
	return nil
}

// orderToRecord converts an in-memory mmokit.Order to a persist
// OrderRecord. The legacy type stores timestamps as int64 unix
// seconds; the repository type uses time.Time. ExpiresAt == 0 means
// "never expires" → return a zero-value time so the persist layer
// writes NULL.
func orderToRecord(o *mmokit.Order) *gamepersist.OrderRecord {
	rec := &gamepersist.OrderRecord{
		ID:         o.ID,
		Side:       uint8(o.Side),
		Owner:      o.Owner,
		LocationID: o.LocationID,
		ItemID:     o.ItemID,
		Price:      o.Price,
		Quantity:   o.Quantity,
		OrigQty:    o.OrigQty,
		CreatedAt:  time.Unix(o.CreatedAt, 0),
	}
	if o.ExpiresAt > 0 {
		rec.ExpiresAt = time.Unix(o.ExpiresAt, 0)
	}
	return rec
}

// recordToOrder is the inverse of orderToRecord. Used during LoadAll
// to rebuild the in-memory book from persisted records.
func recordToOrder(r *gamepersist.OrderRecord) *mmokit.Order {
	o := &mmokit.Order{
		ID:         r.ID,
		Side:       mmokit.OrderSide(r.Side),
		Owner:      r.Owner,
		LocationID: r.LocationID,
		ItemID:     r.ItemID,
		Price:      r.Price,
		Quantity:   r.Quantity,
		OrigQty:    r.OrigQty,
		CreatedAt:  r.CreatedAt.Unix(),
	}
	if !r.ExpiresAt.IsZero() {
		o.ExpiresAt = r.ExpiresAt.Unix()
	}
	return o
}

// tradeToRecord converts an in-memory mmokit.Trade to a persist
// TradeRecord. The trade ID is dropped — market_trades.id is
// BIGSERIAL and never referenced from memory.
func tradeToRecord(t *mmokit.Trade) *gamepersist.TradeRecord {
	return &gamepersist.TradeRecord{
		ItemID:     t.ItemID,
		LocationID: t.LocationID,
		Price:      t.Price,
		Quantity:   t.Quantity,
		Buyer:      t.Buyer,
		Seller:     t.Seller,
		OccurredAt: time.Unix(t.Timestamp, 0),
	}
}

func (st *Settlement) sendTradeNotification(username string, orderID uint64, itemID uint32, qty int32, price int64, youSold bool, fluxChange int64) {
	if st.notify == nil {
		return
	}
	st.notify(username, &MarketTradeNotification{
		OrderID:        orderID,
		ItemID:         itemID,
		FilledQty:      qty,
		Price:          price,
		YouSold:        youSold,
		CurrencyChange: fluxChange,
		CurrencyID:     st.currencyID,
	})
}
