# MMOKIT

MMOKIT is a server-authoritative multiplayer game framework and reference space game written in Go. The reusable framework is exposed through [`pkg/mmokit`](pkg/mmokit/); the repository also contains a complete game, browser clients, generated client SDK tooling, and distributed deployment examples.

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

## Quick start

The smallest runnable example is [`examples/simple`](examples/simple/). It starts PostgreSQL, builds a Go system, runs the server, and serves a small browser client.

Prerequisites:

- Go 1.25.1
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

The production composition root is [`cmd/server`](cmd/server/), with game code under [`internal`](internal/) and the PixiJS client under [`web-pixi`](web-pixi/).

```bash
just db-up
just dev
```

Open the Vite client at <http://localhost:5173>. The gateway listens on port 8080 and the admin dashboard on port 9101.

`just dev` is a long-running workflow. It builds the space SDK, both web applications, and `bin/server`, then runs the game and Vite client together.

## Run a distributed mesh

[`examples/4node-basic`](examples/4node-basic/) is a compact click-to-move game designed to exercise topology, replication, services, and cross-host handoff.

```bash
just db-up
cd examples/4node-basic
just distributed
```

This opens a tmux layout with a coordinator, two hosts, a gateway/service process, and a static browser client. Use `just distributed-stop` from the example directory to stop a detached session.

For the full space game, run `just distributed-space` from the repository root.

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
| [`internal`](internal/) | Reference space-game components, systems, persistence, and marketplace logic |
| [`cmd/server`](cmd/server/) | Reference space-game composition root |
| [`cmd/sdkgen`](cmd/sdkgen/) | TypeScript and C# client SDK generator |
| [`web-pixi`](web-pixi/) | PixiJS space-game client |
| [`web-admin`](web-admin/) | Svelte 5 operator dashboard |
| [`csharp`](csharp/) | Shared C# SDK runtime and golden tests |
| [`proto/meshpb`](proto/meshpb/) | Server-internal protobuf schema |
| [`world`](world/) | Tracked world-manifest content |

## Common development commands

| Command | Result |
| --- | --- |
| `just build-go` | Compile the server to `bin/server` without running database-backed schema generation |
| `just build` | Generate the space SDK, build both web applications, and compile `bin/server` |
| `just run` | Run the fully built reference game |
| `just proto` | Regenerate `gen/go/meshpb` from the internal mesh schema |
| `just space-sdk` | Regenerate the PixiJS game's typed SDK |
| `just client-sdk examples/4node-basic` | Regenerate the example's typed TypeScript SDK |
| `just admin-typecheck` / `just admin-test` | Check the admin application |
| `just csharp-test` | Run the shared C# SDK tests |
| `just ts-core-test` | Run the shared TypeScript codec/interpolation tests |
| `just lint-no-ark` | Enforce the game/framework ECS boundary |
| `go test ./...` | Run Go tests; localhost socket tests require a normal network-enabled environment |

`just build` does not regenerate protobuf or WASM modules. Run `just proto` or `just wasm-build` when those sources change. `just db-reset` removes the PostgreSQL volume and is destructive.

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
- [`AGENTS.md`](AGENTS.md) — authoritative repository guidance for coding agents and contributors
- [`CLAUDE.md`](CLAUDE.md) — concise Claude Code working notes

Current source, tests, and [`justfile`](justfile) recipes are authoritative when a dated planning document disagrees with the implementation.

## License

MIT — see [`LICENSE`](LICENSE).

The licence covers **MMOKIT core**: `pkg/`, `examples/`, `cmd/sdkgen`, `cmd/csharp-golden`, `proto/`, `gen/`, `csharp/`, `web-admin/`, `scripts/`, and `docs/`.

The reference space game is **not part of the distributed framework**. That is `internal/`, `cmd/server`, `cmd/botclient`, `web-pixi/`, `data/`, and `world/`. It lives here as a worked example and as the framework's most demanding test, but it is not published as part of MMOKIT and its assets carry no licence grant — in particular the audio under `web-pixi/public/audio/` has no attribution recorded and is not covered.

`pkg/` and `examples/` contain zero imports of `internal/`, and both examples ship their own `main.go`, so the framework is usable without any of the game code. See [`docs/roadmap.md`](docs/roadmap.md) (OSS-001) for the published/not-published boundary and the open extraction decision.

Contributing guidance is in [`CONTRIBUTING.md`](CONTRIBUTING.md); vulnerability reporting is in [`SECURITY.md`](SECURITY.md).
