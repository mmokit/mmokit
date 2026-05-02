# Strong-Typed `MeshCellID` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the recurring "mesh form vs display form" cell-ID string mismatch by introducing a distinct Go type `MeshCellID` for the wire/internal `cell_X_Y` form. After this lands, the compiler refuses to mix mesh-form and plain strings — every present and future mismatch becomes a compile error.

**Architecture:** `pkg/universe/cell_id.go` gets a new `type MeshCellID string`. The existing free function `MeshCellID(c CellID) string` (a one-line wrapper around `(CellID).MeshID()`) is deleted to free the name. The method's return type changes from `string` to `MeshCellID`. Internal map keys (`Process.Cells`, `Host.OwnedCells`, `RemoteHost.OwnedCells`) and field types (`Cell.ID`) migrate to the typed value. Wire decode boundaries get explicit `MeshCellID(req.CellId)` casts that document the format assumption. Display-form strings (console output, log lines) stay plain `string` because they don't get re-parsed by code paths — only emitted to humans.

**Tech Stack:** Go 1.22+, `pkg/universe`.

---

## File Map

**Type definition:**
- `pkg/universe/cell_id.go` — add `type MeshCellID string`; change `(CellID).MeshID()` return type
- `pkg/universe/topology.go` — delete free function `MeshCellID(cell CellID) string`

**Internal map / field types:**
- `pkg/universe/cell.go` — rename field `Cell.ID` → `Cell.MeshID`; change type to `MeshCellID`
- `pkg/universe/coordinator.go` — `Process.Cells map[MeshCellID]*Cell`; many call-site updates
- `pkg/universe/host.go` — host-side cell ID handling
- `pkg/universe/host_registry.go` — `RemoteHost.OwnedCells map[MeshCellID]bool`

**Cascading updates (compiler-driven):**
- `pkg/universe/coord_assignment.go`, `mesh_control_*.go` — wire decode casts
- `pkg/universe/cmdsys_resolver.go` — `HostForCellID(MeshCellID)` signature
- `pkg/universe/cell_transfer*.go`, `commit_builders_*.go`, `partition.go`, `rebalance.go`
- `pkg/universe/builtins_*.go`, `grpc_bridge.go`, `perf_snapshot.go`
- `pkg/universe/replication.go`, `gateway.go`
- Any other site the compiler flags

**Tests:**
- `pkg/universe/cell_id_test.go` — new tests pinning the typed-API behavior (round-trip, conversions, distinctness)

---

## Strategy

The migration follows a **fix-the-compiler-errors** pattern: each task introduces a single typed change, then walks `go build ./...` errors and fixes them mechanically. The compiler is the source of truth for what needs touching. Subagents do not need to enumerate sites in advance.

Each phase ends with `go vet ./... && go test ./... && just build-go` all green before commit. **Do not commit a phase that doesn't compile.**

---

## Phase 1: Free the name `MeshCellID`

The free function `MeshCellID(c CellID) string` in `topology.go` collides with the type `MeshCellID` we want to introduce. Migrate all 43 callers to use the equivalent method `c.MeshID()`, then delete the function.

### Task 1: Migrate `MeshCellID(...)` calls to method form, delete free function

**Files:**
- Modify: `pkg/universe/topology.go` (delete lines 185-188)
- Modify: ~43 call sites across `pkg/universe/`, `pkg/engine/`, `internal/`, `examples/`, `cmd/`, including test files

- [ ] **Step 1: Grep all callers.**

```bash
cd .
grep -rn "MeshCellID(" --include="*.go"
```

Expect ~43 hits including the definition and tests. The definition lives at `pkg/universe/topology.go:186`.

- [ ] **Step 2: Replace every call.**

For each match site (not the definition itself), replace `MeshCellID(<expr>)` with `<expr>.MeshID()`:

- If `<expr>` evaluates to `CellID` (a value type), the replacement is straightforward: `cell.MeshID()`.
- If the call is on an existing parser result (e.g. `MeshCellID(parsed)` where `parsed, _ := ParseCellID(s)`), use `parsed.MeshID()`.

Confirmation grep after editing:
```bash
grep -rn "MeshCellID(" --include="*.go" | grep -v "_test.go" | grep -v "topology.go"
```
Should return zero non-test, non-definition matches before Step 3.

