# 4node-basic

`4node-basic` is the compact distributed MMOKIT example. It is a click-to-move game built to exercise framework behavior rather than game depth:

- Single-process and multi-process role composition
- A 2×2 cell grid distributed across hosts
- Cross-host entity and player handoff
- Generated TypeScript client SDK and browser client
- Auth, chat, and example echo services
- Bot operator commands and a custom admin panel
- Hot-swappable WASM tint system
- Strict authority/NetID invariants during development

The current composition is in [`main.go`](main.go). Entity bundles are in [`components.go`](components.go), gameplay systems in [`system_bots.go`](system_bots.go), and the browser application in [`web`](web/).

## Prerequisites

- Go 1.25.1
- Just
- Docker Compose and PostgreSQL from the root `docker-compose.yml`
- Bun
- tmux for combined and distributed recipes

Start PostgreSQL once from the repository root:

```sh
just db-up
```

The example uses its own `mmo_4node` database. Override its URL with `POSTGRES_URL`.

## Single-process development

```sh
cd examples/4node-basic
just build
just dev
```

The one-time build installs browser dependencies and refreshes the generated SDK. `just dev` then runs the Go process in the default all-role mode and starts the Vite client. Open <http://localhost:5173>.

The browser asks for a player name, creates that development account on first use, and reuses it later. The fixed local-only password used by the example client is `4node-demo-password`.

For a production-style static client build instead of Vite:

```sh
just run
```

Open <http://localhost:5174>. The gateway remains on port 8080 and the admin dashboard on <http://localhost:9101/admin/>. An empty development database seeds the admin operator `admin` / `admin`.

## Distributed mode

```sh
just distributed
```

This builds the SDK, clients, WASM module, and binary, then opens a tmux layout containing:

- One coordinator on MeshControl port 9100
- Two host processes owning the four cells
- One gateway/service process on HTTP port 8080 running auth and chat
- One static browser client on port 5174

Pane output is mirrored to `log/distributed-basic/`.

Use these recipes from the example directory:

```sh
just distributed-logs
just distributed-stop
```

The two host IDs in the recipe are deliberately chosen to produce a 2–2 rendezvous-hash assignment for the 2×2 grid.

## Services and WASM

`main.go` registers auth, chat, and the game-defined echo service. Which service kinds actually run is controlled by the process role and `--services` selection.

For an all-role service-development process with echo and chat enabled:

```sh
just echo-dev
```

The WASM tint module is built by the root `just wasm-build` recipe and loaded into every cell with `mmokit.AddWasmSystem`. It can be managed at runtime through the framework's WASM operator commands.

## SDK workflow

Registered Go entity kinds, inputs, events, and operations are the client schema source of truth. Regenerate the example SDK with:

```sh
just sdk
```

The generated files live under `web/sdk/`; edit the Go registrations or shared generator/core source rather than hand-editing generated output.

## Useful recipes

| Recipe | Result |
| --- | --- |
| `just sdk` | Regenerate the TypeScript SDK |
| `just build-go` | Build WASM and `bin/4node-basic` |
| `just build-web` | Build the browser client to `web/dist` |
| `just build` | Generate and build SDK, web, admin, WASM, and Go binary |
| `just dev` | Run all roles with Vite HMR |
| `just run` | Run all roles with the static production client |
| `just distributed` | Run coordinator, two hosts, gateway/services, and web in tmux |
| `just clean` | Remove the binary and web build output |

The end-to-end mesh coverage is in [`mesh_e2e_test.go`](mesh_e2e_test.go).
