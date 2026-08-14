// Package static embeds the built admin SPA — index.html plus the hashed
// JS/CSS bundles under dist/assets, produced by `bun run build` in web-admin/
// and written here directly by `just admin-build`.
//
// The whole directory is committed, not just index.html. This is generated
// output in the tree, which the repository otherwise avoids, and it is
// deliberate: go:embed resolves at compile time against the checkout, so a
// consumer who runs `go get` and mounts pkg/admin serves exactly these bytes.
// Committing only index.html shipped a page whose <script src> pointed at a
// hashed file that was gitignored and therefore did not exist for anyone but
// this repository's developer.
//
// Regenerate with `just admin-build`; CI asserts the committed bundle matches
// a fresh build.
package static

import "embed"

//go:embed dist
var FS embed.FS
