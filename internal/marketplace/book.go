package marketplace

import "sort"

// OrderBook holds the sell and buy sides for a single (station, item) pair.
// Sells are sorted ascending by price, then oldest first (FIFO).
// Buys are sorted descending by price, then oldest first (FIFO).
type OrderBook struct {
	Sells []*Order
	Buys  []*Order
}

// InsertSell adds a sell order in sorted position.
func (ob *OrderBook) InsertSell(o *Order) {
	i := sort.Search(len(ob.Sells), func(i int) bool {
		if ob.Sells[i].Price == o.Price {
			return ob.Sells[i].CreatedAt > o.CreatedAt
		}
		return ob.Sells[i].Price > o.Price
	})
	ob.Sells = append(ob.Sells, nil)
	copy(ob.Sells[i+1:], ob.Sells[i:])
	ob.Sells[i] = o
}

// InsertBuy adds a buy order in sorted position.
func (ob *OrderBook) InsertBuy(o *Order) {
	i := sort.Search(len(ob.Buys), func(i int) bool {
		if ob.Buys[i].Price == o.Price {
			return ob.Buys[i].CreatedAt > o.CreatedAt
		}
		return ob.Buys[i].Price < o.Price
	})
	ob.Buys = append(ob.Buys, nil)
	copy(ob.Buys[i+1:], ob.Buys[i:])
	ob.Buys[i] = o
}

// RemoveOrder removes an order by ID from either side. Returns the removed order or nil.
func (ob *OrderBook) RemoveOrder(orderID uint64) *Order {
	for i, o := range ob.Sells {
		if o.ID == orderID {
			ob.Sells = append(ob.Sells[:i], ob.Sells[i+1:]...)
			return o
		}
	}
	for i, o := range ob.Buys {
		if o.ID == orderID {
			ob.Buys = append(ob.Buys[:i], ob.Buys[i+1:]...)
			return o
		}
	}
	return nil
}

// AggregateSells returns aggregated price levels for the sell side.
func (ob *OrderBook) AggregateSells(maxLevels int) []PriceLevel {
	return aggregateLevels(ob.Sells, maxLevels)
}

// AggregateBuys returns aggregated price levels for the buy side.
func (ob *OrderBook) AggregateBuys(maxLevels int) []PriceLevel {
	return aggregateLevels(ob.Buys, maxLevels)
}

func aggregateLevels(orders []*Order, maxLevels int) []PriceLevel {
	if len(orders) == 0 {
		return nil
	}
	levels := make([]PriceLevel, 0, maxLevels)
	var cur PriceLevel
	cur.Price = orders[0].Price
	for _, o := range orders {
		if o.Price != cur.Price {
			levels = append(levels, cur)
			if maxLevels > 0 && len(levels) >= maxLevels {
				return levels
			}
			cur = PriceLevel{Price: o.Price}
		}
		cur.Quantity += o.Quantity
		cur.Count++
	}
	levels = append(levels, cur)
	return levels
}