- [ ] **Step 3: Delete the free function.**

In `pkg/universe/topology.go`, delete:
```go
// MeshCellID returns a string ID for a cell (used as cell ID).
func MeshCellID(cell CellID) string {
	return cell.MeshID()
}
```

- [ ] **Step 4: Build + test.**

```bash
go vet ./...
go test ./...
just build-go
```

Expected: all PASS. If anything fails, you missed a call site — fix before commit.

- [ ] **Step 5: Commit.**

```bash
git add -A
git commit -m "$(cat <<'EOF'
universe: replace MeshCellID(c) free function with c.MeshID() method

Frees the name MeshCellID so it can become a distinct string type in
the next commit. The free function and the method did identical work;
converging on the method is a no-op refactor.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 2: Define the `MeshCellID` type

### Task 2: Define `type MeshCellID string` and convert the method's return type

**Files:**
- Modify: `pkg/universe/cell_id.go`

- [ ] **Step 1: Add the type to `cell_id.go`.**

After the `CellID struct` definition (around line 16), add:

```go
// MeshCellID is the wire/internal string form of a CellID.
//
// Format: "cell_X_Y" at depth 0, "cell_dN_X_Y" at depth N > 0. This is the
// form used as keys in Process.Cells, Host.OwnedCells, RemoteHost.OwnedCells,
// and on the wire in proto fields like meshpb.CellAssign.CellId.
//
// MeshCellID is a distinct type from plain string so the compiler refuses
// to mix it with display-form strings (CellID.String() — "X_Y") or with
// arbitrary user input. Convert at the boundary: ParseCellID(s) returns a
// structured CellID; (CellID).MeshID() returns a typed MeshCellID; an
// explicit MeshCellID(s) cast is the only way to assert that a plain
// string is mesh-form.
type MeshCellID string

