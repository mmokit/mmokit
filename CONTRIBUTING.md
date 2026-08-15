# Contributing

## How this project is governed

**One maintainer decides.** This is a benevolent-dictator project: Josh Stout
has final say on scope, design, and what gets merged. There is no committee, no
vote, and no RFC process. That is a deliberate choice for a framework whose
load-bearing design — authority epochs, phase-ordered execution, the wire
contract — degrades badly under design-by-consensus.

**Write access is by invitation.** The `main` branch is protected; nobody
pushes to it directly, including the maintainer. Collaborators are added when
they have already contributed something substantial and shown they understand
the invariants in [`AGENTS.md`](AGENTS.md). You do not apply; you get asked.

**Everyone else contributes by fork and pull request.** That path is fully open
and pull requests from outside are genuinely welcome — the access model is about
who can merge, not who can propose.

### What this means in practice

- **Open an issue before writing anything substantial.** A PR that arrives
  unannounced and rewrites a subsystem will usually be declined regardless of
  quality, because the maintainer has a sequenced plan in
  [`docs/roadmap.md`](docs/roadmap.md) and unsequenced work costs more to
  integrate than it saves. Small, obvious fixes need no issue.
- **Expect slow responses.** One person, spare time. A week of silence is
  normal and is not a signal about your work.
- **Expect "no" sometimes, without a long justification.** A declined change is
  not a judgement of you or your code. The most common reason is scope: the
  project has explicit non-goals, listed in the roadmap, and they are not
  re-litigated.
- **Decisions get recorded, not defended.** When direction changes, the change
  is written into `docs/roadmap.md` with the reasoning. If you want to know why
  something is the way it is, that file is the answer more often than not.

### Pre-1.0 means the maintainer will break things

There is no API or wire compatibility promise. If your PR's main argument is
"this would be a breaking change", that is not by itself an objection here —
see the Stability section of [`README.md`](README.md).

## Licensing of contributions

By submitting a pull request you agree that your contribution is licensed under
the [MIT License](LICENSE), the same terms as the rest of the project, and that
you have the right to license it that way. There is no CLA and no copyright
assignment — you keep your copyright.

If your employer owns your work, get their sign-off before submitting.

## Submitting a change

```bash
# 1. Fork on GitHub, then:
git clone https://github.com/<you>/mmokit && cd mmokit
git remote add upstream https://github.com/mmokit/mmokit

# 2. Branch. Never work on main — it is protected upstream and you will
#    want to rebase cleanly.
git checkout -b fix/short-description

# 3. Make the change, then run the checks proportionate to it (see below).
go vet ./... && go test ./... -short -count=1 -p 1
gofmt -w $(git ls-files '*.go')

# 4. Push to your fork and open a PR against mmokit/mmokit main.
git push -u origin fix/short-description
```

What a reviewable PR looks like here:

- **One concern per PR.** A formatting sweep bundled with a behaviour change
  gets asked to split, because the two need different scrutiny.
- **The commit body explains *why*.** The diff already shows what. If a change
  is motivated by a measurement, put the number in the body — a commit message
  is the only place it survives.
- **A behaviour change ships with the smallest test that fails before it.**
  Not full coverage; one test that would have caught the bug.
- **Green CI.** It runs `go vet`, tree-wide `gofmt`, the ark-boundary gate,
  `go test -short`, every example's build, both TypeScript suites, the C# SDK
  tests, and the cross-language goldens. Fix a red before asking for review.
- **Say what you did not run.** PostgreSQL, .NET, Docker and Bun are not
  available to everyone; an honest "did not run `just test-pg`" is fine, a
  claim that the full suite passed when it did not is not.

Things that get declined quickly, so you do not waste the effort: dependency
additions without a stated reason `pkg/` cannot do it itself; re-exports,
aliases or compatibility shims (the project deletes and updates callers
instead); repository-wide reformatting or regeneration as incidental cleanup;
and anything landing in `pkg/` that only one example needs.

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

## Repository layout

