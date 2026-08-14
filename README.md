# MMOKIT

[![CI](https://github.com/mmokit/mmokit/actions/workflows/ci.yml/badge.svg)](https://github.com/mmokit/mmokit/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/mmokit/mmokit.svg)](https://pkg.go.dev/github.com/mmokit/mmokit)
[![Go Version](https://img.shields.io/github/go-mod/go-version/mmokit/mmokit)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

MMOKIT is a server-authoritative multiplayer game framework written in Go, exposed through [`pkg/mmokit`](pkg/mmokit/). The repository ships the framework plus three runnable examples — including a complete space game with a PixiJS client — along with generated client SDK tooling and distributed deployment recipes.

The goal is a framework that can host any genre, with 2D and 3D both first-class. The implementation is 2D today; see [`docs/roadmap.md`](docs/roadmap.md) for the full vision, non-goals, and sequenced plan.

Clients send typed input and render authoritative world-space updates. Cell ownership, host placement, replicas, and handoffs stay inside the server mesh.

## Highlights

- Ark-based ECS with a fixed-timestep loop per cell (20 Hz by default)
- Spatial cells with border replication, hard-cut entity handoff, runtime split/merge, and host migration
- Single-process development or distributed `coordinator`, `host`, `gateway`, and `service` roles
- Per-viewer area-of-interest filtering and quantized delta replication
- Typed binary client events, input, and request/response operations over WebSocket or UDP
- TypeScript and C# SDK generation from registered Go types and entity bundles
- PostgreSQL-backed engine and game persistence
- Typed distributed commands shared by the console and Svelte admin dashboard
- Pluggable auth, chat, and game services
- Hot-swappable Go/WASM game systems

The client protocol uses MMOKIT's reflection-based codec. Protobuf and gRPC are used only for server-internal MeshControl and MeshData traffic.

## Stability

**Pre-1.0. There is no compatibility promise, and that is deliberate rather than an oversight.**

- **No API compatibility across minor versions.** Go module semantics already say this for `v0`, and the project uses it: the preferred fix for a bad API is to change it, not to add a second one beside it.
- **No wire compatibility across any two commits.** Client wire IDs are `fnv32a(reflect.Type.String())`, so renaming a registered type or its package changes its identity. **Build client and server from the same tag.** A version mismatch is not currently detected — a client decodes a frame it does not recognise and skips it silently. Making that mismatch *detectable* is tracked as CE-009 in [`docs/roadmap.md`](docs/roadmap.md).
- **Upgrading can mean a lockstep redeploy** of every process in a cluster. See non-goal 4 in the roadmap.

If you are running this somewhere that matters, pin a commit and carry your own patches. [`SECURITY.md`](SECURITY.md) lists the known, tracked, unfixed limitations — read it before exposing anything to a network.

## Quick start

The smallest runnable example is [`examples/simple`](examples/simple/). It starts PostgreSQL, builds a Go system, runs the server, and serves a small browser client.

Prerequisites:

- Go 1.26 or newer
- [Just](https://github.com/casey/just)
- Docker with Compose
- [Bun](https://bun.sh/)
- `tmux` for the bundled multi-process and combined server/client recipes

From the repository root:

```bash
cd examples/simple
just run
```

Then open:

- Browser example: <http://localhost:5174>
- Admin dashboard: <http://localhost:9101/admin/>

An empty development database seeds the admin dashboard with `admin` / `admin`. Change that password before using the server outside local development. Press Ctrl-C to stop the example and its static-file tmux session.

## Run the reference space game

[`examples/space`](examples/space/) is the framework's most demanding consumer and its regression bed: a complete 2D space game with combat, mining, NPCs, an economy, and a PixiJS client. It is an example in the sense that it consumes only the public framework — not in the sense of being small.

```bash
cd examples/space
just dev
```

Open the Vite client at <http://localhost:5173>. The gateway listens on port 8080 and the admin dashboard on port 9101.

`just dev` is a long-running workflow. It starts PostgreSQL, regenerates the typed SDK, builds both web applications and the server, then runs the game and Vite client together.

## Run a distributed mesh

[`examples/4node-basic`](examples/4node-basic/) is a compact click-to-move game designed to exercise topology, replication, services, and cross-host handoff.

```bash
just db-up
cd examples/4node-basic
just distributed
```

This opens a tmux layout with a coordinator, two hosts, a gateway/service process, and a static browser client. Use `just distributed-stop` from the example directory to stop a detached session.

For the full space game, run `just distributed` from [`examples/space`](examples/space/).

## Repository map

| Path | Purpose |
| --- | --- |
| [`pkg/mmokit`](pkg/mmokit/) | Public, single-import game-facing facade |
| [`pkg/engine`](pkg/engine/) | ECS loop, systems, player lifecycle, loop jobs, and console foundations |
| [`pkg/universe`](pkg/universe/) | Processes, cells, topology, mesh control/data, handoffs, and integrity checks |
| [`pkg/system`](pkg/system/) | Generic physics, spatial, lifetime, movement, and replication systems |
| [`pkg/net`](pkg/net/) | WebSocket and UDP transports plus connection management |
| [`pkg/cmdsys`](pkg/cmdsys/) | Typed and routable operator commands |
| [`pkg/service`](pkg/service/) and [`pkg/services`](pkg/services/) | Service framework and built-in services |
| [`examples/simple`](examples/simple/) | Smallest runnable game — start here |
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

## Common development commands

Recipes split by scope: the repository root owns framework-wide tooling, and each example owns its own build and run.

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

`just db-reset` removes the PostgreSQL volume and is destructive. Neither build regenerates protobuf or WASM modules; run `just proto` or `just wasm-build` when those sources change.

## Client protocol and SDKs

Go registrations are the client schema source of truth:

- `mmokit.RegisterKind` declares entity component bundles.
- `mmokit.RegisterEvent` declares server-to-client typed events.
- `mmokit.HandleClient` declares client-to-server typed input.
- `mmokit.RegisterOp` declares typed request/response operations.

The SDK generator assembles those registries after the process builds. Wire type IDs are `fnv32a(reflect.Type.String())`, which qualifies by package *name* rather than import path, so renaming a registered type or its package is a protocol-breaking change. Relocating a package between directories is not.

## Documentation

- [`docs/README.md`](docs/README.md) — documentation index and maintenance status
- [`docs/roadmap.md`](docs/roadmap.md) — vision, non-goals, and all active tracking
- [`docs/architecture.md`](docs/architecture.md) — current roles, runtime, networking, replication, and persistence
- [`architecture.excalidraw`](architecture.excalidraw) — editable visual architecture overview
- [`pkg/mmokit/README.md`](pkg/mmokit/README.md) — game-facing framework guide
- [`examples/simple/README.md`](examples/simple/README.md) — minimal runnable example
- [`examples/4node-basic/README.md`](examples/4node-basic/README.md) — distributed example and SDK workflow
- [`examples/space/README.md`](examples/space/README.md) — the reference space game
- [`AGENTS.md`](AGENTS.md) — authoritative repository guidance for coding agents and contributors
- [`CLAUDE.md`](CLAUDE.md) — concise Claude Code working notes

Current source, tests, and [`justfile`](justfile) recipes are authoritative when a dated planning document disagrees with the implementation.

## License

MIT — see [`LICENSE`](LICENSE). It covers the whole repository: framework, examples, tooling, and docs alike.

There are no third-party **media** assets — the space example's audio is synthesised by a committed script and its art is drawn procedurally with PixiJS `Graphics`. One piece of third-party **code** is redistributed in compiled form: the operator dashboard's bundle, which is committed because it is embedded into the server binary. Its dependencies and their licences are listed in [`ATTRIBUTION.md`](ATTRIBUTION.md).

**The framework does not depend on any example.** `pkg/` imports nothing from `examples/`, and Go enforces it rather than convention doing so: each example's game code sits under its own `internal/` directory, which the compiler makes unimportable from outside that example. You can depend on `pkg/mmokit` without pulling in a line of game code.

Contributing guidance is in [`CONTRIBUTING.md`](CONTRIBUTING.md); vulnerability reporting is in [`SECURITY.md`](SECURITY.md).
