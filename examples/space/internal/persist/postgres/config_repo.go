package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	gamepersist "github.com/mmokit/mmokit/examples/space/internal/persist"
)

type configRepo struct {
	pool *pgxpool.Pool
}

var _ gamepersist.ConfigRepository = (*configRepo)(nil)

// Load returns the singleton config row, or ErrNotFound on first run.
func (r *configRepo) Load(ctx context.Context) (*gamepersist.ConfigSnapshot, error) {
	var snap gamepersist.ConfigSnapshot
	err := r.pool.QueryRow(ctx,
		`SELECT data, version FROM space.config WHERE id = 1`,
	).Scan(&snap.Data, &snap.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, gamepersist.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("configRepo.Load: %w", err)
	}
	return &snap, nil
}

// Save upserts the singleton config row.
func (r *configRepo) Save(ctx context.Context, snapshot *gamepersist.ConfigSnapshot) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO space.config (id, data, version, updated_at)
		VALUES (1, $1, $2, NOW())
		ON CONFLICT (id) DO UPDATE SET
			data       = EXCLUDED.data,
			version    = EXCLUDED.version,
			updated_at = NOW()
	`,
		snapshot.Data, snapshot.Version,
	)
	if err != nil {
		return fmt.Errorf("configRepo.Save: %w", err)
	}
	return nil
}
