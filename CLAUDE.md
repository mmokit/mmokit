# Claude Code repository guide

Read [`AGENTS.md`](AGENTS.md) before making changes. It is the authoritative, comprehensive repository instruction file. This document is intentionally shorter and highlights the context most likely to prevent stale-API or architectural mistakes.

## Source of truth

- Verify behavior against current source, nearby tests, `go.mod`, and [`justfile`](justfile).
- Use package READMEs for orientation, then confirm identifiers with `rg` before writing code.
- Treat `docs/planning/` as a historical index and `docs/superpowers/{plans,specs}/` as dated design records, not proof of current behavior. Use `docs/architecture.md` for the maintained architecture overview.
- Preserve unrelated worktree changes. Do not perform repository-wide formatting or regeneration as incidental cleanup.

## Current architecture

This repository contains a reusable server-authoritative 2D MMO framework and a reference space game:

- `pkg/mmokit/` is the public game-facing facade. Games and examples should normally import it instead of several lower-level packages.
- `pkg/engine/` owns the ECS loop, system lifecycle, players, loop jobs, and console foundations.
- `pkg/universe/` owns processes, cells, roles, topology, host assignment, handoff, split/merge/migrate, and cluster integrity.
- `pkg/system/`, `pkg/replication/`, `pkg/quantize/`, and `pkg/net/` implement generic simulation and networking.
- `pkg/cmdsys/`, `pkg/service/`, and `pkg/services/` implement routed commands and pluggable services.
- `pkg/admin/` and `web-admin/` implement the admin backend and embedded Svelte dashboard.
- `internal/` is the reference space game. Its composition root is `cmd/server/`; its browser client is `web-pixi/`.
- `examples/simple/` is the smallest current API example. `examples/4node-basic/` exercises distributed roles and client SDK generation.
- `proto/meshpb/` is the only protobuf schema. It is server-internal; client frames do not use protobuf.

Processes can contain any valid set of the built-in roles:

- `coordinator`: cluster control plane and assignment state
- `host`: cells and their ECS loops
- `gateway`: client WebSocket/UDP termination and routing
- `service`: selected service kinds such as auth or chat

The default `all` preset includes all four. In a distributed deployment, per-tick and service payloads travel directly between the relevant gateways, hosts, and services; do not turn the coordinator into a payload relay.

## Current MMOKIT shape

Custom systems embed the non-generic `mmokit.SystemBase`. The prototype passed to `mmokit.NewSystem` supplies type information only; the framework creates a fresh zero value per cell.

```go
type MovementSystem struct {
    mmokit.SystemBase
    entities mmokit.Query[struct {
        Position *mmokit.Position
        Velocity *mmokit.Velocity
    }]
}

func (s *MovementSystem) Update(dt float32) {
    for _, bundle := range s.entities.Iter {
        bundle.Position.X += bundle.Velocity.X * dt
        bundle.Position.Y += bundle.Velocity.Y * dt
    }
}

process.AddSystem(mmokit.NewSystem(&MovementSystem{}))
```

Query fields are discovered and built automatically. Configure a non-default filter with `q.With(...)` during `Init`, then range over `q.Iter`. Queries exclude Ghost and Replica by default. Bundle pointers are reused between iterations and must not escape the loop.

Use the current process-level APIs:

- `mmokit.New(mmokit.Config{...})` and `process.Start()`
- `process.AddSystem(mmokit.NewSystem(&MySystem{}))`
- `mmokit.AddState[T](process, factory)` and `mmokit.State[T](stage)` for typed per-cell state
- `mmokit.RegisterKind[Bundle](process, kind, name, ...)` for entity kinds
- `process.OnPlayerJoin(...)` and `stage.SpawnPlayer(...)` for player creation
- `mmokit.HandleClient`, `mmokit.RegisterEvent`, and `mmokit.RegisterOp` for typed wire messages
- `stage.Spawn(...)` for ordinary entity construction

Do not revive removed APIs found in historical documents, including:

- `mmokit.NewCoordinator`
- generic `mmokit.SystemBase[World]`
- `GameWorld` structs embedding `*mmokit.Stage`
- query `Init` or `All` methods
- `SpawnEntity`, `WithCollider`, or callback-style `AddSystem(name, factory)`
- client-facing `enginepb` messages

## ECS and concurrency rules

- `pkg/` is reusable and must not import `internal/`.
- Generic components live in `pkg/component`; space-game components live in `internal/component`.
- Production files in `internal/game/` must not import Ark directly except the existing binding glue allowlist. Run `just lint-no-ark` after game-layer changes.
- System registration order is semantic. Preserve the order in `internal/game/factory.go`; Network stays last.
- ECS structural changes are illegal while a query holds the world lock. Queue them through `s.Commands()` or `stage.Commands()`.
- The engine flushes deferred commands after each system, so system N's changes are visible to system N+1 in the same tick.
- ECS state belongs to the owning cell-loop goroutine. Route admin, service, and other off-loop work through `RunOnLoop` or cmdsys helpers.
- `Stage.Spawn` owns `NetworkID` and `CellCoord`, requires exactly one `Position`, rejects pointer components and duplicate types, and adds zero `Velocity` when absent.
- Split-created stages receive transferred state. Initialization hooks must honor `Stage.FromSplit()` and avoid duplicating bootstrap entities.

## Wire and mesh invariants

- Client channel `0x00` carries typed events/input; channel `0x01` carries typed operations. Both use the reflection codec.
- Package-qualified Go type names determine wire type IDs. Registered type moves and renames are breaking changes.
- Regenerate affected SDKs after changing entity bundles, replication tags, client input, events, broadcasts, or operations.
- Clients remain topology-agnostic and receive absolute world coordinates.
- Preserve cluster-clock timestamps, commit-tick authority handoff, and fresh destination snapshots.
- Cell topology changes go through commit plans. Never update ownership maps as a shortcut.
- Parse cell IDs at boundaries with `ParseCellID`; do not introduce another syntax.
- In `pkg/universe`, acquire `Process.mu` before `Control.mu` when both are needed.

## Admin, persistence, and generated files

- Implement operator mutations as typed cmdsys verbs first. The console and HTTP admin UI must share the same command, RBAC, and audit path.
- Engine persistence belongs to `pkg/persist`; game persistence belongs to `internal/persist`.
- PostgreSQL-backed builds and schema dumps require a running database. Use `just db-up`; never run destructive `just db-reset` without explicit intent.
- Do not hand-edit generated Go protobuf, SDK, admin bundle, or wire-golden output. Change the source and run the relevant `just` recipe.
- Do not weaken secure cookies, origin checks, CORS, or TLS defaults. `--dev-insecure-cookie` is only for local HTTP development.

## Build and validation

Do not run `go build ./...` or write binaries into package directories. Use:

```bash
just build-go
just build
go vet ./...
go test ./... -count=1 -timeout 300s
```

`just build-go` is the DB-free compile check. `just build` generates the space SDK, builds both web applications, and writes `bin/server`; it needs the frontend toolchain and a running PostgreSQL instance. It does not regenerate protobuf or WASM modules.

Run the smallest relevant validation first, then broaden according to [`AGENTS.md`](AGENTS.md). Common focused checks include:

```bash
just lint-no-ark
just admin-typecheck
just admin-test
just ts-core-test
just csharp-test
just test-pg
```

Localhost TCP/UDP tests require an environment that permits socket listeners. Report checks skipped because PostgreSQL, Docker, Bun, .NET, Buf, or network access was unavailable.
