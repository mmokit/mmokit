// Package postgres is the PostgreSQL implementation of the persist
// repository interfaces. Open returns a *Store that exposes the three
// repos via Players(), Market(), Config().
package postgres

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/persist"
)

const (
	poolMaxConnsCap     = 32
	poolMaxConnsPerCPU  = 4
	poolMinConns        = 4
	poolMaxConnLifetime = 30 * time.Minute
	poolMaxConnIdleTime = 5 * time.Minute
	poolHealthCheckIntv = 1 * time.Minute
)

// Store is the PostgreSQL-backed persistence root. Holds a single
// pgxpool.Pool shared by all three repository implementations.
type Store struct {
	pool *pgxpool.Pool
}

// Open creates a connection pool, pings the server, runs any pending
// schema migrations, and returns a ready-to-use Store. The caller
// must call Close when finished.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse url: %w", err)
	}

	maxConns := max(min(runtime.NumCPU()*poolMaxConnsPerCPU, poolMaxConnsCap), poolMinConns)
	cfg.MaxConns = int32(maxConns)
	cfg.MinConns = poolMinConns
	cfg.MaxConnLifetime = poolMaxConnLifetime
	cfg.MaxConnIdleTime = poolMaxConnIdleTime
	cfg.HealthCheckPeriod = poolHealthCheckIntv

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: new pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}

	if err := runMigrations(url); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: %w", err)
	}

	return &Store{pool: pool}, nil
}

// Close releases the connection pool. Safe to call multiple times
// (pgxpool.Pool.Close is idempotent).
func (s *Store) Close() {
	s.pool.Close()
}

// Players returns the PlayerRepository implementation.
func (s *Store) Players() persist.PlayerRepository { return &playerRepo{pool: s.pool} }

// Market returns the MarketRepository implementation.
func (s *Store) Market() persist.MarketRepository { return &marketRepo{pool: s.pool} }

// Config returns the ConfigRepository implementation.
func (s *Store) Config() persist.ConfigRepository { return &configRepo{pool: s.pool} }
