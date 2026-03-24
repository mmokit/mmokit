package marketplace

import (
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/orderbook"
	"github.com/zenion/mmoserver/pkg/persist"
)

// fluxItemID is the well-known item ID for the Flux currency.
const fluxItemID uint32 = 1

// logCatMarket is the log category used by the marketplace service.
const logCatMarket = "market"

// BankOps provides callbacks for the marketplace to access player banks.
// These are called from the ops router worker goroutines (under Service.mu).
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

// Service is the marketplace matching engine. All methods are thread-safe.
type Service struct {
	mu     sync.Mutex
	books  map[orderbook.BookKey]*orderbook.OrderBook
	orders map[uint64]*orderbook.Order // global order index

	bank   BankOps
	cfg    orderbook.Config
	nextID uint64
	log    *logger.Logger
	writer *persist.AsyncWriter

	// notify sends a push notification to an online player.
	// code is the OperationCode, payload is already-serialized bytes.
	notify func(username string, code uint32, payload []byte)
}

// NewService creates a marketplace service.
func NewService(
	bank BankOps,
	cfg orderbook.Config,
	log *logger.Logger,
	writer *persist.AsyncWriter,
	notify func(username string, code uint32, payload []byte),
) *Service {
	return &Service{
		books:  make(map[orderbook.BookKey]*orderbook.OrderBook),
		orders: make(map[uint64]*orderbook.Order),
		bank:   bank,
		cfg:    cfg,
		log:    log,
		writer: writer,
		notify: notify,
		nextID: 1,
	}
}

// SetNextID sets the next order/trade ID (called after loading persisted state).
func (s *Service) SetNextID(id uint64) {
	s.nextID = id
}

func (s *Service) allocID() uint64 {
	id := s.nextID
	s.nextID++
	return id
}

func (s *Service) getBook(stationID, itemID uint32) *orderbook.OrderBook {
	key := orderbook.BookKey{LocationID: stationID, ItemID: itemID}
	ob, ok := s.books[key]
	if !ok {
		ob = &orderbook.OrderBook{}
		s.books[key] = ob
	}
	return ob
}

// Browse returns an aggregated order book view for a given station and item.
func (s *Service) Browse(stationID, itemID uint32) orderbook.OrderBookView {
	s.mu.Lock()
	defer s.mu.Unlock()

	ob := s.getBook(stationID, itemID)
	return orderbook.OrderBookView{
		ItemID:     itemID,
		SellLevels: ob.AggregateSells(20),
		BuyLevels:  ob.AggregateBuys(20),
	}
}

// PlayerOrders returns all active orders for a player.
func (s *Service) PlayerOrders(player string) []*orderbook.Order {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result []*orderbook.Order
	for _, o := range s.orders {
		if o.Player == player {
			result = append(result, o)
		}
	}
	return result
}

// playerOrderCount returns the number of active orders for a player (caller holds mu).
func (s *Service) playerOrderCount(player string) int {
	count := 0
	for _, o := range s.orders {
		if o.Player == player {
			count++
		}
	}
	return count
}

