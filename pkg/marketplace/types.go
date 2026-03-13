package marketplace

// OrderSide indicates whether an order is a buy or sell.
type OrderSide uint8

const (
	SideSell OrderSide = 1
	SideBuy  OrderSide = 2
)

// Order represents a resting limit order in the order book.
type Order struct {
	ID        uint64    `json:"id"`
	Side      OrderSide `json:"side"`
	Player    string    `json:"player"`     // username (lowercase)
	StationID uint32    `json:"station_id"` // per-station markets
	ItemID    uint32    `json:"item_id"`
	Price     int64     `json:"price"`     // price per unit in Flux
	Quantity  int32     `json:"quantity"`  // remaining qty
	OrigQty   int32     `json:"orig_qty"` // original qty
	CreatedAt int64     `json:"created_at"`
	ExpiresAt int64     `json:"expires_at"`
}

// Trade represents a completed trade between two players.
type Trade struct {
	ID        uint64  `json:"id"`
	ItemID    uint32  `json:"item_id"`
	StationID uint32  `json:"station_id"`
	Price     int64   `json:"price"`
	Quantity  int32   `json:"quantity"`
	Buyer     string  `json:"buyer"`
	Seller    string  `json:"seller"`
	Timestamp int64   `json:"timestamp"`
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

// bookKey identifies an order book by station and item.
type bookKey struct {
	StationID uint32
	ItemID    uint32
}
