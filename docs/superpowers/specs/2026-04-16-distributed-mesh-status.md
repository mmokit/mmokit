# Distributed Mesh — Status & Remaining Work

**Branch:** `feature/distributed-mesh`

**Last updated:** 2026-04-16

This is a rolling status snapshot for the distributed mesh line of work. The original spec is at [2026-04-12-distributed-mesh-design.md](2026-04-12-distributed-mesh-design.md).

---

## Spec progress (S1–S9)

| Stage | Title | Status |
|---|---|---|
| S1 | Rename Node → Cell + 1:N cell hosting (in-process) | ✅ Shipped |
| S2 | Handoff protocol wiring (in-process) | ✅ Shipped |
| S3 | Proto schema + gRPC bridge | ✅ Shipped |
| S4 | Coordinator as control-plane service | ✅ Shipped |
| S5 | Persistence: BoltDB → Postgres | ✅ Shipped |
| S6 | Gateway | ✅ Shipped |
| S7 | Distributed cell splits + merges + migrate | ✅ Shipped |
| S8 | Multi-process 4node demo + e2e bot test | ✅ Shipped |
| S9 | Space game full distributed | 🟡 **Partial** |

---

## S9 status detail

The original S9 bullet list and its current state:

### ✅ Admin commands route through coordinator

Shipped via the **distributed command system** (a larger foundation than S9 originally scoped). Full architecture at [2026-04-15-distributed-command-system.md](../plans/2026-04-15-distributed-command-system.md).

Key deliverables:
- `pkg/cmdsys/` — typed Command registry with capability-tag RBAC (precedence matcher: literal > wildcard-prefix > global; deny ties), reflected JSON Schema, FNV-64a schema versioning, structured audit log, request-id promise pattern.
- `MeshControl.CommandRequest` / `CommandResponse` / `CommandCancel` oneof variants for cross-process dispatch.
- `engine.RunOnLoop(ctx, fn)` — goroutine-ID-aware game-loop scheduler replacing the brittle `PendingAdminCmds` channel. Detects on-loop reentrance to prevent nested-schedule deadlocks; 8ms per-tick drain budget with slow-job warnings.
- Every admin verb (12 game + universe perf/cell/host + engine config/entity/log + 4node-basic bot) migrated to typed `cmdsys.Command` registrations. Old `CommandGroup`/`Cmd` shim deleted.
- `GET /commands` and `GET /commands/{verb}` HTTP introspection alongside `/metrics`.
- Off-loop console dispatch: handlers run on the REPL goroutine and use `RunOnLoop` for ECS access. Fixed the cell-split deadlock class — handler can call `SplitCell` (which internally schedules loop work via executor goroutines) without freezing the sim.

### ✅ Player routing respects StationCell

Shipped. See [2026-04-16-player-spawn-routing.md](../plans/2026-04-16-player-spawn-routing.md).

- `coords.SpawnPoint` + `coords.WorldCenterOfCell(x, y)` helper.
- `Config.DefaultSpawn SpawnPoint` — world-coord fallback, topology-independent.
- `Coordinator.SetSpawnResolver(func(username) (worldX, worldY float32, ok bool))` — replaces `SetPlayerRouter`. Returns world coords, not cell IDs — topology-blind.
- `MeshControl.ResolveSpawn` / `SpawnResolved` RPC for standalone gateways to consult the coordinator's resolver.
- `Gateway.resolveSpawn`: inline call for embedded; RPC with 2s deadline for standalone; `DefaultSpawn` fallback.
- `cachedTopology.cellAtPosition(worldX, worldY)` resolves the world coord to the current owning cell by parsing cellIDs and walking `WorldBounds` — survives arbitrary split depths.
- `Coordinator.CellAtPosition` sibling to the existing `NodeAtPosition`.
- `RequestRespawn` (death-respawn path) uses the same resolver → CellAtPosition flow — login and respawn now share spawn logic.

### 🔴 Marketplace cross-host — deferred