// PlaceSellOrder places a limit sell order, matching against existing buy orders first.
func (s *Service) PlaceSellOrder(player string, stationID, itemID uint32, price int64, qty int32) (*orderbook.PlaceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if itemID == fluxItemID {
		return nil, fmt.Errorf("Flux cannot be traded on the marketplace")
	}
	if price < s.cfg.MinPrice {
		return nil, fmt.Errorf("price %d below minimum %d", price, s.cfg.MinPrice)
	}
	if qty <= 0 {
		return nil, fmt.Errorf("quantity must be positive")
	}
	if s.playerOrderCount(player) >= s.cfg.MaxOrders {
		return nil, fmt.Errorf("max active orders (%d) reached", s.cfg.MaxOrders)
	}

	// Verify seller has enough items in bank
	bankBal := s.bank.GetBankBalance(player, itemID)
	if bankBal < qty {
		return nil, fmt.Errorf("insufficient bank balance: have %d, need %d", bankBal, qty)
	}

	ob := s.getBook(stationID, itemID)
	result := &orderbook.PlaceResult{}
	remaining := qty
	var totalFlux int64

	// Match against buy book (highest buyers first)
	i := 0
	for i < len(ob.Buys) && remaining > 0 {
		buy := ob.Buys[i]
		if buy.Price < price {
			break // no more buyers willing to pay our price
		}
		tradePrice := buy.Price
		tradeQty := remaining
		if tradeQty > buy.Quantity {
			tradeQty = buy.Quantity
		}

		fluxEarned := int64(float64(tradeQty) * float64(tradePrice) * (1.0 - s.cfg.TaxPct))
		totalFlux += fluxEarned

		s.bank.ModifyFlux(player, fluxEarned)
		s.bank.MarkDirty(player)

		s.bank.ModifyBank(buy.Player, func(bank map[uint32]int32) {
			bank[itemID] += tradeQty
		})
		s.bank.MarkDirty(buy.Player)

		trade := &orderbook.Trade{
			ID: s.allocID(), ItemID: itemID, LocationID: stationID,
			Price: tradePrice, Quantity: tradeQty,
			Buyer: buy.Player, Seller: player, Timestamp: time.Now().Unix(),
		}
		s.persistTrade(trade)

		s.log.Log(logCatMarket, "trade: seller=%s buyer=%s item=%d qty=%d price=%d",
			player, buy.Player, itemID, tradeQty, tradePrice)

		s.sendTradeNotification(buy.Player, buy.ID, itemID, tradeQty, tradePrice, false, int64(tradeQty))

		buy.Quantity -= tradeQty
		remaining -= tradeQty
		result.FilledQty += tradeQty

		if buy.Quantity <= 0 {
			delete(s.orders, buy.ID)
			s.deletePersistOrder(buy.ID)
			ob.Buys = append(ob.Buys[:i], ob.Buys[i+1:]...)
		} else {
			s.persistOrder(buy)
			i++
		}
	}

	// Withdraw sold items from seller bank
	if soldQty := result.FilledQty; soldQty > 0 {
		s.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[itemID] -= soldQty
			if bank[itemID] <= 0 {
				delete(bank, itemID)
			}
		})
		s.bank.MarkDirty(player)
	}

	if result.FilledQty > 0 {
		result.AvgPrice = totalFlux / int64(result.FilledQty)
		result.TotalCost = totalFlux
	}

	// Place resting sell order for unfilled portion
	if remaining > 0 {
		s.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[itemID] -= remaining
			if bank[itemID] <= 0 {
				delete(bank, itemID)
			}
		})
		s.bank.MarkDirty(player)

		order := &orderbook.Order{
			ID: s.allocID(), Side: orderbook.SideSell, Player: player,
			LocationID: stationID, ItemID: itemID, Price: price,
			Quantity: remaining, OrigQty: qty,
			CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + s.cfg.OrderExpiry,
		}
		ob.InsertSell(order)
		s.orders[order.ID] = order
		s.persistOrder(order)
		result.OrderID = order.ID

		s.log.Log(logCatMarket, "sell order placed: player=%s id=%d item=%d qty=%d price=%d",
			player, order.ID, itemID, remaining, price)
	}

	s.bank.SendBankUpdate(player)
	return result, nil
}