| Path | Purpose |
| --- | --- |
| [`pkg/mmokit`](pkg/mmokit/) | Public, single-import game-facing facade |
| [`pkg/engine`](pkg/engine/) | ECS loop, systems, player lifecycle, loop jobs, console foundations |
| [`pkg/universe`](pkg/universe/) | Processes, cells, topology, mesh control/data, handoffs, integrity checks |
| [`pkg/system`](pkg/system/) | Generic physics, spatial, lifetime, movement, replication systems |
| [`pkg/net`](pkg/net/) | WebSocket and UDP transports plus connection management |
| [`pkg/cmdsys`](pkg/cmdsys/) | Typed and routable operator commands |
| [`pkg/service`](pkg/service/), [`pkg/services`](pkg/services/) | Service framework and built-in services |
| [`examples/simple`](examples/simple/) | Smallest runnable game |
| [`examples/4node-basic`](examples/4node-basic/) | Distributed roles, services, WASM systems, generated SDK |
| [`examples/space`](examples/space/) | Reference space game: composition root, game layer, PixiJS client, world manifest |
| [`cmd/sdkgen`](cmd/sdkgen/) | TypeScript and C# client SDK generator |
| [`cmd/csharp-golden`](cmd/csharp-golden/) | Regenerates the C# wire golden from Go |
| [`web-admin`](web-admin/) | Svelte 5 operator dashboard, embedded into `pkg/admin` |
| [`csharp`](csharp/) | Shared C# SDK runtime and golden tests |
| [`proto/meshpb`](proto/meshpb/) | Server-internal protobuf schema |
| [`scripts`](scripts/) | Architectural-invariant checks run by CI |
| [`db-init`](db-init/) | Per-example PostgreSQL database creation |
| [`diagnostics`](diagnostics/) | Delivery-timing probe for a running gateway |

## Recipes

Recipes split by scope: the repository root owns framework-wide tooling, and each
example owns its own build and run.

| Root command | Result |
| --- | --- |
| `just db-up` | Start PostgreSQL via Docker Compose |
| `just proto` | Regenerate `gen/go/meshpb` from the internal mesh schema |
| `just client-sdk examples/space` | Regenerate any example's typed TypeScript SDK |
| `just admin-typecheck` / `just admin-test` / `just admin-build` | Check and build the operator dashboard |
| `just csharp-test` | Run the shared C# SDK tests |
| `just ts-core-test` | Run the shared TypeScript codec/interpolation tests |
| `just web-test` | Run every example web client's prediction/interpolation suites |
| `just lint-no-ark` | Enforce the game/framework ECS boundary |
| `just fuzz` | Mutate-fuzz every decoder family (smoke budget) |
| `go vet ./...` | Compile check and lint. `go build ./...` is forbidden — it writes binaries into package directories |
| `go test ./... -short` | Run Go tests; localhost socket tests need a network-enabled environment |

| Example command (from `examples/<name>`) | Result |
| --- | --- |
| `just build` | SDK, web client, admin bundle, and server binary |
| `just run` | Build and run |
| `just dev` | Build and run with a Vite dev server |
| `just distributed` | Multi-process cluster in tmux (`space`, `4node-basic`) |

`just db-reset` removes the PostgreSQL volume and is destructive. Neither build
regenerates protobuf or WASM modules; run `just proto` or `just wasm-build` when
those sources change.

## Client protocol and SDKs

Go registrations are the client schema source of truth:

- `mmokit.RegisterKind` declares entity component bundles.
- `mmokit.RegisterEvent` declares server-to-client typed events.
- `mmokit.HandleClient` declares client-to-server typed input.
- `mmokit.RegisterOp` declares typed request/response operations.

The SDK generator assembles those registries after the process builds. Wire type
IDs are `fnv32a(reflect.Type.String())`, which qualifies by package *name* rather
than import path, so renaming a registered type or its package is a
protocol-breaking change. Relocating a package between directories is not.

## Which checks to run

Run the smallest relevant checks first, then broaden in proportion to the
change. This table is transcribed from `AGENTS.md`, which owns it.

