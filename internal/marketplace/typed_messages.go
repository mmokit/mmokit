// Package marketplace — typed_messages.go defines the typed Go structs
// for marketplace ops on the typed-op channel (channel 0x01,
// mmokit.RegisterOp[Req, Res]).
//
// These run through the engine's reflection codec — same shape as typed
// events on channel 0x00. Field names use Go's CamelCase convention; the
// generated TypeScript SDK exposes them as lowerCamelCase
// (`MarketBrowseRequest{ItemID}` → `client.marketBrowse({itemID})`).
//
// Wire-stable typeIDs are derived from the package-qualified type name
// (`marketplace.MarketBrowseRequest` etc.), so renames are wire-breaking.

package marketplace

// MarketBrowseRequest fetches the aggregated order book for a single item
// at the calling player's current station. RoutePlayerCell — the cell
// engine accesses Settlement (which wraps the in-memory order book +
// MarketRepository).
type MarketBrowseRequest struct {
	ItemID uint32
}

// MarketOrderBookResponse is the aggregated order-book view returned by
// MarketBrowseRequest. SellLevels and BuyLevels are sorted best-price-
// first by Settlement.Browse → orderbook.OrderBookView.
type MarketOrderBookResponse struct {
	ItemID     uint32
	SellLevels []MarketPriceLevel
	BuyLevels  []MarketPriceLevel
}

// MarketPriceLevel aggregates all orders at one price tier. Price is in
// settlement-currency units; Quantity is the sum of remaining quantity
// across all orders at this price; OrderCount is the number of distinct
// orders.
type MarketPriceLevel struct {
	Price      int64
	Quantity   int32
	OrderCount uint32
}

// MarketCreateOrderRequest places either a buy or sell order at the
// player's current station. IsBuy=true → buy order (locks currency),
// IsBuy=false → sell order (locks bank items). Routes to player-cell.
type MarketCreateOrderRequest struct {
	ItemID       uint32
	IsBuy        bool
	PricePerUnit int64
	Quantity     int32
}

// MarketCancelOrderRequest cancels one of the calling player's open
// orders by ID. Settlement validates ownership before touching the book.
type MarketCancelOrderRequest struct {
	OrderID uint64
}

// MarketMyOrdersRequest fetches every open order owned by the calling
// player. Empty payload — identity is on the OpContext.
type MarketMyOrdersRequest struct{}

// MarketMyOrdersResponse carries the calling player's open orders.
type MarketMyOrdersResponse struct {
	Orders []MarketOrderEntry
}

// MarketOrderEntry is one row in the player-orders view. Mirrors the
// fields of orderbook.Order minus owner/location (those are scoped by
// the calling user_id + station).
type MarketOrderEntry struct {
	OrderID      uint64
	ItemID       uint32
	IsBuy        bool
	PricePerUnit int64
	Quantity     int32
	OrigQuantity int32
	CreatedAt    int64
	ExpiresAt    int64
}

// MarketInstantTradeRequest is a market-order-style execution — fills at
// the best available price, no resting order created. IsBuy=true buys
// from the cheapest sell level; false sells into the highest buy level.
type MarketInstantTradeRequest struct {
	ItemID   uint32
	IsBuy    bool
	Quantity int32
}

// MarketOrderResultResponse is the shared result type for create-order,
// cancel-order, and instant-trade. OrderID is the resting order's id
// (zero on instant-trades); FilledQty / AvgPrice / TotalCost summarize
// any matches that fired during placement.
type MarketOrderResultResponse struct {
	OrderID   uint64
	FilledQty int32
	AvgPrice  int64
	TotalCost int64
}

// MarketTradeNotification is a server-side push to an online player when
// one of their resting orders fills (in part or in full) against another
// trader's order. Sent as a typed event on channel 0x00 — the recipient is
// not necessarily the one who triggered the trade.
type MarketTradeNotification struct {
	OrderID        uint64
	ItemID         uint32
	FilledQty      int32
	Price          int64
	YouSold        bool
	CurrencyChange int64
	CurrencyID     uint32
}