Not blocking S9. Will be **reworked into a standalone service** with its own Postgres schema and its own deployment story (initially colocated with the coordinator, but deployable independently). The existing in-memory marketplace stays on `--mode=coordinator`-carrying processes for now; nodes route market ops to it via existing operation-router plumbing when colocated, and the cross-host wire path lands when the service rework happens.

Separate plan TBD. Not on the immediate critical path.

### 🟡 `just distributed-space` recipe

Pending. Not yet implemented. Needs:
- Top-level recipe that spawns `--mode=coordinator --control-listen=:9100` + two `--mode=node --coordinator-addr=...` + one `--mode=gateway --coordinator-addr=...` in background processes.
- PID-file tracking for clean shutdown (`distributed-space-stop`).
- Log aggregation (`distributed-space-logs`).
- Pre-req: `just db-up` + `just build`.
- Parallel pattern exists in the Makefile-era multi-process spawns; `mesh_e2e_test.go` covers the in-process equivalent.

Estimated scope: ~30 lines of justfile + doc update. Small.

### 🟡 Distributed smoke test

Pending. Not yet implemented. Shape would be a Go test under `cmd/server/` or `internal/game/` that:

1. Builds an in-process coordinator + 2 hosts via `TestHosts: ["h0", "h1"]`.
2. Fakes a client connect + `CE_LOGIN`, asserts spawn routes to the correct host per `SpawnResolver`.
3. Dispatches a cross-host admin command (`tp <user>`) via the coordinator's dispatcher, asserts the player moves on the correct host.
4. Exercises cross-host chat fan-out (if/when chat-admin-commands land).
5. Asserts clean shutdown via `coord.Shutdown()`.

Mirrors `examples/4node-basic/mesh_e2e_test.go` for the space game. Won't cover the marketplace path until the marketplace rework lands.

Estimated scope: ~400 lines test + ~100 lines helpers. Medium.

---

## Known gaps from the command-system work

Small items that were noted during the cmdsys build but not blocking:

- **`internal/game/commands/` has no unit tests.** Each of the 12 game verbs (`tp`, `damage`, `kick`, `currency`, `say`, `npcs`, `tpto`, `spawnnpcs`, `kill`, `heal`, `give`, `players`) lives in its own file with zero coverage. The 31s e2e mesh test in 4node-basic covers the dispatcher mechanics, but per-verb smoke tests would catch game-side regressions faster. Could add a table-driven `commands_test.go` under TestHosts.

- **`pkg/engine/console.go` is 455 lines.** Down from 844 after C4's shim deletion and the C2 split, but still over the 200-line soft target. The REPL loop, readline plumbing, completer, status display, and perf format all live here. Could split into `console.go` (Console struct + Run), `console_readline.go` (readline + completer), `console_status.go` (print helpers + FormatPerfOutput). Not urgent.

- **`pkg/engine/builtins_log.go` (333 lines) and `pkg/universe/builtins_cell.go` (516 lines)** are both over target but cohesive (one command group each). Splitting is optional polish.

---

## Future work (beyond S9)

Deferred by design. Plans don't exist yet for most of these — listed so the roadmap is visible:

### Marketplace rework into a standalone service
- Own Postgres schema (orders, trades, settlement ledger).
- Own lifecycle — can run as a sidecar or colocated.
- Order placement via gRPC (wire format TBD — likely leverages the existing ops-router pattern).
- Notifications via the `cmdsys`-adjacent push channel (coord → gateway → client).
- Settlement atomicity: Postgres transactions, not in-memory state.
- First target: migrate current in-memory Settlement to Postgres-backed, keep the coordinator embedded.
- Follow-up: expose standalone binary `cmd/marketplace/` with its own `--mode=marketplace` role.

### Chat system rework + chat-driven admin commands
- Current chat is a best-effort broadcast; no history, no channels, no DM, no moderation.
- Rework: typed channels, server-side history, per-player permission check on chat-invoked admin commands.
- Once chat is typed and permission-aware, hook the cmdsys `ChatCallerSource` (reserved in the enum; not yet used) and add `pkg/universe/cmdsys_chat_adapter.go` — foundation is ready.
- RBAC grants persist in Postgres (`cmdsys.GrantStore` interface already abstracted).

