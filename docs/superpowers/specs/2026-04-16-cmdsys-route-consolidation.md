# cmdsys Route Consolidation

**Shipped:** 2026-04-16 on `feature/distributed-mesh`.

## Problem

Typing `entity.tp xennion 50 50` at the distributed coordinator console failed with `cmdsys: route not yet wired (future phase)`. Four context-sensitive route kinds — `RoutePlayerOwner`, `RouteEntityOwner`, `RouteSpecificHost`, `RouteSpecificCell` — never worked from the REPL.

## Root cause

The C3 foundation phase (commit `1171e9a` "feat(cmdsys): cross-process MeshControl transport") shipped the wire format and the args-aware resolver (`meshRouteResolver.ResolveWithArgs`) but **didn't wire it into `Dispatcher.Invoke`**. Instead a parallel entry point, `Coordinator.InvokeCmd` + `invokeCmdTargets`, was added that only the C3 integration tests called. The console adapter calls `Dispatcher.Invoke` directly, which went through `RouteResolver.Resolve(route, verb)` — a method whose signature had no args parameter, so context-sensitive routes returned `ErrNotYetWired`.

The original author's own comment at [coordinator.go:486-503](pkg/universe/coordinator.go#L486-L503) (deleted in this change) documented the bypass as a deliberate workaround to avoid changing the `RouteResolver` interface signature.

A secondary bug compounded it: in distributed mode the node's `Players.SessionCallbacks` fire `notifySessionActive` on the node process's own in-memory `Coordinator` struct — not the remote coordinator that actually dispatches commands. The real coord's `c.players` username→host index stayed empty. Even after the resolver wiring was fixed, `ActiveUserNode("xennion")` returned `""` because the coord had never been told xennion was active on `space-node-2`.

## Change

Two orthogonal fixes:

### 1. Unified dispatch path

- `RouteResolver.Resolve` signature extended to `Resolve(route RouteKind, verb string, args any)`. Parsed args travel through the resolver so context-sensitive routes can extract `Username`/`NetID`/`HostID`/`CellID` by reflection.
- `meshRouteResolver.ResolveWithArgs` folded into `Resolve`. Single switch handles all route kinds.
- `Dispatcher.Invoke` passes `argsVal` (already parsed by `coerceArgs`) into the resolver.
- `Coordinator.InvokeCmd`, `invokeCmdTargets`, `unmarshalResult` deleted (~130 lines). Four test call sites in `cmdsys_meshcontrol_test.go` migrated to `coord.CmdDispatcher().Invoke(...)`.
- Colocation short-circuit stays in `meshControlTransport.Send` ([cmdsys_transport.go:134-140](pkg/universe/cmdsys_transport.go#L134-L140)) which already routes colocated hostIDs through `sendLocal` → `InvokeLocal`. The dispatcher never needed its own copy.

### 2. Sync `c.players` from gateway announcements

- `SessionAnnounce` handler on `mesh_control_server.go` now calls `notifySessionActive(username, hostID)` on new announcements and `notifySessionRemoved(username)` on tombstones, keeping `c.players` in sync with `sessionRoutes` on the coordinator process.
- `notifyPlayerMigrated` reads the username from `sessionRoutes` before `Migrate` and calls `notifySessionActive(username, destHost)` after the migration so `ActiveUserNode` returns the new host after cross-host handoff.

### 3. Collateral cleanup

- Dead `getPlayerNode` and `getCellOwner` methods in `coordinator.go` removed (unused since predecessor refactors).
- Dead `marshalResult` helper in `cmdsys_transport.go` removed (only caller was `invokeCmdTargets`).
- Unused `encoding/json` and `reflect` imports dropped from `coordinator.go`.
- Single-case `select` in the cancel-monitor goroutine simplified to a direct `<-ctx.Done()`.
- `sendLocal` comment documents that this path preserves caller `Source` (unlike `executeCommandRequest` which overwrites to `SourceMeshControl`).

## Files touched

- [pkg/cmdsys/route.go](../../../pkg/cmdsys/route.go) — interface signature + test resolvers
- [pkg/cmdsys/dispatcher.go](../../../pkg/cmdsys/dispatcher.go) — pass argsVal to resolver
- [pkg/universe/cmdsys_resolver.go](../../../pkg/universe/cmdsys_resolver.go) — folded `ResolveWithArgs` into `Resolve`
- [pkg/universe/coordinator.go](../../../pkg/universe/coordinator.go) — deleted `InvokeCmd` + `invokeCmdTargets` + dead helpers; `notifyPlayerMigrated` syncs `c.players`
- [pkg/universe/cmdsys_transport.go](../../../pkg/universe/cmdsys_transport.go) — deleted `marshalResult`; simplified cancel monitor
- [pkg/universe/mesh_control_server.go](../../../pkg/universe/mesh_control_server.go) — `SessionAnnounce` handler syncs `c.players`
- [pkg/universe/cmdsys_meshcontrol_test.go](../../../pkg/universe/cmdsys_meshcontrol_test.go) — 4 test call sites migrated

## Verification

- `go vet ./...` clean.
- `go test -count=1 ./pkg/cmdsys/... ./pkg/universe/... ./cmd/server/... ./examples/4node-basic/... ./internal/...` green (47s universe, 31s 4node-basic e2e).
- `grep -rn "InvokeCmd\|ResolveWithArgs\|invokeCmdTargets" pkg/ cmd/ internal/ examples/` returns zero matches — no legacy entry points remain.
- **Live smoke** on `just distributed-space` (5-pane tmux with 3 nodes):
  - `entity.tp <offline-user> 50 50` → `ErrRouteNoOwner` (previously `ErrNotYetWired`) — resolver fires, user not found, correct error.
  - `entity.tp xennion 50 50` with xennion logged in → teleport succeeds, confirmed via client position update.
  - Cross-cell teleport crosses host boundaries cleanly — exercises the full `RoutePlayerOwner` → `ActiveUserNode` → MeshControl `CommandRequest` → `InvokeLocal` → `PlayerMigrated` round-trip.
