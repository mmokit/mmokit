# Role Separation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the god-object `Coordinator` into a composed `Process` with role-scoped planes (`ControlPlane`, `Host`, `Gateway`); unify cell-operation commit paths via a `hostOps` abstraction; fix the three open Stage-1 bugs as natural consequences; drop the `TestHosts` legacy; rename `Coordinator`→`Process` with `mmokit.New(Config)` as the entry point.

**Architecture:** Eight sequential phases. Each phase leaves `main` compile-green + full-suite-green (`go test ./pkg/...`). Phases 1-2 are additive groundwork; Phases 3-4 collapse local/remote branches through the new `hostOps` interface and add two new MeshControl messages (`CellReleaseAndAck`, `CellRename`, `HostOpAck`); Phase 5 moves Topology to ControlPlane; Phase 6 drops TestHosts; Phases 7-8 rename and delete the Coordinator wrapper.

**Tech Stack:** Go 1.22+, existing `pkg/universe/` (Coordinator, HostRegistry, meshControlServer, assignmentEngine), gRPC (already in use), protobuf via `buf generate`. No new dependencies.

**Reference files (read before starting):**
- `docs/superpowers/specs/2026-04-18-role-separation-design.md` — the spec
- `docs/superpowers/specs/2026-04-18-dual-topology-test-harness-design.md` — Stage 1 (the safety net)
- `pkg/universe/coordinator.go` — today's god object (lines 190-320 show the struct)
- `pkg/universe/cell_transfer_commit.go` — the commit paths that get unified
- `pkg/universe/cluster_fixture_test.go` + `cluster_fixture_distributed_test.go` — the test harness

**Safety contract for every task:**
1. Before starting: confirm `git status` is clean.
2. Implement the task's code changes.
3. Run `go vet ./pkg/universe/...` — must be clean.
4. Run `go test ./pkg/universe/ -count=1 -timeout 600s` — must pass.
5. Commit with the exact message specified in the task.
6. Every 3-5 tasks: run `go test ./pkg/universe/ -count=2 -timeout 900s` as a stability sanity check.

If any task's tests start failing when previously passing, treat as regression — investigate before continuing. Do NOT relax fixture workarounds; the goal is shrinking them, not growing them.

---

## File structure

**New files:**
- `pkg/universe/control_plane.go` — `ControlPlane` struct + methods (created in Phase 1)
- `pkg/universe/host_ops.go` — `hostOps` interface + local/remote impls + `hostProxy` (created in Phase 3)

**Modified files (scope of changes given per phase):**
- `pkg/universe/coordinator.go` — today's god object, shrinks across phases; deleted in Phase 8
- `pkg/universe/host.go` — expanded to hold all host-role state
- `pkg/universe/gateway.go` — expanded to hold all gateway-role state
- `pkg/universe/cell_transfer_commit.go` — commit paths collapse through `hostProxy`
- `pkg/universe/coord_assignment.go` — topology rebuild hooks
- `pkg/universe/mesh_control_server.go` — `HostOpAck` routing
- `pkg/universe/mesh_control_client.go` — `CellRename` + `CellReleaseAndAck` handlers
- `proto/meshpb/mesh.proto` — new messages
- `pkg/mmokit/*.go` — re-exports
- `examples/4node-basic/*.go` + `examples/slither/*.go` — game-side renames
- `internal/game/*.go` + `internal/bot/*.go` — same
- `pkg/universe/cluster_fixture_test.go` + `cluster_fixture_distributed_test.go` — fixture tracks API changes

---

# Phase 1: Plane structs as internal holders

**Goal:** Create `ControlPlane`, expand existing `Host` and `Gateway` structs with the fields they'll own. `Coordinator` gets pointer accessors to them but keeps existing raw fields. Pure groundwork — zero behavioral change.

## Task 1.1: Create ControlPlane skeleton

**Files:**
- Create: `pkg/universe/control_plane.go`

- [ ] **Step 1: Create the file**

```go
package universe

import (
	stdnet "net"

	"github.com/zenion/mmokit/pkg/logger"
)

// ControlPlane holds the state belonging to the RoleCoordinator role.
// Always present on every Process (even bare RoleHost needs a control
// client, and RoleGateway-only still tracks sessionRoutes). Fields
// migrate here from Coordinator across Phases 1-5.
type ControlPlane struct {
	log *logger.Logger

	// Populated in Phase 1 — mirror of Coordinator fields, same
	// pointers. After Phase 2 these become authoritative and
	// Coordinator's raw fields are unexported.
	hostRegistry    *HostRegistry
	gatewayRegistry *GatewayRegistry

	// Control-stream infra. Populated by coord.startControlPlane().
	controlServer     *meshControlServer
	controlGrpcServer interface{} // grpc.Server — typed loosely during Phase 1 to keep imports minimal
	controlListener   stdnet.Listener

	// Remote-host client (populated when this process has only RoleHost).
	controlClient *meshControlClient

	// Assignment engine (populated with control server).
	assignmentEngine *assignmentEngine
}

// newControlPlane allocates a ControlPlane with the logger captured.
// Registries and server references are wired during coord.Build().
func newControlPlane(log *logger.Logger) *ControlPlane {
	return &ControlPlane{log: log}
}
```

Use the typed `*grpc.Server` instead of `interface{}` — easier to grep later. Here's the corrected file content:

```go
package universe

import (
	stdnet "net"

	"google.golang.org/grpc"

	"github.com/zenion/mmokit/pkg/logger"
)

// ControlPlane holds the state belonging to the RoleCoordinator role.
// Always present on every Process. Fields migrate here from Coordinator
// across Phases 1-5 of the role-separation refactor.
type ControlPlane struct {
	log *logger.Logger

	hostRegistry    *HostRegistry
	gatewayRegistry *GatewayRegistry

	controlServer     *meshControlServer
	controlGrpcServer *grpc.Server
	controlListener   stdnet.Listener

	controlClient *meshControlClient

	assignmentEngine *assignmentEngine
}

func newControlPlane(log *logger.Logger) *ControlPlane {
	return &ControlPlane{log: log}
}
```

Use this second version. Discard the interface{} sketch above.

- [ ] **Step 2: Add `Control` field on Coordinator**

In `pkg/universe/coordinator.go`, find the `Coordinator` struct (line 190). After the `Log *logger.Logger` field (around line 197), add:

```go
	// Control holds RoleCoordinator state. Phase 1 wiring: pointer to
	// a ControlPlane whose fields mirror the raw Coordinator fields
	// below. Phase 2 migration: callers move from coord.hostRegistry
	// to coord.Control.hostRegistry, then the raw fields unexport.
	Control *ControlPlane
```

- [ ] **Step 3: Initialize `Control` in `NewCoordinator`**

In `pkg/universe/coordinator.go`, find `NewCoordinator` (line 281). After the `c := &Coordinator{...}` literal (around line 315), before `c.orchestrator = newCellTransferOrchestrator(c)`:

```go
	c.Control = newControlPlane(c.Log)
```

- [ ] **Step 4: Mirror hostRegistry into Control during Build()**

In `pkg/universe/coordinator.go`, find the control-plane startup block (grep for `c.hostRegistry = NewHostRegistry(c.Log)` — around line 1047). After that line AND after `c.gatewayRegistry = NewGatewayRegistry(c.Log)`:

```go
	c.Control.hostRegistry = c.hostRegistry
	c.Control.gatewayRegistry = c.gatewayRegistry
```

Do the same for `c.controlServer`, `c.controlGrpcServer`, `c.controlListener`, `c.assignmentEngine` — wherever each is assigned to the Coordinator, mirror the assignment onto `c.Control.X`.

For `c.controlClient` — find where it's set (grep `c.controlClient = newMeshControlClient`) and after that line add:

```go
	c.Control.controlClient = c.controlClient
```

- [ ] **Step 5: Verify compile + run tests**

Run: `go vet ./pkg/universe/...`
Expected: no output.

Run: `go test ./pkg/universe/ -count=1 -timeout 600s`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/control_plane.go pkg/universe/coordinator.go
git commit -m "$(cat <<'EOF'
refactor(universe): introduce ControlPlane struct (Phase 1/8)

Adds a ControlPlane struct holding the coord-plane field set. Coordinator
gains a c.Control pointer mirroring the raw fields. Callers continue to
use c.hostRegistry etc. directly — Phase 2 migrates them.

Pure groundwork; no behavioral change. Part of the role-separation
refactor (docs/superpowers/specs/2026-04-18-role-separation-design.md).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 1.2: Expand Host struct for host-plane fields

**Files:**
- Modify: `pkg/universe/host.go`
- Modify: `pkg/universe/coordinator.go`

**Goal:** Existing `Host` struct gains the fields that currently live on `Coordinator` but logically belong on a host (worldFactory, systemDefs, netIDAlloc, hostExecutor, vcm). Initialized during Build() from the Coordinator's values. Both locations hold the same pointer during Phase 1.

- [ ] **Step 1: Add fields to Host**

In `pkg/universe/host.go`, find the `Host` struct (grep `^type Host struct`). Add these fields at the end of the struct:

```go
	// Host-plane state. Populated during Build() from Coordinator's
	// corresponding fields. Phase 2 migration makes these
	// authoritative; Phase 6 drops Coordinator's copies.
	netIDAlloc    *NetIDAllocator
	systemDefs    []engine.SystemDef
	worldFactory  func(base *WorldBase) GameWorld
	onInit        func(w *WorldBase)
	executor      *cellTransferExecutor
	vcm           *VirtualConnManager
```

Add `"github.com/zenion/mmokit/pkg/engine"` to the import list if not already present.

- [ ] **Step 2: Populate during Build()**

In `pkg/universe/coordinator.go`, find where `c.Hosts[hid] = h` happens in both the `TestHosts` branch (around line 800) and the remote-host branch (grep `buildRemoteHost`, line 932). Immediately after each host is inserted into `c.Hosts`, mirror the Coordinator's host-plane fields onto the host:

For the local-host branch:
```go
				h.netIDAlloc = c.netIDAlloc
				h.systemDefs = c.systemDefs
				h.worldFactory = c.worldFactory
				h.onInit = c.onInit
				h.executor = c.hostExecutors[hid]
				h.vcm = c.vcm
```

For `buildRemoteHost`:
```go
	host.netIDAlloc = c.netIDAlloc
	host.systemDefs = c.systemDefs
	host.worldFactory = c.worldFactory
	host.onInit = c.onInit
	host.executor = c.hostExecutors[hostID]
	host.vcm = c.vcm
```

- [ ] **Step 3: Verify compile + tests**

Run: `go vet ./pkg/universe/...`
Expected: clean.

Run: `go test ./pkg/universe/ -count=1 -timeout 600s`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/host.go pkg/universe/coordinator.go
git commit -m "$(cat <<'EOF'
refactor(universe): expand Host struct with host-plane fields (Phase 1/8)

Host gains netIDAlloc/systemDefs/worldFactory/onInit/executor/vcm. These
mirror the Coordinator fields during Phase 1 so callers can still use
the Coordinator path. Phase 2 migrates callers to Host.X; later phases
drop Coordinator's copies.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 1.3: Expand Gateway struct for gateway-plane fields