| Changed area | Minimum relevant checks |
| --- | --- |
| Go package | Targeted `go test ./path -run TestName`, then `go vet ./...` |
| Broad Go/runtime behavior | `go test ./... -count=1 -timeout 300s` in an environment that permits localhost TCP/UDP |
| An example's game layer | Go checks plus that example's `just lint-no-ark` |
| Go compile only | `go vet ./...`, or an example's `just build-go` |
| `web-admin` | `just admin-typecheck`, `just admin-test`, and `just admin-build` when the embedded bundle must change |
| An example web client | `cd examples/<name>/web && bun run typecheck && bun test && bun run build` |
| Shared TypeScript codec/interpolation | `just ts-core-test` |
| Protobuf | `just proto`, inspect generated diff, then affected Go checks |
| Entity/event/input/op schema | Regenerate each affected SDK, inspect the diff, then run the corresponding frontend checks |
| Persistence, migrations, or DB services | Standard Go checks plus `just db-up && just test-pg` (root for engine and services, the example's own for game persistence); the pgtest packages run serially and TRUNCATE shared tables |
| C# core/generator | `just csharp-test`; generator changes also run `just csharp-compile-test`; regenerate goldens when wire bytes intentionally change |
| Markdown | Check commands/links and run `markdownlint-cli2 <changed files>` when available |

Report any check you did not run and the prerequisite that was missing —
PostgreSQL, Docker, Bun, .NET, Buf, or network access. Do not claim a full suite
passed on the strength of a targeted test.

**`go build ./...` is forbidden.** It writes binaries into package directories.
`go vet ./...` compiles everything and is the compile check; `just build-go` is
the DB-free server build.

## Continuous integration

`.github/workflows/ci.yml` runs on every push and pull request: a Go job
(`go vet`, tree-wide `gofmt`, `just lint-no-ark`'s script, `go test -short`,
and a build of every example), a frontend job (`just ts-core-test`,
`just web-test`, and web-admin typecheck/test/build), a C# job pinned to
`csharp/Mmokit.Sdk.slnx`, and a drift job that regenerates the C# wire golden
and the ship-dynamics parity fixture and fails if either moved.

`.github/workflows/nightly.yml` runs on a schedule only: `go test -race` and a
five-minutes-per-target fuzz campaign. Neither is on the pull-request path.

**CI is not a superset of local validation.** It provisions no PostgreSQL, so
`just test-pg` and every `//go:build pgtest` package are unrun. Full SDK
regeneration is also local-only, because `just client-sdk` runs the example,
which opens its database. SDK *staleness* is covered without a database by
`sdk_typeid_parity_test.go`. Run the rest locally when you touch persistence or
a client schema.

The workflows deliberately encode the rules below — tree-wide `gofmt`, `-p 1`,
`-short` on the merge path, and never `go build ./...`. If you change one, change both, and treat
this file as the explanation and the workflow as the mechanism.

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
reproduce at commit `4b1d8965`: one default-`-p` full run passed (48 s), as did
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

**The tree is gofmt-clean and CI gates on it tree-wide.** Before committing:

```bash
gofmt -w $(git ls-files '*.go')
```

This used to be a changed-files-only rule, justified by the claim that the 67
unformatted files were "almost entirely pre-existing Go 1.19+ doc-comment
reflow" that `AGENTS.md` forbids clearing as incidental cleanup. That claim was
measured and found false: of the 424 changed diff lines, 29 were comments and
17 blank — **89% was code**, including struct-field and composite-literal
alignment and a misordered import block. It was formatted in one deliberate
commit, so the tree-wide gate is green and stays green.

`AGENTS.md`'s rule still holds for everything else: no repository-wide
reformatting or regeneration as incidental cleanup, and preserve unrelated
worktree changes. Formatting is the one exception, because a mechanical,
verifiable, idempotent transform is exactly what a gate can enforce cheaply —
and an ungated one accumulates.

`go.mod` pins `toolchain go1.26.6` alongside the `go 1.26.0` language directive,
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

Everything here is published under one MIT grant: the framework in `pkg/`, the
three examples in `examples/`, the tooling, and the docs.

The boundary that matters for contributions runs the other way. **`pkg/` must
never depend on an example.** Go enforces this rather than convention: each
example keeps its game code under its own `internal/` directory, so a `pkg/`
file that imports one fails to compile with "use of internal package not
allowed". If you find yourself wanting that import, the thing you want belongs
in `pkg/` — propose promoting it.

Recipes follow the same split. The repository root owns framework-wide tooling
(`proto`, `fuzz`, `admin-*`, `csharp-*`, `ts-core-test`, `db-*`); each example
owns its own `build`, `run`, `dev`, and any gate specific to it. `just
lint-no-ark`, `just shipdyn-golden` and `just test-pg` exist at both levels
with different targets — run the example's copy when you change an example.

## Security

Do not report vulnerabilities through issues or pull requests. See
[`SECURITY.md`](SECURITY.md), which also lists the known, tracked, unfixed
limitations — check it before reporting, since the mesh and UDP transport
weaknesses are already recorded there.
