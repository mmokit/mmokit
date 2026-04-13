package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	pgxdriver "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// runMigrations applies every embedded .up.sql in order. Idempotent.
//
// golang-migrate's pgx/v5 driver requires a *sql.DB rather than a
// *pgxpool.Pool. We open a short-lived stdlib bridge from the pool via
// stdlib.OpenDBFromPool and close it as soon as the migration finishes —
// the runtime pool is unaffected.
func runMigrations(_ context.Context, pool *pgxpool.Pool) error {
	src, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("postgres migrate: iofs source: %w", err)
	}

	// Bridge pgx pool -> *sql.DB for golang-migrate.
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()

	db, err := pgxdriver.WithInstance(sqlDB, &pgxdriver.Config{})
	if err != nil {
		return fmt.Errorf("postgres migrate: pgx driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx5", db)
	if err != nil {
		return fmt.Errorf("postgres migrate: new instance: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("postgres migrate: up: %w", err)
	}
	return nil
}
