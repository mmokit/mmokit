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
//
// The engine source uses the default schema_migrations table so existing
// dev databases keep working without a wipe; each extra source gets its
// own schema_migrations_extra_N table so version numbers don't have to
// coordinate across sources.
func runMigrations(url string, extras []extraSource) error {
	if err := applyMigrationSource(url, "", migrationFS, "migrations"); err != nil {
		return err
	}
	for i, ex := range extras {
		label := fmt.Sprintf("extra_%d", i)
		if err := applyMigrationSource(url, label, ex.fs, ex.root); err != nil {
			return err
		}
	}
	return nil
}

// applyMigrationSource opens a short-lived sql.DB and applies all
// migrations from sourceFS rooted at sourceRoot. label appears in error
// messages and selects the schema_migrations table name. Empty label =
// engine migrations using the default "schema_migrations" table; non-
// empty label uses "schema_migrations_<label>" so extra sources don't
// share version sequence with the engine or each other.
func applyMigrationSource(url, label string, sourceFS fs.FS, sourceRoot string) error {
	logLabel := label
	if logLabel == "" {
		logLabel = "engine"
	}
	src, err := iofs.New(sourceFS, sourceRoot)
	if err != nil {
		return fmt.Errorf("postgres migrate %s: iofs source: %w", logLabel, err)
	}

	// pgx/v5 registers itself as "pgx/v5" in database/sql (see the
	// blank import above). Use that to get a standalone sql.DB
	// independent of the runtime pgxpool.
	sqlDB, err := sql.Open("pgx/v5", url)
	if err != nil {
		return fmt.Errorf("postgres migrate %s: sql.Open: %w", logLabel, err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	driverCfg := &pgxdriver.Config{}
	if label != "" {
		driverCfg.MigrationsTable = sanitizeMigrationsTableName("schema_migrations_" + label)
	}

	db, err := pgxdriver.WithInstance(sqlDB, driverCfg)
	if err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("postgres migrate %s: pgx driver: %w", logLabel, err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx5", db)
	if err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("postgres migrate %s: new instance: %w", logLabel, err)
	}

	upErr := m.Up()
	// migrate.Close() releases both the source and the database driver,
	// which in turn closes the sqlDB. Explicit cleanup before we return.
	sourceErr, databaseErr := m.Close()
	if upErr != nil && !errors.Is(upErr, migrate.ErrNoChange) {
		return fmt.Errorf("postgres migrate %s: up: %w", logLabel, upErr)
	}
	if sourceErr != nil {
		return fmt.Errorf("postgres migrate %s: source close: %w", logLabel, sourceErr)
	}
	if databaseErr != nil {
		return fmt.Errorf("postgres migrate %s: database close: %w", logLabel, databaseErr)
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
