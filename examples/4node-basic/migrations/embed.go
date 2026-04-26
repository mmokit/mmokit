// Package migrations holds 4node-basic's example-specific Postgres
// migrations, embedded into the binary at build time. Wired into the
// engine via Config.ExtraMigrations / postgres.WithExtraMigrations.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
