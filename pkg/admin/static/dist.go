// Package static embeds the built admin SPA. The actual JS/CSS bundles are
// produced by `bun run build` in web-admin/ and copied here by `just admin-build`.
// v1 ships with a placeholder index.html so the backend can boot before the
// frontend lands.
package static

import "embed"

//go:embed dist
var FS embed.FS
