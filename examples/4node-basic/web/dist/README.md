# web/dist — Vite build output

This directory is the target for `bun run build` in `examples/4node-basic/web/`.
It's embedded into the `4node-basic` binary at compile time via `//go:embed all:web/dist`.

Only this placeholder README is committed. The actual built assets
(`assets/*.js`, `assets/*.css`, `index.html`) are produced by the Vite build
and gitignored — they change on every build.

If you see this file served by the running binary, the web build was never run.
From the repo root, run `just build` (or from this example dir, `bun run build` in web/).
