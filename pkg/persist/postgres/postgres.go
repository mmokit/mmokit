// Package postgres is the PostgreSQL implementation of the persist
// repository interfaces. Open returns a *Store that exposes the
// repos via Players(), Market(), Config(), AdminOperators().
package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"runtime"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmokit/mmokit/pkg/persist"
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

// Option customizes Open behavior.
type Option func(*options)

type options struct {
	extraMigrations []extraSource
}

type extraSource struct {
	fs    fs.FS
	root  string
	label string
}

// WithExtraMigrations adds a game- or example-specific migration source
// that is applied AFTER the engine's built-in migrations. The fs must
// contain golang-migrate-style files (NNN_name.up.sql / NNN_name.down.sql)
// at root. label scopes the schema_migrations table for this source
// (becomes "schema_migrations_<label>"); pick something stable and unique
// per source so versioning is independent across sources and across the
// engine's built-in schema. Multiple calls are applied in registration
// order.
func WithExtraMigrations(migrationFS fs.FS, root, label string) Option {
	return func(o *options) {
		o.extraMigrations = append(o.extraMigrations, extraSource{fs: migrationFS, root: root, label: label})
	}
}

// Open creates a connection pool, pings the server, runs any pending
// schema migrations (engine + any WithExtraMigrations sources, in order),
// and returns a ready-to-use Store. The caller must call Close when finished.
func Open(ctx context.Context, url string, opts ...Option) (*Store, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

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

	if err := runMigrations(url, o.extraMigrations); err != nil {
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

// Pool returns the underlying pgx connection pool. Reserved for callers
// (notably service framework consumers) that need to issue ad-hoc queries
// against tables outside the typed repository interfaces.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Players returns the PlayerRepository implementation.
func (s *Store) Players() persist.PlayerRepository { return &playerRepo{pool: s.pool} }

// AdminOperators returns the AdminOperatorRepository implementation.
func (s *Store) AdminOperators() persist.AdminOperatorRepository {
	return &adminOperatorRepo{pool: s.pool}
}
