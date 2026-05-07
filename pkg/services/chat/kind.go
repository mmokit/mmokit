package chat

import (
	"embed"
	"io/fs"
)

const KindName = "chat"

//go:embed postgres/migrations/*.sql
var pgMigrations embed.FS

// MigrationsFS returns the chat-package's Postgres migrations.
func MigrationsFS() fs.FS {
	sub, err := fs.Sub(pgMigrations, "postgres/migrations")
	if err != nil {
		panic(err)
	}
	return sub
}