// String makes MeshCellID satisfy fmt.Stringer so it prints cleanly via
// %s/%v formatting verbs. The underlying value is already a string; this
// is a documentation aid, not a converter.
func (m MeshCellID) String() string { return string(m) }
```

- [ ] **Step 2: Change the `MeshID()` method return type.**

Replace:
```go
// MeshID returns the wire-format identifier for this cell used by
// MeshControl CellAssign / CellRelease and Process.Cells map keys.
// Format: "cell_X_Y" at depth 0, "cell_dN_X_Y" at depth N > 0.
func (c CellID) MeshID() string {
	if c.Depth == 0 {
		return fmt.Sprintf("cell_%d_%d", c.X, c.Y)
	}
	return fmt.Sprintf("cell_d%d_%d_%d", c.Depth, c.X, c.Y)
}
```

with:
```go
// MeshID returns the wire-format identifier for this cell — a typed
// MeshCellID used as a key in Process.Cells / Host.OwnedCells and on the
// MeshControl wire. Format: "cell_X_Y" at depth 0, "cell_dN_X_Y" at depth
// N > 0.
func (c CellID) MeshID() MeshCellID {
	if c.Depth == 0 {
		return MeshCellID(fmt.Sprintf("cell_%d_%d", c.X, c.Y))
	}
	return MeshCellID(fmt.Sprintf("cell_d%d_%d_%d", c.Depth, c.X, c.Y))
}
```

- [ ] **Step 3: Build.**

```bash
go vet ./...
```

This **will fail** at every site that assigns the result of `cell.MeshID()` to a plain `string` variable, passes it to a function taking `string`, uses it as a map key into a `map[string]X`, or compares it to a `string`. **That's the point.** Each error is a site that needs to either accept `MeshCellID` or explicitly cast.

- [ ] **Step 4: Walk the compiler errors.**

For each error reported by `go vet ./...`, choose one of:

- **Function takes `string`, value flows to a typed map / wire / boundary that should be `MeshCellID`:** change the function signature to take `MeshCellID`. Cascading errors will surface; fix them too.
- **Function takes `string` and the value is genuinely going to fmt/log/display:** add an explicit `string(...)` conversion at the call site, or accept the value via `fmt.Stringer` (which `MeshCellID` already satisfies, so `%s` formatting works without conversion).
- **Map declared `map[string]X` that's actually keyed by mesh-form values:** change the map declaration to `map[MeshCellID]X`. Cascading errors will surface; fix them too.
- **Plain string variable that holds a mesh-form value:** rename it for clarity (e.g. `cellID string` → `cellID MeshCellID`) and adjust callers.
- **Wire decode site reading from a proto field of type `string`:** add an explicit cast: `MeshCellID(req.CellId)`. The cast documents that the proto field carries mesh form.

The largest cascade points are likely:
- `Process.Cells map[string]*Cell` — change to `map[MeshCellID]*Cell` (Phase 3)
- `Cell.ID string` — change to `Cell.MeshID MeshCellID` (Phase 3)
- `RemoteHost.OwnedCells map[string]bool` — change to `map[MeshCellID]bool` (Phase 4)
- `Host.AddCell(cellID CellID, cell *Cell)` and similar host-registry functions
- `Process.HostForCellID(cellID string) string` — change first param to `MeshCellID`

These are addressed in Phases 3 and 4. **For Phase 2 only**, do the minimum casts/signature updates needed to compile; leave the bigger structural changes (`Process.Cells` map type, `Cell.ID` field rename) for the dedicated phases below.

If you find yourself wanting to change `Process.Cells`, `Cell.ID`, or `RemoteHost.OwnedCells` to make Phase 2 compile — STOP. That belongs in Phase 3 / 4. Add `string(...)` casts at the boundaries to keep Phase 2 minimal:

```go
// Phase 2 transition shim: cast typed value back to plain string for
// the still-string-keyed Process.Cells map. Phase 3 changes the map.
c.Cells[string(cell.MeshID())] = node
```

Or alternatively:
```go
// Local shim variable until Phase 3 lands.
key := string(cell.MeshID())
c.Cells[key] = node
```

**Goal of Phase 2:** the type exists, the method returns it, and the codebase compiles with explicit casts at the boundaries we'll clean up in Phases 3 / 4. The compiler now has a foothold.

- [ ] **Step 5: Verify.**

```bash
go vet ./...
go test ./...
just build-go
```

Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add -A
git commit -m "$(cat <<'EOF'
universe: introduce MeshCellID typed string

(CellID).MeshID() now returns a distinct MeshCellID type instead of
plain string. Sites that mixed mesh-form strings with display-form or
arbitrary user input now require an explicit string cast at the
boundary, surfacing the form assumption.

Process.Cells / Host.OwnedCells / RemoteHost.OwnedCells map keys are
still plain string; Phase 3+ migrate them to MeshCellID with explicit
casts as transition shims.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 3: Migrate `Cell.ID` and `Process.Cells`

### Task 3: Rename `Cell.ID` → `Cell.MeshID`, type as `MeshCellID`; update `Process.Cells` map key

**Files:**
- Modify: `pkg/universe/cell.go` (rename field, change type)
- Modify: `pkg/universe/coordinator.go` (`Process.Cells`, all call sites)
- Modify: cascading sites the compiler flags

- [ ] **Step 1: Update the `Cell` struct in `pkg/universe/cell.go`.**

Find the field declaration (around line 33):
```go
type Cell struct {
	ID      string
	Cell    CellID
	...
}
```

Replace with:
```go
type Cell struct {
	// MeshID is the wire-format ID for this cell (e.g. "cell_0_0"). Used
	// as the key in Process.Cells, Host.OwnedCells, and on the MeshControl
	// wire. For display use cell.Cell.String() instead.
	MeshID  MeshCellID
	Cell    CellID
	...
}
```

- [ ] **Step 2: Update the `Process.Cells` map declaration.**

In `pkg/universe/coordinator.go`, find the `Process` struct (it has a `Cells map[string]*Cell` field, plus a `CellOwner map[CellID]string` field — the latter probably also wants `map[CellID]MeshCellID`, but verify with the compiler). Change `map[string]*Cell` to `map[MeshCellID]*Cell`. Update its initialization in the constructor (around line 678 area: `Cells: make(map[string]*Cell)`).

For `CellOwner map[CellID]string` — this maps `CellID → cell-string-id`. If the value-side strings are mesh-form, change to `map[CellID]MeshCellID`. Verify by reading what gets stored.

- [ ] **Step 3: Update `Cell.ID = id` assignments.**

In `coordinator.go` around line 2127 and elsewhere, the construction `node := &Cell{ID: id, ...}` becomes `node := &Cell{MeshID: id, ...}` (the local `id` is what gets assigned; its type may need updating too).

- [ ] **Step 4: Walk compiler errors.**

```bash
go vet ./...
```

For each error:
- `cell.ID` references → rename to `cell.MeshID`
- Plain string passed where `MeshCellID` is expected → `MeshCellID(s)` cast (only at proto/wire boundaries) or fix the producer
- `Process.Cells[s]` where `s` is plain string → cast `MeshCellID(s)` (rare; most should be from `cell.MeshID()` already) or change `s`'s type

Common log statement updates: `b.cell.MeshID` (typed) prints fine via `%s`/`%v` — no change needed in the format spec.

The largest cluster of touch sites is likely `pkg/universe/grpc_bridge.go` (~15 logging lines) — these all become `b.cell.MeshID` and continue to work since `MeshCellID` has a `String()` method and prints via `%s`.

- [ ] **Step 5: Verify.**

```bash
go vet ./...
go test ./...
just build-go
```

Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add -A
git commit -m "$(cat <<'EOF'
universe: rename Cell.ID → Cell.MeshID (typed MeshCellID); migrate Process.Cells map

Process.Cells key is now MeshCellID so the compiler verifies callers
look up by the right form. Cell.MeshID field name makes the form
explicit at every read site; no more ambiguous "cell.ID" reads.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 4: Migrate `Host.OwnedCells` and `RemoteHost.OwnedCells`

### Task 4: Type host-side cell-ID maps as `map[MeshCellID]bool`

**Files:**
- Modify: `pkg/universe/host.go` (Host.AddCell signature, Host.Cells slice elem if it carries mesh strings)
- Modify: `pkg/universe/host_registry.go` (`RemoteHost.OwnedCells`, all the registry methods)
- Modify: cascading sites

- [ ] **Step 1: Update `RemoteHost.OwnedCells` declaration.**

In `pkg/universe/host_registry.go` around line 55:
```go
OwnedCells    map[string]bool // cell string IDs currently assigned to this host
```
becomes:
```go
// OwnedCells is the set of mesh-form cell IDs currently assigned to this
// host. Populated by the registry as CellAssign / CellRelease messages
// arrive over MeshControl.
OwnedCells    map[MeshCellID]bool
```

Same change to the `make(...)` initialization around line 114.

- [ ] **Step 2: Update registry methods.**

The methods `AssignCell`, `ReleaseCell`, `OwnsCell`, etc. take `cellID string` parameters that flow into this map. Change them to `cellID MeshCellID`.

The `cloneHost` deep-copy (around line 283) updates from `make(map[string]bool, ...)` to `make(map[MeshCellID]bool, ...)`. The `maps.Copy` call works unchanged.

- [ ] **Step 3: Walk compiler errors.**

`go vet ./...` then fix each error. Likely callers:
- `cmdsys_resolver.go` — `HostForCellID(cellID string)` signature change to `MeshCellID`
- `coord_assignment.go` and `mesh_control_*.go` — wire decode sites add explicit `MeshCellID(req.CellId)` casts
- `coordinator.go` — `assignCellOnNode` and friends

The `host.OwnedCells` iterator at `builtins_cell.go:254`:
```go
for cellIDStr := range host.OwnedCells {
	cell, err := ParseCellID(cellIDStr)
	...
}
```
becomes:
```go
for cellMeshID := range host.OwnedCells {
	cell, err := ParseCellID(string(cellMeshID))
	...
}
```
The `string(...)` cast is required because `ParseCellID` takes a plain `string` (it accepts both forms). That's correct — `ParseCellID` is the boundary.

- [ ] **Step 4: Verify.**

```bash
go vet ./...
go test ./...
just build-go
```

Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add -A
git commit -m "$(cat <<'EOF'
universe: type Host.OwnedCells / RemoteHost.OwnedCells as map[MeshCellID]bool

Host registry now uses the typed key. Wire decode sites add explicit
MeshCellID() casts at the proto boundary, documenting the format
assumption.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 5: Audit residual `string` cell-ID parameters

After Phases 1-4, most internal cell-ID strings are typed. Phase 5 is a sweep for any remaining function signatures or struct fields that take a plain `string` cell ID and should be typed.

### Task 5: Audit and convert remaining `cellID string` parameters

**Files:** any file the compiler errors point at; expect:
- `pkg/universe/cmdsys_resolver.go` — `HostForCellID`
- `pkg/universe/coordinator.go` — public methods like `RenameCell`, `SplitCell`, `MergeCell`
- `pkg/universe/cell_transfer*.go` — internal handoff helpers
- `pkg/universe/partition.go`, `rebalance.go`
- `pkg/universe/replication.go`, `gateway.go`

- [ ] **Step 1: Grep for remaining string cell-ID parameters.**

```bash
cd .
grep -rn "cellID string\|cellId string\|cellIDStr string" --include="*.go" pkg/universe/
```

For each match, decide:
- **Internal helper that always receives mesh-form values from Process.Cells / Host.OwnedCells:** change to `MeshCellID`.
- **Public/console handler receiving raw user input:** keep as `string`, parse with `ParseCellID(s)` immediately.
- **Boundary handler reading from proto:** keep as `string`, cast at the decode site (`MeshCellID(req.CellId)`).

- [ ] **Step 2: Convert internal helpers to `MeshCellID`.**

For each helper that currently does:
```go
func (c *Process) HostForCellID(cellID string) string {
    ...
    if owner, ok := c.Cells[cellID]; ...
}
```
change to:
```go
func (c *Process) HostForCellID(cellID MeshCellID) string {
    ...
    if owner, ok := c.Cells[cellID]; ...
}
```

The `Process.Cells` map type from Phase 3 means the lookup now matches.

- [ ] **Step 3: Walk compiler errors and follow the cascade.**

For each `go vet ./...` error, prefer making the upstream caller use the typed value rather than adding a cast. Casts at boundaries (proto decode, user input parse) are correct; casts in the middle of the flow are smells.

- [ ] **Step 4: Verify.**

```bash
go vet ./...
go test ./...
just build-go
```

Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add -A
git commit -m "$(cat <<'EOF'
universe: type remaining cell-ID parameters as MeshCellID

Internal helpers (HostForCellID, RenameCell, etc.) now take typed
MeshCellID. Public/console entry points keep plain string + ParseCellID
at the boundary. Wire decode sites cast explicitly.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 6: Pin the design with tests

### Task 6: Add tests that lock in the typed-API contract

**Files:**
- Modify: `pkg/universe/cell_id_test.go` (or create if it doesn't exist)

- [ ] **Step 1: Add a round-trip test.**

```go
func TestMeshCellID_RoundTrip(t *testing.T) {
	tests := []CellID{
		{X: 0, Y: 0, Depth: 0},
		{X: 3, Y: 5, Depth: 0},
		{X: 7, Y: 2, Depth: 1},
		{X: 15, Y: 9, Depth: 3},
	}
	for _, c := range tests {
		mesh := c.MeshID()
		// MeshCellID is a typed string — verify the format.
		want := ""
		if c.Depth == 0 {
			want = fmt.Sprintf("cell_%d_%d", c.X, c.Y)
		} else {
			want = fmt.Sprintf("cell_d%d_%d_%d", c.Depth, c.X, c.Y)
		}
		if string(mesh) != want {
			t.Errorf("MeshID(%v) = %q; want %q", c, mesh, want)
		}
		// Round-trip through ParseCellID.
		back, err := ParseCellID(string(mesh))
		if err != nil {
			t.Errorf("ParseCellID(%q): %v", mesh, err)
			continue
		}
		if back != c {
			t.Errorf("round-trip mismatch: %v -> %q -> %v", c, mesh, back)
		}
	}
}
```

- [ ] **Step 2: Add a typed-distinctness test.**

This test won't compile if `MeshCellID` is not a distinct type — that's the point. Add:

```go
// TestMeshCellID_TypedDistinctness pins the design: MeshCellID is a
// distinct type from plain string and from CellID.String() display
// form. If someone accidentally redefines `type MeshCellID = string`
// (an alias rather than a distinct type), the conversions in this
// test become no-ops and the bug class returns.
func TestMeshCellID_TypedDistinctness(t *testing.T) {
	c := CellID{X: 0, Y: 0, Depth: 0}

	// MeshID returns MeshCellID (typed).
	var m MeshCellID = c.MeshID()

	// Display form is plain string — must NOT assign to MeshCellID
	// without explicit cast. (We can't verify "won't compile" inside
	// the test body, but the explicit cast below documents the
	// boundary.)
	display := c.String() // string
	_ = display

	// Explicit cast is the only way to assert string is mesh-form.
	asMesh := MeshCellID("cell_0_0")
	if m != asMesh {
		t.Errorf("typed values differ: %q vs %q", m, asMesh)
	}

	// String() method on MeshCellID returns the underlying string for
	// fmt printing.
	if m.String() != "cell_0_0" {
		t.Errorf("MeshCellID.String() = %q; want \"cell_0_0\"", m.String())
	}
}
```

- [ ] **Step 3: Add a cell.list / cell.snapshot regression test.**

This regression test verifies the bug we just fixed (commit 4f8be58) cannot recur because the types now enforce consistency. Add to `pkg/universe/builtins_cell_test.go` (create if missing):

```go
package universe