**Files:**
- Modify: `pkg/universe/gateway.go`
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Add fields to Gateway**

In `pkg/universe/gateway.go`, find the `Gateway` struct (grep `^type Gateway struct`). Add:

```go
	// Gateway-plane state mirroring Coordinator fields during Phase 1.
	loginSvc_      *loginService // underscored until Phase 2 migrates callers
	spawnResolver_ SpawnResolver
	sessionRoutes_ *sessionRoutes
	httpServer_    *http.Server
```

Underscore suffixes are a parking-lot technique to avoid Go's "field already declared" collisions if Gateway already has fields with those names (grep before adding). If existing Gateway already has e.g. `loginSvc` — skip adding and reuse the existing one. Otherwise add as-is (no underscore).

**In practice:** Gateway already has some of these. Grep first:
```bash
grep -n "loginSvc\|spawnResolver\|sessionRoutes\|httpServer" pkg/universe/gateway.go
```
Reuse whatever exists; add only what's missing.

Add `"net/http"` to imports if not present.

- [ ] **Step 2: Populate Gateway fields during Build()**

For each field that isn't already populated in Gateway construction (find where `c.gateway = &Gateway{...}` happens — around `coordinator.go:989`), add the assignment. Example pattern:

```go
	c.gateway.sessionRoutes_ = c.sessionRoutes  // if not already wired
	c.gateway.httpServer_ = c.httpServer
```

If the field already exists on Gateway and is populated, leave it alone.

- [ ] **Step 3: Verify**

Run: `go vet ./pkg/universe/...` — clean.
Run: `go test ./pkg/universe/ -count=1 -timeout 600s` — PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/gateway.go pkg/universe/coordinator.go
git commit -m "$(cat <<'EOF'
refactor(universe): expand Gateway struct with gateway-plane fields (Phase 1/8)

Only adds fields not already on Gateway. Populated from Coordinator
during Build(). Phase 2 migrates callers.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 1.4: Full-suite stability check

- [ ] **Step 1: Run suite 2× back-to-back**

Run: `go test ./pkg/universe/ -count=2 -timeout 1200s`
Expected: PASS both iterations.

If either fails, the Phase 1 wiring is buggy — investigate before continuing.

- [ ] **Step 2: No commit required** (sanity check only).

---

# Phase 2: Canonical accessors + peek-caller migration

**Goal:** Add accessor methods on `ControlPlane` and `Host`. Migrate every caller of the raw maps (`coord.cellToHostMap`, `coord.Hosts[h].CellByID`, `coord.Cells[key]`) to use accessors. Unexport the raw maps as the final step — after this the bug class is structurally impossible.

## Task 2.1: Add ControlPlane accessors

**Files:**
- Modify: `pkg/universe/control_plane.go`

- [ ] **Step 1: Add accessor methods**

Append to `pkg/universe/control_plane.go`:

```go
// OwnerOf returns the host currently owning cellKey, or ("", false) if
// no host owns it. Unified view: consults hostRegistry first (the
// authoritative source in distributed deployments), falls back to the
// coord's cellToHostMap (populated by Build() for local hosts and by
// applyPeerList on remote hosts).
func (c *ControlPlane) OwnerOf(cellKey string) (string, bool) {
	if c.hostRegistry != nil {
		if h := c.hostRegistry.HostForCell(cellKey); h != "" {
			return h, true
		}
	}
	// cellToHostMap fallback — read with the parent coord's mu.
	// During Phase 2 migration this still lives on Coordinator, so we
	// pass through to the coord via a field the coord sets at init.
	if c.cellToHostMapRef != nil {
		c.coordMuRef.RLock()
		h, ok := (*c.cellToHostMapRef)[cellKey]
		c.coordMuRef.RUnlock()
		return h, ok
	}
	return "", false
}

// AllCells iterates every (cellKey, hostID) pair currently known. Union
// of hostRegistry ownership and cellToHostMap entries.
func (c *ControlPlane) AllCells(yield func(cellKey, hostID string) bool) {
	seen := make(map[string]struct{})
	if c.hostRegistry != nil {
		for _, h := range c.hostRegistry.LiveHosts() {
			for cellID := range h.OwnedCells {
				if _, dup := seen[cellID]; dup {
					continue
				}
				seen[cellID] = struct{}{}
				if !yield(cellID, h.ID) {
					return
				}
			}
		}
	}
	if c.cellToHostMapRef != nil {
		c.coordMuRef.RLock()
		defer c.coordMuRef.RUnlock()
		for k, v := range *c.cellToHostMapRef {
			if _, dup := seen[k]; dup {
				continue
			}
			if !yield(k, v) {
				return
			}
		}
	}
}

// CellsOwnedBy iterates cell keys owned by the named host. Empty if
// hostID is unknown.
func (c *ControlPlane) CellsOwnedBy(hostID string, yield func(cellKey string) bool) {
	c.AllCells(func(cellKey, owner string) bool {
		if owner != hostID {
			return true
		}
		return yield(cellKey)
	})
}
```

Add `"sync"` import if not present.

- [ ] **Step 2: Add the `cellToHostMapRef` / `coordMuRef` bridge fields**

Temporarily, the `ControlPlane` needs to see the Coordinator's `cellToHostMap` + `mu`. Add fields to `ControlPlane`:

```go
	// Phase 2 migration bridges: ControlPlane reads Coordinator's raw
	// maps through these pointers while the fields live on Coordinator.
	// Removed in Phase 6 after the raw maps are deleted.
	cellToHostMapRef *map[string]string
	coordMuRef       *sync.RWMutex
```

Wire them in `NewCoordinator`, right after `c.Control = newControlPlane(c.Log)`:

```go
	c.Control.cellToHostMapRef = &c.cellToHostMap
	c.Control.coordMuRef = &c.mu
```

- [ ] **Step 3: Verify compile + tests**

Run: `go vet ./pkg/universe/...` — clean.
Run: `go test ./pkg/universe/ -run TestFixtureSmoke -count=1 -timeout 60s` — PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/control_plane.go pkg/universe/coordinator.go
git commit -m "$(cat <<'EOF'
refactor(universe): add ControlPlane.OwnerOf/AllCells/CellsOwnedBy (Phase 2/8)

Canonical ownership accessors. Internally bridge to Coordinator's raw
cellToHostMap via pointer + mutex refs during migration — Phase 6 drops
the bridge when the raw maps are deleted.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 2.2: Add Host accessors

**Files:**
- Modify: `pkg/universe/host.go`

- [ ] **Step 1: Add accessor methods**

Append to `pkg/universe/host.go` (after existing methods). First grep for existing `ID()` / `Cells()` methods on Host to avoid duplicates:

```bash
grep -n "^func (h \*Host)" pkg/universe/host.go
```

Add only what's missing. Target API:

```go
// ID returns the host's stable ID. Add only if not already defined.
func (h *Host) ID() string {
	return h.id
}

// Cells iterates every local cell currently running on this host.
func (h *Host) Cells(yield func(*Cell) bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.cells {
		if !yield(c) {
			return
		}
	}
}

// Cell returns the local *Cell for cellKey, or nil if this host
// doesn't own it. Alias for the existing CellByID with the shorter name
// the post-refactor code uses.
func (h *Host) Cell(cellKey string) *Cell {
	return h.CellByID(cellKey)
}

// OwnsCell is a bool convenience for Cell(cellKey) != nil.
func (h *Host) OwnsCell(cellKey string) bool {
	return h.CellByID(cellKey) != nil
}
```

If `Host.ID` is already defined as a field (lowercase `id`), confirm the field access pattern: the existing `Host` struct has an exported `ID string` field today (grep confirms this). In that case DON'T add an `ID()` method — leaving the field exported is fine for Phase 2. A later phase tightens this.

- [ ] **Step 2: Verify**

Run: `go vet ./pkg/universe/...` — clean.
Run: `go test ./pkg/universe/ -count=1 -timeout 600s` — PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/host.go
git commit -m "$(cat <<'EOF'
refactor(universe): add Host.Cells/Cell/OwnsCell accessors (Phase 2/8)

Cell is an alias for CellByID with the shorter name the post-refactor
code uses. OwnsCell returns bool for membership checks.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Deferred follow-up: unexport `Host.Cells` field

Task 2.2 discovered that the plan's original method name `Cells(...)` collided with the existing exported field `Cells map[CellID]*Cell` — Go forbids a method and a field sharing a name on the same type. The method was renamed `EachCell` (commit `49b4e53` + `5dbc132`). A later phase should unexport `Host.Cells` → `cells` and rename `EachCell` → `Cells` for final API cleanliness. Scope: ~20 call sites across pkg/universe (direct `h.Cells[...]` / `range h.Cells` reads); most are inside Host's own methods where the lock is already held and can be switched to read the unexported field directly. External callers (e.g. `builtins_cluster.go`, `builtins_perf.go`, test files) migrate to `EachCell`/`Cell`/`OwnsCell`/`SnapshotCellIDs`. Defer until after Phase 2 caller migration is complete (post-Task 2.6) so the accessor surface is stable before the field flip.

## Task 2.3: Migrate callers of `coord.cellToHostMap`

**Files:**
- Modify: multiple `pkg/universe/*.go` files

**Approach:** Every read of `coord.cellToHostMap[key]` or `c.cellToHostMap[key]` becomes `cp.OwnerOf(key)` where `cp` is the `*ControlPlane`. If the caller has a `*Coordinator`, use `coord.Control.OwnerOf(key)`. **Writes** stay targeting `cellToHostMap` directly for now (only reads move).

- [ ] **Step 1: Find all call sites**

Run: `grep -rn 'cellToHostMap\b' pkg/universe/ --include='*.go' | grep -v '_test.go'`
Expected: ~15-25 hits.

Separate: reads (right-hand side) vs writes (left-hand side). Reads migrate; writes stay until Phase 6.

- [ ] **Step 2: Replace reads one file at a time**

For each file containing reads of `cellToHostMap`:

Example: `pkg/universe/gateway.go` around line 571:
```go
// Old:
hostID := t.coord.cellToHostMap[cellID]
// New:
hostID, _ := t.coord.Control.OwnerOf(cellID)
```

For lookups in bool contexts (`if _, ok := ...cellToHostMap[k]; ok`):
```go
// Old:
if _, ok := c.cellToHostMap[k]; !ok { ... }
// New:
if _, ok := c.Control.OwnerOf(k); !ok { ... }
```

Do NOT change any line where the left-hand side is a write (`c.cellToHostMap[k] = v` or `delete(c.cellToHostMap, k)`).

Do NOT change test files yet (Phase 2.5 handles those).

After each file, run:
```bash
go vet ./pkg/universe/...
go test ./pkg/universe/ -count=1 -timeout 600s
```

Commit per file with message:
```bash
git commit -m "refactor(universe): migrate $FILENAME to ControlPlane.OwnerOf (Phase 2/8)"
```

- [ ] **Step 3: Verify no production-code reads remain**

