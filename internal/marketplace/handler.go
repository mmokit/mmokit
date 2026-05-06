package marketplace

import (
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// RegisterHandlers registers all marketplace operation handlers with the router.
func RegisterHandlers(router *mmokit.OpRouter, svc *Settlement, stationID uint32) {
	mmokit.RegisterProtoOp(
		router, uint32(gamepb.OperationCode_OP_MARKET_BROWSE), "marketBrowse",
		func(ctx *mmokit.OpContext, req *gamepb.MarketBrowseRequest) (*gamepb.MarketOrderBookResponse, error) {
			view := svc.Browse(stationID, req.ItemId)
			resp := &gamepb.MarketOrderBookResponse{
				ItemId: view.ItemID,
			}
			for _, l := range view.SellLevels {
				resp.SellLevels = append(resp.SellLevels, &gamepb.MarketPriceLevel{
					Price:      l.Price,
					Quantity:   l.Quantity,
					OrderCount: uint32(l.Count),
				})
			}
			for _, l := range view.BuyLevels {
				resp.BuyLevels = append(resp.BuyLevels, &gamepb.MarketPriceLevel{
					Price:      l.Price,
					Quantity:   l.Quantity,
					OrderCount: uint32(l.Count),
				})
			}
			return resp, nil
		})

	mmokit.RegisterProtoOp(
		router, uint32(gamepb.OperationCode_OP_MARKET_CREATE_ORDER), "marketCreateOrder",
		func(ctx *mmokit.OpContext, req *gamepb.MarketCreateOrderRequest) (*gamepb.MarketOrderResultResponse, error) {
			var result *mmokit.PlaceResult
			var err error
			if req.IsBuy {
				result, err = svc.PlaceBuyOrder(ctx.Username, stationID, req.ItemId, req.PricePerUnit, req.Quantity)
			} else {
				result, err = svc.PlaceSellOrder(ctx.Username, stationID, req.ItemId, req.PricePerUnit, req.Quantity)
			}
			if err != nil {
				return nil, err
			}
			return &gamepb.MarketOrderResultResponse{
				OrderId:   result.OrderID,
				FilledQty: result.FilledQty,
				AvgPrice:  result.AvgPrice,
				TotalCost: result.TotalCost,
			}, nil
		})

	mmokit.RegisterProtoOp(
		router, uint32(gamepb.OperationCode_OP_MARKET_CANCEL_ORDER), "marketCancelOrder",
		func(ctx *mmokit.OpContext, req *gamepb.MarketCancelOrderRequest) (*gamepb.MarketOrderResultResponse, error) {
			if err := svc.CancelOrder(ctx.Username, req.OrderId); err != nil {
				return nil, err
			}
			return &gamepb.MarketOrderResultResponse{OrderId: req.OrderId}, nil
		})

	mmokit.RegisterProtoOp(
		router, uint32(gamepb.OperationCode_OP_MARKET_MY_ORDERS), "marketMyOrders",
		func(ctx *mmokit.OpContext, _ *gamepb.MarketMyOrdersRequest) (*gamepb.MarketMyOrdersResponse, error) {
			orders := svc.PlayerOrders(ctx.Username)
			resp := &gamepb.MarketMyOrdersResponse{}
			for _, o := range orders {
				resp.Orders = append(resp.Orders, &gamepb.MarketOrderEntry{
					OrderId:      o.ID,
					ItemId:       o.ItemID,
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

	mmokit.RegisterProtoOp(
		router, uint32(gamepb.OperationCode_OP_MARKET_INSTANT_TRADE), "marketInstantTrade",
		func(ctx *mmokit.OpContext, req *gamepb.MarketInstantTradeRequest) (*gamepb.MarketOrderResultResponse, error) {
			var result *mmokit.PlaceResult
			var err error
			if req.IsBuy {
				result, err = svc.InstantBuy(ctx.Username, stationID, req.ItemId, req.Quantity)
			} else {
				result, err = svc.InstantSell(ctx.Username, stationID, req.ItemId, req.Quantity)
			}
			if err != nil {
				return nil, err
			}
			return &gamepb.MarketOrderResultResponse{
				FilledQty: result.FilledQty,
				AvgPrice:  result.AvgPrice,
				TotalCost: result.TotalCost,
			}, nil
		})
}
