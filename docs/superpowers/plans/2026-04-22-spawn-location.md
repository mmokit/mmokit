# Spawn Location System Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `coords.SpawnPoint` + `coords.WorldCenterOfCell` with a single `coords.Location{X, Y, Facing, Tag}` type that flows from the login/respawn resolver through `PlayerSession.SpawnLocation` into a new `WorldBase.SpawnAtLocation` helper. Remove the silent `anyCellID` routing fallback. Fix fresh-login cell routing in 4node-basic distributed mode.

**Architecture:** New type lives in `pkg/coords`. Two new integration points: a field on `engine.PlayerSession` (adds a `pkg/engine → pkg/coords` dependency — coords is a leaf, so this is fine) and a spawn helper on `universe.WorldBase` that converts world-space → cell-local using `rootCell()`. Migration is staged: the new type, field, option, helper, and proto fields land *additively* first; a single "flip day" commit then swaps every caller in one coherent change. No backcompat aliases (per project preference).

**Tech Stack:** Go 1.22, Protobuf (buf generate), `github.com/mlange-42/ark/ecs`, PostgreSQL-backed persistence (unchanged).

**Spec:** [docs/superpowers/specs/2026-04-22-spawn-location-design.md](../specs/2026-04-22-spawn-location-design.md)

---

## File Structure

**Created:**
- `pkg/coords/location.go` — new `Location` type + `IsZero()` method
- `pkg/coords/location_test.go` — type tests

**Modified (additive first, then flip-day):**
- `pkg/coords/spawn.go` — deleted at flip day (Task 12)
- `pkg/mmokit/mmokit.go:420-445` — facade: add `Location`, `WithFacing`, `SpawnAtLocation`; drop `SpawnPoint` and `WorldCenterOfCell` exports at flip day
- `pkg/engine/player_session.go:32-43` — add `SpawnLocation` field
- `pkg/universe/world_base.go:58-110, 112-120, 1387-*` — add `WithFacing` option, `SpawnAtLocation` method, invariant check
- `pkg/universe/world_base_test.go` (may need creation) — test `SpawnAtLocation` across root cells
- `pkg/universe/message.go:27-39, 116-131` — extend `SpawnTransfer` and `PlayerAssignment` Go structs with `SpawnLocation` field
- `pkg/universe/coordinator.go:154-160` — `DefaultSpawn` field type
- `pkg/universe/spawn_resolver.go:20-82` — `SpawnResolver` type, `resolveSpawn` body
- `pkg/universe/gateway.go:98-104, 263-305` — `localSession.spawnLoc`, `processLogin` drops `anyCellID` fallback, dispatches with Location
- `pkg/universe/cell_bridge_impl.go:173-204` — `RequestRespawn` resolves Location, includes in `SpawnTransfer`, drops "any cell" fallback
- `pkg/universe/universe_test.go:125-131` — fixture default
- `proto/meshpb/mesh.proto:369-400` — proto `Location` message, extend `SpawnTransfer` + `PlayerAssignment`
- `gen/go/meshpb/*` — regenerated
- `cmd/server/main.go:363-376` — `SetSpawnResolver` closure returns `coords.Location`
- `examples/4node-basic/main.go:25-46` — literal `Location{}` DefaultSpawn
- `examples/4node-basic/mesh_e2e_test.go:160-215` — two Config literals
- `examples/4node-basic/world.go:56-77, 119-138` — `OnEnter` uses `SpawnAtLocation`, drop local `spawnPlayer`
- `examples/slither/main.go:42-55` — set `cfg.DefaultSpawn` with explicit coords