Run: `grep -rn 'cellToHostMap\[' pkg/universe/ --include='*.go' | grep -v '_test.go' | grep -v '= '`
Expected: only the ones that assign (`cellToHostMap[k] = v`). Empty if none.

Run: `grep -rn 'cellToHostMap$\|cellToHostMap ' pkg/universe/ --include='*.go' | grep -v '_test.go' | grep -v 'cellToHostMap = '`
Expected: empty or only field-definition lines.

## Task 2.4: Migrate callers of `coord.Hosts[h].CellByID`

**Files:**
- Modify: multiple `pkg/universe/*.go` files

- [ ] **Step 1: Find call sites**

Run: `grep -rn 'Hosts\[.*\]\.' pkg/universe/ --include='*.go' | grep -v '_test.go'`

- [ ] **Step 2: Replace per file**

Pattern: `coord.Hosts[h].CellByID(key)` → choose based on usage:

For boolean test: `coord.Control.hostRegistry.Get(h) != nil && [go through the host ptr]` — actually simpler:

The question is: do we need to reach INSIDE the remote-host's Coordinator to find a local `*Cell`? In the coord process itself, `coord.Hosts[h]` holds local Hosts only. For remote hosts in distributed mode, we never have a local `*Host` — we'd use `hostProxy` (Phase 3).

During Phase 2, leave `coord.Hosts[h].CellByID` calls alone; they're only hit in local-host paths. Grep the results and verify each one is only called where local hosts exist (i.e., in code guarded by `coord.roles.Has(RoleHost)` or executed on the host side). If any call happens on a pure-coord path, that's ALREADY a bug and Phase 3 fixes it via `hostProxy`.

- [ ] **Step 3: Note any violations in the commit message**

If any Hosts-map access happens from a path that could execute on pure-coord, document it in a commit message for Phase 3 to fix:

