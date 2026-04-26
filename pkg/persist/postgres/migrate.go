package postgres

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	pgxdriver "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx/v5" driver with database/sql
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// runMigrations applies every embedded engine .up.sql in order, then
// each WithExtraMigrations source in registration order. Idempotent.
//
// golang-migrate's pgx/v5 driver requires a *sql.DB rather than a
// *pgxpool.Pool. Rather than bridging the runtime pool via
// stdlib.OpenDBFromPool (which leaks a connection reference through
// the migration driver and causes pool.Close to hang), we open a
// standalone short-lived sql.DB for migrations and close it cleanly
// before returning. The runtime pgx pool is untouched.
func runMigrations(url string, extras []extraSource) error {
	if err := applyMigrationSource(url, "engine", migrationFS, "migrations"); err != nil {
		return err
	}
	for i, ex := range extras {
		label := fmt.Sprintf("extra[%d]", i)
		if err := applyMigrationSource(url, label, ex.fs, ex.root); err != nil {
			return err
		}
	}
	return nil
}

// applyMigrationSource opens a short-lived sql.DB and applies all
// migrations from sourceFS rooted at sourceRoot. label appears in error
// messages so operators can tell engine vs game-specific failures apart.
//
// Each source has its own version table (postgres-migrate's default
// "schema_migrations" is rebound per source via Config.MigrationsTable)
// so engine migrations and game migrations don't collide on version
// numbers.
func applyMigrationSource(url, label string, sourceFS fs.FS, sourceRoot string) error {
	src, err := iofs.New(sourceFS, sourceRoot)
	if err != nil {
		return fmt.Errorf("postgres migrate %s: iofs source: %w", label, err)
	}

	// pgx/v5 registers itself as "pgx/v5" in database/sql (see the
	// blank import above). Use that to get a standalone sql.DB
	// independent of the runtime pgxpool.
	sqlDB, err := sql.Open("pgx/v5", url)
	if err != nil {
		return fmt.Errorf("postgres migrate %s: sql.Open: %w", label, err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	driverCfg := &pgxdriver.Config{
		MigrationsTable: "schema_migrations_" + label,
	}
	// Strip characters that would be invalid in a postgres identifier.
	driverCfg.MigrationsTable = sanitizeMigrationsTableName(driverCfg.MigrationsTable)

	db, err := pgxdriver.WithInstance(sqlDB, driverCfg)
	if err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("postgres migrate %s: pgx driver: %w", label, err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx5", db)
	if err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("postgres migrate %s: new instance: %w", label, err)
	}

	upErr := m.Up()
	// migrate.Close() releases both the source and the database driver,
	// which in turn closes the sqlDB. Explicit cleanup before we return.
	sourceErr, databaseErr := m.Close()
	if upErr != nil && !errors.Is(upErr, migrate.ErrNoChange) {
		return fmt.Errorf("postgres migrate %s: up: %w", label, upErr)
	}
	if sourceErr != nil {
		return fmt.Errorf("postgres migrate %s: source close: %w", label, sourceErr)
	}
	if databaseErr != nil {
		return fmt.Errorf("postgres migrate %s: database close: %w", label, databaseErr)
	}
	return nil
}

// sanitizeMigrationsTableName strips characters that aren't safe inside
// a Postgres identifier without quoting. Keeps lowercase letters, digits,
// and underscores; replaces anything else with '_'.
func sanitizeMigrationsTableName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= '0' && c <= '9',
			c == '_':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