// PlaceBuyOrder places a limit buy order, matching against existing sell orders first.
func (s *Service) PlaceBuyOrder(player string, stationID, itemID uint32, price int64, qty int32) (*orderbook.PlaceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if itemID == fluxItemID {
		return nil, fmt.Errorf("Flux cannot be traded on the marketplace")
	}
	if price < s.cfg.MinPrice {
		return nil, fmt.Errorf("price %d below minimum %d", price, s.cfg.MinPrice)
	}
	if qty <= 0 {
		return nil, fmt.Errorf("quantity must be positive")
	}
	if s.playerOrderCount(player) >= s.cfg.MaxOrders {
		return nil, fmt.Errorf("max active orders (%d) reached", s.cfg.MaxOrders)
	}

	maxCost := price * int64(qty)
	fluxBal := s.bank.GetFlux(player)
	if fluxBal < maxCost {
		return nil, fmt.Errorf("insufficient Flux: have %d, need %d", fluxBal, maxCost)
	}

	ob := s.getBook(stationID, itemID)
	result := &orderbook.PlaceResult{}
	remaining := qty
	var totalCost int64

	i := 0
	for i < len(ob.Sells) && remaining > 0 {
		sell := ob.Sells[i]
		if sell.Price > price {
			break
		}
		tradePrice := sell.Price
		tradeQty := remaining
		if tradeQty > sell.Quantity {
			tradeQty = sell.Quantity
		}

		tradeCost := tradePrice * int64(tradeQty)
		totalCost += tradeCost

		s.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[itemID] += tradeQty
		})
		s.bank.ModifyFlux(player, -tradeCost)
		s.bank.MarkDirty(player)

		sellerFlux := int64(float64(tradeQty) * float64(tradePrice) * (1.0 - s.cfg.TaxPct))
		s.bank.ModifyFlux(sell.Player, sellerFlux)
		s.bank.MarkDirty(sell.Player)

		trade := &orderbook.Trade{
			ID: s.allocID(), ItemID: itemID, LocationID: stationID,
			Price: tradePrice, Quantity: tradeQty,
			Buyer: player, Seller: sell.Player, Timestamp: time.Now().Unix(),
		}
		s.persistTrade(trade)

		s.log.Log(logCatMarket, "trade: buyer=%s seller=%s item=%d qty=%d price=%d",
			player, sell.Player, itemID, tradeQty, tradePrice)

		s.sendTradeNotification(sell.Player, sell.ID, itemID, tradeQty, tradePrice, true, sellerFlux)

		sell.Quantity -= tradeQty
		remaining -= tradeQty
		result.FilledQty += tradeQty

		if sell.Quantity <= 0 {
			delete(s.orders, sell.ID)
			s.deletePersistOrder(sell.ID)
			ob.Sells = append(ob.Sells[:i], ob.Sells[i+1:]...)
		} else {
			s.persistOrder(sell)
			i++
		}
	}

	if result.FilledQty > 0 {
		result.AvgPrice = totalCost / int64(result.FilledQty)
		result.TotalCost = totalCost
	}

	if remaining > 0 {
		escrowCost := price * int64(remaining)
		s.bank.ModifyFlux(player, -escrowCost)
		s.bank.MarkDirty(player)

		order := &orderbook.Order{
			ID: s.allocID(), Side: orderbook.SideBuy, Player: player,
			LocationID: stationID, ItemID: itemID, Price: price,
			Quantity: remaining, OrigQty: qty,
			CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + s.cfg.OrderExpiry,
		}
		ob.InsertBuy(order)
		s.orders[order.ID] = order
		s.persistOrder(order)
		result.OrderID = order.ID

		s.log.Log(logCatMarket, "buy order placed: player=%s id=%d item=%d qty=%d price=%d",
			player, order.ID, itemID, remaining, price)
	}

	s.bank.SendBankUpdate(player)
	return result, nil
}

