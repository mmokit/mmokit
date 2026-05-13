package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	gamepersist "github.com/zenion/mmoserver/internal/persist"
)

type marketRepo struct {
	pool *pgxpool.Pool
}

var _ gamepersist.MarketRepository = (*marketRepo)(nil)

// PlaceOrder inserts a new resting order. The caller MUST set
// OrderRecord.ID — the orderbook owns ID allocation, not the DB.
func (r *marketRepo) PlaceOrder(ctx context.Context, o *gamepersist.OrderRecord) error {
	var expiresAt any // *time.Time when set, nil otherwise
	if !o.ExpiresAt.IsZero() {
		expiresAt = o.ExpiresAt
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO space.market_orders (
			id, side, owner, location_id, item_id, price, quantity, orig_qty,
			created_at, expires_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		o.ID, o.Side, o.Owner, o.LocationID, o.ItemID, o.Price,
		o.Quantity, o.OrigQty, o.CreatedAt, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("marketRepo.PlaceOrder %d: %w", o.ID, err)
	}
	return nil
}

// LoadMaxOrderID returns the highest persisted market_orders.id, or
// 0 if the table is empty. Used by the orderbook at startup to seed
// its in-memory NextID counter.
func (r *marketRepo) LoadMaxOrderID(ctx context.Context) (uint64, error) {
	var maxID uint64
	err := r.pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM space.market_orders`).Scan(&maxID)
	if err != nil {
		return 0, fmt.Errorf("marketRepo.LoadMaxOrderID: %w", err)
	}
	return maxID, nil
}

// UpdateQuantity decrements the remaining quantity on a partial fill.
func (r *marketRepo) UpdateQuantity(ctx context.Context, id uint64, newQty int32) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE space.market_orders SET quantity = $1 WHERE id = $2`,
		newQty, id,
	)
	if err != nil {
		return fmt.Errorf("marketRepo.UpdateQuantity %d: %w", id, err)
	}
	return nil
}

// DeleteOrder removes a fully-filled, cancelled, or expired order.
// No error if the ID doesn't exist (the post-condition is "not in
// table"; the row already being absent satisfies that).
func (r *marketRepo) DeleteOrder(ctx context.Context, id uint64) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM space.market_orders WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("marketRepo.DeleteOrder %d: %w", id, err)
	}
	return nil
}

// RecordTrade appends to the audit log.
func (r *marketRepo) RecordTrade(ctx context.Context, t *gamepersist.TradeRecord) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO space.market_trades (
			item_id, location_id, price, quantity, buyer, seller, occurred_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`,
		t.ItemID, t.LocationID, t.Price, t.Quantity,
		t.Buyer, t.Seller, t.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("marketRepo.RecordTrade: %w", err)
	}
	return nil
}

// LoadActiveOrders streams every non-expired order in id-ascending
// order so the in-memory book sees orders in placement order.
func (r *marketRepo) LoadActiveOrders(ctx context.Context, fn func(*gamepersist.OrderRecord) error) error {
	rows, err := r.pool.Query(ctx, `
		SELECT id, side, owner, location_id, item_id, price, quantity, orig_qty,
		       created_at, expires_at
		FROM space.market_orders
		WHERE expires_at IS NULL OR expires_at > NOW()
		ORDER BY id
	`)
	if err != nil {
		return fmt.Errorf("marketRepo.LoadActiveOrders query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var rec gamepersist.OrderRecord
		var expiresAt *time.Time
		if err := rows.Scan(
			&rec.ID, &rec.Side, &rec.Owner, &rec.LocationID, &rec.ItemID,
			&rec.Price, &rec.Quantity, &rec.OrigQty, &rec.CreatedAt, &expiresAt,
		); err != nil {
			return fmt.Errorf("marketRepo.LoadActiveOrders scan: %w", err)
		}
		if expiresAt != nil {
			rec.ExpiresAt = *expiresAt
		}
		if err := fn(&rec); err != nil {
			return err
		}
	}
	return rows.Err()
}
