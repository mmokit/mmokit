package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/persist"
)

type configRepo struct {
	pool *pgxpool.Pool
}

var _ persist.ConfigRepository = (*configRepo)(nil)

// Load returns the singleton config row, or ErrNotFound on first run.
func (r *configRepo) Load(ctx context.Context) (*persist.ConfigSnapshot, error) {
	var snap persist.ConfigSnapshot
	err := r.pool.QueryRow(ctx,
		`SELECT data, version FROM game_config WHERE id = 1`,
	).Scan(&snap.Data, &snap.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, persist.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("configRepo.Load: %w", err)
	}
	return &snap, nil
}

// Save upserts the singleton config row.
func (r *configRepo) Save(ctx context.Context, snapshot *persist.ConfigSnapshot) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO game_config (id, data, version, updated_at)
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
