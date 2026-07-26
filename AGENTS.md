# Repository guidance for Codex

This file applies to the entire repository. Keep it focused on durable rules; put narrowly scoped guidance in a closer `AGENTS.md` only when a subtree genuinely needs different instructions.

## Authority and context

- Current source, tests, and `justfile` recipes are authoritative.
- Read the implementation and nearby tests before changing a package. Use the nearest README and `CLAUDE.md` for orientation only, then verify names and behavior with `rg`; several examples in those documents describe removed APIs.
- Use `docs/architecture.md` for the maintained architecture overview and `docs/roadmap.md` for vision, non-goals, and all active tracking. `architecture.md` describes what is; `roadmap.md` describes what will be — never state direction in `architecture.md`. Treat `docs/superpowers/{plans,specs}` as plugin-owned dated design history, not proof of current behavior; do not edit it during ordinary documentation maintenance.
- Every fact has exactly one owning document; others link to it. Do not duplicate a claim into a second file.
- Preserve unrelated worktree changes. Never perform broad cleanup, regeneration, or formatting unless the task requires it.

## Project snapshot

This is a server-authoritative multiplayer game framework and space-game implementation. The Go module targets Go 1.25.1. Each cell has a 20 Hz ECS loop; clients send typed input and render absolute world-space updates without knowing the mesh topology. The implementation is 2D today; first-class 3D support is planned and scoped in `docs/roadmap.md`.

- `pkg/mmokit/`: public game-facing facade. Prefer this single import in games and examples.
- `pkg/universe/`: processes, roles, cells, gateways, meshing, transfers, integrity checks, and cluster coordination.
- `pkg/engine/`: ECS loop, player lifecycle, systems, loop jobs, and console foundations.
- `pkg/system/`, `pkg/replication/`, `pkg/quantize/`, `pkg/net/`: generic systems, replication, codecs, WebSocket, and UDP transport.
- `pkg/cmdsys/`, `pkg/service/`, `pkg/services/`: distributed commands and pluggable services such as auth and chat.
- `pkg/admin/` and `web-admin/`: Go admin backend plus the Svelte 5 operator UI embedded in the server binary.
- `pkg/world/` and `world/`: world-manifest model/repository and tracked space-game content.
- `pkg/wasmabi/`, `pkg/wasmhost/`, `pkg/wasmsys/`: hot-swappable WASM systems.
- `internal/`: space-game components, systems, items, persistence, and marketplace behavior.
- `cmd/server/`: production space-game composition root. `cmd/sdkgen/` generates typed client SDKs.
- `examples/simple/`: smallest current MMOKIT example with a custom Go system.
- `examples/4node-basic/`: distributed mesh/auth/chat/admin example with a generated TypeScript client.
- `web-pixi/`: PixiJS space-game client. `csharp/`: shared C# SDK core and golden tests.
- `proto/meshpb/`: the only protobuf schema. It is for server-internal MeshControl/MeshData traffic, not the client protocol.

## Architectural boundaries

- `pkg/` is reusable engine code and must never import `internal/`.
- Generic components live in `pkg/component`; space-game components live in `internal/component`. When both are needed, use clear aliases such as `comp` and `gamecomp`.
- Production files under `internal/game/` must not import Ark ECS directly, except the existing glue files `entity_kinds.go` and `var_tail_bindings.go`. Tests are exempt. Run `just lint-no-ark` after game-layer changes; do not expand the allowlist casually.
- Game code should use MMOKIT wrappers. Raw Ark access belongs in framework code or explicit binding glue.
- System registration order is semantic. Preserve the order in `internal/game/factory.go`; in particular, Ability precedes Projectile, Lifetime precedes AoE, Spatial precedes Collision, and Network remains last.

## Current MMOKIT and ECS patterns

### Systems and per-cell state

