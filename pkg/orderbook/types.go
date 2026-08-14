package orderbook

// OrderSide indicates whether an order is a buy or sell.
type OrderSide uint8

const (
	SideSell OrderSide = 1
	SideBuy  OrderSide = 2
)

// Order represents a resting limit order in the order book.
type Order struct {
	ID         uint64    `json:"id"`
	Side       OrderSide `json:"side"`
	Owner      string    `json:"owner"`       // username (lowercase)
	LocationID uint32    `json:"location_id"` // per-location markets
	ItemID     uint32    `json:"item_id"`
	Price      int64     `json:"price"`    // price per unit in settlement currency
	Quantity   int32     `json:"quantity"` // remaining qty
	OrigQty    int32     `json:"orig_qty"` // original qty
	CreatedAt  int64     `json:"created_at"`
	ExpiresAt  int64     `json:"expires_at"`
}

// Trade represents a completed trade between two players.
type Trade struct {
	ID         uint64 `json:"id"`
	ItemID     uint32 `json:"item_id"`
	LocationID uint32 `json:"location_id"`
	Price      int64  `json:"price"`
	Quantity   int32  `json:"quantity"`
	Buyer      string `json:"buyer"`
	Seller     string `json:"seller"`
	Timestamp  int64  `json:"timestamp"`
}

// PlaceResult is returned from order placement, summarising fills.
type PlaceResult struct {
	OrderID   uint64
	FilledQty int32
	AvgPrice  int64
	TotalCost int64
}

// TradeResult is an alias for PlaceResult.
type TradeResult = PlaceResult

// OrderBookView is the aggregated view of an order book for a single item.
type OrderBookView struct {
	ItemID     uint32
	SellLevels []PriceLevel
	BuyLevels  []PriceLevel
}

// PriceLevel is one aggregated row in an order book view.
type PriceLevel struct {
	Price    int64
	Quantity int32
	Count    int
}

// BookKey identifies an order book by location and item.
type BookKey struct {
	LocationID uint32
	ItemID     uint32
}
