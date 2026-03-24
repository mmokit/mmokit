package orderbook

import (
	"fmt"
	"sync"
	"time"
)

// MatchEvent describes a single fill between a buy and sell order.
type MatchEvent struct {
	BuyOrderID  uint64
	SellOrderID uint64
	BuyerID     string
	SellerID    string
	ItemID      uint32
	LocationID  uint32
	Quantity    int32
	Price       int64
}

// Service is a generic order matching engine. All methods are thread-safe.
// It handles order storage, matching, and lifecycle but has no knowledge of
// currencies, banks, or persistence.
type Service struct {
	mu     sync.Mutex
	books  map[BookKey]*OrderBook
	orders map[uint64]*Order
	cfg    Config
	nextID uint64
}

// NewService creates a new order matching service.
func NewService(cfg Config) *Service {
	return &Service{
		books:  make(map[BookKey]*OrderBook),
		orders: make(map[uint64]*Order),
		cfg:    cfg,
		nextID: 1,
	}
}

// SetNextID sets the next order ID (called after loading persisted state).
func (s *Service) SetNextID(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID = id
}

// NextID returns the current next ID value.
func (s *Service) NextID() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextID
}

func (s *Service) allocID() uint64 {
	id := s.nextID
	s.nextID++
	return id
}

// GetBook returns the order book for a given location and item, creating it if needed.
func (s *Service) GetBook(locationID, itemID uint32) *OrderBook {
	key := BookKey{LocationID: locationID, ItemID: itemID}
	ob, ok := s.books[key]
	if !ok {
		ob = &OrderBook{}
		s.books[key] = ob
	}
	return ob
}

// OrderCount returns the number of active orders for a player.
func (s *Service) OrderCount(playerID string) int {
	count := 0
	for _, o := range s.orders {
		if o.Player == playerID {
			count++
		}
	}
	return count
}

// PlaceSellOrder places a limit sell order, matching against existing buy orders first.
// Returns match events, the resting order (nil if fully filled), and any error.
func (s *Service) PlaceSellOrder(sellerID string, locationID, itemID uint32, price int64, qty int32) ([]MatchEvent, *Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if price < s.cfg.MinPrice {
		return nil, nil, fmt.Errorf("price %d below minimum %d", price, s.cfg.MinPrice)
	}
	if qty <= 0 {
		return nil, nil, fmt.Errorf("quantity must be positive")
	}
	if s.OrderCount(sellerID) >= s.cfg.MaxOrders {
		return nil, nil, fmt.Errorf("max active orders (%d) reached", s.cfg.MaxOrders)
	}

	ob := s.GetBook(locationID, itemID)
	var matches []MatchEvent
	remaining := qty

	// Match against buy book (highest buyers first)
	i := 0
	for i < len(ob.Buys) && remaining > 0 {
		buy := ob.Buys[i]
		if buy.Price < price {
			break
		}
		tradeQty := remaining
		if tradeQty > buy.Quantity {
			tradeQty = buy.Quantity
		}

		matches = append(matches, MatchEvent{
			BuyOrderID:  buy.ID,
			SellOrderID: 0, // incoming sell, no resting ID yet
			BuyerID:     buy.Player,
			SellerID:    sellerID,
			ItemID:      itemID,
			LocationID:  locationID,
			Quantity:    tradeQty,
			Price:       buy.Price,
		})

		buy.Quantity -= tradeQty
		remaining -= tradeQty

		if buy.Quantity <= 0 {
			delete(s.orders, buy.ID)
			ob.Buys = append(ob.Buys[:i], ob.Buys[i+1:]...)
		} else {
			i++
		}
	}

	// Place resting sell order for unfilled portion
	var resting *Order
	if remaining > 0 {
		resting = &Order{
			ID: s.allocID(), Side: SideSell, Player: sellerID,
			LocationID: locationID, ItemID: itemID, Price: price,
			Quantity: remaining, OrigQty: qty,
			CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + s.cfg.OrderExpiry,
		}
		ob.InsertSell(resting)
		s.orders[resting.ID] = resting
	}

	return matches, resting, nil
}

