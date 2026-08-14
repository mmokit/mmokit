package marketplace

import (
	"github.com/mmokit/mmokit/pkg/mmokit"
)

// RegisterHandlers registers all marketplace operation handlers. Each
// op runs as a typed RoutePlayerCell handler — the dispatcher routes the
// frame to the player's authoritative cell engine, runs the handler on
// the loop goroutine (safe ECS access), and ships the response back.
//
// The Settlement methods invoked here (Browse / PlaceBuyOrder /
// PlaceSellOrder / CancelOrder / PlayerOrders / InstantBuy / InstantSell)
// are themselves mutex-protected so they tolerate concurrent calls from
// any number of cells; the cell-routing constraint exists primarily so
// future ops on this same kind can safely reach the cell's ECS state.
func RegisterHandlers(svc *Settlement, stationID uint32) {
	mmokit.RegisterOp[MarketBrowseRequest, MarketOrderBookResponse](mmokit.RoutePlayerCell,
		func(_ *mmokit.OpContext, req *MarketBrowseRequest) (*MarketOrderBookResponse, error) {
			view := svc.Browse(stationID, req.ItemID)
			resp := &MarketOrderBookResponse{ItemID: view.ItemID}
			for _, l := range view.SellLevels {
				resp.SellLevels = append(resp.SellLevels, MarketPriceLevel{
					Price:      l.Price,
					Quantity:   l.Quantity,
					OrderCount: uint32(l.Count),
				})
			}
			for _, l := range view.BuyLevels {
				resp.BuyLevels = append(resp.BuyLevels, MarketPriceLevel{
					Price:      l.Price,
					Quantity:   l.Quantity,
					OrderCount: uint32(l.Count),
				})
			}
			return resp, nil
		})

	mmokit.RegisterOp[MarketCreateOrderRequest, MarketOrderResultResponse](mmokit.RoutePlayerCell,
		func(ctx *mmokit.OpContext, req *MarketCreateOrderRequest) (*MarketOrderResultResponse, error) {
			var (
				result *mmokit.PlaceResult
				err    error
			)
			if req.IsBuy {
				result, err = svc.PlaceBuyOrder(ctx.Username, stationID, req.ItemID, req.PricePerUnit, req.Quantity)
			} else {
				result, err = svc.PlaceSellOrder(ctx.Username, stationID, req.ItemID, req.PricePerUnit, req.Quantity)
			}
			if err != nil {
				return nil, err
			}
			return &MarketOrderResultResponse{
				OrderID:   result.OrderID,
				FilledQty: result.FilledQty,
				AvgPrice:  result.AvgPrice,
				TotalCost: result.TotalCost,
			}, nil
		})

	mmokit.RegisterOp[MarketCancelOrderRequest, MarketOrderResultResponse](mmokit.RoutePlayerCell,
		func(ctx *mmokit.OpContext, req *MarketCancelOrderRequest) (*MarketOrderResultResponse, error) {
			if err := svc.CancelOrder(ctx.Username, req.OrderID); err != nil {
				return nil, err
			}
			return &MarketOrderResultResponse{OrderID: req.OrderID}, nil
		})

	mmokit.RegisterOp[MarketMyOrdersRequest, MarketMyOrdersResponse](mmokit.RoutePlayerCell,
		func(ctx *mmokit.OpContext, _ *MarketMyOrdersRequest) (*MarketMyOrdersResponse, error) {
			orders := svc.PlayerOrders(ctx.Username)
			resp := &MarketMyOrdersResponse{}
			for _, o := range orders {
				resp.Orders = append(resp.Orders, MarketOrderEntry{
					OrderID:      o.ID,
					ItemID:       o.ItemID,
					IsBuy:        o.Side == mmokit.SideBuy,
					PricePerUnit: o.Price,
					Quantity:     o.Quantity,
					OrigQuantity: o.OrigQty,
					CreatedAt:    o.CreatedAt,
					ExpiresAt:    o.ExpiresAt,
				})
			}
			return resp, nil
		})

	mmokit.RegisterOp[MarketInstantTradeRequest, MarketOrderResultResponse](mmokit.RoutePlayerCell,
		func(ctx *mmokit.OpContext, req *MarketInstantTradeRequest) (*MarketOrderResultResponse, error) {
			var (
				result *mmokit.PlaceResult
				err    error
			)
			if req.IsBuy {
				result, err = svc.InstantBuy(ctx.Username, stationID, req.ItemID, req.Quantity)
			} else {
				result, err = svc.InstantSell(ctx.Username, stationID, req.ItemID, req.Quantity)
			}
			if err != nil {
				return nil, err
			}
			return &MarketOrderResultResponse{
				FilledQty: result.FilledQty,
				AvgPrice:  result.AvgPrice,
				TotalCost: result.TotalCost,
			}, nil
		})
}