### `mmoctl` standalone CLI
- Consumes `GET /commands` to list verbs + fetch schemas.
- Invokes via a new gRPC admin endpoint (not yet exposed) or the existing MeshControl stream with a mTLS-fenced admin peer class.
- Tab-completion via the JSON Schema.
- ~1 week.

### Admin web dashboard
- Reads `/commands` for the catalog, renders typed forms from JSON Schema.
- Tails `/metrics` and the audit log.
- Own project, own repo, TBD.

### mTLS / external auth + signed caller tokens
- Required before exposing any admin endpoint outside the cluster.
- cmdsys architecture explicitly left room: `Caller` field carries grants; the "re-validate on receive" pattern works today with cryptographic binding layered on.
- JWT or Macaroon-based signed tokens — pick one when it becomes a blocker.

### cmdsys v1.1 features (reserved in the plan)
- Streaming / long-running commands (dispatcher signature is compatible).
- Per-caller rate limiting (token bucket keyed on `caller.id`).
- Idempotency keys for destructive commands.
- Two-phase confirm for destructive commands (`cell.wipe`, `entity.delete`).
- Tab-completion pruning on permission.
- Groups / inheritance in RBAC (precedence resolver is built to accept a second grant source).

---

## Session landmarks (for context-compact handoff)

Commits on `feature/distributed-mesh` that shipped the above, most recent first:

| SHA | Description |
|---|---|
| `a04507e` | refactor(4node-basic): move DefaultSpawn + DisabledPartitionConfig into Config literal |
| `80e9940` | feat(universe): player spawn routing via SpawnResolver + DefaultSpawn |
| `3bdc34f` | fix(replication): exclude self netID from handoff farewell frame |
| `a97b8d1` | refactor(engine): remove dead ExecOnGameLoop / SetExecFunc / execFunc |
| `2790f3d` | docs: update CLAUDE.md for cmdsys + RunOnLoop architecture |
| `d486216` | refactor(engine): split oversized console_cmdsys.go and builtins.go |
| `55e2d46` | fix(4node-basic): bot handlers use RunOnLoop for ECS access |
| `f8e1644` | fix(engine): entity + config builtins use RunOnLoop per-handler |
| `f09643a` | feat(engine): RunOnLoop replaces PendingAdminCmds — eliminates deadlocks |
| `1365090` | fix(4node-basic): bot command handlers call OnLoop helpers directly |
| `2998312` | fix(cmdsys): dedupe help + honour sub-verbs for shimless groups |
| `82522ed` | fix(cmdsys): help walks the Registry directly — all commands now visible |
| `c4483e7` | fix(cmdsys): make coordinator builtins a fallback after OnConsoleReady |
| `383d304` | fix(cmdsys): support int / uint32 / uint64 field kinds |
| `2f1ea8c` | feat(cmdsys): GET /commands introspection endpoint |
| `d33ac25` | feat(cmdsys): migrate all commands to typed cmdsys + delete legacy shim |
| `6f8db8c` | feat(cmdsys): cross-process MeshControl transport |
| `b3ab221` | feat(cmdsys): console adapter + migrate engine builtins |
| `f8eae06` | refactor(cmdsys): address code quality review |
| `7cf4599` | fix(cmdsys): required fields default to required (spec alignment) |
| `af0d164` | fix(cmdsys): spec-compliance corrections from C1 review |
| `6ce0ed7` | feat(cmdsys): foundation package |

Branch tip is `a04507e`, green on `go test ./...` (31s e2e + 47s universe pass).

---

## Immediate next targets (pick one)

1. **`just distributed-space` recipe** — small, operator-facing, unblocks multi-process manual smoke.
2. **Distributed smoke test** — catches regressions on the dispatcher + SpawnResolver + cross-host handoff path in CI.
3. **Game-command unit tests** — per-verb coverage for `internal/game/commands/`.
4. **Marketplace rework plan** — start the design doc for the standalone marketplace service (own Postgres schema, own deployment story).
5. **Ship the branch** — merge `feature/distributed-mesh` into main and open a fresh branch for whatever's next.