// PlaceBuyOrder places a limit buy order, matching against existing sell orders first.
// Returns match events, the resting order (nil if fully filled), and any error.
func (s *Service) PlaceBuyOrder(buyerID string, locationID, itemID uint32, price int64, qty int32) ([]MatchEvent, *Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if price < s.cfg.MinPrice {
		return nil, nil, fmt.Errorf("price %d below minimum %d", price, s.cfg.MinPrice)
	}
	if qty <= 0 {
		return nil, nil, fmt.Errorf("quantity must be positive")
	}
	if s.OrderCount(buyerID) >= s.cfg.MaxOrders {
		return nil, nil, fmt.Errorf("max active orders (%d) reached", s.cfg.MaxOrders)
	}

	ob := s.GetBook(locationID, itemID)
	var matches []MatchEvent
	remaining := qty

	i := 0
	for i < len(ob.Sells) && remaining > 0 {
		sell := ob.Sells[i]
		if sell.Price > price {
			break
		}
		tradeQty := remaining
		if tradeQty > sell.Quantity {
			tradeQty = sell.Quantity
		}

		matches = append(matches, MatchEvent{
			BuyOrderID:  0, // incoming buy, no resting ID yet
			SellOrderID: sell.ID,
			BuyerID:     buyerID,
			SellerID:    sell.Player,
			ItemID:      itemID,
			LocationID:  locationID,
			Quantity:    tradeQty,
			Price:       sell.Price,
		})

		sell.Quantity -= tradeQty
		remaining -= tradeQty

		if sell.Quantity <= 0 {
			delete(s.orders, sell.ID)
			ob.Sells = append(ob.Sells[:i], ob.Sells[i+1:]...)
		} else {
			i++
		}
	}

	var resting *Order
	if remaining > 0 {
		resting = &Order{
			ID: s.allocID(), Side: SideBuy, Player: buyerID,
			LocationID: locationID, ItemID: itemID, Price: price,
			Quantity: remaining, OrigQty: qty,
			CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + s.cfg.OrderExpiry,
		}
		ob.InsertBuy(resting)
		s.orders[resting.ID] = resting
	}

	return matches, resting, nil
}

// InstantSell matches against the buy book without placing a resting order.
// Returns match events and any error.
func (s *Service) InstantSell(sellerID string, locationID, itemID uint32, qty int32) ([]MatchEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if qty <= 0 {
		return nil, fmt.Errorf("quantity must be positive")
	}

	ob := s.GetBook(locationID, itemID)
	var matches []MatchEvent
	remaining := qty

	i := 0
	for i < len(ob.Buys) && remaining > 0 {
		buy := ob.Buys[i]
		tradeQty := remaining
		if tradeQty > buy.Quantity {
			tradeQty = buy.Quantity
		}

		matches = append(matches, MatchEvent{
			BuyOrderID:  buy.ID,
			SellOrderID: 0,
			BuyerID:     buy.Player,
			SellerID:    sellerID,
			ItemID:      itemID,
			LocationID:  locationID,
			Quantity:    tradeQty,
			Price:       buy.Price,
		})

		buy.Quantity -= tradeQty
		remaining -= tradeQty

		if buy.Quantity <= 0 {
			delete(s.orders, buy.ID)
			ob.Buys = append(ob.Buys[:i], ob.Buys[i+1:]...)
		} else {
			i++
		}
	}

	return matches, nil
}