```bash
git commit --allow-empty -m "$(cat <<'EOF'
refactor(universe): audit coord.Hosts[] call sites (Phase 2/8)

Confirmed all coord.Hosts[h].CellByID call sites run on paths that have
a local host (RoleHost active). Calls on pure-coord paths — none found
/ found in {file:line} — migrated in Phase 3 via hostProxy.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

If no violations, skip the empty commit.

## Task 2.5: Migrate test fixture callers

**Files:**
- Modify: `pkg/universe/cluster_fixture_test.go`
- Modify: `pkg/universe/cluster_fixture_distributed_test.go`

- [ ] **Step 1: Update colocated fixture's CellOwner / HostOwnsCell**

Inside `pkg/universe/cluster_fixture_test.go`, the colocated `HostOwnsCell` already goes via `coord.Hosts[h]`. Change to:

```go
func (f *colocatedFixture) HostOwnsCell(hostID, cellKey string) bool {
	return f.CellOn(hostID, cellKey) != nil
}
```
(already delegates — no change needed; verify with grep).

`CellOwner` uses `HostForCellID` which is still the right call. Leave alone.

No concrete file edit needed in this step if both helpers already go through accessors.

- [ ] **Step 2: Audit other test files**

Run: `grep -rn 'cellToHostMap\[' pkg/universe/*_test.go`
Expected: a handful of direct access in older unit tests (e.g., `cell_transfer_test.go` — the orchestrator unit tests that pre-seed cellToHostMap).

These tests are in-memory fixtures that pre-seed `coord.cellToHostMap` directly as test setup — they don't exercise the bug class and keeping direct map writes is fine. Leave them.

- [ ] **Step 3: Verify + commit (if anything changed)**

If no edits were needed, skip commit.

## Task 2.6: Unexport cellToHostMap

**Files:**
- Modify: `pkg/universe/coordinator.go`

After Phase 2.3 + 2.4, no external code reads `cellToHostMap`. Confirm and unexport.

- [ ] **Step 1: Run one final audit**

```bash
grep -rn 'cellToHostMap\b' pkg/universe/ --include='*.go' | grep -v '_test.go' | grep -v coordinator.go | grep -v mesh_control_client.go | grep -v cell_transfer_commit.go
```

Expected: empty (or only cases that write). Remaining writers (cell_transfer_commit.go, mesh_control_client.go, coordinator.go) stay — they're the owners.

- [ ] **Step 2: Rename field**

In `pkg/universe/coordinator.go`, find the `cellToHostMap` field declaration in the `Coordinator` struct (around line 225):

```go
	// Before (already lowercase):
	cellToHostMap map[string]string
```

It's already unexported. Good — no action for this step. (If you find callers from `internal/game/` or other packages using `coord.cellToHostMap`, they'd fail to compile — Phase 2 is done when the compiler confirms no external callers.)

- [ ] **Step 3: Verify no external access**

Run: `grep -rn 'cellToHostMap' internal/ examples/ pkg/mmokit/`
Expected: empty.

- [ ] **Step 4: Commit the accessor migration as complete**

If Phase 2.3's per-file commits already landed, this task is confirmation only:

```bash
git commit --allow-empty -m "$(cat <<'EOF'
refactor(universe): confirm cellToHostMap has no external callers (Phase 2/8)

All production reads of coord.cellToHostMap go through
Control.OwnerOf / AllCells. Field stays unexported (was already). External
packages never reached it. Safe to delete in Phase 6.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 2.7: Phase 2 stability check

- [ ] **Step 1: 2× full suite**

Run: `go test ./pkg/universe/ -count=2 -timeout 1200s`
Expected: PASS both.

---

# Phase 3: hostOps + unified commit for migrate and split

**Goal:** Introduce the `hostOps` interface. Local impl wraps `*Host`; remote impl dispatches MeshControl messages and blocks on `HostOpAck`. `applyMigrateCommit` and `applySplitCommit` collapse to single code paths. Fixes the `req.Done` asymmetry and source-teardown-async bugs.

## Task 3.1: Define hostOps interface + local impl

**Files:**
- Create: `pkg/universe/host_ops.go`

- [ ] **Step 1: Create the file**

```go
package universe

import (
	"context"
	"fmt"
)

// hostOps is the topology-abstract operation API used by commit paths.
// Local impl wraps a *Host (direct method calls, synchronous). Remote
// impl dispatches MeshControl messages and blocks on HostOpAck. Both
// honor the caller's ctx deadline.
type hostOps interface {
	// ReleaseCell shuts down the cell on the target host, blocking
	// until teardown is observable (game loop stopped, netID range
	// returned, Host.cells entry removed). Error on unknown cellKey or
	// ctx deadline.
	ReleaseCell(ctx context.Context, cellKey string) error

	// StartCell creates a cell on the target host, blocking until the
	// game loop is running and the host has acked CellReady. Error
	// if the cell already exists or ctx deadline fires.
	StartCell(ctx context.Context, cellID CellID) error

	// RenameCell rekeys a cell on the target host from `from` to `to`,
	// blocking until the rename is visible on the target's game loop.
	// Used by merge commit to rename the survivor sibling to the
	// parent ID. Error if the cell doesn't exist or ctx deadline fires.
	RenameCell(ctx context.Context, from, to string) error
}

// localHostOps is the in-process impl: method calls go directly to the
// local *Host. Synchronous; blocks trivially because the operations
// themselves complete before returning.
type localHostOps struct {
	host *Host
}

func (l *localHostOps) ReleaseCell(ctx context.Context, cellKey string) error {
	cell := l.host.CellByID(cellKey)
	if cell == nil {
		return fmt.Errorf("host %s: ReleaseCell: unknown cell %s", l.host.ID, cellKey)
	}
	l.host.RemoveCell(cell.Cell)
	cell.Shutdown()
	// Caller is responsible for releasing the NetID range (belongs to
	// the coord's netIDAlloc, not the host).
	return nil
}

func (l *localHostOps) StartCell(ctx context.Context, cellID CellID) error {
	// Local StartCell is wired in Phase 3.3 once the coord's createNode
	// path is refactored. Phase 3.1 stubs this so the interface
	// implementation compiles.
	return fmt.Errorf("localHostOps.StartCell: not yet implemented (Phase 3.3)")
}

func (l *localHostOps) RenameCell(ctx context.Context, from, to string) error {
	// Local RenameCell lands in Phase 4 alongside the remote impl.
	return fmt.Errorf("localHostOps.RenameCell: not yet implemented (Phase 4)")
}
```

- [ ] **Step 2: Verify compile**

Run: `go vet ./pkg/universe/...` — clean.
Run: `go test ./pkg/universe/ -count=1 -timeout 600s` — PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/host_ops.go
git commit -m "$(cat <<'EOF'
refactor(universe): add hostOps interface + local impl skeleton (Phase 3/8)

hostOps.ReleaseCell is fully wired to local Host methods. StartCell and
RenameCell are stubbed — Phase 3.3 and Phase 4 fill them in.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 3.2: Add HostOpAck proto message + routing infrastructure

**Files:**
- Modify: `proto/meshpb/mesh.proto`
- Regen: `gen/go/meshpb/*.pb.go`
- Modify: `pkg/universe/control_plane.go`
- Modify: `pkg/universe/mesh_control_server.go`
- Modify: `pkg/universe/mesh_control_client.go`

- [ ] **Step 1: Add HostOpAck to proto**

In `proto/meshpb/mesh.proto`, find the `HostMessage` oneof (grep `HostMessage_`). Add a new field to the oneof:

```proto
message HostOpAck {
  uint64 req_id = 1;
  bool   ok     = 2;
  string error  = 3;
}
```

And add it to the HostMessage oneof, using the next available field number.

Also update the `CellRelease` message to carry a req_id (we rebrand it as the "AndAck" variant per the spec):

```proto
message CellRelease {
  string cell_id = 1;
  uint64 req_id  = 2;  // 0 = fire-and-forget (back-compat with bookkeeping); non-zero = expect HostOpAck
}
```

Per the spec's "no backward compat" preference, we could rename to `CellReleaseAndAck` — but since the wire format is additive (new field), keeping the existing name saves renaming.

- [ ] **Step 2: Regen**

Run: `just proto` (or `buf generate` if the just target doesn't exist).
Expected: `gen/go/meshpb/*.pb.go` updated.

Verify:
```bash
git diff --stat gen/go/meshpb/
```
Expected: changes to mesh.pb.go reflecting the new message + updated field.

- [ ] **Step 3: Add pending-ack registry to ControlPlane**

In `pkg/universe/control_plane.go`, add the pending-ack map + allocator:

```go
import (
	...
	"sync"
	"sync/atomic"
)

type ControlPlane struct {
	... // existing fields

	// Pending host-op acks. Keyed by req_id. Populated by remote
	// hostOps.ReleaseCell / StartCell / RenameCell; drained by the
	// meshControlServer's handleHostControl when HostOpAck arrives.
	pendingOps    sync.Map // map[uint64]chan hostOpResult
	nextHostOpID  uint64   // atomic counter
}

type hostOpResult struct {
	ok    bool
	error string
}

func (c *ControlPlane) allocHostOpID() uint64 {
	return atomic.AddUint64(&c.nextHostOpID, 1)
}

func (c *ControlPlane) registerPendingOp(id uint64) chan hostOpResult {
	ch := make(chan hostOpResult, 1)
	c.pendingOps.Store(id, ch)
	return ch
}

func (c *ControlPlane) completePendingOp(id uint64, result hostOpResult) {
	if v, ok := c.pendingOps.LoadAndDelete(id); ok {
		ch := v.(chan hostOpResult)
		ch <- result
	}
}

func (c *ControlPlane) cancelPendingOp(id uint64) {
	c.pendingOps.Delete(id)
}
```

Add imports for `"sync"` and `"sync/atomic"` if needed.

- [ ] **Step 4: Wire HostOpAck handler on coord side**

In `pkg/universe/mesh_control_server.go`, find the `handleHostControl` recv loop (around line 156-250). Add a new case in the `switch v := msg.Msg.(type)`:

```go
			case *meshpb.HostMessage_HostOpAck:
				ack := v.HostOpAck
				if ack != nil && s.coord.Control != nil {
					s.coord.Control.completePendingOp(ack.ReqId, hostOpResult{
						ok:    ack.Ok,
						error: ack.Error,
					})
				}
```

- [ ] **Step 5: Wire CellRelease req_id on host side**

In `pkg/universe/mesh_control_client.go`, find the `CellRelease` handler (grep `CoordMessage_CellRelease`, around line 562). Update to capture req_id and send HostOpAck:

```go
	case *meshpb.CoordMessage_CellRelease:
		rel := v.CellRelease
		if rel == nil {
			break
		}
		c.log.Log(CatMeshCell, "host: CellRelease %s (req=%d)", rel.CellId, rel.ReqId)
		go func(cellID string, reqID uint64) {
			c.coord.releaseCellOnNode(cellID)
			if reqID != 0 {
				// Send HostOpAck back.
				ack := &meshpb.HostMessage{
					Msg: &meshpb.HostMessage_HostOpAck{
						HostOpAck: &meshpb.HostOpAck{
							ReqId: reqID,
							Ok:    true,
						},
					},
				}
				_ = c.send(ack)
			}
		}(rel.CellId, rel.ReqId)
```

- [ ] **Step 6: Verify compile + tests**

Run: `go vet ./... 2>&1 | head`
Expected: clean. (If proto-generated code references new types, they'll be present after regen.)

Run: `go test ./pkg/universe/ -count=1 -timeout 600s`
Expected: PASS (no test yet uses HostOpAck; infrastructure is additive).

- [ ] **Step 7: Commit**

```bash
git add proto/meshpb/mesh.proto gen/go/meshpb/ pkg/universe/control_plane.go pkg/universe/mesh_control_server.go pkg/universe/mesh_control_client.go
git commit -m "$(cat <<'EOF'
feat(meshpb,universe): HostOpAck routing + CellRelease req_id (Phase 3/8)

Adds HostOpAck proto message and req_id routing on both ends of the
MeshControl stream. Enables hostOps remote impl (next task) to block
on completion rather than fire-and-forget.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 3.3: Remote hostOps implementation + hostProxy

**Files:**
- Modify: `pkg/universe/host_ops.go`
- Modify: `pkg/universe/control_plane.go`

- [ ] **Step 1: Add remote impl to host_ops.go**

Append to `pkg/universe/host_ops.go`:

```go
// remoteHostOps dispatches MeshControl messages to a remote host and
// blocks on HostOpAck. The Control field is the coord-role ControlPlane
// hosting the pending-op registry.
type remoteHostOps struct {
	control *ControlPlane
	hostID  string
}

func (r *remoteHostOps) ReleaseCell(ctx context.Context, cellKey string) error {
	if r.control.controlServer == nil {
		return fmt.Errorf("remoteHostOps: no control server (cannot dispatch to %s)", r.hostID)
	}
	reqID := r.control.allocHostOpID()
	ch := r.control.registerPendingOp(reqID)

	msg := &meshpb.CoordMessage{
		CoordEpoch: r.control.coordEpoch(),
		Msg: &meshpb.CoordMessage_CellRelease{
			CellRelease: &meshpb.CellRelease{
				CellId: cellKey,
				ReqId:  reqID,
			},
		},
	}
	if err := r.control.controlServer.sendCoordMessageToHost(r.hostID, msg); err != nil {
		r.control.cancelPendingOp(reqID)
		return fmt.Errorf("remoteHostOps: dispatch to %s failed: %w", r.hostID, err)
	}

	select {
	case result := <-ch:
		if !result.ok {
			return fmt.Errorf("remoteHostOps: %s ReleaseCell %s failed: %s", r.hostID, cellKey, result.error)
		}
		return nil
	case <-ctx.Done():
		r.control.cancelPendingOp(reqID)
		return fmt.Errorf("remoteHostOps: %s ReleaseCell %s: ctx expired: %w", r.hostID, cellKey, ctx.Err())
	}
}

func (r *remoteHostOps) StartCell(ctx context.Context, cellID CellID) error {
	return fmt.Errorf("remoteHostOps.StartCell: not yet implemented (Phase 3.3)")
}

func (r *remoteHostOps) RenameCell(ctx context.Context, from, to string) error {
	return fmt.Errorf("remoteHostOps.RenameCell: not yet implemented (Phase 4)")
}
```

Add imports for `meshpb "github.com/zenion/mmokit/gen/go/meshpb"` if not present.

- [ ] **Step 2: Add coordEpoch method on ControlPlane**

In `pkg/universe/control_plane.go`, add:

```go
// coordEpoch returns the parent coordinator's epoch. Temporary bridge
// until Phase 7 moves coordEpoch onto Process directly.
func (c *ControlPlane) coordEpoch() uint64 {
	if c.coordEpochRef != nil {
		return *c.coordEpochRef
	}
	return 0
}
```

Add field:
```go
	coordEpochRef *uint64
```

Wire in `NewCoordinator`, after other Control bridges:
```go
	c.Control.coordEpochRef = &c.coordEpoch
```

- [ ] **Step 3: Add hostProxy on ControlPlane**

Append to `control_plane.go`:

```go
// hostProxy returns a hostOps implementation for the named host. If the
// host is local (this process's own Host), direct method calls are used.
// Otherwise MeshControl routing is used.
func (c *ControlPlane) hostProxy(hostID string) hostOps {
	if c.localHostRef != nil && c.localHostRef.ID == hostID {
		return &localHostOps{host: c.localHostRef}
	}
	return &remoteHostOps{control: c, hostID: hostID}
}
```

Add field:
```go
	// Bridge to the process's local Host, if any. Nil on pure-coord
	// deployments. Set during Build() after the local Host is constructed.
	localHostRef *Host
```

Wire in `Build()` for local-host paths. In the `RoleHost` non-remote branch (around `coordinator.go:786`, after hosts slice is built):
```go
	// Phase 3: expose the single local host to the ControlPlane so
	// hostProxy can short-circuit to localHostOps.
	if len(hosts) == 1 {
		c.Control.localHostRef = hosts[0]
	}
	// For TestHosts>1 (multi-local), hostProxy falls back to remoteOps
	// which will work once Phase 6 replaces TestHosts with
	// multi-Process-in-binary.
```

For `buildRemoteHost`, add at the end:
```go
	c.Control.localHostRef = host
```

- [ ] **Step 4: Verify + commit**

Run: `go vet ./pkg/universe/...` — clean.
Run: `go test ./pkg/universe/ -count=1 -timeout 600s` — PASS.

```bash
git add pkg/universe/host_ops.go pkg/universe/control_plane.go pkg/universe/coordinator.go
git commit -m "$(cat <<'EOF'
refactor(universe): remoteHostOps + ControlPlane.hostProxy (Phase 3/8)

remoteHostOps.ReleaseCell dispatches via MeshControl and blocks on
HostOpAck. hostProxy picks local vs remote based on whether the target
host is this process's own. StartCell/RenameCell are still stubs —
Phase 4 fills RenameCell; StartCell is wired during Phase 4 as well
since it's orthogonal to the commit-path collapse.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 3.4: Collapse applyMigrateCommit

**Files:**
- Modify: `pkg/universe/cell_transfer_commit.go`

- [ ] **Step 1: Rewrite the source-teardown branch**

In `pkg/universe/cell_transfer_commit.go`, find the `applyMigrateCommit` function. Near the end (grep for `if srcCell != nil`, around line 264) there's the local/remote branch:

```go
// Old:
	if srcCell != nil {
		// In-process cell — shut down directly.
		srcCell.Shutdown()
		c.netIDAlloc.Release(srcCell.Engine.NetIDBase())
	} else {
		// Remote cell — send CellRelease via MeshControl.
		c.sendCellRelease(srcHost, srcCellKey)
	}
```

Replace with:

```go
	// Unified teardown via hostProxy: local == direct call; remote ==
	// MeshControl with blocking HostOpAck. Holds the caller's ctx for
	// deadline control. netIDAlloc.Release happens unconditionally
	// after teardown completes.
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer releaseCancel()
	if err := c.Control.hostProxy(srcHost).ReleaseCell(releaseCtx, srcCellKey); err != nil {
		c.Log.Log(CatMeshCell, "applyMigrateCommit: ReleaseCell %s -> %s failed: %v", srcCellKey, srcHost, err)
		// Failure is logged but we don't roll back — the migrate's
		// ownership flip has already succeeded on the coord side. The
		// source host may have a leaked cell; next host restart or
		// manual intervention cleans it up. See Stage-2 TODO for
		// tighter rollback semantics if needed.
	}
	if srcCell != nil {
		c.netIDAlloc.Release(srcCell.Engine.NetIDBase())
	}
```

Keep the local-host `srcCell` lookup earlier in the function (it's still useful for knowing whether to release the NetID range — `srcCell != nil` means we had a local cell whose range is ours to release).

Add imports for `"context"`, `"time"` if not present.

- [ ] **Step 2: Verify tests**

Run: `go test ./pkg/universe/ -run 'TestS7Migrate|TestMigrateEpoch' -v -count=1 -timeout 120s`
Expected: all PASS in both `/colocated` and `/distributed`.

Specifically: the distributed subtests should now complete without needing the fixture's `WaitForCellReleased` poll. Verify by temporarily setting the deadline to 0 in the fixture's `seedCtx` — if the tests still pass, `req.Done` now blocks until completion. (Revert the deadline change before committing.)

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/cell_transfer_commit.go
git commit -m "$(cat <<'EOF'
refactor(universe): collapse applyMigrateCommit local/remote branch (Phase 3/8)

Unified teardown via c.Control.hostProxy(srcHost).ReleaseCell. Same
blocking semantics in both topologies — req.Done fires only after source
teardown completes. Fixes the req.Done async-semantics asymmetry Stage-1
surfaced.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 3.5: Collapse applySplitCommit

**Files:**
- Modify: `pkg/universe/cell_transfer_commit.go`

- [ ] **Step 1: Rewrite the parent-teardown branch**

In `applySplitCommit` (same file), find the `if hadParent { ... } else if len(req.commands) > 0 { ... }` block (around line 186):

```go
// Old:
	if hadParent {
		parentCell.Shutdown()
		c.netIDAlloc.Release(parentCell.Engine.NetIDBase())
	} else if len(req.commands) > 0 {
		if srcHost := req.commands[0].SrcHostID; srcHost != "" {
			c.sendCellRelease(srcHost, parentKey)
		}
	}
```

Replace with:
```go
	// Unified parent teardown via hostProxy. All split commands share
	// the same parent and SrcHostID by construction; any command's
	// SrcHostID is the parent's host.
	if len(req.commands) > 0 {
		if srcHost := req.commands[0].SrcHostID; srcHost != "" {
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer releaseCancel()
			if err := c.Control.hostProxy(srcHost).ReleaseCell(releaseCtx, parentKey); err != nil {
				c.Log.Log(CatMeshCell, "applySplitCommit: ReleaseCell parent %s -> %s failed: %v", parentKey, srcHost, err)
			}
		}
	}
	if hadParent {
		c.netIDAlloc.Release(parentCell.Engine.NetIDBase())
	}
```

- [ ] **Step 2: Tests**

Run: `go test ./pkg/universe/ -run TestS7Split -v -count=1 -timeout 120s`
Expected: PASS in both subtests.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/cell_transfer_commit.go
git commit -m "$(cat <<'EOF'
refactor(universe): collapse applySplitCommit local/remote branch (Phase 3/8)

Matches the applyMigrateCommit pattern from the previous commit. Parent
teardown goes through hostProxy; no more in-process-only skip.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 3.6: Remove defensive WaitForCellReleased usage from migrated tests

**Files:**
- Modify: `pkg/universe/s7_migrate_test.go`
- Modify: `pkg/universe/s7_migrate_epoch_test.go`
- Modify: `pkg/universe/s7_split_test.go`
- Modify: `pkg/universe/s7_graceful_shutdown_test.go`
- Modify: `pkg/universe/s7_concurrent_test.go`

**Goal:** Tests that used `WaitForCellReleased` with a 5s poll as a workaround for the async race can now use a single-shot check. Keep `WaitForCellReleased` in the fixture as defense-in-depth, but the test code reads cleaner without the ctx+poll boilerplate.

- [ ] **Step 1: Per file, replace WaitForCellReleased pattern**

Example in `s7_migrate_test.go`:
```go
// Old:
releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
defer releaseCancel()
if err := fx.WaitForCellReleased(releaseCtx, srcKey, "host-a"); err != nil {
    t.Errorf("post-commit: %v", err)
}

// New:
if fx.HostOwnsCell("host-a", srcKey) {
    t.Errorf("post-commit: source host still owns cell %s", srcKey)
}
```

Since `req.Done` now blocks until teardown lands, a single-shot `HostOwnsCell` check is sufficient. If a test fails with this simpler check, `req.Done` isn't actually blocking properly — that's a bug to investigate, not paper over.

- [ ] **Step 2: Run per file**

After each file edit:
```bash
go test ./pkg/universe/ -run <TestName> -v -count=1 -timeout 120s
```

If PASS, commit:
```bash
git add <file>
git commit -m "test(universe): drop WaitForCellReleased workaround in <test> — req.Done now blocks (Phase 3/8)"
```

If FAIL: `req.Done` still isn't truly synchronous. Investigate before continuing.

- [ ] **Step 3: Stability check**

After all files migrated:
```bash
go test ./pkg/universe/ -count=2 -timeout 1200s
```
Expected: PASS both iterations.

---

# Phase 4: CellRename wire protocol + unified merge

**Goal:** Add the `CellRename` MeshControl message, wire coord dispatcher + host handler, slot into `hostOps.RenameCell`. Collapse `applyMergeCommit`. Re-enable Task 9's `/distributed` subtest.

## Task 4.1: CellRename proto

**Files:**
- Modify: `proto/meshpb/mesh.proto`

- [ ] **Step 1: Add CellRename message**

In `proto/meshpb/mesh.proto`, add:

```proto
message CellRename {
  string from_cell_id = 1;
  string to_cell_id   = 2;
  uint64 req_id       = 3;
}
```

Add to the `CoordMessage` oneof with a fresh field number. Grep the existing file for `oneof msg` to find the right location.

- [ ] **Step 2: Regen**

Run: `just proto` (or `buf generate`).

- [ ] **Step 3: Verify**

```bash
go vet ./... 2>&1 | head
```
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add proto/meshpb/mesh.proto gen/go/meshpb/ gen/csharp/ gen/es/
git commit -m "$(cat <<'EOF'
proto(meshpb): add CellRename message (Phase 4/8)

For the merge commit survivor-rename protocol. Host-side handler and
coord-side dispatcher land next.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 4.2: Host-side CellRename handler (renameCellOnNode)

**Files:**
- Modify: `pkg/universe/coordinator.go`
- Modify: `pkg/universe/mesh_control_client.go`

- [ ] **Step 1: Add renameCellOnNode method**

In `pkg/universe/coordinator.go`, near `releaseCellOnNode` (around line 1638), add:

```go
// renameCellOnNode rekeys a local cell from `from` to `to`. Rewrites
// Host.cells map under h.mu, coord.Cells map under c.mu, and the
// *Cell struct's ID/Cell fields on the cell's own game loop (so
// PostSystems reads don't race with the write).
func (c *Coordinator) renameCellOnNode(from, to string) error {
	host := c.localHost()
	if host == nil {
		return fmt.Errorf("host: renameCellOnNode: no local host")
	}

	c.mu.Lock()
	cell := host.CellByID(from)
	if cell == nil {
		c.mu.Unlock()
		return fmt.Errorf("host: renameCellOnNode: unknown cell %q", from)
	}
	// Move the host-side entry first.
	host.RemoveCell(cell.Cell)
	toCellID, err := ParseCellID(to)
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("host: renameCellOnNode: parse %q: %w", to, err)
	}
	host.AddCell(toCellID, cell)
	// Update coord's Cells / CellOwner maps (local-host copies — in
	// remote-host mode c.Cells is only the cells this process owns).
	delete(c.Cells, from)
	delete(c.CellOwner, cell.Cell)
	c.Cells[to] = cell
	c.CellOwner[toCellID] = to
	c.mu.Unlock()

	// Rewrite the cell's own identity on its game loop so PostSystems
	// reads don't race.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := cell.Engine.RunOnLoop(ctx, func() error {
		cell.ID = to
		cell.Cell = toCellID
		if cell.World != nil {
			cell.World.UpdateCellBounds(toCellID, coords.CellSize)
		}
		if cell.Metrics != nil {
			cell.Metrics.SetCellID(to)
		}
		return nil
	})
	if runErr != nil {
		return fmt.Errorf("host: renameCellOnNode: RunOnLoop: %w", runErr)
	}
	return nil
}
```

Ensure imports include `"context"`, `"time"`, `"github.com/zenion/mmokit/pkg/coords"`.

- [ ] **Step 2: Wire dispatch on host side**

In `pkg/universe/mesh_control_client.go`, find the `dispatch` function (around line 529). Add a new case:

```go
	case *meshpb.CoordMessage_CellRename:
		req := v.CellRename
		if req == nil {
			break
		}
		c.log.Log(CatMeshCell, "host: CellRename %s -> %s (req=%d)", req.FromCellId, req.ToCellId, req.ReqId)
		go func(from, to string, reqID uint64) {
			err := c.coord.renameCellOnNode(from, to)
			ok := err == nil
			errStr := ""
			if !ok {
				errStr = err.Error()
			}
			if reqID != 0 {
				ack := &meshpb.HostMessage{
					Msg: &meshpb.HostMessage_HostOpAck{
						HostOpAck: &meshpb.HostOpAck{
							ReqId: reqID,
							Ok:    ok,
							Error: errStr,
						},
					},
				}
				_ = c.send(ack)
			}
		}(req.FromCellId, req.ToCellId, req.ReqId)
```

- [ ] **Step 3: Verify + commit**

Run: `go vet ./pkg/universe/...` — clean.
Run: `go test ./pkg/universe/ -count=1 -timeout 600s` — PASS.

```bash
git add pkg/universe/coordinator.go pkg/universe/mesh_control_client.go
git commit -m "$(cat <<'EOF'
feat(universe): host-side CellRename handler (Phase 4/8)

renameCellOnNode rekeys the local cell across Host.cells, coord.Cells,
coord.CellOwner, and the *Cell struct (ID/Cell fields updated via
Engine.RunOnLoop for race safety). Coord-side dispatch lands next.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 4.3: Wire localHostOps + remoteHostOps RenameCell

**Files:**
- Modify: `pkg/universe/host_ops.go`
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Replace localHostOps.RenameCell stub**

```go
func (l *localHostOps) RenameCell(ctx context.Context, from, to string) error {
	// Dispatch through the parent coordinator's renameCellOnNode so the
	// locking + RunOnLoop discipline lives in one place.
	if l.host.coord == nil {
		return fmt.Errorf("localHostOps.RenameCell: host has no parent coord")
	}
	return l.host.coord.renameCellOnNode(from, to)
}
```

- [ ] **Step 2: Add coord pointer to Host**

In `pkg/universe/host.go`, add field:
```go
	coord *Coordinator
```

In `pkg/universe/coordinator.go`, wherever a Host is created (both the multi-host TestHosts branch AND `buildRemoteHost`), set `h.coord = c` right after construction.

- [ ] **Step 3: Replace remoteHostOps.RenameCell stub**

In `pkg/universe/host_ops.go`:

```go
func (r *remoteHostOps) RenameCell(ctx context.Context, from, to string) error {
	if r.control.controlServer == nil {
		return fmt.Errorf("remoteHostOps: no control server (cannot dispatch to %s)", r.hostID)
	}
	reqID := r.control.allocHostOpID()
	ch := r.control.registerPendingOp(reqID)

	msg := &meshpb.CoordMessage{
		CoordEpoch: r.control.coordEpoch(),
		Msg: &meshpb.CoordMessage_CellRename{
			CellRename: &meshpb.CellRename{
				FromCellId: from,
				ToCellId:   to,
				ReqId:      reqID,
			},
		},
	}
	if err := r.control.controlServer.sendCoordMessageToHost(r.hostID, msg); err != nil {
		r.control.cancelPendingOp(reqID)
		return fmt.Errorf("remoteHostOps: dispatch CellRename to %s: %w", r.hostID, err)
	}

	select {
	case result := <-ch:
		if !result.ok {
			return fmt.Errorf("remoteHostOps: %s CellRename %s->%s failed: %s", r.hostID, from, to, result.error)
		}
		return nil
	case <-ctx.Done():
		r.control.cancelPendingOp(reqID)
		return fmt.Errorf("remoteHostOps: %s CellRename %s->%s: ctx expired: %w", r.hostID, from, to, ctx.Err())
	}
}
```

- [ ] **Step 4: Verify + commit**

Run: `go test ./pkg/universe/ -count=1 -timeout 600s` — PASS (no test yet exercises the new code path, but infrastructure must compile).

```bash
git add pkg/universe/host_ops.go pkg/universe/host.go pkg/universe/coordinator.go
git commit -m "$(cat <<'EOF'
refactor(universe): wire localHostOps/remoteHostOps RenameCell (Phase 4/8)

Both impls delegate to the existing renameCellOnNode (local: direct call;
remote: CellRename MeshControl message + HostOpAck wait).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 4.4: Collapse applyMergeCommit's survivor rename

**Files:**
- Modify: `pkg/universe/cell_transfer_commit.go`

- [ ] **Step 1: Rewrite the survivor rename block**

In `applyMergeCommit`, find the `if survivor != nil && survivor.Engine != nil { ... RunOnLoop ... }` block (around line 424-448). This is the in-process-only rename that silently skips remote survivors.

Replace with a `hostOps.RenameCell` call. Compute the survivor's host (from the mutation), then:

```go
	// Unified rename via hostProxy. Works for both local and remote
	// survivors — the local impl calls renameCellOnNode directly; the
	// remote impl dispatches CellRename + blocks on HostOpAck.
	survivorHost := req.mutation.add[parentKey] // after-merge, parent lives on survivorHost
	renameCtx, renameCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer renameCancel()
	if err := c.Control.hostProxy(survivorHost).RenameCell(renameCtx, survivorKey, parentKey); err != nil {
		c.Log.Log(CatMeshCell, "applyMergeCommit: RenameCell %s -> %s on %s failed: %v", survivorKey, parentKey, survivorHost, err)
		// Log but don't fail commit — the ownership flip has already
		// happened on the coord side. Future hardening could add
		// rollback here.
	}
```

Remove the old `if survivor != nil && survivor.Engine != nil { ... }` block entirely.

Keep the `c.mu.Lock()` block that updates `c.Cells` / `c.CellOwner` — those are coord-local state. Only the cell-struct identity rewrite (which was race-prone) moves to hostProxy.

- [ ] **Step 2: Update merge Cells/CellOwner map logic**

The old code assumed `survivor != nil` (local only). The rewrite: the coord still needs to rekey `c.Cells[survivorKey] → c.Cells[parentKey]` — but this only works for local survivors. For remote survivors, `c.Cells` doesn't have the survivor at all, so there's nothing to rekey.

Wrap the coord-local rekey in a local-only guard:
```go
	c.mu.Lock()
	if local, ok := c.Cells[survivorKey]; ok {
		delete(c.Cells, survivorKey)
		delete(c.CellOwner, survivorCellID)
		c.Cells[parentKey] = local
		c.CellOwner[parent] = parentKey
	}
	// Remote survivor: host-side renameCellOnNode handles Host.cells
	// and the host's own c.Cells. Nothing for THIS coord process to do.
	c.mu.Unlock()
```

- [ ] **Step 3: Remove donor teardown duplication**

Grep for `c.sendCellRelease` in `applyMergeCommit`. Replace with `hostProxy(...).ReleaseCell` pattern:

```go
	// Old:
	for _, cmd := range req.commands {
		if cmd.Kind != CellTransferMerge || cmd.SrcCellID == "" || cmd.SrcHostID == "" {
			continue
		}
		if _, alreadyLocal := localReleased[cmd.SrcCellID]; alreadyLocal {
			continue
		}
		c.sendCellRelease(cmd.SrcHostID, cmd.SrcCellID)
	}

	// New:
	for _, cmd := range req.commands {
		if cmd.Kind != CellTransferMerge || cmd.SrcCellID == "" || cmd.SrcHostID == "" {
			continue
		}
		if _, alreadyLocal := localReleased[cmd.SrcCellID]; alreadyLocal {
			continue
		}
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := c.Control.hostProxy(cmd.SrcHostID).ReleaseCell(releaseCtx, cmd.SrcCellID); err != nil {
			c.Log.Log(CatMeshCell, "applyMergeCommit: ReleaseCell donor %s -> %s failed: %v", cmd.SrcCellID, cmd.SrcHostID, err)
		}
		releaseCancel()
	}
```

- [ ] **Step 4: Tests**

Run: `go test ./pkg/universe/ -run 'TestMerge|TestS7Merge' -v -count=1 -timeout 120s`
Expected: `/colocated` PASS. `/distributed` still SKIPPED because we haven't removed the skip yet.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/cell_transfer_commit.go
git commit -m "$(cat <<'EOF'
refactor(universe): collapse applyMergeCommit rename + donor teardown (Phase 4/8)

Survivor rename via c.Control.hostProxy(host).RenameCell — works for both
local and remote survivors via the CellRename MeshControl message. Donor
teardowns unified via hostProxy.ReleaseCell.

Fixes the CRITICAL Stage-1 bug: merge on deployments where the survivor
lands on a remote host was silently corrupting state because the
identity rewrite never propagated. Now unified with local path.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 4.5: Re-enable s7_merge_test.go distributed subtest

**Files:**
- Modify: `pkg/universe/s7_merge_test.go`

- [ ] **Step 1: Find and remove the skip**

Grep for the skip message:
```bash
grep -n "merge survivor rename not yet propagated" pkg/universe/s7_merge_test.go
```

Remove the block:
```go
// Old:
if _, isDistributed := fx.(*distributedFixture); isDistributed {
    t.Skip("merge survivor rename not yet propagated to remote hosts — Stage-2 blocker, see spec")
}

// New: (delete the block entirely)
```

- [ ] **Step 2: Run merge tests**

```bash
go test ./pkg/universe/ -run TestS7Merge -v -count=1 -timeout 120s
```
Expected: both `/colocated` and `/distributed` PASS.

If `/distributed` fails, the merge refactor has a subtle bug. Investigate before continuing. Common failure modes:
- Survivor host computation wrong (double-check `req.mutation.add[parentKey]`).
- CellRename ack not arriving (grep coord log for "CellRename" and "HostOpAck").
- Race in `renameCellOnNode` (verify RunOnLoop returned before HostOpAck was sent).

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/s7_merge_test.go
git commit -m "$(cat <<'EOF'
test(universe): re-enable s7_merge_test.go distributed subtest (Phase 4/8)

Merge survivor rename now propagates via CellRename MeshControl message
+ HostOpAck blocking. Both /colocated and /distributed pass for every
merge test.

Resolves Task 9 of the Stage-1 plan.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 4.6: Phase 4 stability check

- [ ] **Step 1: 2× full suite**

```bash
go test ./pkg/universe/ -count=2 -timeout 1200s
```
Expected: PASS both.

---

# Phase 5: Topology → ControlPlane

**Goal:** Move the `Topology` struct from `Coordinator` to `ControlPlane`. Hook recomputation into `hostRegistry.AssignCell` / `ReleaseCell`. Remove the Task 13 distributed skip.

## Task 5.1: Move Topology field

**Files:**
- Modify: `pkg/universe/control_plane.go`
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Add Topology to ControlPlane**

In `pkg/universe/control_plane.go`, add to the `ControlPlane` struct:

```go
	// Topology tracks cell-neighbor adjacency. Rebuilt incrementally on
	// ownership changes (hostRegistry.AssignCell / ReleaseCell) and
	// restructuring events (split, merge).
	Topology Topology
```

- [ ] **Step 2: Remove Topology from Coordinator**

In `pkg/universe/coordinator.go`, find and delete the `Topology Topology` field from the `Coordinator` struct (around line 194).

- [ ] **Step 3: Update all readers**

Grep every reference to `coord.Topology`, `c.Topology` in `pkg/universe/*.go`:
```bash
grep -rn '\bTopology\b' pkg/universe/ --include='*.go' | grep -v '_test.go' | grep -v Topology:
```

Replace `c.Topology` → `c.Control.Topology` and `coord.Topology` → `coord.Control.Topology`. Test files too, where they peek into the topology.

- [ ] **Step 4: Verify + commit**

```bash
go vet ./pkg/universe/...
go test ./pkg/universe/ -count=1 -timeout 600s
```

```bash
git add pkg/universe/
git commit -m "$(cat <<'EOF'
refactor(universe): move Topology from Coordinator to ControlPlane (Phase 5/8)

Topology logically belongs to the coord plane. All callers now access
it via coord.Control.Topology. Next task wires automatic rebuild on
hostRegistry events so pure-coord processes keep topology current.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 5.2: Hook Topology rebuild into hostRegistry events

**Files:**
- Modify: `pkg/universe/host_registry.go`
- Modify: `pkg/universe/control_plane.go`
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Add callback on HostRegistry**

In `pkg/universe/host_registry.go`, add a field + setter:

```go
type HostRegistry struct {
	...
	onOwnershipChanged func(cellID string)
}

func (r *HostRegistry) SetOwnershipChangedCallback(fn func(cellID string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onOwnershipChanged = fn
}
```

In `AssignCell` + `ReleaseCell`, call the callback after the mutation (still holding the lock is fine; callback is a function pointer set once at wiring time):

```go
func (r *HostRegistry) AssignCell(hostID, cellID string) error {
	r.mu.Lock()
	host, ok := r.hosts[hostID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("...")
	}
	host.OwnedCells[cellID] = true
	cb := r.onOwnershipChanged
	r.mu.Unlock()
	if cb != nil {
		cb(cellID)
	}
	return nil
}

func (r *HostRegistry) ReleaseCell(hostID, cellID string) {
	r.mu.Lock()
	if host, ok := r.hosts[hostID]; ok {
		delete(host.OwnedCells, cellID)
	}
	cb := r.onOwnershipChanged
	r.mu.Unlock()
	if cb != nil {
		cb(cellID)
	}
}
```

- [ ] **Step 2: Wire the callback in Build()**

In `pkg/universe/coordinator.go`, after `c.hostRegistry = NewHostRegistry(c.Log)` (around line 1047), add:

```go
	c.hostRegistry.SetOwnershipChangedCallback(func(cellID string) {
		c.Control.rebuildTopologyForCell(cellID)
	})
```

- [ ] **Step 3: Add rebuildTopologyForCell to ControlPlane**

In `pkg/universe/control_plane.go`:

```go
// rebuildTopologyForCell recomputes neighbor adjacency for the given
// cell after its ownership changes. Uses the existing
// Topology.RebuildNeighborsFor helper — scoped to the affected cell
// plus its former neighbors. Safe to call from any goroutine.
func (c *ControlPlane) rebuildTopologyForCell(cellKey string) {
	cid, err := ParseCellID(cellKey)
	if err != nil {
		return
	}
	// Cell-size lookup needs to come from somewhere; in Phase 5 we
	// assume coords.CellSize is the default (coord always uses the
	// same base size). Future: pipe cellSize through ControlPlane.
	if c.Topology.Neighbors == nil {
		return
	}
	c.Topology.RebuildNeighborsFor([]CellID{cid}, coords.CellSize)
}
```

Add import for `"github.com/zenion/mmokit/pkg/coords"`.

- [ ] **Step 4: Test the distributed topology skip**

In `pkg/universe/partition_test.go`, find the `TestSplitCell_TopologyCorrect` test's distributed-skip block:

```bash
grep -n "TopologyCorrect" pkg/universe/partition_test.go
```

Remove the skip. Run the test:

```bash
go test ./pkg/universe/ -run TestSplitCell_TopologyCorrect -v -count=1 -timeout 60s
```
Expected: both subtests PASS.

If `/distributed` fails, the Topology isn't being maintained through the callback — debug the wiring.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/
git commit -m "$(cat <<'EOF'
feat(universe): event-driven Topology maintenance on ControlPlane (Phase 5/8)

HostRegistry.AssignCell/ReleaseCell fire an ownership-changed callback;
ControlPlane responds by calling Topology.RebuildNeighborsFor on the
affected cell. Pure-coord processes now maintain topology correctly.

Re-enables Task 13's TestSplitCell_TopologyCorrect/distributed subtest.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 5.3: Phase 5 stability check

```bash
go test ./pkg/universe/ -count=2 -timeout 1200s
```

---

# Phase 6: Drop Config.TestHosts

**Goal:** Delete the multi-host-in-one-process legacy. Config.TestHosts field goes away. Colocated fixture uses single-`*Process` with `RoleAll`. Any tests still on TestHosts get updated.

## Task 6.1: Audit TestHosts call sites

- [ ] **Step 1: Grep for usage**

```bash
grep -rn "TestHosts" --include='*.go' | head -30
```

Categorize:
- Production code (`pkg/universe/coordinator.go`, `pkg/universe/bootstrap.go`) — deleted in next task.
- Test fixtures — migrated to use the distributed fixture instead.
- Documentation / comments — cleaned up.

- [ ] **Step 2: Sanity — all Stage 1 migrated tests should already use `forEachTopology`**

```bash
grep -rn "TestHosts:" pkg/universe/*_test.go
```

Expected: only `cluster_fixture_test.go` (the colocated fixture itself) references TestHosts.

If other test files still list `TestHosts:` in a Config literal, they need migration to `forEachTopology` first. This shouldn't happen after Stage 1 but verify.

## Task 6.2: Update colocated fixture to not use TestHosts

**Files:**
- Modify: `pkg/universe/cluster_fixture_test.go`

- [ ] **Step 1: Rewrite colocatedFixture**

Since colocated mode was TestHosts-based, and we're dropping TestHosts, colocated now means: one `*Process` with `RoleAll` and ONE host. Multi-host testing becomes multi-Process-in-binary (distributed fixture's territory).

Update `newColocatedFixture` in `pkg/universe/cluster_fixture_test.go`:

```go
func newColocatedFixture(t *testing.T, cfg FixtureConfig) clusterFixture {
	t.Helper()
	coords.SetCellSize(cfg.CellSize)

	// Colocated = single Process with RoleAll + exactly one host.
	// Multi-host-in-binary testing lives in distributedFixture now.
	if len(cfg.HostIDs) != 1 {
		// Backward compat for existing tests: if the declared layout
		// has multiple host IDs, force-fall-through to
		// distributedFixture behavior. Simpler: require single host
		// and fail loudly if callers try to use two.
		t.Fatalf("colocatedFixture: expected exactly 1 host ID (got %d %v). Use the distributedFixture for multi-host scenarios.", len(cfg.HostIDs), cfg.HostIDs)
	}

	coord := NewCoordinator(Config{
		CellsX:       cfg.CellsX,
		CellsY:       cfg.CellsY,
		CellSize:     cfg.CellSize,
		HostID:       cfg.HostIDs[0], // single host takes the declared ID
		Headless:     true,
		ConnManager:  net.NewConnManager(),
		Logger:       logger.New(),
		LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
	})
	// ... rest unchanged (SetWorld, Build, cell goroutines, cleanup, return &colocatedFixture{...}) ...
}
```

Wait — this loses multi-host-in-one-process testing for colocated. Since every Stage 1 target test used 2 hosts, colocated subtests would all fail.

Two options:
A. Keep `TestHosts` as-is (don't actually drop it in Phase 6).
B. Migrate the colocated fixture to also be multi-Process-in-binary when >1 host.

Option B is the right call per the spec — "multi-host testing becomes multi-Process-in-binary". The colocated fixture becomes essentially the same as the distributed fixture except ALL processes run with RoleAll + ControlListen on one "coord-of-coords" designated process.

This is a significant refactor of the fixture. Let me instead propose a simpler option:

**Option C (pragmatic):** keep TestHosts internally for the colocated fixture as an implementation detail. External code and tests don't use it. `Config.TestHosts` becomes unexported. Production paths that branched on `len(TestHosts) > 1` still work inside the fixture.

Actually the simpler approach: **Option D** — just drop `TestHosts` from public Config. Colocated fixture stops needing it because colocated becomes "one Process, one Host, the simplest case". Multi-host tests (which are exactly the /distributed subtests) already exist.

But existing colocated subtests that run with 2 hosts would break. Let me think... The Stage 1 tests run /colocated with 2 hosts today. If we drop TestHosts and colocated only supports 1 host, /colocated tests that depend on multi-host (migrate tests, split tests, merge tests) can't run colocated at all — they must become /distributed-only.

That's actually a SIMPLIFICATION worth taking. Tests that need multi-host are by definition distribution tests. Running them "colocated" with TestHosts is the legacy pattern we're eliminating.

- [ ] **Step 2 (revised): Delete colocatedFixture entirely**

A cleaner path: delete the colocated topology entirely. All migrated tests become distributed-only. The harness becomes one-topology instead of two; every test runs the full mesh-protocol path.

This has a cost: longer test runtimes (every test does gRPC setup) and losing the "colocated is fast feedback" dev experience.

**Decision point for implementer:** if you want to preserve fast colocated feedback for simple tests, keep colocated with 1 host (fast path). If you're OK with slower tests, delete colocated entirely. **I recommend keeping colocated as 1-host** — simpler, fast, still useful for tests that don't need multi-host.

Rewrite `colocatedFixture` to support 1 host only:

```go
func newColocatedFixture(t *testing.T, cfg FixtureConfig) clusterFixture {
	t.Helper()
	coords.SetCellSize(cfg.CellSize)

	hostID := "local"
	if len(cfg.HostIDs) >= 1 {
		hostID = cfg.HostIDs[0]
	}
	// Multi-host requests → distributedFixture handles them. Ignore
	// any additional host IDs; log a warning.
	if len(cfg.HostIDs) > 1 {
		t.Logf("colocatedFixture: multi-host request ignored; using only %q. Use distributedFixture for multi-host scenarios.", hostID)
	}

	coord := NewCoordinator(Config{
		CellsX:       cfg.CellsX,
		CellsY:       cfg.CellsY,
		CellSize:     cfg.CellSize,
		HostID:       hostID,
		Headless:     true,
		ConnManager:  net.NewConnManager(),
		Logger:       logger.New(),
		LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
	})
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()

	ctx, cancel := context.WithCancel(context.Background())
	for _, cell := range coord.Cells {
		go cell.Run(ctx)
	}
	time.Sleep(20 * time.Millisecond)

	t.Cleanup(func() {
		cancel()
		coord.Shutdown()
	})

	return &colocatedFixture{coord: coord, hosts: []string{hostID}}
}
```

This means layout seeding for colocated would put ALL cells on the single host. Tests like `TestS7MigrateAcrossHosts` that require 2+ hosts can't run colocated — `forEachTopology` should skip colocated if the declared layout requires multiple hosts. Add that detection:

```go
func forEachTopology(t *testing.T, cfg FixtureConfig, body func(t *testing.T, fx clusterFixture)) {
	t.Helper()
	for _, topo := range topologies {
		// Colocated supports 1 host only; skip if layout needs more.
		if topo.name == "colocated" && len(cfg.HostIDs) > 1 {
			t.Run(topo.name, func(t *testing.T) {
				t.Skip("colocated topology is single-host only; multi-host scenarios run distributed-only")
			})
			continue
		}
		t.Run(topo.name, func(t *testing.T) {
			// ... existing code ...
		})
	}
}
```

- [ ] **Step 3: Test it**

Run the full `TestFixtureSmoke` suite:
```bash
go test ./pkg/universe/ -run TestFixtureSmoke -v -count=1 -timeout 60s
```

`TestFixtureSmoke_Ownership/colocated` will now run with 1 host only. Either update the test to accept that (check only host-a cells are owned; no host-b expectations) OR update FixtureConfig.HostIDs default to just `["host-a"]` in that test.

Update `TestFixtureSmoke_Ownership` to run with the default single-host layout (change Layout asserts accordingly) OR split into two tests: one for single-host coverage (both topologies) and one for multi-host (distributed only).

The simplest fix: most smoke tests use `FixtureConfig{}` which defaults to 2 hosts. Change the normalize default to 1 host:

```go
// In FixtureConfig.normalize():
if len(cfg.HostIDs) == 0 {
	cfg.HostIDs = []string{"host-a"}  // was: []string{"host-a", "host-b"}
}
```

Then the s7_ migrate/split/merge tests (which need 2 hosts) explicitly pass `HostIDs: []string{"host-a", "host-b"}` in their FixtureConfig — and get distributed-only runs via the forEachTopology skip we added.

- [ ] **Step 4: Per-test audit**

Run the full suite:
```bash
go test ./pkg/universe/ -count=1 -timeout 1200s
```

Fix tests that relied on 2-host colocated placements one at a time. For each failing test, decide: skip colocated (test inherently needs multi-host) OR adapt to 1-host colocated.

- [ ] **Step 5: Commit (likely 2-4 commits)**

```bash
git add pkg/universe/cluster_fixture_test.go
git commit -m "test(universe): colocatedFixture is single-host only (Phase 6/8)"
# ...
git add pkg/universe/s7_migrate_test.go
git commit -m "test(universe): mark multi-host-required tests as distributed-only (Phase 6/8)"
# ...
```

## Task 6.3: Delete Config.TestHosts field + branches

**Files:**
- Modify: `pkg/universe/coordinator.go`
- Modify: `pkg/universe/coordinator.go` (Build() multi-host branch)

- [ ] **Step 1: Delete the field**

In `pkg/universe/coordinator.go`, remove the `TestHosts []string` field from `Config` (around line 59).

- [ ] **Step 2: Delete the multi-host branch in Build()**

In `pkg/universe/coordinator.go`, around line 790, the `RoleHost` non-remote branch starts with:

```go
hostIDs := cfg.TestHosts
if len(hostIDs) == 0 {
    hostIDs = []string{"local"}
}
multiHost := len(hostIDs) > 1
```

Replace with:

```go
// Single-host-per-process: Process has exactly one local Host. The
// HostID comes from cfg.HostID (used by tests that need deterministic
// IDs) or defaults to "local" for single-process dev mode.
hostID := cfg.HostID
if hostID == "" {
    hostID = "local"
}
hostIDs := []string{hostID}
multiHost := false
```

Remove all code inside the `if multiHost { ... }` branch — the outer loop still works for len(hostIDs) == 1.

- [ ] **Step 3: Clean up HostNetwork cross-connect loop**

The "multiHost cross-connect" loop that pairs every Host's HostNetwork to every other is dead code now. Remove.

- [ ] **Step 4: Verify**

```bash
go vet ./pkg/universe/...
grep -rn "TestHosts" pkg/universe/  # should return empty
go test ./pkg/universe/ -count=1 -timeout 1200s
```

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "$(cat <<'EOF'
refactor(universe): drop Config.TestHosts and multi-host-in-process path (Phase 6/8)

Single Process now has exactly one local Host. Multi-host testing
happens via multi-Process-in-binary (the distributed fixture). This
kills ~80 lines of legacy code and a source of lock-ordering bugs.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 6.4: Phase 6 stability check

```bash
go test ./pkg/universe/ -count=2 -timeout 1200s
```

---

# Phase 7: Rename Coordinator → Process + mmokit.New

**Goal:** Mechanical rename across the codebase. `Coordinator` → `Process`, `NewCoordinator` → `New`, mmokit facade updated.

## Task 7.1: Rename inside pkg/universe

**Files:**
- Modify: every `.go` file under `pkg/universe/` that references `Coordinator`

- [ ] **Step 1: Use gopls / IDE rename OR sed**

Option A (safer): use `gopls rename` if the tooling supports package-scope rename.

Option B (pragmatic): sed pass.

```bash
# DRY RUN — review first
grep -rln "Coordinator" pkg/universe/ --include='*.go' | xargs -I {} sed -n 's/\(\bCoordinator\b\)/Process/gp' {} | head

# Apply
grep -rln "Coordinator" pkg/universe/ --include='*.go' | xargs sed -i 's/\bCoordinator\b/Process/g'

# Rename NewCoordinator too
grep -rln "NewCoordinator" pkg/universe/ --include='*.go' | xargs sed -i 's/\bNewCoordinator\b/New/g'
```

**IMPORTANT:** this sed pass renames EVERY occurrence of `Coordinator` — including strings, comments, log messages. That's fine for the type name but also affects log strings like `"coordinator: ..."`. Those strings refer to the concept, not the type — they're fine as lowercase `"coordinator: ..."` (already lowercase, so the case-sensitive sed regex `\bCoordinator\b` won't match).

Verify no log-message collateral damage:
```bash
grep -rn "\"coordinator" pkg/universe/ | head
```

- [ ] **Step 2: Verify compile**

```bash
go vet ./pkg/universe/...
```
Expected: clean.

If not clean, the sed missed contextual uses. Fix manually.

- [ ] **Step 3: Tests**

```bash
go test ./pkg/universe/ -count=1 -timeout 600s
```

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/
git commit -m "$(cat <<'EOF'
refactor(universe): rename Coordinator → Process, NewCoordinator → New (Phase 7/8)

Mechanical type rename across pkg/universe. No behavioral change. Ready
for mmokit facade and downstream game updates in follow-up tasks.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 7.2: Update mmokit facade

**Files:**
- Modify: `pkg/mmokit/*.go` — every file re-exporting Coordinator

- [ ] **Step 1: Rename in mmokit**

```bash
grep -rln "Coordinator\|NewCoordinator" pkg/mmokit/ | xargs sed -i -e 's/\bCoordinator\b/Process/g' -e 's/\bNewCoordinator\b/New/g'
```

Verify:
```bash
go vet ./pkg/mmokit/...
```

If `pkg/mmokit` has an entry-point file (check `pkg/mmokit/mmokit.go` or similar), verify it exposes:
```go
type Process = universe.Process
func New(cfg Config) *Process { return universe.New(cfg) }
```

If the existing facade wrapper used a different pattern (factory function + struct), update accordingly.

- [ ] **Step 2: Commit**

```bash
git add pkg/mmokit/
git commit -m "$(cat <<'EOF'
refactor(mmokit): rename facade to Process + New (Phase 7/8)

mmokit.New(Config) *mmokit.Process replaces mmokit.NewCoordinator(Config)
*mmokit.Coordinator. Games get the symbol rename in the next task.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 7.3: Update games + internal

**Files:**
- Modify: `internal/**/*.go`
- Modify: `examples/**/*.go`

- [ ] **Step 1: Rename across the repo**

```bash
find internal/ examples/ -name '*.go' | xargs sed -i -e 's/\bCoordinator\b/Process/g' -e 's/\bNewCoordinator\b/New/g'
```

- [ ] **Step 2: Verify each example builds**

```bash
go vet ./...
go test ./... -count=1 -timeout 1200s
```

- [ ] **Step 3: Commit**

```bash
git add internal/ examples/
git commit -m "$(cat <<'EOF'
refactor(games): Coordinator → Process symbol rename (Phase 7/8)