// InstantSell sells items at market price (matches against buy book, no resting order).
func (s *Service) InstantSell(player string, stationID, itemID uint32, qty int32) (*orderbook.PlaceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if itemID == fluxItemID {
		return nil, fmt.Errorf("Flux cannot be traded on the marketplace")
	}
	if qty <= 0 {
		return nil, fmt.Errorf("quantity must be positive")
	}

	bankBal := s.bank.GetBankBalance(player, itemID)
	if bankBal < qty {
		return nil, fmt.Errorf("insufficient bank balance: have %d, need %d", bankBal, qty)
	}

	ob := s.getBook(stationID, itemID)
	result := &orderbook.PlaceResult{}
	remaining := qty
	var totalFlux int64

	i := 0
	for i < len(ob.Buys) && remaining > 0 {
		buy := ob.Buys[i]
		tradePrice := buy.Price
		tradeQty := remaining
		if tradeQty > buy.Quantity {
			tradeQty = buy.Quantity
		}

		fluxEarned := int64(float64(tradeQty) * float64(tradePrice) * (1.0 - s.cfg.TaxPct))
		totalFlux += fluxEarned

		s.bank.ModifyFlux(player, fluxEarned)
		s.bank.MarkDirty(player)

		s.bank.ModifyBank(buy.Player, func(bank map[uint32]int32) {
			bank[itemID] += tradeQty
		})
		s.bank.MarkDirty(buy.Player)

		trade := &orderbook.Trade{
			ID: s.allocID(), ItemID: itemID, LocationID: stationID,
			Price: tradePrice, Quantity: tradeQty,
			Buyer: buy.Player, Seller: player, Timestamp: time.Now().Unix(),
		}
		s.persistTrade(trade)

		s.log.Log(logCatMarket, "instant sell: seller=%s buyer=%s item=%d qty=%d price=%d",
			player, buy.Player, itemID, tradeQty, tradePrice)
		s.sendTradeNotification(buy.Player, buy.ID, itemID, tradeQty, tradePrice, false, int64(tradeQty))

		buy.Quantity -= tradeQty
		remaining -= tradeQty
		result.FilledQty += tradeQty

		if buy.Quantity <= 0 {
			delete(s.orders, buy.ID)
			s.deletePersistOrder(buy.ID)
			ob.Buys = append(ob.Buys[:i], ob.Buys[i+1:]...)
		} else {
			s.persistOrder(buy)
			i++
		}
	}

	if result.FilledQty > 0 {
		soldQty := result.FilledQty
		s.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[itemID] -= soldQty
			if bank[itemID] <= 0 {
				delete(bank, itemID)
			}
		})
		s.bank.MarkDirty(player)
		result.AvgPrice = totalFlux / int64(result.FilledQty)
		result.TotalCost = totalFlux
	}

	s.bank.SendBankUpdate(player)
	return result, nil
}

// InstantBuy buys items at market price (matches against sell book, no resting order).
func (s *Service) InstantBuy(player string, stationID, itemID uint32, qty int32) (*orderbook.PlaceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if itemID == fluxItemID {
		return nil, fmt.Errorf("Flux cannot be traded on the marketplace")
	}
	if qty <= 0 {
		return nil, fmt.Errorf("quantity must be positive")
	}

	ob := s.getBook(stationID, itemID)
	result := &orderbook.PlaceResult{}
	remaining := qty
	var totalCost int64

	fluxBal := s.bank.GetFlux(player)

	i := 0
	for i < len(ob.Sells) && remaining > 0 {
		sell := ob.Sells[i]
		tradePrice := sell.Price
		tradeQty := remaining
		if tradeQty > sell.Quantity {
			tradeQty = sell.Quantity
		}

		tradeCost := tradePrice * int64(tradeQty)
		if totalCost+tradeCost > fluxBal {
			affordable := fluxBal - totalCost
			if affordable <= 0 {
				break
			}
			tradeQty = int32(affordable / tradePrice)
			if tradeQty <= 0 {
				break
			}
			tradeCost = tradePrice * int64(tradeQty)
		}
		totalCost += tradeCost

		s.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[itemID] += tradeQty
		})
		s.bank.ModifyFlux(player, -tradeCost)
		s.bank.MarkDirty(player)

		sellerFlux := int64(float64(tradeQty) * float64(tradePrice) * (1.0 - s.cfg.TaxPct))
		s.bank.ModifyFlux(sell.Player, sellerFlux)
		s.bank.MarkDirty(sell.Player)

		trade := &orderbook.Trade{
			ID: s.allocID(), ItemID: itemID, LocationID: stationID,
			Price: tradePrice, Quantity: tradeQty,
			Buyer: player, Seller: sell.Player, Timestamp: time.Now().Unix(),
		}
		s.persistTrade(trade)

		s.log.Log(logCatMarket, "instant buy: buyer=%s seller=%s item=%d qty=%d price=%d",
			player, sell.Player, itemID, tradeQty, tradePrice)
		s.sendTradeNotification(sell.Player, sell.ID, itemID, tradeQty, tradePrice, true, sellerFlux)

		sell.Quantity -= tradeQty
		remaining -= tradeQty
		result.FilledQty += tradeQty

		if sell.Quantity <= 0 {
			delete(s.orders, sell.ID)
			s.deletePersistOrder(sell.ID)
			ob.Sells = append(ob.Sells[:i], ob.Sells[i+1:]...)
		} else {
			s.persistOrder(sell)
			i++
		}
	}

	if result.FilledQty > 0 {
		result.AvgPrice = totalCost / int64(result.FilledQty)
		result.TotalCost = totalCost
	}

	s.bank.SendBankUpdate(player)
	return result, nil
}