- Custom systems embed the non-generic `mmokit.SystemBase` and are registered with `process.AddSystem(mmokit.NewSystem(&MySystem{}))`.
- The pointer passed to `NewSystem` supplies type information only. A fresh zero value is constructed per cell, so initialize state in `Init`. Use a real `SystemDef` factory when constructor arguments or captured dependencies are required.
- Register typed per-cell state with `mmokit.AddState[T](process, factory)` and retrieve it with `mmokit.State[T](s.Stage())`, normally once in a system's `Init`. `internal/game.GameWorld` does not embed `Stage`.
- Register player spawn/reconnect behavior with `Process.OnPlayerJoin`; do not overwrite the player manager's active-state callback.
- Custom player-state constants in `internal/game/game.go` start at `StateBuiltinEnd`. Their declaration order must match the per-cell `RegisterState` call order.

### Queries and mutation

- Declare `mmokit.Query[Bundle]` fields where `Bundle` has exported pointer-to-component fields. Optional fields use `ecs:"optional"`.
- Query fields are auto-built. Configure non-default filtering with `q.With(...)` during `Init`, then iterate with `for entity, bundle := range q.Iter`.
- Queries exclude Ghost and Replica by default. `IncludeAll()` clears those defaults; `Without[T]()` adds exclusions.
- Query bundle pointers are reused between iterations. Never retain a bundle or its address after the loop body.
- Mutating fields through component pointers yielded by a query is allowed. Structural ECS changes are not: never spawn, despawn, or add/remove components directly while a query locks the world.
- Queue structural work through `s.Commands()` or `stage.Commands()` using `Despawn`, `Defer`, `mmokit.AddComponent`, and `mmokit.RemoveComponent`. The framework flushes commands after every system, so changes from system N are visible to system N+1 in the same tick.
- Use `stage.TickOne(system, dt)` in unit tests that need the production `Update`-then-command-flush contract.
- Prime dynamically looked-up component types in `Init` when required. Avoid `mmokit.Set` inside a locked query when it might add a missing component.
- ECS state belongs to the owning cell-loop goroutine. Route off-loop, admin, and cross-goroutine access through `RunOnLoop`/cmdsys loop routing. Use synchronized process accessors rather than iterating internal cell maps directly.

### Spawning and messaging

- `Stage.Spawn` is the canonical entity constructor. Pass components by value, include exactly one `Position`, and do not supply framework-owned `NetworkID` or `CellCoord`. Duplicate types and pointer components panic; zero `Velocity` is added automatically.
- `Stage.SpawnPlayer` injects `Position` and `PlayerConn`, so callers must not pass either. Kinded spawns must provide all components required by the `RegisterKind` bundle.
- Split-created stages receive transferred state. Bootstrap hooks must respect `Stage.FromSplit()` and must not duplicate initial world entities.
- Typed client input is registered with `mmokit.HandleClient` and is drained by the engine before systems each tick; it is not an input system.
- Server events use `mmokit.RegisterEvent` plus `mmokit.SendEvent`/`SendEventToAll`. `HandleAll` registers a message handler on current and future stages and broadcasts the result to AoI viewers; use `HandleAllInternal` for server-only messages.
- Typed request/response operations use `mmokit.RegisterOp` and an appropriate route. Prefer the existing typed registries over ad-hoc wire frames.

## Wire and distributed invariants

- Client traffic uses the reflection-based typed codec: channel `0x00` for typed events/input and channel `0x01` for operations. Never introduce client-facing protobuf messages.
- Wire type IDs derive from package-qualified Go type names. Renaming/moving registered event, input, broadcast, or operation types is wire-breaking. Stable enum values and replicated field layouts are protocol contracts.
- Go registries are the client-schema source of truth. Regenerate and review affected SDKs after changing entity bundles, replication tags, typed inputs/events, broadcasts, or operations.
- Built-in process roles are `coordinator`, `host`, `gateway`, and `service`; the default `all` preset includes all four. A service role starts only the selected/auto-added service kinds.
- The coordinator is the control plane and must not become a per-tick, per-action, or service-event payload hop. Data flows directly between gateways, hosts, and services over MeshData.
- Clients remain topology-agnostic and do not predict authority. Preserve absolute world coordinates, cluster-clock timestamps, hard commit-tick handoff, and the destination's fresh-snapshot reset.
- Cell IDs use `cell_X_Y` or `cell_dN_X_Y` in maps/wire formats and `X_Y` or `dN_X_Y` for display. Parse user input at boundaries with `ParseCellID`; do not invent alternate formats.
- One cell may have only one Live or Replica slot for a netID. Authority transitions go through the sanctioned demote/promote paths. Split, merge, and migrate changes go through commit plans; never mutate ownership maps as a shortcut.
- In `pkg/universe`, acquire `Process.mu` before `Control.mu` when both are required. The transfer orchestrator mutex is a leaf lock and must not be acquired while holding `Process.mu`.