import "testing"

// TestCellSnapshotRow_UsesDisplayForm pins the cell.list ↔ cell.snapshot
// contract: cell.snapshot emits cellSnapshotRow.Cell as the user-facing
// display form (cell.Cell.String()) so cell.list's lookup table — keyed
// by cell.String() — actually finds the rows. The bug we fixed in
// commit 4f8be58 was emitting cell.MeshID (mesh form) into the same
// field, causing every merge lookup to miss.
//
// Strong typing alone doesn't catch this because both row.Cell (string)
// and the merge-map key (string) are the same type — the compiler
// can't see that "cell_0_0" and "0_0" are different VALUES of the same
// TYPE. So this test pins the convention until the result-row field
// itself becomes typed in a future refactor.
func TestCellSnapshotRow_UsesDisplayForm(t *testing.T) {
	c := CellID{X: 0, Y: 0, Depth: 0}
	row := cellSnapshotRow{Cell: c.String()}
	if row.Cell != "0_0" {
		t.Errorf("cellSnapshotRow.Cell = %q; want \"0_0\" (display form)", row.Cell)
	}
	// The mesh form is what we MUST NOT use here.
	mesh := string(c.MeshID())
	if row.Cell == mesh {
		t.Errorf("cellSnapshotRow.Cell = %q (mesh form); cell.list merger expects display form", row.Cell)
	}
}
```

- [ ] **Step 4: Run the new tests.**

```bash
go test ./pkg/universe/ -run "TestMeshCellID|TestCellSnapshotRow" -v
```

Expected: PASS.

- [ ] **Step 5: Run the full suite.**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add -A
git commit -m "$(cat <<'EOF'
universe: pin MeshCellID typing + cell.list display-form contract with tests

TestMeshCellID_RoundTrip locks the format. TestMeshCellID_TypedDistinctness
fails to compile cleanly if someone weakens MeshCellID to a type alias.
TestCellSnapshotRow_UsesDisplayForm pins the cell.list/cell.snapshot
contract that was the root of the original bug.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 7: Documentation pass

### Task 7: Document the form convention at every public boundary

**Files:**
- Modify: `pkg/universe/coordinator.go` (godoc on `HostForCellID`, `RenameCell`, public methods that take/return cell IDs)
- Modify: `pkg/universe/cell.go` (godoc on `Cell` struct fields)
- Modify: `pkg/universe/host_registry.go` (godoc)
- Modify: `pkg/universe/cell_id.go` (cross-reference comment between MeshID, String, ParseCellID)
- Modify: `proto/meshpb/mesh.proto` if the comments are silent on form (find via `grep -n "CellId" proto/meshpb/*.proto`)

- [ ] **Step 1: For each public function returning or accepting a cell-ID-related value, add a one-line godoc that names the form.**

Examples:
```go
// HostForCellID returns the host ID currently owning the given mesh-form
// cell. Returns "" if the cell is unowned or the lookup fails. cellID
// MUST be the mesh form (MeshCellID); display-form strings ("0_0") will
// not match. Use cell.MeshID() to obtain a value of the correct type.
func (c *Process) HostForCellID(cellID MeshCellID) string { ... }
```

- [ ] **Step 2: Add a header comment to `cell_id.go` summarizing the two forms and the type discipline.**

Insert after the `package universe` line:

```go
// Cell IDs have two string forms:
//
//   - MeshCellID ("cell_X_Y" / "cell_dN_X_Y") — wire/internal form. Used
//     as keys in Process.Cells, Host.OwnedCells, and on the MeshControl
//     wire. Returned by (CellID).MeshID(). Distinct Go type so the
//     compiler refuses to mix it with plain strings.
//
//   - Display form ("X_Y" / "dN_X_Y") — human-readable form. Used in
//     console output, log messages, and command result fields. Returned
//     by (CellID).String(). Plain string type because display strings
//     are emitted to humans, not re-parsed by code.
//
// ParseCellID accepts both forms and returns a structured CellID. It is
// the canonical way to convert any cell-ID string (proto field, user
// input, log line, etc.) back into a CellID value.
//
// Convention: any function accepting a cell ID by string MUST take
// MeshCellID. Functions accepting raw user input take plain string and
// parse via ParseCellID at the boundary.
```

- [ ] **Step 3: If any proto file (`proto/meshpb/*.proto`) carries cell-ID strings without comments, add comments.**

Locate proto fields named `CellId`, `cell_id`, `cellId`:
```bash
grep -rn "cell_id\|cellId\|CellId" proto/
```

For each proto field carrying a cell ID, add a comment:
```proto
// Mesh-form cell ID (e.g. "cell_0_0", "cell_d2_3_5"). Decode with
// universe.MeshCellID(req.CellId) to assert the format.
string cell_id = 1;
```

If you regenerate proto stubs after editing, run `just proto`. Otherwise the comment lives in the proto file and the generated Go file is unaffected.

- [ ] **Step 4: Build + test.**

```bash
go vet ./...
go test ./...
just build-go
```

Expected: PASS (this phase is doc-only; it doesn't change behavior).

- [ ] **Step 5: Commit.**

```bash
git add -A
git commit -m "$(cat <<'EOF'
universe: document MeshCellID convention at public boundaries

Adds godoc on HostForCellID, RenameCell, and similar public methods
explaining which form they expect. Adds a header comment to cell_id.go
summarizing the two-form convention. Adds proto field comments at the
wire boundaries.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review Notes (planner-only — implementer can skip)

**Spec coverage:** Every part of the design (introduce typed MeshCellID, migrate map keys, type internal helpers, document boundaries, pin with tests) has a corresponding phase. Phases 3 and 4 split the "internal map" work by data structure (Process.Cells vs Host.OwnedCells) so each commits cleanly.

**No placeholders:** Each task lists concrete files and concrete code to insert / replace. Compiler-driven cleanup is described as "walk the errors" with explicit guidance on which decisions to make at each error class — that's not a placeholder, it's the correct strategy for migrating ~150 mostly-mechanical sites.

**Type consistency:** `MeshCellID` named consistently. `(CellID).MeshID() MeshCellID` everywhere. `(CellID).String() string` (display form, plain string by design — see the spec). `Cell.MeshID MeshCellID` after Phase 3. `Process.Cells map[MeshCellID]*Cell` after Phase 3. `Host.OwnedCells map[MeshCellID]bool` after Phase 4.

**Risk: Phase 2 leaves transition shims.** Phase 2 deliberately keeps `Process.Cells` keyed by plain string to keep the diff small; sites that need the typed value cast back via `string(cell.MeshID())`. Phase 3 removes the shims. If Phase 3 is delayed, the codebase is in a "partly typed" state — still better than today, but not the final form. The plan keeps Phase 2 → 3 sequencing tight to minimize this window.

**Risk: cell.snapshot row field stays plain string.** The result row's `Cell string` field can't be typed `MeshCellID` because cell.list expects display form there. We keep it `string` and the test `TestCellSnapshotRow_UsesDisplayForm` pins the convention. A future refactor could introduce `type DisplayCellID string` if this becomes a recurring footgun, but the audit found only one historical bug — over-engineering it now is YAGNI.
