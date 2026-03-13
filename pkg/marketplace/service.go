package marketplace

import (
	"fmt"
	"math"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/pkg/logger"
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
	GetBankBalance func(player string, itemID uint32) float32
	// ModifyBank atomically modifies a player's bank map.
	ModifyBank func(player string, fn func(bank map[uint32]float32))
	// MarkDirty flags a player for persistence.
	MarkDirty func(player string)
	// SendBankUpdate sends an updated SE_BANK_CONTENTS event to the player's client.
	SendBankUpdate func(player string)
}

// Service is the marketplace matching engine. All methods are thread-safe.
type Service struct {
	mu     sync.Mutex
	books  map[bookKey]*OrderBook
	orders map[uint64]*Order // global order index

	bank   BankOps
	cfg    Config
	nextID uint64
	log    *logger.Logger
	writer *persist.AsyncWriter

	// notify sends a push notification to an online player.
	// code is the OperationCode, payload is the proto message.
	notify func(username string, code uint32, payload proto.Message)
}

// NewService creates a marketplace service.
func NewService(
	bank BankOps,
	cfg Config,
	log *logger.Logger,
	writer *persist.AsyncWriter,
	notify func(username string, code uint32, payload proto.Message),
) *Service {
	return &Service{
		books:  make(map[bookKey]*OrderBook),
		orders: make(map[uint64]*Order),
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

func (s *Service) getBook(stationID, itemID uint32) *OrderBook {
	key := bookKey{StationID: stationID, ItemID: itemID}
	ob, ok := s.books[key]
	if !ok {
		ob = &OrderBook{}
		s.books[key] = ob
	}
	return ob
}

// Browse returns an aggregated order book view for a given station and item.
func (s *Service) Browse(stationID, itemID uint32) OrderBookView {
	s.mu.Lock()
	defer s.mu.Unlock()

	ob := s.getBook(stationID, itemID)
	return OrderBookView{
		ItemID:     itemID,
		SellLevels: ob.AggregateSells(20),
		BuyLevels:  ob.AggregateBuys(20),
	}
}

// PlayerOrders returns all active orders for a player.
func (s *Service) PlayerOrders(player string) []*Order {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result []*Order
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
func (s *Service) PlaceSellOrder(player string, stationID, itemID uint32, price float64, qty float32) (*PlaceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	price = math.Floor(price) // prices are always whole Flux
	qty = float32(math.Floor(float64(qty)))
	if itemID == fluxItemID {
		return nil, fmt.Errorf("Flux cannot be traded on the marketplace")
	}
	if price < s.cfg.MinPrice {
		return nil, fmt.Errorf("price %.4f below minimum %.4f", price, s.cfg.MinPrice)
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
		return nil, fmt.Errorf("insufficient bank balance: have %.1f, need %.1f", bankBal, qty)
	}

	ob := s.getBook(stationID, itemID)
	result := &PlaceResult{}
	remaining := qty
	var totalFlux float64

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

		fluxEarned := float64(tradeQty) * tradePrice * (1.0 - s.cfg.TaxPct)
		totalFlux += fluxEarned

		s.bank.ModifyBank(player, func(bank map[uint32]float32) {
			bank[fluxItemID] += float32(fluxEarned)
		})
		s.bank.MarkDirty(player)

		s.bank.ModifyBank(buy.Player, func(bank map[uint32]float32) {
			bank[itemID] += tradeQty
		})
		s.bank.MarkDirty(buy.Player)

		trade := &Trade{
			ID: s.allocID(), ItemID: itemID, StationID: stationID,
			Price: tradePrice, Quantity: tradeQty,
			Buyer: buy.Player, Seller: player, Timestamp: time.Now().Unix(),
		}
		s.persistTrade(trade)

		s.log.Log(logCatMarket, "trade: seller=%s buyer=%s item=%d qty=%.1f price=%.2f",
			player, buy.Player, itemID, tradeQty, tradePrice)

		s.sendTradeNotification(buy.Player, buy.ID, itemID, tradeQty, tradePrice, false, float32(tradeQty))

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
		s.bank.ModifyBank(player, func(bank map[uint32]float32) {
			bank[itemID] -= soldQty
			if bank[itemID] <= 0 {
				delete(bank, itemID)
			}
		})
		s.bank.MarkDirty(player)
	}

	if result.FilledQty > 0 {
		result.AvgPrice = totalFlux / float64(result.FilledQty)
		result.TotalCost = totalFlux
	}

	// Place resting sell order for unfilled portion
	if remaining > 0 {
		s.bank.ModifyBank(player, func(bank map[uint32]float32) {
			bank[itemID] -= remaining
			if bank[itemID] <= 0 {
				delete(bank, itemID)
			}
		})
		s.bank.MarkDirty(player)

		order := &Order{
			ID: s.allocID(), Side: SideSell, Player: player,
			StationID: stationID, ItemID: itemID, Price: price,
			Quantity: remaining, OrigQty: qty,
			CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + s.cfg.OrderExpiry,
		}
		ob.InsertSell(order)
		s.orders[order.ID] = order
		s.persistOrder(order)
		result.OrderID = order.ID

		s.log.Log(logCatMarket, "sell order placed: player=%s id=%d item=%d qty=%.1f price=%.2f",
			player, order.ID, itemID, remaining, price)
	}

	s.bank.SendBankUpdate(player)
	return result, nil
}

// PlaceBuyOrder places a limit buy order, matching against existing sell orders first.
func (s *Service) PlaceBuyOrder(player string, stationID, itemID uint32, price float64, qty float32) (*PlaceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	price = math.Floor(price)
	qty = float32(math.Floor(float64(qty)))
	if itemID == fluxItemID {
		return nil, fmt.Errorf("Flux cannot be traded on the marketplace")
	}
	if price < s.cfg.MinPrice {
		return nil, fmt.Errorf("price %.4f below minimum %.4f", price, s.cfg.MinPrice)
	}
	if qty <= 0 {
		return nil, fmt.Errorf("quantity must be positive")
	}
	if s.playerOrderCount(player) >= s.cfg.MaxOrders {
		return nil, fmt.Errorf("max active orders (%d) reached", s.cfg.MaxOrders)
	}

	maxCost := float32(price * float64(qty))
	fluxBal := s.bank.GetBankBalance(player, fluxItemID)
	if fluxBal < maxCost {
		return nil, fmt.Errorf("insufficient Flux: have %.1f, need %.1f", fluxBal, maxCost)
	}

	ob := s.getBook(stationID, itemID)
	result := &PlaceResult{}
	remaining := qty
	var totalCost float64

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

		tradeCost := float64(tradeQty) * tradePrice
		totalCost += tradeCost

		s.bank.ModifyBank(player, func(bank map[uint32]float32) {
			bank[itemID] += tradeQty
		})
		s.bank.ModifyBank(player, func(bank map[uint32]float32) {
			bank[fluxItemID] -= float32(tradeCost)
			if bank[fluxItemID] <= 0 {
				delete(bank, fluxItemID)
			}
		})
		s.bank.MarkDirty(player)

		sellerFlux := float64(tradeQty) * tradePrice * (1.0 - s.cfg.TaxPct)
		s.bank.ModifyBank(sell.Player, func(bank map[uint32]float32) {
			bank[fluxItemID] += float32(sellerFlux)
		})
		s.bank.MarkDirty(sell.Player)

		trade := &Trade{
			ID: s.allocID(), ItemID: itemID, StationID: stationID,
			Price: tradePrice, Quantity: tradeQty,
			Buyer: player, Seller: sell.Player, Timestamp: time.Now().Unix(),
		}
		s.persistTrade(trade)

		s.log.Log(logCatMarket, "trade: buyer=%s seller=%s item=%d qty=%.1f price=%.2f",
			player, sell.Player, itemID, tradeQty, tradePrice)

		s.sendTradeNotification(sell.Player, sell.ID, itemID, tradeQty, tradePrice, true, float32(sellerFlux))

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
		result.AvgPrice = totalCost / float64(result.FilledQty)
		result.TotalCost = totalCost
	}

	if remaining > 0 {
		escrowCost := float32(price * float64(remaining))
		s.bank.ModifyBank(player, func(bank map[uint32]float32) {
			bank[fluxItemID] -= escrowCost
			if bank[fluxItemID] <= 0 {
				delete(bank, fluxItemID)
			}
		})
		s.bank.MarkDirty(player)

		order := &Order{
			ID: s.allocID(), Side: SideBuy, Player: player,
			StationID: stationID, ItemID: itemID, Price: price,
			Quantity: remaining, OrigQty: qty,
			CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + s.cfg.OrderExpiry,
		}
		ob.InsertBuy(order)
		s.orders[order.ID] = order
		s.persistOrder(order)
		result.OrderID = order.ID

		s.log.Log(logCatMarket, "buy order placed: player=%s id=%d item=%d qty=%.1f price=%.2f",
			player, order.ID, itemID, remaining, price)
	}

	s.bank.SendBankUpdate(player)
	return result, nil
}

// InstantSell sells items at market price (matches against buy book, no resting order).
func (s *Service) InstantSell(player string, stationID, itemID uint32, qty float32) (*PlaceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	qty = float32(math.Floor(float64(qty)))
	if itemID == fluxItemID {
		return nil, fmt.Errorf("Flux cannot be traded on the marketplace")
	}
	if qty <= 0 {
		return nil, fmt.Errorf("quantity must be positive")
	}

	bankBal := s.bank.GetBankBalance(player, itemID)
	if bankBal < qty {
		return nil, fmt.Errorf("insufficient bank balance: have %.1f, need %.1f", bankBal, qty)
	}

	ob := s.getBook(stationID, itemID)
	result := &PlaceResult{}
	remaining := qty
	var totalFlux float64

	i := 0
	for i < len(ob.Buys) && remaining > 0 {
		buy := ob.Buys[i]
		tradePrice := buy.Price
		tradeQty := remaining
		if tradeQty > buy.Quantity {
			tradeQty = buy.Quantity
		}

		fluxEarned := float64(tradeQty) * tradePrice * (1.0 - s.cfg.TaxPct)
		totalFlux += fluxEarned

		s.bank.ModifyBank(player, func(bank map[uint32]float32) {
			bank[fluxItemID] += float32(fluxEarned)
		})
		s.bank.MarkDirty(player)

		s.bank.ModifyBank(buy.Player, func(bank map[uint32]float32) {
			bank[itemID] += tradeQty
		})
		s.bank.MarkDirty(buy.Player)

		trade := &Trade{
			ID: s.allocID(), ItemID: itemID, StationID: stationID,
			Price: tradePrice, Quantity: tradeQty,
			Buyer: buy.Player, Seller: player, Timestamp: time.Now().Unix(),
		}
		s.persistTrade(trade)

		s.log.Log(logCatMarket, "instant sell: seller=%s buyer=%s item=%d qty=%.1f price=%.2f",
			player, buy.Player, itemID, tradeQty, tradePrice)
		s.sendTradeNotification(buy.Player, buy.ID, itemID, tradeQty, tradePrice, false, float32(tradeQty))

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
		s.bank.ModifyBank(player, func(bank map[uint32]float32) {
			bank[itemID] -= soldQty
			if bank[itemID] <= 0 {
				delete(bank, itemID)
			}
		})
		s.bank.MarkDirty(player)
		result.AvgPrice = totalFlux / float64(result.FilledQty)
		result.TotalCost = totalFlux
	}

	s.bank.SendBankUpdate(player)
	return result, nil
}

// InstantBuy buys items at market price (matches against sell book, no resting order).
func (s *Service) InstantBuy(player string, stationID, itemID uint32, qty float32) (*PlaceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	qty = float32(math.Floor(float64(qty)))
	if itemID == fluxItemID {
		return nil, fmt.Errorf("Flux cannot be traded on the marketplace")
	}
	if qty <= 0 {
		return nil, fmt.Errorf("quantity must be positive")
	}

	ob := s.getBook(stationID, itemID)
	result := &PlaceResult{}
	remaining := qty
	var totalCost float64

	fluxBal := s.bank.GetBankBalance(player, fluxItemID)

	i := 0
	for i < len(ob.Sells) && remaining > 0 {
		sell := ob.Sells[i]
		tradePrice := sell.Price
		tradeQty := remaining
		if tradeQty > sell.Quantity {
			tradeQty = sell.Quantity
		}

		tradeCost := float64(tradeQty) * tradePrice
		if float32(totalCost+tradeCost) > fluxBal {
			affordable := float64(fluxBal) - totalCost
			if affordable <= 0 {
				break
			}
			tradeQty = float32(affordable / tradePrice)
			if tradeQty <= 0 {
				break
			}
			tradeCost = float64(tradeQty) * tradePrice
		}
		totalCost += tradeCost

		s.bank.ModifyBank(player, func(bank map[uint32]float32) {
			bank[itemID] += tradeQty
		})
		s.bank.ModifyBank(player, func(bank map[uint32]float32) {
			bank[fluxItemID] -= float32(tradeCost)
			if bank[fluxItemID] <= 0 {
				delete(bank, fluxItemID)
			}
		})
		s.bank.MarkDirty(player)

		sellerFlux := float64(tradeQty) * tradePrice * (1.0 - s.cfg.TaxPct)
		s.bank.ModifyBank(sell.Player, func(bank map[uint32]float32) {
			bank[fluxItemID] += float32(sellerFlux)
		})
		s.bank.MarkDirty(sell.Player)

		trade := &Trade{
			ID: s.allocID(), ItemID: itemID, StationID: stationID,
			Price: tradePrice, Quantity: tradeQty,
			Buyer: player, Seller: sell.Player, Timestamp: time.Now().Unix(),
		}
		s.persistTrade(trade)

		s.log.Log(logCatMarket, "instant buy: buyer=%s seller=%s item=%d qty=%.1f price=%.2f",
			player, sell.Player, itemID, tradeQty, tradePrice)
		s.sendTradeNotification(sell.Player, sell.ID, itemID, tradeQty, tradePrice, true, float32(sellerFlux))

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
		result.AvgPrice = totalCost / float64(result.FilledQty)
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

	key := bookKey{StationID: order.StationID, ItemID: order.ItemID}
	if ob, ok := s.books[key]; ok {
		ob.RemoveOrder(orderID)
	}
	delete(s.orders, orderID)
	s.deletePersistOrder(orderID)

	if order.Side == SideSell {
		s.bank.ModifyBank(player, func(bank map[uint32]float32) {
			bank[order.ItemID] += order.Quantity
		})
	} else {
		refund := float32(order.Price * float64(order.Quantity))
		s.bank.ModifyBank(player, func(bank map[uint32]float32) {
			bank[fluxItemID] += refund
		})
	}
	s.bank.MarkDirty(player)

	s.log.Log(logCatMarket, "order cancelled: player=%s id=%d side=%d item=%d qty=%.1f",
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
			key := bookKey{StationID: order.StationID, ItemID: order.ItemID}
			if ob, ok := s.books[key]; ok {
				ob.RemoveOrder(id)
			}
			delete(s.orders, id)
			s.deletePersistOrder(id)

			if order.Side == SideSell {
				s.bank.ModifyBank(order.Player, func(bank map[uint32]float32) {
					bank[order.ItemID] += order.Quantity
				})
			} else {
				refund := float32(order.Price * float64(order.Quantity))
				s.bank.ModifyBank(order.Player, func(bank map[uint32]float32) {
					bank[fluxItemID] += refund
				})
			}
			s.bank.MarkDirty(order.Player)

			s.log.Log(logCatMarket, "order expired: player=%s id=%d item=%d qty=%.1f",
				order.Player, id, order.ItemID, order.Quantity)
		}
	}
}

// InsertLoadedOrder adds an order loaded from persistence into the in-memory books.
// Called during startup -- does NOT persist or allocate IDs.
func (s *Service) InsertLoadedOrder(order *Order) {
	s.orders[order.ID] = order
	ob := s.getBook(order.StationID, order.ItemID)
	if order.Side == SideSell {
		ob.InsertSell(order)
	} else {
		ob.InsertBuy(order)
	}
}

func (s *Service) sendTradeNotification(username string, orderID uint64, itemID uint32, qty float32, price float64, youSold bool, fluxChange float32) {
	if s.notify == nil {
		return
	}
	notif := &gamepb.MarketTradeNotification{
		OrderId:    orderID,
		ItemId:     itemID,
		FilledQty:  qty,
		Price:      float32(price),
		YouSold:    youSold,
		FluxChange: fluxChange,
	}
	s.notify(username, uint32(gamepb.OperationCode_OP_MARKET_INSTANT_TRADE), notif)
}
