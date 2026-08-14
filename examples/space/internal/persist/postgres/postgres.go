// Package postgres is the space-game PostgreSQL persistence layer.
// Store builds repository handles from an already-open pgxpool; the
// pool itself is created and migrated by pkg/persist/postgres.Open.
package postgres

import (
	"embed"

	"github.com/jackc/pgx/v5/pgxpool"

	gamepersist "github.com/mmokit/mmokit/examples/space/internal/persist"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// MigrationsFS exposes the embedded game-side migration filesystem.
// The space-game wiring (cmd/server/main.go) passes this to
// mmokit.WithExtraMigrations so the engine's Open runs the game's
// migrations after its own.
func MigrationsFS() embed.FS { return migrationFS }

// MigrationsLabel is the schema_migrations_<label> tracking-table
// suffix for this source. Stable per source.
const MigrationsLabel = "space"

// MigrationsRoot is the embed.FS subdirectory holding the .sql files.
const MigrationsRoot = "migrations"

// Store is the space-game PostgreSQL persistence root. Constructed by
// New(pool) from the same pgxpool the engine Store owns; both share
// connections.
type Store struct {
	pool *pgxpool.Pool
}

// New builds a Store from an open pgxpool. The pool is owned by the
// caller (typically pkg/persist/postgres.Store) — this Store does NOT
// close it.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// PlayerState returns the PlayerStateRepository implementation.
func (s *Store) PlayerState() gamepersist.PlayerStateRepository {
	return &playerStateRepo{pool: s.pool}
}

// Market returns the MarketRepository implementation.
func (s *Store) Market() gamepersist.MarketRepository {
	return &marketRepo{pool: s.pool}
}

// Config returns the ConfigRepository implementation.
func (s *Store) Config() gamepersist.ConfigRepository {
	return &configRepo{pool: s.pool}
}
