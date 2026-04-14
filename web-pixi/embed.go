// Package webpixi exposes the built Vite output for the space game's web
// client as an embed.FS so cmd/server can serve it via the engine's HTTP
// listener on gateway-role processes. The root justfile's build recipe
// runs `bun run build` in this directory before `go build` so dist/ is
// populated at embed time.
package webpixi

import "embed"

//go:embed all:dist
var FS embed.FS
