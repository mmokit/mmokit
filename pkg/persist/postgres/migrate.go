package postgres

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	pgxdriver "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx/v5" driver with database/sql
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// runMigrations applies every embedded .up.sql in order. Idempotent.
//
// golang-migrate's pgx/v5 driver requires a *sql.DB rather than a
// *pgxpool.Pool. Rather than bridging the runtime pool via
// stdlib.OpenDBFromPool (which leaks a connection reference through
// the migration driver and causes pool.Close to hang), we open a
// standalone short-lived sql.DB for migrations and close it cleanly
// before returning. The runtime pgx pool is untouched.
func runMigrations(url string) error {
	src, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("postgres migrate: iofs source: %w", err)
	}

	// pgx/v5 registers itself as "pgx/v5" in database/sql (see the
	// blank import above). Use that to get a standalone sql.DB
	// independent of the runtime pgxpool.
	sqlDB, err := sql.Open("pgx/v5", url)
	if err != nil {
		return fmt.Errorf("postgres migrate: sql.Open: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	db, err := pgxdriver.WithInstance(sqlDB, &pgxdriver.Config{})
	if err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("postgres migrate: pgx driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx5", db)
	if err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("postgres migrate: new instance: %w", err)
	}

	upErr := m.Up()
	// migrate.Close() releases both the source and the database driver,
	// which in turn closes the sqlDB. Explicit cleanup before we return.
	sourceErr, databaseErr := m.Close()
	if upErr != nil && !errors.Is(upErr, migrate.ErrNoChange) {
		return fmt.Errorf("postgres migrate: up: %w", upErr)
	}
	if sourceErr != nil {
		return fmt.Errorf("postgres migrate: source close: %w", sourceErr)
	}
	if databaseErr != nil {
		return fmt.Errorf("postgres migrate: database close: %w", databaseErr)
	}
	return nil
}
