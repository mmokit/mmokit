package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/persist"
)

type marketRepo struct {
	pool *pgxpool.Pool
}

var _ persist.MarketRepository = (*marketRepo)(nil)

// PlaceOrder inserts a new resting order. Returns the BIGSERIAL-
// assigned ID via RETURNING.
func (r *marketRepo) PlaceOrder(ctx context.Context, o *persist.OrderRecord) (uint64, error) {
	var id uint64
	var expiresAt any // *time.Time when set, nil otherwise
	if !o.ExpiresAt.IsZero() {
		expiresAt = o.ExpiresAt
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO market_orders (
			side, owner, location_id, item_id, price, quantity, orig_qty,
			created_at, expires_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id
	`,
		o.Side, o.Owner, o.LocationID, o.ItemID, o.Price,
		o.Quantity, o.OrigQty, o.CreatedAt, expiresAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("marketRepo.PlaceOrder: %w", err)
	}
	return id, nil
}

// UpdateQuantity decrements the remaining quantity on a partial fill.
func (r *marketRepo) UpdateQuantity(ctx context.Context, id uint64, newQty int32) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE market_orders SET quantity = $1 WHERE id = $2`,
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
		`DELETE FROM market_orders WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("marketRepo.DeleteOrder %d: %w", id, err)
	}
	return nil
}

// RecordTrade appends to the audit log.
func (r *marketRepo) RecordTrade(ctx context.Context, t *persist.TradeRecord) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO market_trades (
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
func (r *marketRepo) LoadActiveOrders(ctx context.Context, fn func(*persist.OrderRecord) error) error {
	rows, err := r.pool.Query(ctx, `
		SELECT id, side, owner, location_id, item_id, price, quantity, orig_qty,
		       created_at, expires_at
		FROM market_orders
		WHERE expires_at IS NULL OR expires_at > NOW()
		ORDER BY id
	`)
	if err != nil {
		return fmt.Errorf("marketRepo.LoadActiveOrders query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var rec persist.OrderRecord
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