// CancelOrder cancels an active order and returns escrowed assets to the player's bank.
func (s *Service) CancelOrder(player string, orderID uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	order, ok := s.orders[orderID]
	if !ok {
		return fmt.Errorf("order %d not found", orderID)
	}
	if order.Player != player {
		return fmt.Errorf("order %d does not belong to player %s", orderID, player)
	}

	key := orderbook.BookKey{LocationID: order.LocationID, ItemID: order.ItemID}
	if ob, ok := s.books[key]; ok {
		ob.RemoveOrder(orderID)
	}
	delete(s.orders, orderID)
	s.deletePersistOrder(orderID)

	if order.Side == orderbook.SideSell {
		s.bank.ModifyBank(player, func(bank map[uint32]int32) {
			bank[order.ItemID] += order.Quantity
		})
	} else {
		refund := order.Price * int64(order.Quantity)
		s.bank.ModifyFlux(player, refund)
	}
	s.bank.MarkDirty(player)

	s.log.Log(logCatMarket, "order cancelled: player=%s id=%d side=%d item=%d qty=%d",
		player, orderID, order.Side, order.ItemID, order.Quantity)

	s.bank.SendBankUpdate(player)
	return nil
}

// ExpireOrders removes all expired orders and returns escrowed assets. Call periodically.
func (s *Service) ExpireOrders() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	for id, order := range s.orders {
		if order.ExpiresAt > 0 && now >= order.ExpiresAt {
			key := orderbook.BookKey{LocationID: order.LocationID, ItemID: order.ItemID}
			if ob, ok := s.books[key]; ok {
				ob.RemoveOrder(id)
			}
			delete(s.orders, id)
			s.deletePersistOrder(id)

			if order.Side == orderbook.SideSell {
				s.bank.ModifyBank(order.Player, func(bank map[uint32]int32) {
					bank[order.ItemID] += order.Quantity
				})
			} else {
				refund := order.Price * int64(order.Quantity)
				s.bank.ModifyFlux(order.Player, refund)
			}
			s.bank.MarkDirty(order.Player)

			s.log.Log(logCatMarket, "order expired: player=%s id=%d item=%d qty=%d",
				order.Player, id, order.ItemID, order.Quantity)
		}
	}
}

// InsertLoadedOrder adds an order loaded from persistence into the in-memory books.
// Called during startup -- does NOT persist or allocate IDs.
func (s *Service) InsertLoadedOrder(order *orderbook.Order) {
	s.orders[order.ID] = order
	ob := s.getBook(order.LocationID, order.ItemID)
	if order.Side == orderbook.SideSell {
		ob.InsertSell(order)
	} else {
		ob.InsertBuy(order)
	}
}

func (s *Service) sendTradeNotification(username string, orderID uint64, itemID uint32, qty int32, price int64, youSold bool, fluxChange int64) {
	if s.notify == nil {
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
	s.notify(username, uint32(gamepb.OperationCode_OP_MARKET_INSTANT_TRADE), data)
}