**Deleted (flip day):**
- `pkg/coords/spawn.go` (superseded by `location.go`)
- `SpawnPoint` type, `WorldCenterOfCell` function (and facade re-exports)
- `anyCellID` call site in `gateway.processLogin` (function itself can stay — it's still used as a true-emptiness fallback elsewhere; verify in Task 10)

---

## Caller Inventory (pinned 2026-04-22)

From earlier `rg` on current `main`:

1. `pkg/coords/spawn.go` — definition
2. `pkg/mmokit/mmokit.go:437,442` — facade aliases
3. `pkg/universe/coordinator.go:160` — `DefaultSpawn coords.SpawnPoint`
4. `pkg/universe/spawn_resolver.go:23` — `type SpawnResolver func(...)`
5. `pkg/universe/gateway.go:69` — `defaultSpawn coords.SpawnPoint`
6. `pkg/universe/universe_test.go:128-129` — fixture fallback
7. `pkg/universe/universe_test.go:641` — `coords.SpawnPoint{X: ..., Y: ...}` literal (already world-space)
8. `examples/4node-basic/main.go:37` — `mmokit.WorldCenterOfCell(0, 0)` (the bug)
9. `examples/4node-basic/mesh_e2e_test.go:179` — coord Config
10. `examples/4node-basic/mesh_e2e_test.go:211` — host Config
11. `examples/slither/main.go:49` — `cfg.DefaultSpawn = mmokit.WorldCenterOfCell(0, 0)`
12. `cmd/server/main.go:367-375` — `SetSpawnResolver` closure (returns `(float32, float32, bool)`)

Every item above must be addressed by the flip-day commit in Task 11.

---

### Task 1: Create `coords.Location` type

**Files:**
- Create: `pkg/coords/location.go`
- Create: `pkg/coords/location_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/coords/location_test.go`:

```go
package coords

import "testing"

func TestLocation_IsZero(t *testing.T) {
	if !(Location{}).IsZero() {
		t.Fatalf("zero-value Location should report IsZero()==true")
	}
	if (Location{X: 1}).IsZero() {
		t.Fatalf("Location{X:1} should not be zero")
	}
	if (Location{Facing: 0.1}).IsZero() {
		t.Fatalf("Location{Facing:0.1} should not be zero")
	}
	if (Location{Tag: "x"}).IsZero() {
		t.Fatalf("Location{Tag:\"x\"} should not be zero")
	}
}

// TestLocation_NoStaleGlobalRead pins that the new API never reads the
// package-global CellSize — the bug class the whole redesign eliminates.
// Constructing a Location literal must produce identical fields regardless
// of what CellSize is set to when the literal is evaluated.
func TestLocation_NoStaleGlobalRead(t *testing.T) {
	prev := CellSize
	defer func() { CellSize = prev }()

	CellSize = 8192
	a := Location{X: 1000, Y: 1000, Facing: 1.57, Tag: "bank"}
	CellSize = 2000
	b := Location{X: 1000, Y: 1000, Facing: 1.57, Tag: "bank"}
	if a != b {
		t.Fatalf("Location literals must not depend on global CellSize: a=%+v b=%+v", a, b)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/coords/... -run TestLocation -v`

Expected: FAIL — `undefined: Location` build error.

- [ ] **Step 3: Write the Location type**

Create `pkg/coords/location.go`:

```go
package coords

// Location is a world-space anchor for placing a player entity — used for
// initial spawn, respawn, and (later) teleport/warp. The coord frame is
// absolute world-space, NOT cell-local, so a Location survives cell
// split/merge: the gateway resolves which cell currently owns (X, Y) at
// dispatch time.
//
// Facing is in radians, 0 = +X axis. The engine does not auto-apply it —
// games opt in by passing mmokit.WithFacing(loc.Facing) when spawning.
//
// Tag is opaque to the engine. Games that want tagged destinations
// ("bank", "tutorial_start") populate it; games that don't leave it empty.
type Location struct {
	X, Y   float32 // world-space coordinates
	Facing float32 // radians, 0 = +X axis
	Tag    string  // game-defined; ignored by engine
}

// IsZero reports whether l is the zero value — useful as a "no preference"
// sentinel in callers that optionally override a default.
func (l Location) IsZero() bool { return l == (Location{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/coords/... -v`

Expected: PASS. All existing tests and both new ones.

- [ ] **Step 5: Commit**

```bash
git add pkg/coords/location.go pkg/coords/location_test.go
git commit -m "feat(coords): add Location type for spawn/respawn/teleport

Location is a world-space destination descriptor with optional facing
angle and a game-defined tag. Purely additive — existing SpawnPoint
and WorldCenterOfCell remain in place until the flip-day migration."
```

---

### Task 2: Add `PlayerSession.SpawnLocation` field

**Files:**
- Modify: `pkg/engine/player_session.go:32-43`

- [ ] **Step 1: Add a test for the new field**

Append to `pkg/engine/player_manager_test.go` (create if missing is fine — verify first by opening `ls pkg/engine/`):

```go
func TestPlayerSession_SpawnLocationField(t *testing.T) {
	s := &PlayerSession{SpawnLocation: coords.Location{X: 100, Y: 200, Facing: 1.57, Tag: "bank"}}
	if s.SpawnLocation.X != 100 || s.SpawnLocation.Y != 200 {
		t.Fatalf("SpawnLocation not retained: %+v", s.SpawnLocation)
	}
	if s.SpawnLocation.Facing != 1.57 || s.SpawnLocation.Tag != "bank" {
		t.Fatalf("SpawnLocation facing/tag not retained: %+v", s.SpawnLocation)
	}
}
```

Add `"github.com/zenion/mmoserver/pkg/coords"` to the test file's imports.

- [ ] **Step 2: Run the test to verify compile failure**

Run: `go test ./pkg/engine/... -run TestPlayerSession_SpawnLocationField -v`

Expected: build error — `unknown field SpawnLocation`.

- [ ] **Step 3: Add the field + import**

Edit `pkg/engine/player_session.go`. Replace the imports block (top of file) with:

```go
package engine

import (
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/coords"
)
```

Then replace the `PlayerSession` struct (lines 32-43) with:

```go
type PlayerSession struct {
	ID             SessionID
	ConnID         uint32      // 0 = no active connection
	Username       string
	State          PlayerState
	Entity         ecs.Entity  // zero-value when no entity exists
	Data           any         // game-specific session data
	PriorState     PlayerState // state before disconnect, for reconnect resume
	DisconnectTime time.Time   // when connection was lost

	// SpawnLocation is the world-space point the gateway resolved for this
	// session's login (or the most recent respawn/teleport). Populated by
	// the gateway before dispatching the PlayerAssignment; read by the
	// game's OnEnter handler via gw.SpawnAtLocation(s.SpawnLocation, ...).
	SpawnLocation coords.Location

	isTransfer bool // true if created via RegisterTransferSession (entity already exists)
}
```

- [ ] **Step 4: Run the test**

Run: `go test ./pkg/engine/... -run TestPlayerSession_SpawnLocationField -v`

Expected: PASS.

- [ ] **Step 5: Verify full engine tests still pass**

Run: `go test ./pkg/engine/... -v`

Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add pkg/engine/player_session.go pkg/engine/player_manager_test.go
git commit -m "feat(engine): add PlayerSession.SpawnLocation field

The gateway populates this before dispatching a PlayerAssignment; the
game's OnEnter handler reads it to place the entity. Adds a pkg/engine
→ pkg/coords import; coords is a leaf so no new cycle risk."
```

---

### Task 3: Add `WithFacing` spawn option

**Files:**
- Modify: `pkg/universe/world_base.go:58-110` (add after `WithRotation`)

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/world_base_test.go` (create the file if absent):

```go
package universe

import (
	"testing"
)

func TestWithFacing_SetsRotation(t *testing.T) {
	var o spawnOpts
	WithFacing(1.5708).apply(&o) // helper below
	if !o.hasRot {
		t.Fatalf("WithFacing did not set hasRot")
	}
	if o.rotation != 1.5708 {
		t.Fatalf("WithFacing rotation = %v, want 1.5708", o.rotation)
	}
}

// test-only helper so we can apply a SpawnOption without running through
// a full WorldBase.SpawnEntity. Lives in the test file.
func (f SpawnOption) apply(o *spawnOpts) { f(o) }
```

- [ ] **Step 2: Run the test — should fail to compile**

Run: `go test ./pkg/universe/... -run TestWithFacing_SetsRotation -v`

Expected: build error — `undefined: WithFacing`.

- [ ] **Step 3: Add `WithFacing`**

Edit `pkg/universe/world_base.go`. Add immediately after `WithRotation` (around line 104):

```go
// WithFacing sets the entity's facing angle from a Location.Facing value.
// Equivalent to WithRotation — provided as a dedicated option so the
// intent ("apply the destination's facing") is obvious at the call site
// and so a future teleport API can reuse it without semantic drift.
func WithFacing(radians float32) SpawnOption {
	return func(o *spawnOpts) {
		o.rotation = radians
		o.hasRot = true
	}
}
```

- [ ] **Step 4: Run the test**

Run: `go test ./pkg/universe/... -run TestWithFacing_SetsRotation -v`

Expected: PASS.

- [ ] **Step 5: Add facade re-export**

Edit `pkg/mmokit/mmokit.go`. Find the existing `WithRotation = universe.WithRotation` line (search for it) and add a `WithFacing` sibling right after:

```go
// WithFacing sets the entity's facing angle (radians) from a Location.
var WithFacing = universe.WithFacing
```

- [ ] **Step 6: Verify full build**

Run: `go vet ./...`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/world_base.go pkg/universe/world_base_test.go pkg/mmokit/mmokit.go
git commit -m "feat(universe): add WithFacing spawn option

Dedicated alias for WithRotation — reads more clearly at spawn sites
where the angle comes from a Location.Facing field."
```

---

### Task 4: Add `WorldBase.SpawnAtLocation` helper

**Files:**
- Modify: `pkg/universe/world_base.go` (append new method near `SpawnEntity`, ~line 1420)
- Modify: `pkg/universe/world_base_test.go`
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/world_base_test.go`:

```go
import (
	// add if not present:
	// "github.com/zenion/mmoserver/pkg/coords"
	// "github.com/zenion/mmoserver/pkg/component"
)

func TestSpawnAtLocation_ConvertsWorldToLocal(t *testing.T) {
	// Fixture: cellSize=2000, rootCell=(1, 1). World origin of this cell is (2000, 2000).
	// World point (2500, 2900) should become local (500, 900).
	prev := coords.CellSize
	coords.SetCellSize(2000)
	defer coords.SetCellSize(prev)

	wb := newTestWorldBase(t, CellID{X: 1, Y: 1})
	loc := coords.Location{X: 2500, Y: 2900}
	entity := wb.SpawnAtLocation(loc)
	if (entity == ecs.Entity{}) {
		t.Fatalf("SpawnAtLocation returned zero entity")
	}
	posMap := ecs.NewMap1[component.Position](wb.ECSWorld())
	pos := posMap.Get(entity)
	if pos.X != 500 || pos.Y != 900 {
		t.Fatalf("Position=(%v,%v) want (500,900)", pos.X, pos.Y)
	}
	ccMap := ecs.NewMap1[component.CellCoord](wb.ECSWorld())
	cc := ccMap.Get(entity)
	if cc.CellX != 1 || cc.CellY != 1 {
		t.Fatalf("CellCoord=(%v,%v) want (1,1)", cc.CellX, cc.CellY)
	}
}

func TestSpawnAtLocation_OutOfBounds_InvariantLog_Clamps(t *testing.T) {
	prev := coords.CellSize
	coords.SetCellSize(2000)
	defer coords.SetCellSize(prev)

	wb := newTestWorldBase(t, CellID{X: 0, Y: 0}) // bounds [0,2000)×[0,2000)
	wb.coord.invariantMode = InvariantLog // do not panic; clamp instead
	loc := coords.Location{X: 5000, Y: -100}
	entity := wb.SpawnAtLocation(loc)
	posMap := ecs.NewMap1[component.Position](wb.ECSWorld())
	pos := posMap.Get(entity)
	if pos.X >= 2000 || pos.X < 0 {
		t.Fatalf("pos.X=%v not clamped into [0,2000)", pos.X)
	}
	if pos.Y >= 2000 || pos.Y < 0 {
		t.Fatalf("pos.Y=%v not clamped into [0,2000)", pos.Y)
	}
}
```

If `newTestWorldBase` doesn't exist, add this helper at the top of the test file:

```go
// newTestWorldBase constructs a minimal WorldBase for unit tests. It
// creates a bare Process in InvariantOff mode, a single cell at the
// given CellID, and returns the cell's WorldBase.
func newTestWorldBase(t *testing.T, cell CellID) *WorldBase {
	t.Helper()
	p := &Process{
		invariantMode: InvariantOff,
		Log:           logger.New(),
	}
	eng := engine.NewEngine(nil, 20, nil)
	wb := &WorldBase{
		eng:    eng,
		cell:   cell,
		cellID: cell.MeshID(),
		coord:  p,
	}
	wb.spawner = wb.eng.ECS
	wb.rotMap = ecs.NewMap1[component.Rotation](wb.eng.ECS)
	return wb
}
```

(If creating the helper is tricky because of unexported `Process` fields or the real constructor is elaborate, fall back to a fixture helper already present in the package — search `pkg/universe/*_test.go` for `newFixture`, `buildCell`, or similar, and thread the `CellID` through.)

- [ ] **Step 2: Run the test — expect compile failure**

Run: `go test ./pkg/universe/... -run TestSpawnAtLocation -v`

Expected: build error — `wb.SpawnAtLocation undefined`.

- [ ] **Step 3: Implement `SpawnAtLocation`**

Append to `pkg/universe/world_base.go`, right after the `SpawnEntity` method ends (around line ~1460):

```go
// SpawnAtLocation spawns an entity at the given world-space Location.
//
// The Location must fall within this cell's world bounds; callers at the
// gateway already enforce that via CellAtPosition, so this is a correctness
// invariant, not user-facing validation. Out-of-bounds calls log under
// CatInvariant, append a commit-log violation, panic under InvariantPanic,
// or (under InvariantOff/InvariantLog) clamp and continue.
//
// Facing is NOT auto-applied — pass WithFacing(loc.Facing) if the game
// uses rotation.
func (b *WorldBase) SpawnAtLocation(loc coords.Location, opts ...SpawnOption) ecs.Entity {
	rootCell := b.rootCell()
	cellSize := coords.CellSize
	minX := float32(rootCell.X) * cellSize
	minY := float32(rootCell.Y) * cellSize
	maxX := minX + cellSize
	maxY := minY + cellSize

	if loc.X < minX || loc.X >= maxX || loc.Y < minY || loc.Y >= maxY {
		msg := fmt.Sprintf(
			"SpawnAtLocation called with out-of-bounds Location: "+
				"loc=(%f,%f) cell=%s bounds=[%f,%f)×[%f,%f)",
			loc.X, loc.Y, b.cellID, minX, maxX, minY, maxY)
		b.eng.Log.Log(CatInvariant, "%s", msg)
		if b.coord != nil && b.coord.commitLog != nil {
			b.coord.commitLog.Append(CommitEvent{
				Kind:    EventInvariantViolation,
				Step:    "spawn-at-location-out-of-bounds",
				Success: false,
				Error:   msg,
			})
		}
		if b.coord != nil && b.coord.invariantMode == InvariantPanic {
			panic(msg)
		}
		if loc.X < minX {
			loc.X = minX
		} else if loc.X >= maxX {
			loc.X = maxX - 1
		}
		if loc.Y < minY {
			loc.Y = minY
		} else if loc.Y >= maxY {
			loc.Y = maxY - 1
		}
	}

	pos := component.Position{X: loc.X - minX, Y: loc.Y - minY}
	return b.SpawnEntity(pos, opts...)
}
```

Add `"fmt"` to `pkg/universe/world_base.go` imports if not already present.

- [ ] **Step 4: Run the test**

Run: `go test ./pkg/universe/... -run TestSpawnAtLocation -v`

Expected: PASS.

- [ ] **Step 5: Facade re-export**

Edit `pkg/mmokit/mmokit.go`, coords facade section (around line 420-445). Below the existing `type SpawnPoint = coords.SpawnPoint` line — leave existing exports alone for now (flip day handles deletion) and add:

```go
// Location is a world-space anchor for spawn/respawn/teleport targets.
// See coords.Location for the full doc.
type Location = coords.Location
```

`SpawnAtLocation` is a method on `WorldBase`, so games call it as `gw.SpawnAtLocation(...)` — no facade function needed.

- [ ] **Step 6: Verify whole module builds**

Run: `go vet ./...`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/world_base.go pkg/universe/world_base_test.go pkg/mmokit/mmokit.go
git commit -m "feat(universe): add WorldBase.SpawnAtLocation

Converts a world-space Location into a cell-local Position using
rootCell() and delegates to SpawnEntity. Out-of-bounds locations
log under CatInvariant + commit-log event; panics under
InvariantPanic; clamps under InvariantOff/InvariantLog."
```

---

### Task 5: Extend `meshpb` proto with `Location` + carry it in `SpawnTransfer`/`PlayerAssignment`

**Files:**
- Modify: `proto/meshpb/mesh.proto` (near the other `message` declarations, ~line 369-400)
- Regenerate: `gen/go/meshpb/*`

- [ ] **Step 1: Add the Location message + field references**

Edit `proto/meshpb/mesh.proto`. Add this message near the top of the SpawnTransfer/PlayerAssignment section (pick a spot adjacent to line 369):

```proto
// Location is the wire form of coords.Location — world-space anchor for
// spawn/respawn/teleport targets.
message Location {
  float x       = 1;
  float y       = 2;
  float facing  = 3; // radians, 0 = +X axis
  string tag    = 4; // game-defined
}
```

Then edit the existing `SpawnTransfer` message (lines 392-397). Replace with:

```proto
// SpawnTransfer mirrors pkg/universe/message.go SpawnTransfer.
message SpawnTransfer {
  string from_cell_id          = 1;
  uint32 conn_id               = 2;
  string username              = 3;
  Location spawn_location      = 4;
}
```

Edit the existing `PlayerAssignment` message (lines 369-381). Replace with:

```proto
message PlayerAssignment {
  string from_cell_id       = 1;
  uint32 conn_id            = 2;
  string username           = 3;
  bool   is_reconnect       = 4;
  bytes  data               = 5;
  string to_cell_id         = 6;
  string gateway_id         = 7;
  uint64 epoch              = 8;
  Location spawn_location   = 9;
}
```

- [ ] **Step 2: Regenerate proto bindings**

Run: `just proto`

Expected: `buf generate` regenerates `gen/go/meshpb/mesh.pb.go`, `gen/csharp/...`, `gen/es/meshpb/...`. Verify `git status` shows these files modified.

- [ ] **Step 3: Verify Go build**

Run: `go vet ./...`

Expected: PASS. No Go call-site code is using the new field yet; old code ignores it.

- [ ] **Step 4: Commit generated + proto**

```bash
git add proto/meshpb/mesh.proto gen/go/meshpb gen/csharp gen/es/meshpb
git commit -m "feat(meshpb): add Location message, extend SpawnTransfer + PlayerAssignment

New field is additive — existing Go wrappers ignore it. Consumed by
the Go universe layer in a follow-up commit."
```

---

### Task 6: Extend Go `SpawnTransfer` + `PlayerAssignment` structs with `SpawnLocation`

**Files:**
- Modify: `pkg/universe/message.go:27-39`

- [ ] **Step 1: Edit both Go structs**

Replace `pkg/universe/message.go` lines 27-31 (SpawnTransfer struct):

```go
// SpawnTransfer requests a player spawn on another cell.
type SpawnTransfer struct {
	ConnID        uint32
	Username      string
	SpawnLocation coords.Location
}
```

Replace lines 33-39 (PlayerAssignment struct):

```go
// PlayerAssignment is sent by the coordinator to a cell after successful login.
type PlayerAssignment struct {
	ConnID        uint32
	Username      string
	IsReconnect   bool
	Data          any // optional session data from LoginHandler
	SpawnLocation coords.Location
}
```

Add `"github.com/zenion/mmoserver/pkg/coords"` to `pkg/universe/message.go` imports if missing.

- [ ] **Step 2: Verify build**

Run: `go vet ./...`

Expected: PASS — zero-value `coords.Location` is populated implicitly by existing construction sites until Task 11 wires it up.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/message.go
git commit -m "feat(universe): extend SpawnTransfer + PlayerAssignment with SpawnLocation

Additive. Fields default to zero-value Location until gateway + cell
dispatch paths populate them in the flip-day commit."
```

---

### Task 7: Extend wire-format (de)serializer for `SpawnTransfer` / `PlayerAssignment`

**Files:**
- Modify: wherever `SpawnTransfer` ↔ `meshpb.SpawnTransfer` conversion happens (grep for `meshpb.SpawnTransfer{` and `*meshpb.SpawnTransfer`)
- Modify: same for `PlayerAssignment`

- [ ] **Step 1: Find the conversion sites**

Run (inside an Explore subagent if you prefer, or directly):

```bash
rg -n 'meshpb\.SpawnTransfer|meshpb\.PlayerAssignment' pkg/universe/
```

Expected hits: `pkg/universe/mesh_control_server.go`, `pkg/universe/mesh_data_server.go`, `pkg/universe/gateway.go`, and the reverse-direction host code. Read each site to locate the struct→proto and proto→struct conversions.

- [ ] **Step 2: Map `SpawnLocation` through each conversion**

For each site that encodes a Go `*SpawnTransfer` to `meshpb.SpawnTransfer`, add:

```go
pb.SpawnLocation = &meshpb.Location{
	X:      st.SpawnLocation.X,
	Y:      st.SpawnLocation.Y,
	Facing: st.SpawnLocation.Facing,
	Tag:    st.SpawnLocation.Tag,
}
```

For each site that decodes a `*meshpb.SpawnTransfer` to Go `SpawnTransfer`, add:

```go
if pb.SpawnLocation != nil {
	st.SpawnLocation = coords.Location{
		X:      pb.SpawnLocation.X,
		Y:      pb.SpawnLocation.Y,
		Facing: pb.SpawnLocation.Facing,
		Tag:    pb.SpawnLocation.Tag,
	}
}
```

Same pattern for `PlayerAssignment`. Zero-value behaviour on the decode side is safe — missing field decodes to `nil`, leaving `st.SpawnLocation` as zero-value.

- [ ] **Step 3: Verify build + existing tests still green**

Run: `go vet ./... && go test ./pkg/universe/... -short`

Expected: PASS. Existing tests construct `SpawnTransfer`/`PlayerAssignment` literally; the new zero-value field doesn't break them.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/
git commit -m "feat(universe): plumb SpawnLocation through mesh wire codecs

Additive: encoders populate the new proto field, decoders honour it
when non-nil. Field stays zero-valued in practice until the gateway
and cell dispatch paths in the flip-day commit."
```

---

### Task 8: Invariant check for the out-of-bounds case (smoke)

**Files:**
- Modify: `pkg/universe/world_base_test.go`

- [ ] **Step 1: Add the InvariantPanic test**

Append to `pkg/universe/world_base_test.go`:

```go
func TestSpawnAtLocation_OutOfBounds_InvariantPanic(t *testing.T) {
	prev := coords.CellSize
	coords.SetCellSize(2000)
	defer coords.SetCellSize(prev)

	wb := newTestWorldBase(t, CellID{X: 0, Y: 0})
	wb.coord.invariantMode = InvariantPanic

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on out-of-bounds SpawnAtLocation under InvariantPanic")
		}
	}()

	_ = wb.SpawnAtLocation(coords.Location{X: 99999, Y: 99999})
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./pkg/universe/... -run TestSpawnAtLocation_OutOfBounds_InvariantPanic -v`

Expected: PASS — panic is recovered by `defer`.

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/world_base_test.go
git commit -m "test(universe): pin SpawnAtLocation out-of-bounds invariant behaviour

Clamp on InvariantOff/Log, panic on InvariantPanic. Matches the
existing CheckInvariants semantics."
```

---

### Task 9: Add `localSession.spawnLoc` and populate it in `processLogin`

**Files:**
- Modify: `pkg/universe/gateway.go:98-104` — struct
- Modify: `pkg/universe/gateway.go:263-305` — `processLogin`

- [ ] **Step 1: Extend `localSession`**

Edit `pkg/universe/gateway.go:98-104`. Replace:

```go
type localSession struct {
	connID   uint32
	username string
	hostID   string
	cellID   string
	epoch    uint64
}
```

with:

```go
type localSession struct {
	connID   uint32
	username string
	hostID   string
	cellID   string
	epoch    uint64
	spawnLoc coords.Location // resolved at login; forwarded in PlayerAssignment
}
```

Add `"github.com/zenion/mmoserver/pkg/coords"` to gateway.go imports if missing.

- [ ] **Step 2: Do NOT wire `spawnLoc` into `processLogin` yet**

This task is deliberately *just* the struct field. The flip-day commit (Task 11) fills it in. Splitting the field-add from the behaviour change keeps the flip-day diff readable.

- [ ] **Step 3: Verify build**

Run: `go vet ./...`

Expected: PASS — new field zero-valued everywhere.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/gateway.go
git commit -m "feat(universe): add localSession.spawnLoc field

Populated by the flip-day commit. Here in its own commit so the
next diff is behaviour-only."
```

---

### Task 10: Fold `SpawnLocation` into `dispatchPlayerAssignment`

**Files:**
- Modify: `pkg/universe/gateway.go:343-436` (the `dispatchPlayerAssignment` function)

- [ ] **Step 1: Copy `sess.spawnLoc` into every `PlayerAssignment{...}` literal**

Read `pkg/universe/gateway.go:343-436` and find every `PlayerAssignment{...}` struct construction. Each one already has `ConnID`, `Username`, `IsReconnect`, and (in some branches) `Data`. Add `SpawnLocation: sess.spawnLoc` to every one.

Example diff pattern (reconnect branch, around line 389-394):

```go
// Before
Assignment: &PlayerAssignment{
	ConnID:      sess.connID,
	Username:    sess.username,
	IsReconnect: true,
},
```

```go
// After
Assignment: &PlayerAssignment{
	ConnID:        sess.connID,
	Username:      sess.username,
	IsReconnect:   true,
	SpawnLocation: sess.spawnLoc,
},
```

Repeat for the normal-routing branch (around line 426-431) and any `dispatchPlayerAssignmentRemote` branch if it constructs a `PlayerAssignment` directly.

- [ ] **Step 2: Populate the cell-side `PlayerSession.SpawnLocation`**

Find the cell inbox handler that processes `MsgPlayerAssignment` — typically in a cell's `DrainInbox` method. Grep:

```bash
rg -n 'MsgPlayerAssignment' pkg/universe/
```

In the handler, after the `PlayerSession` is created / found for this conn, copy the assignment's `SpawnLocation` onto it:

```go
if sess := eng.Players.ByConnID(msg.Assignment.ConnID); sess != nil {
	sess.SpawnLocation = msg.Assignment.SpawnLocation
}
```

If no existing session is found at the time of assignment, the session is created on the pending path — find that path (grep `Players.NewSessionForConn` or similar) and set `SpawnLocation` there instead.

- [ ] **Step 3: Verify build**

Run: `go vet ./...`

Expected: PASS. The new field is now threaded end-to-end, but `sess.spawnLoc` is still zero at the source (`processLogin`) until Task 11.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/gateway.go pkg/universe/ # whatever cell file you edited
git commit -m "feat(universe): thread SpawnLocation through PlayerAssignment

Gateway dispatchPlayerAssignment copies sess.spawnLoc into the
Assignment literal; the cell inbox handler copies it onto
PlayerSession.SpawnLocation. Source sess.spawnLoc stays zero
until the flip-day commit."
```

---

### Task 11: **FLIP DAY** — swap `SpawnResolver` signature, `Config.DefaultSpawn` type, `resolveSpawn`, `RequestRespawn`, kill `anyCellID` fallback, update every caller

This is the single breaking commit. Every caller from the Caller Inventory section above is updated in one coherent change. Do the whole thing on a throwaway branch first if you're nervous; run `go build ./...` and `go test ./... -short` at the end.

**Files (updated together, one commit):**
- `pkg/universe/coordinator.go:154-160`
- `pkg/universe/spawn_resolver.go:20-82`
- `pkg/universe/gateway.go:69, 263-305`
- `pkg/universe/cell_bridge_impl.go:173-204`
- `pkg/universe/universe_test.go:125-131, ~641`
- `cmd/server/main.go:367-375`
- `examples/4node-basic/main.go:37`
- `examples/4node-basic/mesh_e2e_test.go:179, 211`
- `examples/slither/main.go:49`

- [ ] **Step 1: Change `SpawnResolver` signature**

Edit `pkg/universe/spawn_resolver.go:20-32`. Replace the type + doc with:

```go
// SpawnResolver resolves a username to an absolute world-space Location.
// Called once per login on the process that owns playerDB (typically the
// coordinator). Returns ok=false when the user has no saved location —
// the gateway then falls back to Config.DefaultSpawn.
//
// The resolver is topology-blind: it returns world-space coords only.
// The gateway calls CellAtPosition(loc.X, loc.Y) at dispatch time to find
// the current owning cell, so split/merge between the resolver call and
// dispatch is handled naturally.
type SpawnResolver func(username string) (coords.Location, bool)

// SetSpawnResolver registers the spawn resolver on the coordinator.
// Called from game setup code (typically inside the needsGameState block).
// Must be called before Start().
func (c *Process) SetSpawnResolver(r SpawnResolver) {
	c.mu.Lock()
	c.spawnResolver = r
	c.mu.Unlock()
}
```

- [ ] **Step 2: Rewrite `resolveSpawn`**

Edit `pkg/universe/spawn_resolver.go:49-82`. Replace with:

```go
// resolveSpawn returns the world-space Location for username.
//
//  1. Embedded coordinator with resolver → call inline (zero RPC overhead).
//  2. Standalone gateway → send ResolveSpawn RPC with 2s deadline.
//  3. Resolver absent, returns ok=false, or RPC fails → use DefaultSpawn.
func (g *Gateway) resolveSpawn(ctx context.Context, username string) coords.Location {
	if g.coord != nil {
		g.coord.mu.RLock()
		resolver := g.coord.spawnResolver
		defaultSpawn := g.coord.cfg.DefaultSpawn
		g.coord.mu.RUnlock()
		if resolver != nil {
			if loc, ok := resolver(username); ok {
				return loc
			}
		}
		return defaultSpawn
	}

	if g.controlClient != nil {
		rpcCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		resp, err := g.spawnOrch.send(rpcCtx, g.controlClient, g.id, username)
		if err == nil && resp != nil && resp.Ok {
			return coords.Location{X: resp.WorldX, Y: resp.WorldY}
			// facing/tag not yet in the RPC; leave zero. Teleport spec will extend.
		}
		if err != nil {
			g.log.Log(CatNetConn, "gateway: resolveSpawn RPC failed for %s: %v — using DefaultSpawn", username, err)
		}
	}
	return g.defaultSpawn
}
```

Note: `g.defaultSpawn` is `coords.SpawnPoint` today. It must become `coords.Location` — edit the struct:

Edit `pkg/universe/gateway.go:69`. Replace:

```go
defaultSpawn coords.SpawnPoint
```

with:

```go
defaultSpawn coords.Location
```

Also update the struct's initializer where `defaultSpawn` is populated (grep: `defaultSpawn:` or `.defaultSpawn =` inside `NewGateway` / `newGateway`):

```go
g.defaultSpawn = cfg.DefaultSpawn
```

The type flows through because `Config.DefaultSpawn` is also becoming `coords.Location` in the next step.

- [ ] **Step 3: Change `Config.DefaultSpawn` type**

Edit `pkg/universe/coordinator.go:154-160`. Replace:

```go
DefaultSpawn coords.SpawnPoint
```

with:

```go
// DefaultSpawn is the world-space login/respawn location used when no
// SpawnResolver is registered or the resolver returns ok=false.
// Topology-independent: the gateway resolves the current owning cell
// via CellAtPosition at dispatch time.
DefaultSpawn coords.Location
```

- [ ] **Step 4: Update `processLogin` — drop `anyCellID` fallback, populate `sess.spawnLoc`**

Edit `pkg/universe/gateway.go:263-305`. Replace with:

```go
func (g *Gateway) processLogin(connID uint32, username string, data any) error {
	loc := g.resolveSpawn(context.Background(), username)

	cellID := g.topology.cellAtPosition(loc.X, loc.Y)
	if cellID == "" {
		return fmt.Errorf("spawn point outside world bounds for user %s: loc=(%f,%f)",
			username, loc.X, loc.Y)
	}
	hostID := g.topology.HostForCell(cellID)
	if hostID == "" || hostID == "local" {
		if g.coord == nil {
			return fmt.Errorf("no host for cell %s (topology not yet populated)", cellID)
		}
		hostID = "local"
	}

	sess := &localSession{
		connID:   connID,
		username: username,
		hostID:   hostID,
		cellID:   cellID,
		epoch:    1,
		spawnLoc: loc,
	}
	g.mu.Lock()
	g.sessions[connID] = sess
	g.mu.Unlock()

	g.announceSession(sess)
	return g.dispatchPlayerAssignment(sess, data)
}
```

The `anyCellID` *function* can stay — grep to see if anyone else calls it. If no other caller, delete it; if yes, leave it. Don't chase that down in this task — revisit in Task 14.

- [ ] **Step 5: Rewrite `cellBridge.RequestRespawn`**

Edit `pkg/universe/cell_bridge_impl.go:173-204`. Replace with:

```go
func (b *cellBridge) RequestRespawn(connID uint32, username string) {
	b.cell.Log.Log(CatMeshMsg, "[%s] requesting respawn: conn=%d user=%s", b.cell.ID, connID, username)
	b.coord.mu.RLock()
	resolver := b.coord.spawnResolver
	defaultSpawn := b.coord.cfg.DefaultSpawn
	b.coord.mu.RUnlock()

	loc := defaultSpawn
	if resolver != nil {
		if resolved, ok := resolver(username); ok {
			loc = resolved
		}
	}

	targetCellID := b.coord.CellAtPosition(loc.X, loc.Y)
	if targetCellID == "" {
		b.cell.Log.Log(CatMeshMsg,
			"[%s] respawn rejected: location (%f,%f) outside world bounds (user=%s)",
			b.cell.ID, loc.X, loc.Y, username)
		return
	}
	dest, ok := b.coord.Cells[targetCellID]
	if !ok {
		b.cell.Log.Log(CatMeshMsg,
			"[%s] respawn rejected: cell %s no longer owned (user=%s)",
			b.cell.ID, targetCellID, username)
		return
	}
	dest.Inbox <- CellMessage{
		Type:       MsgSpawnTransfer,
		FromCellID: b.cell.ID,
		Spawn: &SpawnTransfer{
			ConnID:        connID,
			Username:      username,
			SpawnLocation: loc,
		},
	}
	b.coord.setPlayerNode(connID, targetCellID)
}
```

Note: The `MsgSpawnTransfer` inbox handler on the receiving cell must copy `st.SpawnLocation` onto the `PlayerSession.SpawnLocation` before transition. Find the handler (grep `MsgSpawnTransfer` inside cell inbox drain code) and add:

```go
if sess := eng.Players.ByConnID(st.ConnID); sess != nil {
	sess.SpawnLocation = st.SpawnLocation
}
```

If the session is created later on the pending path, set it there instead.

- [ ] **Step 6: Fix universe test fixtures**

Edit `pkg/universe/universe_test.go:125-131`. Replace:

```go
if c.cfg.DefaultSpawn == (coords.SpawnPoint{}) {
	c.cfg.DefaultSpawn = coords.WorldCenterOfCell(0, 0)
}
```

with:

```go
if c.cfg.DefaultSpawn.IsZero() {
	// Pick the center of the 0_0 cell deterministically.
	c.cfg.DefaultSpawn = coords.Location{
		X: c.cfg.CellSize / 2,
		Y: c.cfg.CellSize / 2,
	}
}
```

Edit line ~641 (the other `coords.SpawnPoint{...}` literal):

```go
// Before
c.cfg.DefaultSpawn = coords.SpawnPoint{X: (minX + maxX) / 2, Y: (minY + maxY) / 2}
// After
c.cfg.DefaultSpawn = coords.Location{X: (minX + maxX) / 2, Y: (minY + maxY) / 2}
```

- [ ] **Step 7: Fix cmd/server/main.go**

Edit `cmd/server/main.go:367-375`. Replace with:

```go
coordinator.SetSpawnResolver(func(username string) (coords.Location, bool) {
	pdata := playerDB.Get(username)
	if pdata == nil || !pdata.HasSave {
		return coords.Location{}, false
	}
	return coords.Location{
		X: float32(pdata.CellX)*coords.CellSize + pdata.X,
		Y: float32(pdata.CellY)*coords.CellSize + pdata.Y,
		// Facing + Tag not yet persisted; leave zero. Follow-up work.
	}, true
})
```

- [ ] **Step 8: Fix 4node-basic main.go (hardcode)**

Edit `examples/4node-basic/main.go:37`. Replace:

```go
DefaultSpawn: mmokit.WorldCenterOfCell(0, 0),
```

with:

```go
DefaultSpawn: mmokit.Location{X: CellSize / 2, Y: CellSize / 2},
```

`CellSize` here is the package-local constant `examples/4node-basic/config.go:7` (value `2000.0`).

- [ ] **Step 9: Fix 4node-basic mesh_e2e_test.go**

Edit `examples/4node-basic/mesh_e2e_test.go:179` and `:211`. Replace each:

```go
DefaultSpawn: mmokit.WorldCenterOfCell(0, 0),
```

with:

```go
DefaultSpawn: mmokit.Location{X: CellSize / 2, Y: CellSize / 2},
```

- [ ] **Step 10: Fix slither**

Edit `examples/slither/main.go:42-50`. Replace:

```go
cfg.DefaultSpawn = mmokit.WorldCenterOfCell(0, 0)
```

with:

```go
// Slither doesn't override CellSize, so use the coords default (8192)
// at the time this line evaluates. Since it's after cfg.BindFlags() +
// flag.Parse() but before mmokit.New(cfg), coords.CellSize is still
// the package default unless the operator set it. Explicit literal
// keeps the spawn point obvious in code review.
cfg.DefaultSpawn = mmokit.Location{X: 4096, Y: 4096}
```

Alternative (if slither author prefers derived): read `coords.CellSize` directly:

```go
cs := coords.CellSize
cfg.DefaultSpawn = mmokit.Location{X: cs / 2, Y: cs / 2}
```

Either is fine — pick the literal for clarity.

- [ ] **Step 11: Run the whole build + test**

Run: `go vet ./...` then `go test ./... -short`

Expected: zero build errors. Tests pass — if any fail, read the failure and fix; the most likely culprit is a missed `PlayerAssignment{}` or `SpawnTransfer{}` literal somewhere that needs `SpawnLocation:` added (grep for the struct names to find leftover literals).

- [ ] **Step 12: Commit the big bang**

```bash
git add -A
git commit -m "feat(universe): flip to coords.Location for spawn/respawn resolution

Breaking change; no backcompat. Every caller updated in one commit:

- SpawnResolver now returns coords.Location (was three scalars).
- Config.DefaultSpawn is coords.Location (was coords.SpawnPoint).
- Gateway's processLogin rejects unmapped spawn points instead of
  silently routing via anyCellID (the bug class that scattered fresh
  logins across random cells in 4node-basic distributed mode).
- cellBridge.RequestRespawn carries the resolved Location in
  SpawnTransfer so the destination cell honours it instead of
  re-deriving from a hardcoded zone.
- PlayerSession.SpawnLocation is populated by the cell inbox handler
  for both PlayerAssignment and SpawnTransfer paths.

Fixes fresh-login cell routing in 4node-basic with --mode=host."
```

---

### Task 12: Delete `coords.SpawnPoint` + `coords.WorldCenterOfCell` + facade re-exports

**Files:**
- Delete: `pkg/coords/spawn.go`
- Modify: `pkg/mmokit/mmokit.go:434-442`

- [ ] **Step 1: Delete the old file**

Run: `rm pkg/coords/spawn.go`

- [ ] **Step 2: Remove facade re-exports**

Edit `pkg/mmokit/mmokit.go:434-442`. Delete these lines:

```go
// SpawnPoint is an absolute world-space coordinate. Used for the login
// fallback (Config.DefaultSpawn) and other game-defined anchor points that
// must survive cell split/merge without re-computation.
type SpawnPoint = coords.SpawnPoint

// WorldCenterOfCell returns the world-space center of a base-cell coordinate
// as a SpawnPoint. Topology-independent across any split depth — the gateway
// resolves the current owning child cell at dispatch time.
var WorldCenterOfCell = coords.WorldCenterOfCell
```

Leave the `Location` alias (added in Task 4 Step 5) in place.

- [ ] **Step 3: Verify whole-tree build**

Run: `go vet ./...`

Expected: PASS. If you see `undefined: coords.SpawnPoint` or `undefined: mmokit.WorldCenterOfCell`, that's a caller the flip-day commit missed — grep for it and update.

- [ ] **Step 4: Run full test suite**

Run: `go test ./... -short`

Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(coords): delete SpawnPoint + WorldCenterOfCell

Superseded by coords.Location + the rewritten spawn flow. No backcompat
aliases — every caller was migrated in the flip-day commit."
```

---

### Task 13: Migrate 4node-basic `spawnPlayer` to `SpawnAtLocation`

**Files:**
- Modify: `examples/4node-basic/world.go:56-77, 119-138`

- [ ] **Step 1: Update OnEnter**

Edit `examples/4node-basic/world.go`. Replace the `OnEnter` callback (lines 59-65) with:

```go
OnEnter: func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) {
	s.Entity = gw.SpawnAtLocation(s.SpawnLocation,
		mmokit.WithCollider(PlayerRadius),
		mmokit.WithEntityKind(KindPlayer),
		mmokit.WithComponents(), // auto-adds PlayerName, DebugInfo, MoveTarget
	)
	gw.ConnMap.Add(s.Entity, &mmokit.PlayerConn{ConnID: s.ConnID})
	gw.NameMap.Get(s.Entity).Name = s.Username
	gw.SendSpawnedMsg(s.ConnID, s.Entity)
	// DebugInfoSystem.Update pushes SE_CELL_TOPOLOGY reactively to
	// every active player on change (including first-send to new
	// players), so no per-spawn send is needed here.
},
```

- [ ] **Step 2: Delete the now-unused `spawnPlayer` method**

Edit `examples/4node-basic/world.go`. Delete lines 119-138 (the entire `spawnPlayer` method). The 0.85-of-cellSize fixed position moves out of the game — the gateway's `DefaultSpawn = Location{X: CellSize/2, Y: CellSize/2}` (set in Task 11 Step 8) is now the single source of truth, and `SpawnAtLocation` places the entity there.

If the smoke test needs the old 0.85-of-cellsize spawn to reproduce the boundary-crossing behaviour, update `examples/4node-basic/main.go:37` instead:

```go
DefaultSpawn: mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85},
```

(and keep the file pinned in comments so future reviewers know why 0.85 is load-bearing).

- [ ] **Step 3: Verify build + run the e2e test**

Run: `go vet ./examples/4node-basic/...` then `go test ./examples/4node-basic/...`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add examples/4node-basic/
git commit -m "refactor(4node-basic): spawn via SpawnAtLocation

Player entity is placed at the gateway-resolved world-space Location
instead of a hardcoded cell-local position. The spawnPlayer helper
method is gone; OnEnter is now 5 lines of game-specific wiring
(ConnMap + NameMap + SendSpawnedMsg) plus the engine primitive."
```

---

### Task 14: Check for dead code — `anyCellID`, old `SpawnResolver` RPC callers

**Files:**
- Audit: `pkg/universe/gateway.go` — is `anyCellID` still called?
- Audit: `pkg/universe/mesh_control_server.go` — does the ResolveSpawn RPC handler still use the old signature?

- [ ] **Step 1: Grep for leftover references**

Run:

```bash
rg -n 'anyCellID' pkg/universe/
rg -n 'WorldCenterOfCell' .
rg -n 'coords\.SpawnPoint' .
rg -n 'mmokit\.SpawnPoint' .
```

Expected: only `anyCellID` hits may remain (one or two, if it's still called from a non-spawn path). The other three should return zero results.

- [ ] **Step 2: Evaluate `anyCellID`**

If `anyCellID` is unreferenced, delete it (around `pkg/universe/gateway.go:629-653`). If it has other legitimate callers (e.g. bootstrap paths), leave it but add a `// No longer used as a login fallback — see processLogin.` note above the function.

- [ ] **Step 3: Check `mesh_control_server.go` ResolveSpawn handler**

Search: `rg -n 'ResolveSpawn' pkg/universe/ proto/meshpb/`

If the RPC response message `meshpb.SpawnResolved` still uses `world_x float32, world_y float32`, that's fine — the gateway's `resolveSpawn` already extracts those into a `coords.Location{X, Y}` (see Task 11 Step 2). Extending the RPC to carry facing/tag is a *later* change, filed under the teleport plan.

If the handler on the coordinator side (`pkg/universe/mesh_control_server.go`) calls the resolver with the old signature `(x, y, ok)`, update it to consume the new `(Location, bool)` signature and set `SpawnResolved.WorldX / WorldY` from `loc.X / loc.Y`.

- [ ] **Step 4: Full build + test**

Run: `go vet ./... && go test ./... -short`

Expected: all green.

- [ ] **Step 5: Commit (if any cleanup happened)**

```bash
git add -A
git commit -m "chore(universe): clean up dead spawn-fallback code

Removes anyCellID + any remaining references to SpawnPoint /
WorldCenterOfCell surfaced by the flip-day audit."
```

(Skip this commit if grep came back completely clean.)

---

### Task 15: Integration smoke — fresh login always lands in the resolved cell

**Files:**
- Modify: `examples/4node-basic/mesh_e2e_test.go` — add one test or extend an existing one

- [ ] **Step 1: Add the failing test**

Append to `examples/4node-basic/mesh_e2e_test.go`:

```go
// TestMeshE2E_FreshLoginRoutesToDefaultSpawn pins the fix for the
// anyCellID random-routing bug. Prior to the Location flip day, fresh
// users in distributed mode (coord + 2 hosts) landed in whatever cell
// Go's map iteration visited first. After the fix, DefaultSpawn's
// world point deterministically resolves to cell_0_0.
func TestMeshE2E_FreshLoginRoutesToDefaultSpawn(t *testing.T) {
	c := buildTestCluster(t)

	// Repeat with 8 distinct fresh usernames — any non-determinism would
	// show up as a cellID distribution across the test runs.
	for i := 0; i < 8; i++ {
		username := fmt.Sprintf("smoketest_%d", i)
		cellID, err := c.logInFreshUser(t, username)
		if err != nil {
			t.Fatalf("login %s: %v", username, err)
		}
		if cellID != "cell_0_0" {
			t.Fatalf("fresh login %s routed to %s; want cell_0_0 (DefaultSpawn=%+v)",
				username, cellID, c.coord.Config().DefaultSpawn)
		}
	}
}
```

`c.logInFreshUser(t, username)` may or may not already exist on the fixture. If it doesn't, either (a) add a helper that opens a WebSocket, sends the login message, waits for the PlayerAssignment, and returns the cell ID, or (b) adapt an existing `TestMeshE2E…` test's login helper. Grep `mesh_e2e_test.go` for `login` / `wsDial` / `sendLogin` to find the right entry point.

- [ ] **Step 2: Run — expect PASS (behaviour is fixed by earlier commits)**

Run: `go test ./examples/4node-basic/... -run TestMeshE2E_FreshLoginRoutesToDefaultSpawn -v`

Expected: PASS. If it fails with "routed to cell_1_0" / `cell_0_1` / `cell_1_1`, a lurking `anyCellID` fallback or a forgotten `SpawnLocation` copy is still in play — grep the universe package to track it down.

- [ ] **Step 3: Commit**

```bash
git add examples/4node-basic/mesh_e2e_test.go
git commit -m "test(4node-basic): pin fresh-login routing to cell_0_0

Smoke the deterministic-routing fix from the Location migration.
Previously this test would have been flaky under --mode=host because
anyCellID would scatter logins across all four cells."
```

---

### Task 16: Distributed-mode manual smoke + docs

**Files:**
- Modify: `CLAUDE.md` — one-line note on the new spawn API (search for `DefaultSpawn` or `SpawnResolver` and update)

- [ ] **Step 1: Build + run**

Run: `just build && cd examples/4node-basic && just distributed`

Open `http://localhost:8080` in a browser. Log in as fresh usernames (`smoketest1`, `smoketest2`, `smoketest3`) and confirm each one shows up in cell_0_0 in the overlay. In any tmux pane, confirm the gateway log line reads:

```
gateway: conn=<N> user=smoketestN -> host=<HOST> cell=cell_0_0 (...)
```

Deterministically `cell=cell_0_0`, never any of the other three cells.

- [ ] **Step 2: Kill the tmux session**

Run: `just distributed-stop` (from `examples/4node-basic/`).

- [ ] **Step 3: Tiny CLAUDE.md patch**

Edit `CLAUDE.md`. Find the paragraph about `DefaultSpawn` / `SpawnResolver` (grep for one of them). Update the type references from `coords.SpawnPoint` / `(float32, float32, bool)` to `coords.Location` / `(coords.Location, bool)`. One or two line changes.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: CLAUDE.md — note Location-based spawn/respawn API"
```

---

## Self-Review

**Spec coverage:**

- ✅ `Location` type with `{X, Y, Facing, Tag}` — Task 1
- ✅ `PlayerSession.SpawnLocation` field — Task 2
- ✅ `WithFacing` spawn option — Task 3
- ✅ `WorldBase.SpawnAtLocation` — Task 4 (conversion + invariant check)
- ✅ `SpawnResolver` new signature — Task 11 Step 1
- ✅ `Config.DefaultSpawn Location` — Task 11 Step 3
- ✅ Drop `anyCellID` fallback → login rejection on unmapped — Task 11 Step 4
- ✅ Respawn carries Location via `SpawnTransfer` — Task 11 Step 5 + Tasks 5–7 for wire
- ✅ Delete `SpawnPoint` / `WorldCenterOfCell` — Task 12
- ✅ Update every caller (cmd/server, 4node-basic, slither, universe_test) — Task 11 + Task 13
- ✅ Invariant violation logging — Task 4 + Task 8
- ✅ Integration smoke — Task 15 + Task 16

**Placeholder scan:** no TBDs, no "similar to Task N". Every code-editing step shows full before/after content or exact replacement text. Test code is complete. Commit messages are concrete.

**Type consistency:** `coords.Location{X, Y, Facing, Tag}` used uniformly across every task. `SpawnResolver func(username string) (coords.Location, bool)` signature appears identically in the type declaration (Task 11 Step 1), the `cmd/server/main.go` closure (Task 11 Step 7), and the universe test fixture comment. `SpawnLocation` field name is identical on `PlayerSession`, `PlayerAssignment`, `SpawnTransfer`, and `localSession` (as `spawnLoc` unexported — noted explicitly).

**Risk call-outs reconciled from the spec's "Open questions":**

- Proto shape: we go with a dedicated `meshpb.Location` message, referenced in both `SpawnTransfer.spawn_location` and `PlayerAssignment.spawn_location` — Task 5 Step 1.
- Facing precision: plain `float` (radians) on the wire — Task 5 Step 1. Confirmed as sufficient for infrequent control-plane messages.

**Caller inventory reconciliation:** all 12 caller entries from the inventory section map to tasks — Task 11 lists every one with its file:line.

**Known edge case to watch:** Task 10 Step 2 (copying `SpawnLocation` from `PlayerAssignment` onto `PlayerSession` inside the cell inbox handler) assumes the session already exists when the assignment arrives. If the code path creates the session *inside* the handler, write the field on the newly-created session instead. The instruction in the plan calls this out explicitly, but the engineer executing the plan should confirm by grepping `MsgPlayerAssignment` in `pkg/universe/*.go` before coding the edit.
