# Unify `node` Role Into `host` + Purge Stale Node Terminology + Proto "Node"→"Cell" Rename

## Context

The codebase has two cell-running roles that are functionally identical and only differ by deployment mode: `host` (in-process with the coordinator) and `node` (separate process dialing a remote coordinator). The duplication ripples out across the code:

- Operators see two roles in `--mode=` help text with no real difference between them.
- `host list` already shows both kinds in one table (distinguished only by a trailing `*` on the state column) — the unification already exists operationally.
- Internal identifiers are an inconsistent mix of "Node" (from pre-S1 legacy when "node" meant what's now "cell") and "Host": `PlayerLocation.NodeID` holds a host ID, `ActiveUserNode` returns a host ID, `PlayerRow.Node` column shows the host, and `cellListRow.Node` is a vestigial blank column that has no remaining writer.
- Tests, justfile recipes, CLAUDE.md, and design docs all have to explain the `host`/`node` distinction even though the distinction is an artifact of deployment plumbing.

The goal is one cell-running role (`host`) with deployment mode derived from whether `--coordinator-addr` is set. Eliminate the `RoleNode` constant, rename every internal "Node" identifier that actually means "host", delete the dead `Node` column, and purge stale mentions from docs + justfiles. A smuggled bug fix: the current world-factory gate assumes remote nodes don't need a world factory, but they do (the factory is called on CellAssign).

## End State

### Role matrix

```
--mode=all (default)                  → coordinator + host + gateway, single process
--mode=coordinator                    → pure control plane
--mode=coordinator,gateway            → control plane + embedded gateway, no cells
--mode=coordinator,host               → control plane + in-process cells
--mode=host --coordinator-addr=HOST:PORT    → REMOTE cell worker (was --mode=node)
--mode=host                           → ERROR: "host alone requires --coordinator-addr"
--mode=gateway --coordinator-addr=... → standalone gateway (unchanged)
```

Rules collapse from today's four-role set to three: `coordinator`, `host`, `gateway`. The "node cannot combine" and "host requires coordinator" rules both disappear; replaced by a single Build-time check that bare `host` needs `CoordinatorAddr`.

### Renames

- `RoleNode` constant → **deleted**.
- `PlayerLocation.NodeID` → `HostID`.
- `Coordinator.ActiveUserNode(username)` → `Coordinator.ActiveUserHost(username)` (return type unchanged).
- `notifySessionActive(username, nodeID string)` → `notifySessionActive(username, hostID string)` (param rename).
- `PlayerRow.Node` column → `Host` column.
- `cellListRow.Node` field → **deleted** (it's blank in live output; Host is the correct column).
- Log message templates: `"node: ready, awaiting CellAssign from %s"` → `"host: ready, awaiting CellAssign from %s"`, etc.
- Local `nodeID` variables that hold host IDs → `hostID`.
- Justfile recipes: `--mode=node` → `--mode=host`, log filenames `node-N.log` → `host-N.log`, variable names `node_a_id` / `node_b_id` → `host_a_id` / `host_b_id`.

### Pre-S1 "node means cell" legacy purge (Phase 4 — see below)

The S1 stage renamed the "node" (ECS simulation unit) concept to "cell" but left dozens of identifiers with "Node" in their names. They all carry cell-scoped IDs. We're cleaning this up too so the word "node" disappears from the live codebase entirely.

### Not changing (explicitly out of scope)

- Test host ID string constants (`test-node-0`, `test-node-3`, `space-node-0..2`) — they're arbitrary identifiers, not role names. Leave them as-is to keep git diff surface small.
- Test helper function names (`startS45Node`, `TestNode_DrainInbox_*`) — Phase 4 DOES rename these since they reference pre-S1 "node = cell"; see below.
- `examples/4node-basic/` directory name — stable example path referenced in external scripts and docs.
- Historical plan files in `docs/superpowers/plans/` and pre-2026-04 specs — they're historical records; leave.

## Approach

### Phase 1 — Role system rewrite (single commit: fundamental change)

[pkg/universe/roles.go](../../../pkg/universe/roles.go):
- Delete `RoleNode` const + its entry in `ParseRoles` + its entry in `Roles.String()`.
- Delete the "node cannot combine" exclusivity rule.
- Delete the "host requires coordinator" rule — move validation to `Coordinator.Build()` where `Config.CoordinatorAddr` is visible.
- Update the `unknown role` error message to drop "node" from the valid list.

[pkg/universe/coordinator.go](../../../pkg/universe/coordinator.go):
- New method: `func (c *Config) IsRemoteHost(roles Roles) bool` = `roles == Roles(RoleHost) && strings.TrimSpace(c.CoordinatorAddr) != ""`.
- In Build(): if `roles == Roles(RoleHost) && !isRemoteHost` → panic with the new error `"--mode=host alone requires --coordinator-addr=HOST:PORT (was --mode=node)"`.
- Every `roles.Has(RoleNode)` site:
  - Line 657-689 (RoleNode Build branch) → `if c.cfg.IsRemoteHost(roles)`.
  - Line 1220-1227 (startup log `case RoleNode`) → new case using the predicate, message stays "host: ready, awaiting CellAssign from %s".
  - Line 1545 (grpcBridge wrap) → `if c.cfg.IsRemoteHost(roles) || c.multiHost` (unify with the multi-host TestHost path at 807-827 so the two remote-ish paths don't diverge).
- Line 608-614 world-factory gate — **smuggled bug fix**: today carves out `RoleNode` from requiring `SetWorld/OnInit`, but remote hosts DO need the factory (cells instantiate on CellAssign). Rewrite to `if roles.Has(RoleHost) && c.worldFactory == nil && c.onInit == nil { panic(...) }`.

[pkg/mmokit/mmokit.go:323](../../../pkg/mmokit/mmokit.go#L323) — delete the `RoleNode` re-export.

[cmd/server/main.go:118-123](../../../cmd/server/main.go#L118-L123):
- `isPureGateway` — drop `!roles.Has(mmokit.RoleNode)` conjunct.
- `needsGameState = roles.Has(mmokit.RoleHost) || roles.Has(mmokit.RoleNode)` → just `roles.Has(mmokit.RoleHost)` (now covers remote hosts too since there's no RoleNode).

[pkg/universe/roles_test.go](../../../pkg/universe/roles_test.go):
- Delete: `{"node", RoleNode}`, `{"node,coordinator", ...}`, `{"node,host", ...}`, `{"node,gateway", ...}`, `{Roles(RoleNode), "node"}`.
- Move `{"host", "requires"}` and `{"host,gateway", "requires"}` from ParseRoles tests to a new `TestCoordinatorBuild_BareHostRequiresCoordAddr` in a build-level test file.
- Add: `TestParseRoles_BareHostValid` (bare `host` parses OK; the Build-time check enforces coord-addr).

[examples/4node-basic/main.go](../../../examples/4node-basic/main.go) + any bootstrap_test — drop `--mode=node` override test case, add `--mode=host --coordinator-addr=…` test case.

### Phase 2 — Identifier renames + dead column removal (single commit)

Must be a single commit because `Coordinator.ActiveUserNode` is consumed by [internal/game/commands/players.go:154](../../../internal/game/commands/players.go#L154) — splitting would break the build mid-series.

- [pkg/universe/coordinator.go](../../../pkg/universe/coordinator.go): `PlayerLocation.NodeID` field → `HostID`; `ActiveUserNode` → `ActiveUserHost`; `notifySessionActive` / `notifySessionDisconnected` parameter `nodeID` → `hostID`; update all internal `loc.NodeID` references.
- [pkg/universe/builtins_cluster.go](../../../pkg/universe/builtins_cluster.go), [pkg/universe/builtins_session.go](../../../pkg/universe/builtins_session.go): follow-through on any `.NodeID` reads.
- [internal/game/commands/players.go](../../../internal/game/commands/players.go): `PlayerRow.Node` field → `Host`; rename `ActiveUserNode` call sites; update the Node column assignment in the handler.
- [pkg/universe/builtins_cell.go](../../../pkg/universe/builtins_cell.go): delete `cellListRow.Node` field + its assignment (it's a blank column — confirmed by live smoke earlier in this branch).
- [pkg/universe/mesh_control_client.go](../../../pkg/universe/mesh_control_client.go) + coordinator.go startup/registration log templates: `"node: registered as %q ..."` → `"host: registered as %q ..."`, `"node: ready, awaiting CellAssign..."` → `"host: ready, awaiting CellAssign..."`.
- Comments mentioning "node mode" where they mean "remote host mode" → "remote host mode".

### Phase 3 — Docs + justfiles + help text (single commit)

[justfile](../../../justfile) (top-level `distributed-space` recipe):
- `--mode=node` → `--mode=host` (three occurrences: space-node-0/1/2).
- Log filenames `log/distributed-space/node-N.log` → `host-N.log`.
- Host ID strings `space-node-0/1/2` stay (they're operator-supplied identifiers).

[examples/4node-basic/justfile](../../../examples/4node-basic/justfile):
- Variables `node_a_id` / `node_b_id` → `host_a_id` / `host_b_id`.
- Two `--mode=node` recipe lines → `--mode=host`.
- Comment references to "host IDs" already — no change.

[pkg/universe/bootstrap.go:43-54](../../../pkg/universe/bootstrap.go#L43-L54) help text:
- `--mode` help: drop "node" from the listed options.
- `--coordinator-addr` help: "dial addr (host/gateway roles when running standalone)".
- `--host-id` help: "stable host identifier when running as remote host (empty = auto)".

[CLAUDE.md](../../../CLAUDE.md):
- Lines 76-94 role matrix + presets: rewrite to remove `node` role and its exclusivity rule; rewrite the preset list with the new `--mode=host --coordinator-addr=...` form.
- Line 122-124 `4node-basic` example command: `--mode=node` → `--mode=host --coordinator-addr=...`.
- Add a one-line note in the mesh section: "`CrossNodeAction.source_node_id` is a pre-S1 legacy proto field name that carries a cellID; not renamed yet."

[docs/superpowers/specs/2026-04-16-distributed-mesh-status.md](../specs/2026-04-16-distributed-mesh-status.md):
- Line ~69 "role-labelled prompt (`coordinator >`, `node >`, `gateway >`)" → "`coordinator >`, `host >`, `gateway >`".
- Line ~191 "missing node-level metrics" → "missing host-level metrics".
- Add an entry to "Immediate next targets" marking the node/host consolidation as completed (replaces the older `merge c.players into sessionRoutes` candidate that is now less critical since NodeID → HostID).

[README.md](../../../README.md) (repo root, if present):
- Replace "grid of nodes" / "knowledge of cells, nodes, or grid layout" with "grid of cells" / "knowledge of cells or grid layout".

Search for stragglers: `grep -rn 'RoleNode\|--mode=node\|"node"' pkg/ cmd/ internal/ examples/ justfile` must return zero matches at the end of this phase.

### Phase 4 — Pre-S1 "node means cell" legacy purge (single big commit)

Scope: 158 references across 29 files (confirmed via grep). The S1 rename (Node→Cell) missed every identifier that contained "Node" in its name; they all carry cell-scoped IDs. This phase makes the rename complete.

**Proto rename + regen:**

[proto/meshpb/mesh.proto](../../../proto/meshpb/mesh.proto):
- Message `CrossNodeAction` → `CrossCellAction`.
- Field `source_node_id` → `source_cell_id`.
- Update the oneof slot comment on `MeshFrame` that references `CrossNodeAction`.
- Any other `*node*` field names — audit the whole proto file (likely just CrossNodeAction).

Run `buf generate` (or `just proto`) to regenerate [gen/go/meshpb/mesh.pb.go](../../../gen/go/meshpb/mesh.pb.go). Don't hand-edit generated code.

**Go type / method / field renames (pkg/universe):**

- `CrossNodeAction` (type at [pkg/universe/action.go](../../../pkg/universe/action.go)) → `CrossCellAction`.
- `NodeMessage` (envelope type at [pkg/universe/message.go](../../../pkg/universe/message.go)) → already aliases to `CellMessage`? Audit: if `CellMessage` exists and `NodeMessage` is the same, delete `NodeMessage`. If distinct, rename to `CellMessage`.
- `NodeBridge` interface (in [pkg/universe/bridge.go](../../../pkg/universe/bridge.go)) → `CellBridge`. Rename the `Bridge` type alias if present.
- `NodeOwnerAtPos()` method → `CellOwnerAtPos()`.
- Methods on `*Coordinator`:
  - `NodeAtPosition(worldX, worldY)` ([coordinator.go:376](../../../pkg/universe/coordinator.go#L376)) → delete (use existing `CellAtPosition`).
  - `nodeLoad(cellID)` ([coordinator.go:2340](../../../pkg/universe/coordinator.go#L2340)) → `cellLoad(cellID)`.
  - `allNodeLoads()` ([coordinator.go:2351](../../../pkg/universe/coordinator.go#L2351)) → `allCellLoads()`.
- Bridge API parameter names: `destNodeID` → `destCellID`, `sourceNodeID` → `sourceCellID` throughout [bridge.go](../../../pkg/universe/bridge.go), [cell_bridge_impl.go](../../../pkg/universe/cell_bridge_impl.go), [grpc_bridge.go](../../../pkg/universe/grpc_bridge.go), [boundary_system.go](../../../pkg/universe/boundary_system.go).
- `WorldBase.NodeID()` method ([world_base.go:274](../../../pkg/universe/world_base.go#L274)) → `CellID()` (returns a cellID like "cell_0_0").
- `HandleCrossNodeAction` (interface method on `GameWorld` in [world.go](../../../pkg/universe/world.go) / [mmokit.go](../../../pkg/mmokit/mmokit.go)) → `HandleCrossCellAction`.

**Go component + system renames:**

- [pkg/component/core.go](../../../pkg/component/core.go) `Replica.SourceNodeID` → `SourceCellID`.
- [pkg/system/auto_replicator.go](../../../pkg/system/auto_replicator.go) + [pkg/system/replication_test.go](../../../pkg/system/replication_test.go) — update callers.

**mmokit facade ([pkg/mmokit/mmokit.go](../../../pkg/mmokit/mmokit.go)):**

- `CrossNodeAction` alias → `CrossCellAction`.
- All comments mentioning "cross-node" → "cross-cell".
- `HandleCrossNodeAction` interface → `HandleCrossCellAction`.

**Game + example callers:**

- [internal/game/game.go](../../../internal/game/game.go), [system_ability.go](../../../internal/game/system_ability.go), [system_mining.go](../../../internal/game/system_mining.go), [world.go](../../../internal/game/world.go) — update every `CrossNodeAction` reference, `SourceNodeID` field access, `HandleCrossNodeAction` implementations.
- [examples/slither/world.go](../../../examples/slither/world.go), [system_eating.go](../../../examples/slither/system_eating.go) — same.
- [examples/4node-basic/mesh_e2e_test.go](../../../examples/4node-basic/mesh_e2e_test.go) — update test references.

**Test renames:**

- `pkg/universe/universe_test.go`: `TestNode_DrainInbox_*` → `TestCell_DrainInbox_*` (~7 test functions); `TestCoordinator_NodeOwnership` → `TestCoordinator_CellOwnership`; `TestBridge_NodeOwner` → `TestBridge_CellOwner`; `TestBridge_RelayChatToOtherNodes` → `TestBridge_RelayChatToOtherCells`.
- `pkg/universe/s4_5_cross_node_test.go` → rename file to `s4_5_cross_host_test.go`; `TestS45CrossNodeBorderFrameAndHandoff` → `TestS45CrossHostBorderFrameAndHandoff`; helper `startS45Node` → `startS45Host`.
- `pkg/universe/s4_control_plane_test.go`: `TestS4CoordNodeRegistrationAndAssignment` → `TestS4CoordHostRegistrationAndAssignment`.
- `internal/game/coordinator_test.go`: `TestNewCoordinator_Creates9Nodes` → `TestNewCoordinator_Creates9Cells`.
- `pkg/metrics/node_metrics_test.go`: `TestCellMetrics_InterNodeCounters` → `TestCellMetrics_InterCellCounters`. File rename: `pkg/metrics/node_metrics.go` + test file → `pkg/metrics/cell_metrics.go` (if the type inside is `CellMetrics`, the filename lies).

**Comment + log string cleanup:**

- Comments mentioning "cross-node" where they mean "cross-cell" → rename.
- Log lines in [grpc_bridge.go](../../../pkg/universe/grpc_bridge.go) and friends with "node X → node Y" → "cell X → cell Y".

**CLAUDE.md updates:**

- Rewrite the "Server Meshing" section to remove every remaining "node" reference (the pre-S1 uses). Keep "cell" as the single term for a simulation unit and "host" as the single term for a process running cells.
- Remove the earlier "dangling CrossNodeAction legacy" note — it's no longer dangling.
- README.md: "grid of nodes" / "cross-node transfers" → "grid of cells" / "cross-cell transfers".

**Verification specific to Phase 4:**

```bash
just proto    # or: buf generate
go vet ./...
go test -count=1 ./...
```

Full test suite must pass — this is a rename, not a behavior change. If any test fails, the rename missed a caller.

**Final grep must return zero in Go source (but will still hit intentional residues):**

```bash
grep -rn 'CrossNodeAction\|NodeMessage\|NodeBridge\|NodeOwner\|SourceNodeID\|destNodeID\|sourceNodeID\|NodeAtPosition\|HandleCrossNodeAction' \
    --include='*.go' --include='*.proto' pkg/ cmd/ internal/ examples/ proto/
```

Intentional residues that will still match `grep "node"` repo-wide:
- Test host ID strings (`test-node-0`, `space-node-0..2`) — arbitrary identifiers.
- `examples/4node-basic/` directory name.
- Historical plan/design documents under `docs/superpowers/` from before 2026-04-16.
- Any generated Go field comments in `gen/go/meshpb/mesh.pb.go` that reference old proto field names — these come from `buf generate` and are regenerated correctly.

**Scope estimate:** ~200 LOC actual code change (renames) + ~50 LOC test updates + proto regen. Mechanical, no behavior change. Single commit because proto regen touches every downstream file that references `CrossCellAction`.

## Critical files

**Modified:**
- [pkg/universe/roles.go](../../../pkg/universe/roles.go) — role constant, parse rules, String()
- [pkg/universe/roles_test.go](../../../pkg/universe/roles_test.go) — drop node cases, add bare-host case
- [pkg/universe/coordinator.go](../../../pkg/universe/coordinator.go) — IsRemoteHost predicate, Build() rewiring, rename fields/methods
- [pkg/universe/builtins_cell.go](../../../pkg/universe/builtins_cell.go) — remove dead Node column
- [pkg/universe/builtins_cluster.go](../../../pkg/universe/builtins_cluster.go), [builtins_session.go](../../../pkg/universe/builtins_session.go) — PlayerLocation.HostID reads
- [pkg/universe/mesh_control_client.go](../../../pkg/universe/mesh_control_client.go) — log message rename
- [pkg/universe/bootstrap.go](../../../pkg/universe/bootstrap.go) — flag help text
- [pkg/universe/bootstrap_test.go](../../../pkg/universe/bootstrap_test.go) — mode override test case update
- [pkg/mmokit/mmokit.go](../../../pkg/mmokit/mmokit.go) — drop RoleNode re-export
- [cmd/server/main.go](../../../cmd/server/main.go) — `needsGameState` + `isPureGateway` simplify
- [internal/game/commands/players.go](../../../internal/game/commands/players.go) — PlayerRow.Host, ActiveUserHost caller
- [internal/game/lifecycle.go](../../../internal/game/lifecycle.go) — PlayerLocation.HostID caller (if any)
- [justfile](../../../justfile) + [examples/4node-basic/justfile](../../../examples/4node-basic/justfile) — recipe updates
- [CLAUDE.md](../../../CLAUDE.md) — role matrix rewrite
- [docs/superpowers/specs/2026-04-16-distributed-mesh-status.md](../specs/2026-04-16-distributed-mesh-status.md) — status updates
- [README.md](../../../README.md) — outdated references

**New:**
- No new files.

**Reuses:**
- `Coordinator.CmdDispatcher` / `InvokeInternal` from the recent cmdsys work — no changes needed.
- `hostRegistry` API (`LiveHosts`, `Get`, `HostForCell`) — unchanged. It already uses unified terminology.
- Existing `Config.TestHosts` multi-host-in-process pattern — untouched (uses RoleHost only; unaffected).

## Commit plan (4 commits)

1. **`refactor(universe): unify node role into host + fix world-factory gate`** (Phase 1)
   - Delete `RoleNode`, relax role rules, add `Config.IsRemoteHost(roles)` predicate.
   - Move `host requires coord` check from parse to Build().
   - Replace every `roles.Has(RoleNode)` site. Unify grpcBridge wrap with the multi-host path.
   - Fix the world-factory gate so remote hosts also require `SetWorld/OnInit`.
   - Update `roles_test.go` + `bootstrap_test.go`; drop `RoleNode` re-export from mmokit.
   - Update `cmd/server/main.go` `needsGameState` / `isPureGateway`.

2. **`refactor(universe): rename Node→Host identifiers where they mean host`** (Phase 2)
   - `PlayerLocation.NodeID` → `HostID`; `ActiveUserNode` → `ActiveUserHost`.
   - `notifySessionActive` / `notifySessionDisconnected` param rename.
   - `PlayerRow.Node` → `Host`; delete dead `cellListRow.Node` field.
   - Log-message renames `"node: ..."` → `"host: ..."`.
   - Local `nodeID` vars that hold hosts → `hostID`.

3. **`refactor(universe,proto): finish S1 rename — Node→Cell in proto + bridge + component + systems + tests`** (Phase 4, the big one)
   - Proto: `CrossNodeAction` → `CrossCellAction`, `source_node_id` → `source_cell_id`. Run `buf generate`.
   - Go types: `CrossNodeAction` → `CrossCellAction`, `NodeMessage` → `CellMessage` (or delete if duplicate), `NodeBridge` → `CellBridge`, `Replica.SourceNodeID` → `SourceCellID`.
   - Methods: `HandleCrossNodeAction` → `HandleCrossCellAction`, `NodeOwnerAtPos` → `CellOwnerAtPos`, `nodeLoad`/`allNodeLoads` → `cellLoad`/`allCellLoads`, `WorldBase.NodeID()` → `CellID()`.
   - Delete redundant `Coordinator.NodeAtPosition` (use existing `CellAtPosition`).
   - Bridge API param renames: `destNodeID` → `destCellID`, `sourceNodeID` → `sourceCellID`.
   - Test renames: `TestNode_*` → `TestCell_*` across `universe_test.go`, `TestS45CrossNode*` → `TestS45CrossHost*` (file rename too), `startS45Node` → `startS45Host`, `TestNewCoordinator_Creates9Nodes` → `CreatesCells`, `TestCellMetrics_InterNodeCounters` → `InterCellCounters`.
   - File renames: `pkg/universe/s4_5_cross_node_test.go` → `s4_5_cross_host_test.go`; `pkg/metrics/node_metrics.go` → `cell_metrics.go` (if the type inside is `CellMetrics`).
   - Update callers: `internal/game/{game,system_ability,system_mining,world}.go`, `examples/slither/{world,system_eating}.go`, `examples/4node-basic/mesh_e2e_test.go`, `pkg/mmokit/mmokit.go` aliases + comments.
   - CLAUDE.md: rewrite Server Meshing section to drop every remaining "node" reference (pre-S1 uses). "cell" = simulation unit; "host" = process running cells.
   - README.md: "grid of nodes" / "cross-node transfers" → "grid of cells" / "cross-cell transfers".

4. **`docs+chore: purge stale node terminology (justfile, CLAUDE.md, status, help text)`** (Phase 3)
   - Justfile recipe mode flags + log filenames + variable names.
   - Bootstrap flag help text.
   - CLAUDE.md role matrix rewrite (the `coordinator`/`host`/`gateway` three-role description).
   - Status doc prompt + metrics terminology.
   - Final grep must be clean (see Verification).

**Commit ordering rationale:** Phase 1 first because it's the fundamental role-system change and the other phases reference the new `RoleHost` model. Phase 2 (host-role renames) is small + self-contained. Phase 4 (proto + pre-S1 legacy) is large + mechanical; landing it before the docs commit means the docs can reference the final names without forward-looking qualifiers. Phase 3 (docs) lands last so it describes the final state.

## Verification

**Per-commit build + test:**

```bash
go vet ./...
go test -count=1 ./pkg/cmdsys/... ./pkg/universe/... ./pkg/engine/... ./internal/... ./cmd/... ./examples/4node-basic/...
```

Must stay green through each commit. Especially: `TestS6HandoffAcrossNodes` (gateway + cross-host handoff), S7 family (split/merge/migrate), `mesh_e2e_test.go` (31s 4node e2e), the Phase E inspection tests from the prior admin-command work.

**Role-parse specific:**

```bash
go test -run 'TestParseRoles|TestCoordinatorBuild_BareHost' ./pkg/universe/
```

New tests cover: bare `host` parses OK, bare `host` without coord-addr errors at Build, `host --coordinator-addr=X` works end-to-end.

**Cleanup check (final commit):**

```bash
grep -rn 'RoleNode\|--mode=node' pkg/ cmd/ internal/ examples/ justfile examples/4node-basic/justfile
grep -rn '"node"' pkg/universe/roles*.go pkg/universe/coordinator.go
```

Zero matches expected. `grep "node"` repo-wide will still return hits (comments referring to the pre-S1 "node = cell" legacy, test-host ID strings, proto `source_node_id`, directory name `4node-basic`) — those are intentional leaves documented in "Not changing".

**Live smoke on `just distributed-space`:**

1. Start the recipe. All 5 panes should show prompts `coordinator >`, `host >` (x3), `gateway >`.
2. From coord: `host list` shows 3 remote hosts `space-node-0..2` in `Live` state (note: ID strings preserved; role identity is what changed).
3. From coord: `cluster overview` shows the mesh with role names printed as `coordinator,host,gateway` / etc.
4. `player tp xennion 100 100` still works (cross-host dispatch exercises the full RoutePlayerOwner path).
5. `player info xennion` still works.
6. `cell list --live` fan-out returns per-host cell snapshots (uses `isRemoteHost` branch on each node).

**Migration error check:**

Run `bin/server --mode=node` directly. Must print a clear error: `"--mode=node" is removed; use "--mode=host --coordinator-addr=HOST:PORT"`.

## Risks

- **Muscle memory**: anyone running the repo this week will have `--mode=node` in their tmux recall / shell history. Hard-error with a clear migration hint is the right tradeoff (API is ~1 week old, internal only; no external consumers).
- **Smuggled bug fix**: un-carving-out the world-factory gate could break callers that create a coord with `TestHosts` but no world factory. Grep already confirmed every TestHost caller sets either `SetWorld` or `OnInit`; the guard would fire only on genuinely misconfigured coords.
- **Grep completeness**: the final cleanup check has a few intentional exceptions (listed above). Add them to a comment in Commit 3's message so future auditors don't re-file them as "stale".
- **Proto field `source_node_id` left intact**: creates a dangling "node" reference. Justified: renaming proto is a separate, larger refactor that rips through Replica / CrossNodeAction / bridge.SendAction / ~20 game-side call sites. Deferred with a CLAUDE.md note.
- **Two commits with mid-series broken states**: Phases 1 and 2 each compile on their own. Phase 2 depends on Phase 1 (ActiveUserHost rename assumes RoleNode is gone so naming is consistent). Phase 3 is pure docs/config and can't break builds.
