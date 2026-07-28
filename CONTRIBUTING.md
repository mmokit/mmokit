# Contributing

## Read AGENTS.md first

[`AGENTS.md`](AGENTS.md) is the authoritative repository guidance — architectural
boundaries, ECS and concurrency rules, wire and distributed invariants, generated
file ownership, and build rules. It applies to human contributors and coding
agents equally.

This file deliberately does **not** restate it. The repository's maintenance rule
is that every fact has exactly one owning document and others link to it; a
second copy of the invariants would drift, and the drifted copy would be the one
someone read. If something here appears to contradict `AGENTS.md`, `AGENTS.md`
wins and this file is the bug.

Two further pointers rather than summaries:

- [`docs/architecture.md`](docs/architecture.md) — what the system *is*.
- [`docs/roadmap.md`](docs/roadmap.md) — what it *will be*, plus all active
  tracking. Never state direction in `architecture.md`.

Treat `docs/superpowers/{plans,specs}/` as dated design history. It is not proof
of current behaviour, and several documents there describe removed APIs. Verify
identifiers against source with `rg` before writing code.

## Which checks to run

Run the smallest relevant checks first, then broaden in proportion to the
change. This table is transcribed from `AGENTS.md`, which owns it.

| Changed area | Minimum relevant checks |
| --- | --- |
| Go package | Targeted `go test ./path -run TestName`, then `go vet ./...` |
| Broad Go/runtime behavior | `go test ./... -count=1 -timeout 300s` in an environment that permits localhost TCP/UDP |
| `internal/game` | Go checks plus `just lint-no-ark` |
| Go compile only | `just build-go` |
| `web-admin` | `just admin-typecheck`, `just admin-test`, and `just admin-build` when the embedded bundle must change |
| `web-pixi` | `cd web-pixi && bun run typecheck && bun test && bun run build` |
| 4node web | `cd examples/4node-basic/web && bun run typecheck && bun test && bun run build` |
| Shared TypeScript codec/interpolation | `just ts-core-test` |
| Protobuf | `just proto`, inspect generated diff, then affected Go checks |
| Entity/event/input/op schema | Regenerate each affected SDK, inspect the diff, then run the corresponding frontend checks |
| Persistence, migrations, or DB services | Standard Go checks plus `just db-up && just test-pg`; the pgtest packages intentionally run serially and mutate a shared test DB |
| C# core/generator | `just csharp-test`; generator changes also run `just csharp-compile-test`; regenerate goldens when wire bytes intentionally change |
| Markdown | Check commands/links and run `markdownlint-cli2 <changed files>` when available |

Report any check you did not run and the prerequisite that was missing —
PostgreSQL, Docker, Bun, .NET, Buf, or network access. Do not claim a full suite
passed on the strength of a targeted test.

**`go build ./...` is forbidden.** It writes binaries into package directories.
`go vet ./...` compiles everything and is the compile check; `just build-go` is
the DB-free server build.

## Test-run conventions

### `-p 1` is retained insurance

Run the full suite with `-p 1`:

```bash
go vet ./...
go test ./... -count=1 -p 1 -timeout 900s
./scripts/no_ark_in_game.sh
```

`-p 1` disables parallel *package* execution. The reason is `pkg/universe`,
which has intermittently reported `executor: serialize timeout on cell_0_0`
(`cell_transfer_executor.go`) under parallel package execution. That is CPU
contention against a `RunOnLoop` deadline, not a logic race — several packages
binding localhost listeners and driving 20 Hz cell loops at once starve each
other.

**The measurement says it is currently unnecessary, and we keep it anyway.** A
previously recorded "roughly 1 run in 4 with default `-p`" failure rate did not
reproduce at commit `17a9a2ce`: one default-`-p` full run passed (48 s), as did
two `-p 1` runs, and five consecutive `-p 1` full runs plus two full `-race`
runs passed after the `pkg/admin` race fix. One passing sample cannot refute a
1-in-4 rate, so `-p 1` stays as insurance rather than as a proven necessity.
Under `-race` contention is strictly worse — roughly 5 minutes versus 50 seconds
— so a race job should carry `-p 1` too.

If you drop `-p 1` and see a serialize timeout, that is this, not your change.

### Two standing blind spots in `go test ./...`

Green is not the same as complete:

- **Zero PostgreSQL code is covered.** All four `*/postgres` packages sit behind
  a `//go:build pgtest` tag and report `[no test files]`. Their tests are not
  skipped — they are invisible. Only `just db-up && just test-pg` runs them.
- **Localhost socket tests need a network-enabled environment.** Several
  packages bind localhost TCP and UDP listeners. In a sandbox that forbids
  listeners they fail for reasons unrelated to the change.

## Formatting

**Format only the files you touched**, with `gofmt -w`:

```bash
gofmt -w $(git diff --name-only --diff-filter=ACM HEAD -- '*.go')
```

Do not run `gofmt` over the tree, and do not gate on it tree-wide.
`gofmt -l $(git ls-files '*.go')` reports **69 files** at this commit, almost
entirely pre-existing Go 1.19+ doc-comment reflow. `AGENTS.md` forbids clearing
that as incidental cleanup, so a tree-wide format gate would be red on its first
run and would bury every real finding under a thousand lines of unrelated
reflow. Gate changed files only.

The same rule covers the rest of the tree: no repository-wide reformatting or
regeneration as incidental cleanup, and preserve unrelated worktree changes.

`go.mod` pins `toolchain go1.26.2` alongside the `go 1.25.1` language directive,
so `gofmt` and `vet` results are reproducible rather than a moving target. If
you configure a Go version anywhere else, read it from `go.mod` rather than
hard-coding a string, so there is exactly one place to bump.

## Generated files

Do not hand-edit generated output — protobuf Go, generated SDKs, the admin
bundle, or wire goldens. Change the source and run the relevant `just` recipe.
`AGENTS.md` lists each source → recipe → output triple. Keep a generated diff in
a commit only when the corresponding source actually changed.

## Commits

Conventional Commit subjects (`fix(universe): …`, `docs: …`, `build(dev): …`).
Explain **why** in the body; the diff already shows what. When a change is
motivated by a measurement, put the measurement in the body — a number in a
commit message is the only place it survives.

## Scope of this repository

The published framework is MMOKIT core. The reference space game under
`internal/`, `cmd/server`, `cmd/botclient` and `web-pixi/` is not part of the
distributed framework — see the License section of [`README.md`](README.md).

That boundary has a practical consequence for contributions: `pkg/` must never
import `internal/`, and it currently does not. Several validation recipes are
game-coupled and cannot run against the framework alone — `just lint-no-ark`
targets `internal/game/`, `just shipdyn-golden` and the `just web-test`
prediction goldens span `internal/game/` **and** `web-pixi/`, and
`just space-sdk` runs `cmd/server` and requires PostgreSQL.

## Security

Do not report vulnerabilities through issues or pull requests. See
[`SECURITY.md`](SECURITY.md), which also lists the known, tracked, unfixed
limitations — check it before reporting, since the mesh and UDP transport
weaknesses are already recorded there.