// InstantBuy matches against the sell book without placing a resting order.
// If maxCost > 0, matching stops when the total cost would exceed maxCost.
// Returns match events and any error.
func (s *Service) InstantBuy(buyerID string, locationID, itemID uint32, qty int32, maxCost int64) ([]MatchEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if qty <= 0 {
		return nil, fmt.Errorf("quantity must be positive")
	}

	ob := s.GetBook(locationID, itemID)
	var matches []MatchEvent
	remaining := qty
	var totalCost int64

	i := 0
	for i < len(ob.Sells) && remaining > 0 {
		sell := ob.Sells[i]
		tradeQty := remaining
		if tradeQty > sell.Quantity {
			tradeQty = sell.Quantity
		}

		// Budget limiting
		if maxCost > 0 {
			tradeCost := sell.Price * int64(tradeQty)
			if totalCost+tradeCost > maxCost {
				affordable := maxCost - totalCost
				if affordable <= 0 {
					break
				}
				tradeQty = int32(affordable / sell.Price)
				if tradeQty <= 0 {
					break
				}
				tradeCost = sell.Price * int64(tradeQty)
			}
			totalCost += tradeCost
		}

		matches = append(matches, MatchEvent{
			BuyOrderID:  0,
			SellOrderID: sell.ID,
			BuyerID:     buyerID,
			SellerID:    sell.Player,
			ItemID:      itemID,
			LocationID:  locationID,
			Quantity:    tradeQty,
			Price:       sell.Price,
		})

		sell.Quantity -= tradeQty
		remaining -= tradeQty

		if sell.Quantity <= 0 {
			delete(s.orders, sell.ID)
			ob.Sells = append(ob.Sells[:i], ob.Sells[i+1:]...)
		} else {
			i++
		}
	}

	return matches, nil
}

// CancelOrder removes an order from the book. Returns the removed order for the
// caller to process refunds. Returns an error if not found or wrong player.
func (s *Service) CancelOrder(playerID string, orderID uint64) (*Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	order, ok := s.orders[orderID]
	if !ok {
		return nil, fmt.Errorf("order %d not found", orderID)
	}
	if order.Player != playerID {
		return nil, fmt.Errorf("order %d does not belong to player %s", orderID, playerID)
	}

	key := BookKey{LocationID: order.LocationID, ItemID: order.ItemID}
	if ob, ok := s.books[key]; ok {
		ob.RemoveOrder(orderID)
	}
	delete(s.orders, orderID)

	return order, nil
}

// ExpireOrders removes all expired orders and returns them for the caller to process refunds.
func (s *Service) ExpireOrders() []*Order {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	var expired []*Order
	for id, order := range s.orders {
		if order.ExpiresAt > 0 && now >= order.ExpiresAt {
			key := BookKey{LocationID: order.LocationID, ItemID: order.ItemID}
			if ob, ok := s.books[key]; ok {
				ob.RemoveOrder(id)
			}
			delete(s.orders, id)
			expired = append(expired, order)
		}
	}
	return expired
}

// Browse returns an aggregated order book view for a given location and item.
func (s *Service) Browse(locationID, itemID uint32) OrderBookView {
	s.mu.Lock()
	defer s.mu.Unlock()

	ob := s.GetBook(locationID, itemID)
	return OrderBookView{
		ItemID:     itemID,
		SellLevels: ob.AggregateSells(20),
		BuyLevels:  ob.AggregateBuys(20),
	}
}

// PlayerOrders returns all active orders for a player.
func (s *Service) PlayerOrders(playerID string) []*Order {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result []*Order
	for _, o := range s.orders {
		if o.Player == playerID {
			result = append(result, o)
		}
	}
	return result
}

// InsertLoadedOrder adds an order loaded from persistence into the in-memory books.
// Called during startup -- does NOT persist or allocate IDs.
func (s *Service) InsertLoadedOrder(order *Order) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.orders[order.ID] = order
	ob := s.GetBook(order.LocationID, order.ItemID)
	if order.Side == SideSell {
		ob.InsertSell(order)
	} else {
		ob.InsertBuy(order)
	}
}

// GetOrder returns an order by ID, or nil if not found.
func (s *Service) GetOrder(orderID uint64) *Order {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.orders[orderID]
}

// AllocID allocates and returns a new unique ID (used by settlement for trade IDs).
func (s *Service) AllocID() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allocID()
}
