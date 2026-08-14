# The reference space game

A complete 2D space game — combat, mining, NPCs, an economy, a hand-authored world, and a PixiJS client — built entirely on the public MMOKIT framework. It is an example in the sense that it consumes only `pkg/mmokit` and friends, not in the sense of being small: it is the framework's most demanding consumer and its regression bed.

Layout:

| Path | Purpose |
| --- | --- |
| [`main.go`](main.go) | Composition root — wires the process, systems, services, and persistence |
| [`internal/`](internal/) | Game layer: components, systems, items, marketplace, persistence, commands |
| [`web/`](web/) | PixiJS browser client and its generated SDK |
| [`world/`](world/) | Hand-authored world manifest (stations, POIs, dungeons, belts, regions) |
| [`botclient/`](botclient/) | Headless load-test client |

`internal/` is nested here on purpose: Go's compiler makes it unimportable from outside this example, so the framework cannot accidentally depend on the game.

For the framework's role and data-flow model, see the [architecture overview](../../docs/architecture.md).

## Run it

Prerequisites: Go, [just](https://github.com/casey/just), Docker (for PostgreSQL), [Bun](https://bun.sh), and `tmux` for the dev and distributed recipes.

**From this directory**, not the repository root:

```sh
cd examples/space
just dev
```

That starts PostgreSQL, creates the `mmo_space` database if it is missing, regenerates the typed SDK, builds the web and admin bundles and the server, then runs the game with a Vite dev server. Open <http://localhost:5173>.

For a build-and-run without Vite, use two terminals:

```sh
just run                                    # server: gateway :8080, admin :9101
just web-serve                              # static client on :5174
```

`just run` binds the gateway to `:8080` with no extra CORS origin, so the Vite flow (`just dev`) is the path that works out of the box. If you serve the client on `:5174` instead, pass the matching origin:

```sh
just run --cors-origins=http://localhost:5174
```

The three examples all use ports `:8080`, `:9101` and `:5173`/`:5174`, so **only one can run at a time.**

This example uses its own database, `mmo_space`, so it shares no state with the other examples. Override with `POSTGRES_URL`. An empty admin-operator table is seeded with a development-only `admin` / `admin` operator; rotate it before any non-local deployment.

## Role-aware composition

The same binary can run all roles or a subset selected with `--mode`:

| Process shape | Game-specific initialization |
| --- | --- |
| `all` / contains `host` | Loads configuration and player state, registers game state and systems, runs cell simulation |
| Pure `coordinator` | Opens PostgreSQL for mesh dimensions/configuration and runs control/admin state; owns no cells |
| Pure `gateway` | Skips game configuration and PostgreSQL; terminates and routes client connections |
| `gateway,service` | Opens PostgreSQL for DB-backed services such as auth |
| Pure `service` | Runs selected registered service kinds and their dependencies |

This example's `just distributed` recipe builds a five-process development layout: one coordinator, three hosts, one gateway/service process, and a separate static web host. It passes the same `MMO_CLUSTER_SECRET` to every pane.

**Attaching a remote host to a single-process server needs an explicit secret.** The `all` preset auto-generates one for itself, so a host started against it with only `--mode=host --coordinator-addr=localhost:9100` is rejected with `codes.Unauthenticated`. Pass a matching `--cluster-secret` (any value) to both processes:

```bash
./bin/server --cluster-secret=dev-secret                     # all preset, control plane on :9100
./bin/server --mode=host --coordinator-addr=localhost:9100 \
             --host-id=host-1 --cluster-secret=dev-secret
```

## Startup sequence

The implementation in [`main.go`](main.go) follows this order:

1. Create default engine and connection configuration, bind universal flags, and load the JSON world manifest.
2. Parse the process roles before opening role-specific dependencies.
3. Register game log categories.
4. For roles requiring configuration, open one PostgreSQL store, apply engine/auth/game migrations, and load `GameConfig`.
5. For host roles, load the player working set, construct operation/session routing, load the marketplace, and install game repositories.
6. Create `mmokit.Process`, register game admin commands, and add the per-cell `GameWorld` state factory.
7. Run `game.GameSetup` for host roles; it registers entity kinds, typed messages, gameplay verbs, player lifecycle hooks, and systems.
8. Register spawn resolution and the auth service.
9. Build the process so schemas, routes, listeners, and assigned cells are fully wired.
10. Start the blocking process lifecycle, then synchronously flush dirty players during shutdown.

The operation router and marketplace-expiry ticker run only on processes with hosted game state. Client HTTP and UDP listeners are process-owned and start only when the gateway role is present.

## Important configuration

Universal flags are defined by `mmokit.Config.BindFlags`. Common production-space options are:

| Flag | Purpose |
| --- | --- |
| `--mode` | Comma-separated `coordinator`, `host`, `gateway`, and `service` roles; default `all` |
| `--services` | Service kinds to instantiate on a service-role process |
| `--coordinator-addr` | MeshControl endpoint for standalone hosts, gateways, and services |
| `--control-listen` | Coordinator MeshControl bind; default `:9100` (an all-interfaces bind, not loopback) |
| `--cluster-secret` | Shared secret authenticating mesh peers; env `MMO_CLUSTER_SECRET`. Auto-generated for self-contained role sets (`all`, coordinator+host) and required on **every** process of a multi-process cluster |
| `--port` | Gateway HTTP port for `/ws`, `/auth`, and related routes; default `8080` |
| `--udp-listen` | Gateway UDP bind; EXPERIMENTAL and off by default, pass `:9000` to enable |
| `--admin-listen` | Coordinator admin/metrics bind; default `:9101`, empty to disable |
| `--world-dir` | Directory containing tracked world-manifest JSON; default `world` |
| `--cors-origins` | Browser origins allowed for credentialed HTTP and, by fallback, WebSocket access |
| `--tls-cert`, `--tls-key` | In-process TLS certificate and private key |
| `--tls-mode=self-signed` | Local TLS testing only |
| `--dev-insecure-cookie` | Permit auth cookies over plaintext local HTTP only |
| `--headless` | Disable the interactive console |
| `--dump-schema` | Assemble the Go protocol schema as JSON and exit |

Run `./bin/server -h` after `just build-go` for the complete current list.

## Source map

| Concern | Source |
| --- | --- |
| Composition and role gates | [`main.go`](main.go) |
| Game setup and system ordering | [`internal/game/factory.go`](internal/game/factory.go) |
| Per-cell game state | [`internal/game/gameworld.go`](internal/game/gameworld.go) |
| Player repositories and flushing | [`internal/game/playerdb.go`](internal/game/playerdb.go), [`internal/game/player_flusher.go`](internal/game/player_flusher.go) |
| PostgreSQL migrations/repositories | [`pkg/persist`](../../pkg/persist/), [`internal/persist`](internal/persist/) |
| World JSON repository | [`pkg/world/jsonrepo`](internal/world/jsonrepo/) and [`world`](world/) |
| Space-game operator commands | [`internal/game/commands`](internal/game/commands/) |
| Runtime flags and role listeners | [`pkg/universe/bootstrap.go`](../../pkg/universe/bootstrap.go) |

System registration order is semantic and intentionally lives in `internal/game/factory.go` rather than being duplicated here. In particular, Ability precedes Projectile, Lifetime precedes AoE, Spatial precedes Collision, and Network remains last.

## Persistence ownership

The production binary opens one shared PostgreSQL pool and layers repositories around it:

- Framework identity, sessions, and admin data are owned by `pkg/persist`.
- Space-game configuration, player state, and marketplace data are owned by `internal/persist`.
- Auth contributes its own migrations through `mmokit.WithExtraMigrations`.

The in-memory `PlayerRepo` is a synchronized working set, not the persistence authority. Gameplay mutations that must survive restart mark records dirty; periodic and shutdown flushes write them transactionally.

## Shutdown

`Process.Start` handles signals, stops listeners and cell loops, and tears down process roles. After it returns, `main` performs a final synchronous dirty-player flush before closing the shared PostgreSQL store.