Mechanical rename for internal/game, internal/bot, examples/*. All games
use mmokit.New(Config) *mmokit.Process now.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 7.4: Phase 7 stability check

```bash
go test ./... -count=2 -timeout 1800s
```

---

# Phase 8: Delete Coordinator wrapper (finalize)

**Goal:** Flatten any residual wrapper indirection. The `Process` struct + planes is the final shape.

## Task 8.1: Audit residual wrappers

- [ ] **Step 1: Grep for lingering indirection**

```bash
grep -rn "cellToHostMapRef\|coordMuRef\|coordEpochRef" pkg/universe/
```

These were Phase 2 bridges. If any remain, remove them now that field ownership has settled.

- [ ] **Step 2: Remove bridge fields**

In `pkg/universe/control_plane.go`, delete the `cellToHostMapRef`, `coordMuRef`, `coordEpochRef` fields. Move `cellToHostMap` and `coordEpoch` ownership onto `ControlPlane` directly (coordEpoch can migrate to `Process` shared state instead; pick whichever is cleaner).

In `OwnerOf`, `AllCells`, etc., replace the bridge reads with direct field access.

- [ ] **Step 3: Move cellToHostMap to ControlPlane**

Delete `cellToHostMap` from `Process` (formerly `Coordinator`). Add to `ControlPlane`:

```go
type ControlPlane struct {
	...
	mu            sync.RWMutex  // guards cellToHostMap + Topology
	cellToHostMap map[string]string
}
```

Every writer of `coord.cellToHostMap` moves to `coord.Control.cellToHostMap` (with coord.Control.mu held). Should be 3-5 writers in cell_transfer_commit.go.

Note: `Process.mu` may still exist for unrelated state (players map, etc.). Don't conflate — separate mutexes for separate invariants.

- [ ] **Step 4: Move coordEpoch**

The coordEpoch is shared across planes (ControlPlane sets it; Gateway reads it). Put it on `Process`:

```go
type Process struct {
	...
	coordEpoch uint64  // moved from Coordinator
	...
}
```

`ControlPlane.coordEpoch()` method becomes:

```go
func (c *ControlPlane) coordEpoch() uint64 {
	if c.process != nil {
		return c.process.coordEpoch
	}
	return 0
}
```

Add `process *Process` back-ref on ControlPlane, set during `New()`.

- [ ] **Step 5: Verify + commit**

```bash
go vet ./pkg/universe/...
go test ./pkg/universe/ -count=1 -timeout 600s
```

```bash
git add pkg/universe/
git commit -m "$(cat <<'EOF'
refactor(universe): remove Phase-2 bridge fields, finalize Process shape (Phase 8/8)

cellToHostMap lives on ControlPlane; coordEpoch lives on Process.
Bridge refs (cellToHostMapRef/coordMuRef/coordEpochRef) deleted. The
god-object Coordinator is now truly gone — Process is a thin composer
of ControlPlane + Host + Gateway.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

## Task 8.2: Final acceptance checks

- [ ] **Step 1: Acceptance criteria grep audit**

Per spec Section "Acceptance criteria":

```bash
# 8. grep -r cellToHostMap ./pkg/universe/
grep -rn "cellToHostMap" pkg/universe/ --include='*.go' | grep -v ControlPlane
# Expected: empty or only field declaration on ControlPlane

# 9. grep -r 'coord.Hosts\[' ./pkg/universe/
grep -rn 'coord\.Hosts\[' pkg/universe/
# Expected: empty

# 10. grep -r 'Config.TestHosts' ./
grep -rn "TestHosts" .
# Expected: only in closed/docs; no code references
```

All three should return nothing (or only documented exceptions).

- [ ] **Step 2: Final 3× full-suite stability**

```bash
go test ./... -count=3 -timeout 1800s
```
Expected: PASS all 3 iterations.

- [ ] **Step 3: Document completion**

Append to `docs/superpowers/specs/2026-04-18-role-separation-design.md` (after "Acceptance criteria"):

```markdown
## Completion (2026-MM-DD)

All 8 phases landed on main. Full suite green 3× under -count. Stage-1
followups 1, 2, 3 resolved. Acceptance criteria verified via grep audit.

Coordinator is gone. Process + ControlPlane + Host + Gateway is the final
shape. mmokit.New(Config) is the canonical entry point.
```

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/specs/2026-04-18-role-separation-design.md
git commit -m "$(cat <<'EOF'
docs(spec): mark role-separation refactor complete (Phase 8/8)

All acceptance criteria met. Stage-1 followups resolved. Final Process
shape in place.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review

**Spec coverage:**
- End-state architecture (Process + ControlPlane + Host + Gateway) → Phases 1, 7, 8 ✓
- Canonical accessor API → Phase 2 ✓
- Unified commit paths (hostOps) → Phases 3, 4 ✓
- CellRename + HostOpAck wire messages → Phases 3.2, 4.1 ✓
- Topology maintenance on ControlPlane → Phase 5 ✓
- Drop TestHosts → Phase 6 ✓
- Rename Coordinator → Process + mmokit.New → Phase 7 ✓
- Delete Coordinator wrapper → Phase 8 ✓
- All 10 acceptance criteria → verified in Task 8.2 ✓

**Placeholder scan:** Phases 2.3 and 7.1 describe multi-file migrations without listing every call site verbatim (there are ~170 and ~63 respectively). Each section gives a concrete pattern for the migration (sed command or per-file substitution rule) and a verification grep — this is sufficient for a skilled implementer. Not a placeholder.

**Type consistency:**
- `hostOps` interface has three methods: `ReleaseCell`, `StartCell`, `RenameCell`. Consistent across Phases 3.1, 3.3, 4.3.
- `HostOpAck` fields: `req_id`, `ok`, `error`. Consistent across proto definition (Phase 3.2) and handler (Phase 4.2).
- `ControlPlane.hostProxy` signature: `(hostID string) hostOps`. Consistent.
- `Process` vs `Coordinator` — Phases 1-6 use `Coordinator`, Phase 7 renames, Phase 8 finalizes. Ordering is explicit.

**Known gaps / caveats:**
- Phase 3's `localHostOps.StartCell` is stubbed. Unused by the commit-path collapse (migrate/split/merge all go through release + rename, not start). Left for a future phase if StartCell becomes needed.
- Phase 6 may force some Stage-1 tests to become `/distributed`-only (those that need >1 host). That's a deliberate tradeoff: simpler fixture, longer-running tests. Implementer makes the per-test call.
- Phase 8.1 assumes `Process.mu` is separable from `ControlPlane.mu`. If it turns out they're used for the same invariants, keep one shared mutex and document why.

No placeholder patterns detected. Plan complete.
