package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/persist"
)

type marketRepo struct {
	pool *pgxpool.Pool
}

var _ persist.MarketRepository = (*marketRepo)(nil)

func (r *marketRepo) PlaceOrder(ctx context.Context, o *persist.OrderRecord) (uint64, error) {
	return 0, errors.New("marketRepo.PlaceOrder: not implemented (T4)")
}

func (r *marketRepo) UpdateQuantity(ctx context.Context, id uint64, newQty int32) error {
	return errors.New("marketRepo.UpdateQuantity: not implemented (T4)")
}

func (r *marketRepo) DeleteOrder(ctx context.Context, id uint64) error {
	return errors.New("marketRepo.DeleteOrder: not implemented (T4)")
}

func (r *marketRepo) RecordTrade(ctx context.Context, t *persist.TradeRecord) error {
	return errors.New("marketRepo.RecordTrade: not implemented (T4)")
}

func (r *marketRepo) LoadActiveOrders(ctx context.Context, fn func(*persist.OrderRecord) error) error {
	return errors.New("marketRepo.LoadActiveOrders: not implemented (T4)")
}