## Admin, persistence, and security

- Implement every operator action as a typed cmdsys verb first. The console and admin HTTP surface should share that verb so routing, RBAC, and audit logging remain intact; do not add an ad-hoc admin mutation route.
- In `web-admin`, stores using Svelte runes outside components use a `.svelte.ts` suffix. Never use `window.alert`, `window.confirm`, or `window.prompt`; use the existing dialog/modal components.
- Runtime world mutations use `pkg/world/jsonrepo` and the `world.*` command path so disk writes and live cell state remain synchronized.
- `pkg/persist` owns engine identity/admin storage; `internal/persist` owns `space.*` game tables. Keep migration ownership and ordering stable.
- Keep secure cookies, WebSocket origin checks, CORS allowlists, and TLS behavior intact. `--dev-insecure-cookie` and self-signed TLS are local-development options, not production defaults.
- The default local admin is seeded only for an empty database. Never hard-code those development credentials into production configuration.

## Generated and derived files

Do not hand-edit generated or build output. Change the source and regenerate:

- `proto/meshpb/*.proto` -> `just proto` -> `gen/go/meshpb/` (Buf remote plugins may need network).
- Space-game schema -> `just space-sdk` -> `web-pixi/sdk/`.
- 4node schema -> `just client-sdk examples/4node-basic` -> `examples/4node-basic/web/sdk/`.
- Admin source -> `just admin-build` -> `pkg/admin/static/dist/`.
- WASM source -> `just wasm-build` -> ignored `dist/` artifacts.
- C# wire golden source -> `just csharp-golden` -> tracked golden JSON.

Keep generated diffs only when the corresponding source/schema changed.

## Build and run rules

- `justfile` is the canonical task runner; there is currently no CI configuration or Makefile.
- Never build binaries into the repository or package roots, and do not use `go build ./...`. Use `just build-go` for a DB-free server compile or an explicit `go build -o bin/<name> <package>` recipe.
- `just build` generates the space SDK, builds both web applications, and writes `bin/server`. It does not run protobuf or WASM generation.
- Full root builds and SDK dumps require PostgreSQL. Start it first with `just db-up`; this requires Docker. Most build/run recipes do not declare `db-up` as a dependency.
- `just run`, `just dev`, `web-serve`, distributed recipes, probes, and smoke clients are long-running/manual workflows. Run them only when useful and stop any tmux/server sessions you start.
- Treat `just db-reset` as destructive: it removes the PostgreSQL volume. Do not run it without explicit intent. `resetdb` only removes obsolete SQLite files and is not a PostgreSQL reset.
- Common local ports are gateway HTTP 8080, UDP 9000, control 9100, admin 9101, Vite 5173, and static web 5174. Avoid launching conflicting listeners during tests.

## Validation

Run the smallest relevant checks first, then broaden in proportion to the change. Format only touched Go files with `gofmt -w`; the repository has unrelated formatting debt, so do not reformat the whole tree.

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

If Bun dependencies are absent or changed, run `bun install --frozen-lockfile` in the relevant frontend first. Full SDK generation/builds can also require PostgreSQL and dependency network access. Report any check not run and the missing prerequisite; do not claim a full suite passed based only on a targeted test.
