# Distributed Commands & Entity-TP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move generic player/entity admin commands into the engine layer, fix every command to be multi-cell + multi-host aware, and unify the entity-move primitive so admin TP and natural boundary handoff share one code path.

**Architecture:** Three layers of change in `pkg/`. (1) `Stage.MoveEntityTo` + a generalized `HandoffDriver` give us a single entity-move primitive that handles same-cell, cross-cell, and cross-host moves through the existing commit-tick protocol. (2) A new `RoutePlayerHomeOrOwner` route kind plus `mmokit.ResolvePlayerTarget` helper let any handler treat online-and-offline players uniformly. (3) New `entity.*` and `player.*` engine commands compose those two layers. Then `internal/game/commands/` is trimmed to the 5 game-specific commands, all rewritten on top of the new helper.

**Tech Stack:** Go (1.22+), `github.com/mlange-42/ark/ecs` (ECS world), the existing `pkg/cmdsys/` dispatcher, the existing `pkg/universe/` mesh/coordinator/handoff machinery, the existing `pkg/mmokit/` facade.

**Spec:** [docs/superpowers/specs/2026-04-29-distributed-commands-and-entity-tp-design.md](../specs/2026-04-29-distributed-commands-and-entity-tp-design.md)

---

## File Structure

The plan touches three packages. All new files are in `pkg/universe/` (the place where the resolver, the coordinator, the handoff driver, and the existing `builtins_*.go` files all live). The mmokit facade adds re-export aliases.

**Phase 1 — Engine plumbing (no commands yet):**

- Create: `pkg/universe/db_host_picker.go` — `Coordinator.PickDBHost` + `HasPlayerDB` registration field plumbing.
- Create: `pkg/universe/player_target.go` — `PlayerTarget` type and `ResolvePlayerTarget` helper.
- Create: `pkg/universe/stage_move_entity.go` — `Stage.MoveEntityTo` + `MoveOpt` funcs.
- Modify: `pkg/cmdsys/command.go` — add `RoutePlayerHomeOrOwner` constant + `String()` case + `LocalProcess` interface marker on `LocalContext`.
- Modify: `pkg/universe/cmdsys_resolver.go` — `RoutePlayerHomeOrOwner` resolution case.
- Modify: `pkg/universe/handoff_driver.go` — drop neighbor precondition; add cooldown bypass via crossing-event flag.
- Modify: `pkg/universe/stage.go` — extend `CrossingEvent` with `BypassCooldown bool`.
- Modify: `pkg/mmokit/mmokit.go` — facade re-exports.

**Phase 2 — Engine commands:**

- Create: `pkg/universe/builtins_entity.go` — `entity.spawn`, `entity.despawn`, `entity.list`, `entity.tp`.
- Create: `pkg/universe/builtins_player.go` — `player.tp`, `player.tpto`, `player.list`, `player.info`, `player.kick`.
- Modify: `pkg/universe/coordinator.go` — call into the two new builtins registrars from `RegisterBuiltins`.

**Phase 3 — Space-game cleanup:**

- Delete: `internal/game/commands/tp.go`, `tpto.go`, `kick.go`, `players.go`, `say.go`, `npcs.go`, `spawnnpcs.go`, `resolver.go`.
- Rewrite: `internal/game/commands/damage.go`, `heal.go`, `kill.go`, `give.go`, `currency.go`.
- Modify: `internal/game/commands/registry.go` — trim to 5 registrations.

**Phase 4 — Smoke + clean-up:** verify the example unchanged, add the smoke target.

---

## Phase 0: Preflight

### Task 0: Verify clean baseline

**Files:** none modified.

- [ ] **Step 1: Confirm the working tree is clean and on `main`**

```bash
git status
git rev-parse --abbrev-ref HEAD
```

Expected: `working tree clean`, branch `main`.

- [ ] **Step 2: Run the existing test suite to capture the green baseline**

```bash
just build
go test ./pkg/cmdsys/... ./pkg/universe/... ./pkg/mmokit/... ./internal/game/...
```

Expected: all pass. If anything fails, stop and resolve before starting Phase 1.

---

## Phase 1: Engine Plumbing

This phase introduces new primitives and route-kind support but does not register or expose any new commands. After Phase 1, the existing space-game commands continue to work unchanged.

### Task 1: Add `RoutePlayerHomeOrOwner` route kind

**Files:**

- Modify: `pkg/cmdsys/command.go:22-31` (RouteKind constants), `pkg/cmdsys/command.go:34-55` (RouteKind.String()).
- Test: `pkg/cmdsys/command_test.go` (create or extend).

- [ ] **Step 1: Write the failing test**

Append to (or create) `pkg/cmdsys/command_test.go`:

```go
package cmdsys

import "testing"

func TestRouteKindString_PlayerHomeOrOwner(t *testing.T) {
	if got := RoutePlayerHomeOrOwner.String(); got != "player_home_or_owner" {
		t.Fatalf("RoutePlayerHomeOrOwner.String() = %q, want %q", got, "player_home_or_owner")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/cmdsys/ -run TestRouteKindString_PlayerHomeOrOwner -v
```

Expected: FAIL with `undefined: RoutePlayerHomeOrOwner`.

- [ ] **Step 3: Add the new route kind constant**

In `pkg/cmdsys/command.go`, extend the RouteKind block (which currently ends with `RouteSpecificCell`):

```go
const (
	RouteLocal             RouteKind = iota // handle on the current process
	RouteCoordinator                        // dispatch to the coordinator
	RouteAllHosts                           // fan-out to every host
	RoutePlayerOwner                        // dispatch to the host owning the player
	RouteEntityOwner                        // dispatch to the host owning the entity
	RouteAllGateways                        // fan-out to every gateway
	RouteSpecificHost                       // dispatch to a named host
	RouteSpecificCell                       // dispatch to a named cell
	RoutePlayerHomeOrOwner                  // online → owner host; offline → DB-bearing host
)
```

In the `String()` method, add a new case before `default`:

```go
case RoutePlayerHomeOrOwner:
	return "player_home_or_owner"
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./pkg/cmdsys/ -run TestRouteKindString_PlayerHomeOrOwner -v
```

Expected: PASS.

- [ ] **Step 5: Run the full cmdsys test suite to make sure nothing else regressed**

```bash
go test ./pkg/cmdsys/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/cmdsys/command.go pkg/cmdsys/command_test.go
git commit -m "feat(cmdsys): add RoutePlayerHomeOrOwner route kind"
```

### Task 2: Add `LocalProcess` interface marker on `LocalContext`

**Files:**

- Modify: `pkg/cmdsys/command.go:97-101` (LocalContext type).
- Test: `pkg/cmdsys/command_test.go`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/cmdsys/command_test.go`:

```go
type fakeProcess struct{}

func (fakeProcess) isLocalProcess() {}

func TestLocalContext_AcceptsLocalProcess(t *testing.T) {
	var lp LocalProcess = fakeProcess{}
	lc := LocalContext{Process: lp}
	if lc.Process == nil {
		t.Fatal("LocalContext.Process should retain the assigned LocalProcess")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/cmdsys/ -run TestLocalContext_AcceptsLocalProcess -v
```

Expected: FAIL with `undefined: LocalProcess` or `unknown field Process`.

- [ ] **Step 3: Extend `LocalContext`**

In `pkg/cmdsys/command.go`, replace the existing `LocalContext` block:

```go
// LocalContext is an opaque per-invocation handle for infrastructure
// objects. The dispatcher populates Process at Invoke time when a
// concrete LocalProcess implementation is available; unit tests leave
// it nil and bypass any helper that requires it.
type LocalContext struct {
	Process LocalProcess
}

// LocalProcess is the minimal surface cmdsys exposes to handlers from
// the running process. Implemented by *universe.Process at the universe
// layer via an unexported marker method, which keeps cmdsys a leaf
// package (no import of universe).
type LocalProcess interface {
	isLocalProcess()
}
```

- [ ] **Step 4: Run to verify the new test passes and existing tests still compile**

```bash
go test ./pkg/cmdsys/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/cmdsys/command.go pkg/cmdsys/command_test.go
git commit -m "feat(cmdsys): add LocalProcess marker interface on LocalContext"
```

### Task 3: Wire `*Process` to satisfy `LocalProcess`

**Files:**

- Modify: `pkg/universe/coordinator.go` — add the marker method on `*Process`.
- Modify: `pkg/cmdsys/dispatcher.go` — populate `env.Local.Process` from a config value.
- Test: `pkg/universe/cmdsys_resolver_test.go` (create or extend) — assert the assertion succeeds.

- [ ] **Step 1: Write the failing assertion test**

Append to `pkg/universe/cmdsys_resolver_test.go` (create if it doesn't exist):

```go
package universe

import (
	"testing"

	"github.com/zenion/mmokit/pkg/cmdsys"
)

func TestProcess_ImplementsLocalProcess(t *testing.T) {
	var lp cmdsys.LocalProcess = (*Process)(nil)
	_ = lp
}
```

- [ ] **Step 2: Run to verify it fails to compile**

```bash
go test ./pkg/universe/ -run TestProcess_ImplementsLocalProcess -v
```

Expected: FAIL — `*Process does not implement cmdsys.LocalProcess`.

- [ ] **Step 3: Add the marker method**

At the bottom of `pkg/universe/coordinator.go` (any location after `type Process struct`):

```go
// isLocalProcess satisfies cmdsys.LocalProcess. The marker method is
// unexported on purpose: cmdsys callers cannot construct a LocalProcess
// value themselves; only types in this package can.
func (*Process) isLocalProcess() {}
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./pkg/universe/ -run TestProcess_ImplementsLocalProcess -v
```

Expected: PASS.

- [ ] **Step 5: Wire the dispatcher to populate `env.Local.Process`**

In `pkg/cmdsys/dispatcher.go`, extend `DispatcherConfig` and the `Dispatcher` struct:

```go
// In DispatcherConfig (around line 33):
type DispatcherConfig struct {
	Registry  *Registry
	Resolver  RouteResolver
	Transport Transport
	Audit     AuditSink
	Grants    GrantStore
	Logger    *logger.Logger
	// Process is published into env.Local.Process for every Invoke /
	// InvokeLocal call. Nil for unit tests.
	Process LocalProcess
}

// In Dispatcher struct (around line 16):
type Dispatcher struct {
	registry  *Registry
	resolver  RouteResolver
	transport Transport
	audit     AuditSink
	grants    GrantStore
	log       *logger.Logger
	process   LocalProcess

	mu      sync.Mutex
	pending map[uint64]*pendingReq
	nextID  uint64

	closeOnce sync.Once
	closeCh   chan struct{}
}
```

In `NewDispatcher` (around line 43), copy the field:

```go
	d := &Dispatcher{
		registry:  cfg.Registry,
		resolver:  cfg.Resolver,
		transport: cfg.Transport,
		audit:     cfg.Audit,
		grants:    cfg.Grants,
		log:       cfg.Logger,
		process:   cfg.Process,
		pending:   make(map[uint64]*pendingReq),
		closeCh:   make(chan struct{}),
	}
```

In `InvokeLocal` (around line 93) and `Invoke` (around line 240), where `env := &Env{...Local: &LocalContext{},...}` is built, change to:

```go
	env := &Env{
		Caller:  caller,
		TraceID: traceID,
		Local:   &LocalContext{Process: d.process},
		Logger:  d.log,
	}
```

(There are two such `env := &Env{...}` constructions — `InvokeLocal` and `Invoke`. Update both.)

- [ ] **Step 6: Find where the universe layer constructs the dispatcher and pass `*Process`**

```bash
grep -n "cmdsys.NewDispatcher\|DispatcherConfig{" pkg/universe/*.go | grep -v _test
```

Wherever `cmdsys.NewDispatcher(cmdsys.DispatcherConfig{...})` is called inside `pkg/universe/`, add `Process: c` (the `*Process` value) to the config. There is one such site in `pkg/universe/coordinator.go`. Add the line.

- [ ] **Step 7: Run the universe + cmdsys tests**

```bash
go test ./pkg/cmdsys/... ./pkg/universe/...
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add pkg/cmdsys/dispatcher.go pkg/universe/coordinator.go pkg/universe/cmdsys_resolver_test.go
git commit -m "feat(universe): wire *Process as cmdsys.LocalProcess in Env.Local"
```

### Task 4: Add `Coordinator.PickDBHost` + `HasPlayerDB` advertisement

**Files:**

- Create: `pkg/universe/db_host_picker.go`.
- Modify: `pkg/universe/host_registry.go` — extend the registration record with `HasPlayerDB`.
- Modify: `pkg/universe/coordinator.go` — surface `HasPlayerDB` on `Process` for the local process and pass it through `RegisterHost`.
- Test: `pkg/universe/db_host_picker_test.go`.

- [ ] **Step 1: Inspect host registry to know the field shape**

```bash
grep -n "type liveHost\|hostInfo\b\|HostState\b\|hosts map\|hostRegistry" pkg/universe/host_registry.go | head -20
```

Note the existing struct (likely `liveHost` or similar) so the new boolean field is added in the right place.

- [ ] **Step 2: Write the failing test**

Create `pkg/universe/db_host_picker_test.go`:

```go
package universe

import "testing"

func TestPickDBHost_PrefersLexFirstWithDB(t *testing.T) {
	c := &Process{
		Cells: map[string]*Cell{},
	}
	// Register two hosts, only one has the DB.
	c.registerLiveHost("host_b", true)
	c.registerLiveHost("host_a", false)

	if got := c.PickDBHost(); got != "host_b" {
		t.Fatalf("PickDBHost() = %q, want host_b", got)
	}

	// Register a third host with DB; lex-first wins.
	c.registerLiveHost("host_c", true)
	if got := c.PickDBHost(); got != "host_b" {
		t.Fatalf("after host_c registered, PickDBHost() = %q, want host_b", got)
	}
}

func TestPickDBHost_NoneAvailable(t *testing.T) {
	c := &Process{Cells: map[string]*Cell{}}
	c.registerLiveHost("host_a", false)
	if got := c.PickDBHost(); got != "" {
		t.Fatalf("PickDBHost() with no DB host = %q, want empty", got)
	}
}
```

- [ ] **Step 3: Run to verify it fails**

```bash
go test ./pkg/universe/ -run TestPickDBHost -v
```

Expected: FAIL — `PickDBHost undefined`, `registerLiveHost undefined`.

- [ ] **Step 4: Add the `HasPlayerDB` field to the host registry**

In `pkg/universe/host_registry.go`, find the live-host struct (e.g. `liveHost` or `hostInfo`) and add:

```go
	HasPlayerDB bool // advertised by the host at RegisterHost time
```

If `RegisterHost` takes a request struct (e.g. `*meshpb.RegisterHostRequest`), copy the field across at registration time. If the registration request lives in the proto, add `bool has_player_db = N;` to `proto/meshpb/mesh.proto` first and run `just proto`.

- [ ] **Step 5: Add the proto field if needed**

```bash
grep -n "RegisterHost\b" proto/meshpb/mesh.proto
```

If `RegisterHostRequest` exists in the proto, add the new field. Look at the existing largest field number and pick the next:

```proto
message RegisterHostRequest {
  string host_id = 1;
  // ...existing fields...
  bool has_player_db = N;  // new — N = next unused number
}
```

Then run:

```bash
just proto
```

Expected: regenerated `gen/go/meshpb/*.go` files with the new `HasPlayerDB` getter.

- [ ] **Step 6: Wire host.HasPlayerDB through RegisterHost**

Find `RegisterHost`:

```bash
grep -n "func .* RegisterHost\b" pkg/universe/*.go
```

In the implementation, when the request is converted to a `liveHost` record, copy `req.GetHasPlayerDB()` into the new field.

- [ ] **Step 7: Add test helper `registerLiveHost` and `PickDBHost`**

Create `pkg/universe/db_host_picker.go`:

```go
package universe

import "sort"

// registerLiveHost is a test-only helper that injects a host record
// directly into the registry without going through RegisterHost. The
// production registration path runs in mesh_control_server.go.
func (c *Process) registerLiveHost(id string, hasDB bool) {
	c.hostRegistryMu.Lock()
	defer c.hostRegistryMu.Unlock()
	if c.liveHosts == nil {
		c.liveHosts = make(map[string]*liveHost)
	}
	c.liveHosts[id] = &liveHost{ID: id, HasPlayerDB: hasDB, Live: true}
}

// PickDBHost returns the lexicographically first live host whose process
// advertised a PlayerRepository at RegisterHost time. Returns "" if no
// host carries the DB. Used by RoutePlayerHomeOrOwner to dispatch
// offline player commands.
func (c *Process) PickDBHost() string {
	c.hostRegistryMu.RLock()
	defer c.hostRegistryMu.RUnlock()
	candidates := make([]string, 0, len(c.liveHosts))
	for id, h := range c.liveHosts {
		if h.Live && h.HasPlayerDB {
			candidates = append(candidates, id)
		}
	}
	sort.Strings(candidates)
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}
```

(Field names `hostRegistryMu`, `liveHosts`, `liveHost` may differ — adapt to whatever shape `pkg/universe/host_registry.go` actually uses. Use the grep from Step 1 to discover them; the marker is "must use the live-host map's lock and iterate live, DB-bearing hosts.")

- [ ] **Step 8: Run to verify the unit test passes**

```bash
go test ./pkg/universe/ -run TestPickDBHost -v
```

Expected: PASS.

- [ ] **Step 9: Advertise HasPlayerDB from the local process**

Find where this process registers itself with its own coord (single-process `all` mode). Look for:

```bash
grep -n "RegisterHost\|notifyHostRegistered\|self.*host" pkg/universe/coordinator.go | head -20
```

When the process opens a `PlayerRepository`-bearing role (i.e. `Config.PostgresURL != ""` triggers the DB connection), set `HasPlayerDB = true` on the local liveHost record. The simplest path: a public `Process.SetHasPlayerDB(bool)` setter that the bootstrap calls right after the DB opens. Add it to `db_host_picker.go`:

```go
// SetHasPlayerDB advertises whether this process has a loaded
// PlayerRepository. Called by the bootstrap after Postgres opens.
// Routed through PeerList broadcasts to remote coordinators.
func (c *Process) SetHasPlayerDB(b bool) {
	c.hostRegistryMu.Lock()
	c.localHasPlayerDB = b
	if h := c.liveHosts[c.HostID()]; h != nil {
		h.HasPlayerDB = b
	}
	c.hostRegistryMu.Unlock()
	c.broadcastPeerList()
}
```

(Adapt method names to the existing peer-broadcast helper. If `broadcastPeerList` doesn't exist by that exact name, find it via `grep -n "PeerList" pkg/universe/coordinator.go`.)

- [ ] **Step 10: Call `SetHasPlayerDB(true)` from main**

In `cmd/server/main.go`, immediately after `playerDB` is loaded:

```go
if playerDB != nil {
	coordinator.SetHasPlayerDB(true)
}
```

- [ ] **Step 11: Run the full universe + cmd suite**

```bash
go test ./pkg/universe/ ./pkg/cmdsys/
just build
```

Expected: PASS, build OK.

- [ ] **Step 12: Commit**

```bash
git add pkg/universe/db_host_picker.go pkg/universe/db_host_picker_test.go pkg/universe/host_registry.go pkg/universe/coordinator.go cmd/server/main.go proto/meshpb/mesh.proto gen/go/meshpb gen/csharp gen/es
git commit -m "feat(universe): add Coordinator.PickDBHost with HasPlayerDB advertisement"
```

### Task 5: Resolve `RoutePlayerHomeOrOwner` in `meshRouteResolver`

**Files:**

- Modify: `pkg/universe/cmdsys_resolver.go:30-115` (the `Resolve` switch).
- Test: `pkg/universe/cmdsys_resolver_test.go`.

- [ ] **Step 1: Write the failing tests**

Append to `pkg/universe/cmdsys_resolver_test.go`:

```go
type playerArgs struct {
	Username string
}

func TestResolve_PlayerHomeOrOwner_Online(t *testing.T) {
	c := &Process{Cells: map[string]*Cell{}}
	c.setActiveUserHost("alice", "host_a") // helper that mirrors notifySessionActive
	c.registerLiveHost("host_a", true)
	r := newMeshRouteResolver(c)
	got, err := r.Resolve(cmdsys.RoutePlayerHomeOrOwner, "player.tp", playerArgs{Username: "alice"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].ID != "host_a" {
		t.Fatalf("Resolve(online) = %+v, want one target host_a", got)
	}
}

func TestResolve_PlayerHomeOrOwner_Offline_FallsBackToDBHost(t *testing.T) {
	c := &Process{Cells: map[string]*Cell{}}
	c.registerLiveHost("host_a", false)
	c.registerLiveHost("host_b", true)
	r := newMeshRouteResolver(c)
	got, err := r.Resolve(cmdsys.RoutePlayerHomeOrOwner, "player.tp", playerArgs{Username: "bob"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].ID != "host_b" {
		t.Fatalf("Resolve(offline) = %+v, want one target host_b", got)
	}
}

func TestResolve_PlayerHomeOrOwner_Offline_NoDBHost(t *testing.T) {
	c := &Process{Cells: map[string]*Cell{}}
	c.registerLiveHost("host_a", false)
	r := newMeshRouteResolver(c)
	_, err := r.Resolve(cmdsys.RoutePlayerHomeOrOwner, "player.tp", playerArgs{Username: "bob"})
	if err == nil {
		t.Fatalf("Resolve(no-db) should have returned ErrRouteNoOwner")
	}
}
```

(`setActiveUserHost` will need a small test helper; if it doesn't exist, add it next to `registerLiveHost` in `db_host_picker.go` — it just writes to whichever map `ActiveUserHost` reads.)

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/universe/ -run TestResolve_PlayerHomeOrOwner -v
```

Expected: FAIL — the resolver currently returns `cmdsys.ErrNotYetWired` for the new route kind.

- [ ] **Step 3: Add the resolver case**

In `pkg/universe/cmdsys_resolver.go`, in the `Resolve` switch (just before `default:`):

```go
case cmdsys.RoutePlayerHomeOrOwner:
	username := extractStringField(args, "Username")
	if username == "" {
		return nil, ErrRouteMissingField
	}
	if hostID := r.coord.ActiveUserHost(username); hostID != "" {
		return []cmdsys.Target{{Kind: cmdsys.RoutePlayerHomeOrOwner, ID: hostID}}, nil
	}
	if hostID := r.coord.PickDBHost(); hostID != "" {
		return []cmdsys.Target{{Kind: cmdsys.RoutePlayerHomeOrOwner, ID: hostID}}, nil
	}
	return nil, ErrRouteNoOwner
```

- [ ] **Step 4: Add the missing `setActiveUserHost` test helper**

In `pkg/universe/db_host_picker.go`, alongside `registerLiveHost`:

```go
// setActiveUserHost is a test-only helper that injects an entry into
// the active-user → host map. Production code uses notifySessionActive.
func (c *Process) setActiveUserHost(username, hostID string) {
	c.activeUsersMu.Lock()
	if c.activeUsers == nil {
		c.activeUsers = make(map[string]string)
	}
	c.activeUsers[username] = hostID
	c.activeUsersMu.Unlock()
}
```

(Adapt the field names to whatever `ActiveUserHost` actually reads — discover via `grep -n "ActiveUserHost\b" pkg/universe/coordinator.go`.)

- [ ] **Step 5: Run the resolver tests**

```bash
go test ./pkg/universe/ -run TestResolve_PlayerHomeOrOwner -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/cmdsys_resolver.go pkg/universe/cmdsys_resolver_test.go pkg/universe/db_host_picker.go
git commit -m "feat(universe): resolve RoutePlayerHomeOrOwner via online owner / DB-bearing fallback"
```

### Task 6: Define `PlayerTarget` and `ResolvePlayerTarget`

**Files:**

- Create: `pkg/universe/player_target.go`.
- Test: `pkg/universe/player_target_test.go`.

- [ ] **Step 1: Write the failing test**

Create `pkg/universe/player_target_test.go`:

```go
package universe

import (
	"testing"

	"github.com/zenion/mmokit/pkg/cmdsys"
)

func TestResolvePlayerTarget_NotFound(t *testing.T) {
	c := &Process{Cells: map[string]*Cell{}}
	env := &cmdsys.Env{Local: &cmdsys.LocalContext{Process: c}}
	target := ResolvePlayerTarget(env, "ghost")
	if target.Online != nil || target.Offline != nil {
		t.Fatalf("expected NotFound, got Online=%v Offline=%v", target.Online, target.Offline)
	}
	if target.Username != "ghost" {
		t.Fatalf("Username = %q, want ghost", target.Username)
	}
}

func TestResolvePlayerTarget_NilProcess(t *testing.T) {
	env := &cmdsys.Env{Local: &cmdsys.LocalContext{}}
	target := ResolvePlayerTarget(env, "alice")
	if target.Online != nil || target.Offline != nil {
		t.Fatalf("nil process should return NotFound")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/universe/ -run TestResolvePlayerTarget -v
```

Expected: FAIL — `ResolvePlayerTarget` undefined.

- [ ] **Step 3: Define the type and helper**

Create `pkg/universe/player_target.go`:

```go
package universe

import (
	"strings"

	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/engine"
)

// PlayerTarget is the result of ResolvePlayerTarget. Exactly one of
// Online or Offline is non-nil when the player exists; both are nil
// when the player is unknown to this process.
type PlayerTarget struct {
	Username  string
	Stage     *Stage              // non-nil iff Online != nil
	Online    *engine.PlayerSession
	Offline   PlayerDataAccessor  // non-nil iff persisted state was found here
	DirtyMark func()              // call after mutating Offline (no-op when Online)
}

// PlayerDataAccessor is the minimal surface ResolvePlayerTarget exposes
// for offline players. The repository layer satisfies it via its
// existing PlayerData type. Defined here as an interface so universe
// stays game-agnostic — the game's *PlayerData embeds it (or implements
// it) and registers a Locator.
type PlayerDataAccessor interface {
	GetUsername() string
	GetCellX() int32
	GetCellY() int32
	GetX() float32
	GetY() float32
	SetCell(cellX, cellY int32)
	SetPosition(x, y float32)
}

// PlayerDataLocator is the universe-side hook that game code installs
// at startup. universe never reaches into PlayerRepo directly.
type PlayerDataLocator interface {
	Get(username string) (PlayerDataAccessor, func(), bool)
}

// ResolvePlayerTarget looks up the named user across local cells (online
// branch) and falls back to the registered PlayerDataLocator (offline
// branch). Returns a NotFound zero-value PlayerTarget when neither
// branch hits.
func ResolvePlayerTarget(env *cmdsys.Env, username string) PlayerTarget {
	username = strings.ToLower(username)
	t := PlayerTarget{Username: username, DirtyMark: func() {}}
	if env == nil || env.Local == nil || env.Local.Process == nil {
		return t
	}
	proc, ok := env.Local.Process.(*Process)
	if !ok {
		return t
	}
	for _, cell := range proc.Cells {
		if cell == nil || cell.Stage == nil {
			continue
		}
		sess := proc.lookupSessionInCell(cell, username)
		if sess != nil {
			t.Stage = cell.Stage
			t.Online = sess
			return t
		}
	}
	if proc.playerDataLocator != nil {
		if data, mark, ok := proc.playerDataLocator.Get(username); ok {
			t.Offline = data
			if mark != nil {
				t.DirtyMark = mark
			}
			return t
		}
	}
	return t
}
```

Add `playerDataLocator PlayerDataLocator` to the `Process` struct in `pkg/universe/coordinator.go` plus a setter:

```go
// SetPlayerDataLocator installs the offline-player lookup hook. Called
// by game bootstrap after PlayerRepo is constructed.
func (c *Process) SetPlayerDataLocator(loc PlayerDataLocator) {
	c.mu.Lock()
	c.playerDataLocator = loc
	c.mu.Unlock()
}
```

Add the `lookupSessionInCell` helper next to it:

```go
// lookupSessionInCell extracts an engine.PlayerSession by username from
// the cell's GameWorld via the Players accessor, if present. Returns
// nil when the cell doesn't carry the requested user.
func (c *Process) lookupSessionInCell(cell *Cell, username string) *engine.PlayerSession {
	if cell == nil || cell.World == nil {
		return nil
	}
	type playersByUsername interface {
		PlayersByUsername(string) *engine.PlayerSession
	}
	if pbu, ok := cell.World.(playersByUsername); ok {
		return pbu.PlayersByUsername(username)
	}
	return nil
}
```

(The `GameWorld` interface in `pkg/universe/world.go` may need to grow a `PlayersByUsername(username string) *engine.PlayerSession` method, OR — preferred — `*Stage` already exposes a player-manager that `cell.Stage` can be queried through; if so, switch the helper to use `cell.Stage`. Inspect `pkg/universe/world.go` and `pkg/universe/stage.go` to choose; use whichever already exists.)

- [ ] **Step 4: Run to verify the test passes**

```bash
go test ./pkg/universe/ -run TestResolvePlayerTarget -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/player_target.go pkg/universe/player_target_test.go pkg/universe/coordinator.go pkg/universe/world.go
git commit -m "feat(universe): add PlayerTarget + ResolvePlayerTarget helper"
```

### Task 7: Implement the offline-side `PlayerDataLocator` in space-game

**Files:**

- Modify: `internal/game/playerdb.go` — implement `universe.PlayerDataAccessor` on `*PlayerData` plus a `Locator` adapter on `*PlayerRepo`.
- Modify: `cmd/server/main.go` — install the locator on the coordinator.
- Test: `internal/game/playerdb_test.go` (extend or create).

- [ ] **Step 1: Write the failing test**

Append to `internal/game/playerdb_test.go`:

```go
func TestPlayerDataAccessor_RoundTrip(t *testing.T) {
	pd := &PlayerData{Username: "alice"}
	pd.SetCell(3, -2)
	pd.SetPosition(10.5, 20.5)
	if pd.CellX != 3 || pd.CellY != -2 {
		t.Fatalf("SetCell didn't take: %d,%d", pd.CellX, pd.CellY)
	}
	if pd.X != 10.5 || pd.Y != 20.5 {
		t.Fatalf("SetPosition didn't take: %g,%g", pd.X, pd.Y)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/game/ -run TestPlayerDataAccessor_RoundTrip -v
```

Expected: FAIL — `SetCell` / `SetPosition` undefined.

- [ ] **Step 3: Add accessor methods on `*PlayerData`**

In `internal/game/playerdata.go`:

```go
// GetUsername / GetCellX / etc. satisfy universe.PlayerDataAccessor so
// ResolvePlayerTarget can return a *PlayerData as its Offline branch.

func (pd *PlayerData) GetUsername() string { return pd.Username }
func (pd *PlayerData) GetCellX() int32     { return pd.CellX }
func (pd *PlayerData) GetCellY() int32     { return pd.CellY }
func (pd *PlayerData) GetX() float32       { return pd.X }
func (pd *PlayerData) GetY() float32       { return pd.Y }

func (pd *PlayerData) SetCell(cx, cy int32)     { pd.CellX, pd.CellY = cx, cy }
func (pd *PlayerData) SetPosition(x, y float32) { pd.X, pd.Y = x, y }
```

- [ ] **Step 4: Add the `Locator` adapter on `*PlayerRepo`**

In `internal/game/playerdb.go`:

```go
// Locator satisfies universe.PlayerDataLocator. The closure returned
// for DirtyMark calls MarkDirty(username) so the change flushes to
// Postgres on the next FlushDirty cycle.
func (r *PlayerRepo) Locator() universePlayerDataLocator {
	return repoLocator{repo: r}
}

type universePlayerDataLocator = interface {
	Get(username string) (any, func(), bool)
}

type repoLocator struct{ repo *PlayerRepo }

func (l repoLocator) Get(username string) (any, func(), bool) {
	pd := l.repo.Get(username)
	if pd == nil {
		return nil, nil, false
	}
	return pd, func() { l.repo.MarkDirty(username) }, true
}
```

The `any` return values are incompatible with the universe interface signature. Fix this — replace the alias with a proper bridge package or inline the universe import. The cleanest path is to import `universe` directly here, since `internal/game` already does:

```bash
grep -n "pkg/universe" internal/game/playerdb.go
```

If yes, reshape to:

```go
import "github.com/zenion/mmokit/pkg/universe"

func (r *PlayerRepo) Locator() universe.PlayerDataLocator {
	return repoLocator{repo: r}
}

type repoLocator struct{ repo *PlayerRepo }

func (l repoLocator) Get(username string) (universe.PlayerDataAccessor, func(), bool) {
	pd := l.repo.Get(username)
	if pd == nil {
		return nil, nil, false
	}
	return pd, func() { l.repo.MarkDirty(username) }, true
}
```

- [ ] **Step 5: Install the locator from `cmd/server/main.go`**

After `playerDB` is constructed and after `coordinator = mmokit.New(...)`:

```go
coordinator.SetPlayerDataLocator(playerDB.Locator())
```

(Use the `*Process` value returned from the existing setup.)

- [ ] **Step 6: Run the unit + integration tests**

```bash
go test ./internal/game/ ./pkg/universe/ -run "TestResolvePlayerTarget|TestPlayerDataAccessor" -v
just build
```

Expected: PASS, build OK.

- [ ] **Step 7: Commit**

```bash
git add internal/game/playerdata.go internal/game/playerdb.go internal/game/playerdb_test.go cmd/server/main.go
git commit -m "feat(game): implement universe.PlayerDataLocator on PlayerRepo"
```

### Task 8: Generalize `HandoffDriver` (drop neighbor precondition + cooldown bypass)

**Files:**

- Modify: `pkg/universe/stage.go:50-56` (CrossingEvent) — add `BypassCooldown bool`.
- Modify: `pkg/universe/handoff_driver.go:249+` (`handleCrossing`) — branch the cooldown lookup on the new flag.
- Modify: `pkg/universe/handoff_driver.go:373` (`bridge.SendHandoff` call) — drop the neighbor-only precondition that lives upstream of this call (likely in `BoundarySystem` or in `handleCrossing` itself).
- Test: `pkg/universe/handoff_driver_test.go`.

- [ ] **Step 1: Map the existing neighbor precondition**

```bash
grep -n "Neighbor\|neighbor\|isNeighbor" pkg/universe/handoff_driver.go pkg/universe/cell.go pkg/universe/stage.go | head -20
```

Locate the specific line that rejects non-neighbor `DestCellID` values. If the rejection is in `BoundarySystem` (which generates the crossing) rather than in `handleCrossing`, the patch site moves accordingly. The discovery here informs which file you modify; the test below is identical either way.

- [ ] **Step 2: Write the failing test**

Append to `pkg/universe/handoff_driver_test.go`:

```go
func TestHandoffDriver_AcceptsNonNeighborDestination(t *testing.T) {
	stage, bridge, hd := setupHandoffDriverHarness(t) // existing helper
	stage.QueueCrossing(CrossingEvent{
		Entity:     spawnTestEntity(t, stage),
		NetID:      42,
		DestCellID: "cell_5_5", // far from stage's cell_0_0; not a Moore neighbor
	})
	hd.Tick(1)
	if !bridge.gotHandoffTo("cell_5_5") {
		t.Fatalf("HandoffDriver dropped non-neighbor crossing; bridge saw: %v", bridge.handoffs())
	}
}

func TestHandoffDriver_BypassCooldown(t *testing.T) {
	stage, bridge, hd := setupHandoffDriverHarness(t)
	for i := 0; i < 3; i++ {
		stage.QueueCrossing(CrossingEvent{
			Entity:         spawnTestEntity(t, stage),
			NetID:          uint32(100 + i),
			DestCellID:     "cell_1_0",
			BypassCooldown: true,
		})
		hd.Tick(uint64(i + 1))
	}
	if got := bridge.handoffCount("cell_1_0"); got != 3 {
		t.Fatalf("with BypassCooldown=true, expected 3 handoffs, got %d", got)
	}
}
```

The harness helpers `setupHandoffDriverHarness`, `spawnTestEntity`, and the recording-bridge methods are derived from the existing `handoffRecordingBridge` (see `pkg/universe/handoff_driver_test.go:101`). Reuse what's there; if a method like `gotHandoffTo` doesn't exist, add a one-liner to the recording bridge.

- [ ] **Step 3: Run to verify both tests fail**

```bash
go test ./pkg/universe/ -run TestHandoffDriver_AcceptsNonNeighborDestination -v
go test ./pkg/universe/ -run TestHandoffDriver_BypassCooldown -v
```

Expected: FAIL — first test fails because the crossing is rejected as non-neighbor; second test fails because `CrossingEvent.BypassCooldown` is undefined.

- [ ] **Step 4: Extend `CrossingEvent`**

In `pkg/universe/stage.go:50-56`:

```go
type CrossingEvent struct {
	Entity         ecs.Entity
	NetID          uint32
	ConnID         uint32 // non-zero for player entities
	Username       string // non-empty for player entities
	DestCellID     string // cell ID string the entity crossed into
	BypassCooldown bool   // true for explicit teleports; false for natural boundary crossings
}
```

- [ ] **Step 5: Drop the neighbor-only precondition**

In whichever file the neighbor check lives (likely `pkg/universe/handoff_driver.go`'s `handleCrossing`, or `pkg/universe/boundary_system.go` if the rejection happens at queue time), delete the early-return that filters non-neighbors. Replace any comment/log line about "neighbor-only" with a comment explaining that the destination cell is now resolved at handoff-send time via the bridge's `SendHandoff` (which already routes via `Coordinator.HostForCellID`).

If the check is in `handleCrossing`, the patch is roughly:

```go
// REMOVE this block (or whatever shape it has):
//
// if _, ok := stage.neighborMap[evt.DestCellID]; !ok {
//     return
// }
```

- [ ] **Step 6: Branch the cooldown on `BypassCooldown`**

In `pkg/universe/handoff_driver.go:handleCrossing`, find the cooldown lookup against `hd.lastHandoff`. Wrap it:

```go
if !evt.BypassCooldown {
	if last, ok := hd.lastHandoff[evt.NetID][evt.DestCellID]; ok {
		if currentClusterTick-last < HandoffCooldownTicks {
			return // cooldown active for organic crossings
		}
	}
}
```

(Adapt to the existing surrounding code; the marker is "the lookup that returns early when last < HandoffCooldownTicks ago.")

- [ ] **Step 7: Run to verify both tests pass**

```bash
go test ./pkg/universe/ -run "TestHandoffDriver_AcceptsNonNeighborDestination|TestHandoffDriver_BypassCooldown" -v
```

Expected: PASS.

- [ ] **Step 8: Run the full handoff test family to ensure no regression**

```bash
go test ./pkg/universe/ -run TestHandoffDriver -v
```

Expected: all existing TestHandoffDriver_* still pass.

- [ ] **Step 9: Commit**

```bash
git add pkg/universe/stage.go pkg/universe/handoff_driver.go pkg/universe/handoff_driver_test.go
git commit -m "feat(universe): generalize HandoffDriver — non-neighbor dest + cooldown bypass"
```

### Task 9: Implement `Stage.MoveEntityTo`

**Files:**

- Create: `pkg/universe/stage_move_entity.go`.
- Test: `pkg/universe/stage_move_entity_test.go`.

- [ ] **Step 1: Write the failing test**

Create `pkg/universe/stage_move_entity_test.go`:

```go
package universe

import (
	"testing"

	"github.com/zenion/mmokit/pkg/coords"
)

func TestMoveEntityTo_SameCell_UpdatesPositionInline(t *testing.T) {
	stage := setupTestStage(t, "cell_0_0") // existing helper
	e := spawnTestEntity(t, stage)
	stage.PositionMap().Get(e).X = 10
	stage.PositionMap().Get(e).Y = 10
	if err := stage.MoveEntityTo(e, 50, 60); err != nil {
		t.Fatalf("MoveEntityTo: %v", err)
	}
	pos := stage.PositionMap().Get(e)
	if pos.X != 50 || pos.Y != 60 {
		t.Fatalf("Position = (%g,%g), want (50,60)", pos.X, pos.Y)
	}
	if got := stage.DrainCrossingQueue(); len(got) != 0 {
		t.Fatalf("same-cell move should not enqueue a crossing, got %d", len(got))
	}
}

func TestMoveEntityTo_CrossCell_EnqueuesCrossingWithBypass(t *testing.T) {
	stage := setupTestStage(t, "cell_0_0")
	e := spawnTestEntity(t, stage)
	farX, farY := float32(coords.CellSize*5+10), float32(coords.CellSize*5+10)
	if err := stage.MoveEntityTo(e, farX, farY, MoveBypassCooldown()); err != nil {
		t.Fatalf("MoveEntityTo: %v", err)
	}
	q := stage.DrainCrossingQueue()
	if len(q) != 1 {
		t.Fatalf("cross-cell move should enqueue one crossing, got %d", len(q))
	}
	if !q[0].BypassCooldown {
		t.Fatalf("crossing should carry BypassCooldown=true")
	}
	if q[0].DestCellID == "" {
		t.Fatalf("crossing missing DestCellID")
	}
}
```

`setupTestStage` and `spawnTestEntity` exist as test helpers; reuse. If a "create a fresh stage rooted at cell_0_0" helper isn't present under that exact name, locate the closest equivalent in `pkg/universe/*_test.go` and adapt the call sites.

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/universe/ -run TestMoveEntityTo -v
```

Expected: FAIL — `MoveEntityTo` and `MoveBypassCooldown` undefined.

- [ ] **Step 3: Implement the primitive**

Create `pkg/universe/stage_move_entity.go`:

```go
package universe

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/coords"
)

// MoveOpt configures a MoveEntityTo call.
type MoveOpt func(*moveOpts)

type moveOpts struct {
	bypassCooldown bool
}

// MoveBypassCooldown skips HandoffCooldownTicks for an explicit
// teleport. Boundary crossings (which never set this flag) keep the
// default anti-thrash cooldown.
func MoveBypassCooldown() MoveOpt {
	return func(o *moveOpts) { o.bypassCooldown = true }
}

// MoveEntityTo moves the named entity to absolute world coordinates.
// Same-cell → direct Position update. Different cell → enqueues a
// crossing event for HandoffDriver to convert into a hard-cut handoff
// at currentClusterTick + HandoffLeadTicks. Cross-host moves go
// through the existing bridge.SendHandoff path.
//
// MUST be called on the cell's loop goroutine. cmdsys handlers that
// run via mmokit.OnLoop satisfy this; off-loop callers must wrap in
// engine.RunOnLoop.
func (s *Stage) MoveEntityTo(e ecs.Entity, worldX, worldY float32, opts ...MoveOpt) error {
	cfg := moveOpts{}
	for _, o := range opts {
		o(&cfg)
	}
	if !s.ECSWorld().Alive(e) {
		return fmt.Errorf("MoveEntityTo: entity not alive")
	}
	if !s.posMap.HasAll(e) {
		return fmt.Errorf("MoveEntityTo: entity has no Position")
	}

	destCellX := int32(worldX / coords.CellSize)
	destCellY := int32(worldY / coords.CellSize)
	destCellID := fmt.Sprintf("cell_%d_%d", destCellX, destCellY)

	// Same-cell fast path.
	if destCellID == s.CellID() {
		pos := s.posMap.Get(e)
		pos.X = worldX - float32(destCellX)*coords.CellSize
		pos.Y = worldY - float32(destCellY)*coords.CellSize
		if s.velMap.HasAll(e) {
			vel := s.velMap.Get(e)
			vel.X, vel.Y = 0, 0
		}
		if s.cellMap.HasAll(e) {
			cc := s.cellMap.Get(e)
			cc.CellX, cc.CellY = destCellX, destCellY
		}
		// Reset MoveTarget if present (not all entities have one — the
		// game's component package owns that type, so we look it up via
		// reflection-free interface check).
		if mt, ok := s.maybeMoveTarget(e); ok {
			mt.Active = false
		}
		return nil
	}

	// Cross-cell branch — pre-stamp the entity's coordinates so the
	// destination receives the post-TP world position, then enqueue a
	// synthetic crossing for HandoffDriver to commit.
	pos := s.posMap.Get(e)
	pos.X = worldX - float32(destCellX)*coords.CellSize
	pos.Y = worldY - float32(destCellY)*coords.CellSize
	if s.cellMap.HasAll(e) {
		cc := s.cellMap.Get(e)
		cc.CellX, cc.CellY = destCellX, destCellY
	}

	netID := uint32(0)
	if s.netIDMap.HasAll(e) {
		netID = s.netIDMap.Get(e).ID
	}
	connID, username := s.maybePlayerSession(e) // returns 0/"" for non-player entities

	s.QueueCrossing(CrossingEvent{
		Entity:         e,
		NetID:          netID,
		ConnID:         connID,
		Username:       username,
		DestCellID:     destCellID,
		BypassCooldown: cfg.bypassCooldown,
	})
	return nil
}

// maybeMoveTarget exposes a MoveTarget-like component if the running
// game registered one against this Stage. Returns (nil, false) when no
// such component is mapped, so universe stays game-agnostic. The
// game's component definition must satisfy:
//
//	type MoveTargetLike interface { SetActive(bool) }
//
// Stage.RegisterMoveTargetMap below installs the lookup at startup.
func (s *Stage) maybeMoveTarget(e ecs.Entity) (*moveTargetView, bool) {
	if s.moveTargetMap == nil {
		return nil, false
	}
	if !s.moveTargetMap.HasAll(e) {
		return nil, false
	}
	v := s.moveTargetMap.Get(e)
	return &moveTargetView{Active: &v.Active}, true
}

type moveTargetView struct {
	Active *bool
}

func (v *moveTargetView) setActive(b bool) { *v.Active = b }

// maybePlayerSession walks the cell's session table to find a session
// whose Entity matches e. Returns (0, "") when e is not a player.
func (s *Stage) maybePlayerSession(e ecs.Entity) (uint32, string) {
	if s.players == nil {
		return 0, ""
	}
	if sess := s.players.SessionForEntity(e); sess != nil {
		return sess.ConnID, sess.Username
	}
	return 0, ""
}
```

The `moveTargetView` indirection is a placeholder — the cleanest way is to add a `MoveTargetMap *ecs.Map1[component.MoveTarget]` to `pkg/component/` so `pkg/universe/` can reference a concrete generic component. Inspect `pkg/component/`:

```bash
grep -n "MoveTarget\b" pkg/component/*.go
```

If `MoveTarget` already exists in `pkg/component/`, drop the interface dance and do:

```go
if s.moveTargetMap != nil && s.moveTargetMap.HasAll(e) {
	s.moveTargetMap.Get(e).Active = false
}
```

`s.moveTargetMap` is added to `Stage` (pkg/universe/stage.go) alongside `posMap`/`velMap`/`cellMap` and initialized in the existing Stage constructor. Use whichever path matches the codebase's actual `MoveTarget` location.

The `players` field on Stage and `SessionForEntity` are equally codebase-shape-dependent. Discover via:

```bash
grep -n "SessionForEntity\|playersByEntity\|s.players\b" pkg/universe/stage.go
```

If a per-entity session lookup doesn't exist on Stage today, the simplest path is to skip `maybePlayerSession` and have `Stage.MoveEntityTo` accept an optional `(connID, username)` pair via a `MoveOpt` (`MoveAsPlayer(connID, username)`). The caller already has the session in hand when it calls MoveEntityTo for a player.

- [ ] **Step 4: Run to verify the tests pass**

```bash
go test ./pkg/universe/ -run TestMoveEntityTo -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/stage_move_entity.go pkg/universe/stage_move_entity_test.go pkg/universe/stage.go
git commit -m "feat(universe): add Stage.MoveEntityTo unified entity-move primitive"
```

### Task 10: Add facade re-exports in mmokit

**Files:**

- Modify: `pkg/mmokit/mmokit.go` — re-export new symbols.
- Test: `pkg/mmokit/wire_system_test.go` (extend) — confirm the re-exports compile and reference the right types.

- [ ] **Step 1: Write the failing test**

Append to `pkg/mmokit/wire_system_test.go` (or create `pkg/mmokit/move_entity_test.go`):

```go
func TestMmokitFacade_ExportsMoveEntityAPI(t *testing.T) {
	var _ MoveOpt = MoveBypassCooldown()
	var _ PlayerTarget = PlayerTarget{}
	_ = ResolvePlayerTarget // used as value
	_ = RoutePlayerHomeOrOwner
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/mmokit/ -run TestMmokitFacade_ExportsMoveEntityAPI -v
```

Expected: FAIL — symbols undefined.

- [ ] **Step 3: Add the re-exports**

In `pkg/mmokit/mmokit.go`, find the existing facade-export block and add:

```go
// Entity-move primitive (re-exported from pkg/universe).
type MoveOpt = universe.MoveOpt

// MoveBypassCooldown re-export.
var MoveBypassCooldown = universe.MoveBypassCooldown

// PlayerTarget re-export.
type PlayerTarget = universe.PlayerTarget

// PlayerDataAccessor / PlayerDataLocator re-exports.
type (
	PlayerDataAccessor = universe.PlayerDataAccessor
	PlayerDataLocator  = universe.PlayerDataLocator
)

// ResolvePlayerTarget re-export.
var ResolvePlayerTarget = universe.ResolvePlayerTarget

// RoutePlayerHomeOrOwner re-export.
const RoutePlayerHomeOrOwner = cmdsys.RoutePlayerHomeOrOwner
```

(`cmdsys` is already imported elsewhere in `mmokit.go`; the existing import block stays unchanged.)

- [ ] **Step 4: Run to verify the test passes**

```bash
go test ./pkg/mmokit/ -run TestMmokitFacade_ExportsMoveEntityAPI -v
```

Expected: PASS.

- [ ] **Step 5: Run the full test suite as a Phase-1 checkpoint**

```bash
go test ./pkg/cmdsys/... ./pkg/universe/... ./pkg/mmokit/... ./internal/game/...
just build
```

Expected: all PASS, build OK.

- [ ] **Step 6: Commit**

```bash
git add pkg/mmokit/mmokit.go pkg/mmokit/wire_system_test.go
git commit -m "feat(mmokit): re-export MoveEntityTo + PlayerTarget + RoutePlayerHomeOrOwner"
```

---

## Phase 2: Engine Commands

After Phase 2, both old space-game commands and new engine commands coexist. The engine commands are what the spec specifies as "should-work-everywhere"; the old game commands stay until Phase 3 deletes the duplicates.

### Task 11: Build `entity.spawn`

**Files:**

- Create: `pkg/universe/builtins_entity.go` (with just `entity.spawn` for this task).
- Test: `pkg/universe/builtins_entity_test.go`.

- [ ] **Step 1: Write the failing test**

Create `pkg/universe/builtins_entity_test.go`:

```go
package universe

import (
	"context"
	"testing"
	"time"

	"github.com/zenion/mmokit/pkg/cmdsys"
)

func TestEntitySpawn_RoutesToCellOwningWorldPos(t *testing.T) {
	c, dispatcher := setupTestCluster(t, 2 /*cells*/) // existing or new helper
	registerEntityCommands(c)
	registerKindForTest(c, "test_dummy") // helper that invokes mmokit.RegisterKind on a trivial bundle
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatcher.Invoke(ctx, testCaller(), "entity.spawn", "test_dummy 3 100 100")
	if err != nil {
		t.Fatalf("entity.spawn: %v", err)
	}
	got := res.PerTarget[0].Result.(entitySpawnResult)
	if got.Spawned != 3 {
		t.Fatalf("Spawned=%d, want 3", got.Spawned)
	}
	if got.CellID == "" {
		t.Fatalf("missing CellID in result")
	}
}
```

(Test harness helpers `setupTestCluster`, `registerKindForTest`, `testCaller` may not exist verbatim. The existing test in `pkg/universe/cmdsys_meshcontrol_test.go` and `pkg/universe/builtins_perf_test.go` already construct similar harnesses — copy what's there.)

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/universe/ -run TestEntitySpawn -v
```

Expected: FAIL — `registerEntityCommands` undefined.

- [ ] **Step 3: Implement `entity.spawn`**

Create `pkg/universe/builtins_entity.go`:

```go
package universe

import (
	"context"
	"fmt"
	"math"
	"math/rand"

	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/coords"
)

type entitySpawnArgs struct {
	Kind   string  `cmd:"help=registered entity kind name,complete=kinds"`
	Count  int32   `cmd:"help=number of entities to spawn"`
	X      float32 `cmd:"help=center world X"`
	Y      float32 `cmd:"help=center world Y"`
	Radius float32 `cmd:"optional,help=randomization radius (0=exact center)"`
}

type entitySpawnResult struct {
	Kind    string
	Count   int32
	CellID  string
	HostID  string
	Spawned int32
}

// registerEntityCommands registers entity.spawn / entity.despawn /
// entity.list / entity.tp on the dispatcher's registry. Called from
// Process.RegisterBuiltins.
func registerEntityCommands(coord *Process) error {
	reg := coord.CommandRegistry()
	if err := reg.Register(cmdsys.Command{
		Verb:        "entity.spawn",
		Capability:  "entity.spawn",
		Description: "spawn N entities of a registered kind at a world location",
		Route:       cmdsys.RouteSpecificCell,
		Args:        entitySpawnArgs{},
		Result:      entitySpawnResult{},
		Handler:     entitySpawnHandler(coord),
	}); err != nil {
		return fmt.Errorf("entity.spawn: %w", err)
	}
	return nil
}

func entitySpawnHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(entitySpawnArgs)
		if args.Count <= 0 {
			return nil, fmt.Errorf("count must be >= 1")
		}
		destCellID := fmt.Sprintf("cell_%d_%d",
			int32(args.X/coords.CellSize), int32(args.Y/coords.CellSize))
		cell := coord.Cells[destCellID]
		if cell == nil {
			return nil, fmt.Errorf("entity.spawn: cell %q not owned by this host", destCellID)
		}
		if !coord.IsKindRegistered(args.Kind) {
			return nil, fmt.Errorf("entity.spawn: kind %q not registered", args.Kind)
		}
		spawned, err := cmdsys.OnLoop(ctx, cell.Engine, func() (int32, error) {
			rng := rand.New(rand.NewSource(int64(args.Count)))
			var n int32
			for range int(args.Count) {
				ex, ey := args.X, args.Y
				if args.Radius > 0 {
					theta := rng.Float64() * 2 * math.Pi
					r := math.Sqrt(rng.Float64()) * float64(args.Radius)
					ex += float32(math.Cos(theta) * r)
					ey += float32(math.Sin(theta) * r)
				}
				if err := coord.SpawnKindAtWorld(cell, args.Kind, ex, ey); err != nil {
					return n, err
				}
				n++
			}
			return n, nil
		})
		if err != nil {
			return nil, err
		}
		return entitySpawnResult{
			Kind:    args.Kind,
			Count:   args.Count,
			CellID:  destCellID,
			HostID:  coord.HostID(),
			Spawned: spawned,
		}, nil
	}
}
```

The two helpers used here — `coord.IsKindRegistered(name)` and `coord.SpawnKindAtWorld(cell, name, x, y)` — must exist on `*Process`. If they don't, add them next to the existing kind-registry helpers:

```bash
grep -n "RegisterKind\|kindReg\|kindByName" pkg/universe/*.go | head -10
```

The kind registry already maps `name → KindDef`. Add:

```go
// IsKindRegistered reports whether the given entity-kind name is
// registered on this process.
func (c *Process) IsKindRegistered(name string) bool {
	c.kindMu.RLock()
	defer c.kindMu.RUnlock()
	_, ok := c.kindsByName[name]
	return ok
}

// SpawnKindAtWorld instantiates the named kind at the given world
// position inside the given cell. The kind's default Init runs; per-
// call init hooks aren't supported here — games that need them write
// their own composite command. MUST be called on cell.Engine's loop.
func (c *Process) SpawnKindAtWorld(cell *Cell, kindName string, worldX, worldY float32) error {
	c.kindMu.RLock()
	def, ok := c.kindsByName[kindName]
	c.kindMu.RUnlock()
	if !ok {
		return fmt.Errorf("kind %q not registered", kindName)
	}
	cellX := int32(worldX / coords.CellSize)
	cellY := int32(worldY / coords.CellSize)
	localX := worldX - float32(cellX)*coords.CellSize
	localY := worldY - float32(cellY)*coords.CellSize
	cell.Stage.SpawnEntity(component.Position{X: localX, Y: localY},
		WithEntityKind(def.TypeID),
		Init(def.DefaultInit),
	)
	return nil
}
```

(Field names `kindMu`, `kindsByName`, `def.TypeID`, `def.DefaultInit` are placeholders — adapt to whatever `pkg/universe/entity_kind.go` already exposes. The existing `mmokit.RegisterKind` call site is the canonical reference.)

Also add the helper at the end:

```go
func (c *Process) CommandRegistry() *cmdsys.Registry {
	return c.dispatcher.Registry() // or whichever exposed accessor exists
}
```

- [ ] **Step 4: Run to verify the test passes**

```bash
go test ./pkg/universe/ -run TestEntitySpawn -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_entity.go pkg/universe/builtins_entity_test.go pkg/universe/entity_kind.go pkg/universe/coordinator.go
git commit -m "feat(universe): add engine entity.spawn command"
```

### Task 12: Add `entity.despawn`

**Files:**

- Modify: `pkg/universe/builtins_entity.go` — add `entity.despawn`.
- Test: `pkg/universe/builtins_entity_test.go`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/builtins_entity_test.go`:

```go
func TestEntityDespawn_MarksForRemovalOnOwnerHost(t *testing.T) {
	c, dispatcher := setupTestCluster(t, 1)
	registerEntityCommands(c)
	registerKindForTest(c, "test_dummy")
	netID := spawnTestEntityViaCmd(t, dispatcher, "test_dummy", 100, 100)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatcher.Invoke(ctx, testCaller(), "entity.despawn",
		fmt.Sprintf("%d", netID))
	if err != nil {
		t.Fatalf("entity.despawn: %v", err)
	}
	got := res.PerTarget[0].Result.(entityDespawnResult)
	if got.NetID != netID {
		t.Fatalf("NetID = %d, want %d", got.NetID, netID)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/universe/ -run TestEntityDespawn -v
```

Expected: FAIL — `entity.despawn` not registered.

- [ ] **Step 3: Add the command**

In `pkg/universe/builtins_entity.go`, append to the file:

```go
type entityDespawnArgs struct {
	NetID uint32 `cmd:"help=entity network ID"`
}

type entityDespawnResult struct {
	NetID    uint32
	Kind     string
	WorldX   float32
	WorldY   float32
	CellID   string
	HostID   string
}
```

In `registerEntityCommands`, after the `entity.spawn` registration:

```go
if err := reg.Register(cmdsys.Command{
	Verb:        "entity.despawn",
	Capability:  "entity.despawn",
	Description: "remove an entity by net ID",
	Route:       cmdsys.RouteEntityOwner,
	Args:        entityDespawnArgs{},
	Result:      entityDespawnResult{},
	Handler:     entityDespawnHandler(coord),
}); err != nil {
	return fmt.Errorf("entity.despawn: %w", err)
}
```

Then the handler:

```go
func entityDespawnHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(entityDespawnArgs)
		for cellID, cell := range coord.Cells {
			res, err := cmdsys.OnLoop(ctx, cell.Engine, func() (entityDespawnResult, error) {
				e, ok := cell.Stage.EntityByNetID(args.NetID) // helper from netIDIndex
				if !ok {
					return entityDespawnResult{}, nil // signal "not on this cell"
				}
				pos := cell.Stage.PositionMap().Get(e)
				kindName := coord.KindNameOf(e, cell.Stage)
				cell.Stage.MarkForRemoval(e)
				return entityDespawnResult{
					NetID:  args.NetID,
					Kind:   kindName,
					WorldX: float32(cell.Cell.X)*coords.CellSize + pos.X,
					WorldY: float32(cell.Cell.Y)*coords.CellSize + pos.Y,
					CellID: cellID,
					HostID: coord.HostID(),
				}, nil
			})
			if err != nil {
				return nil, err
			}
			if res.NetID != 0 {
				return res, nil
			}
		}
		return nil, fmt.Errorf("entity %d not found on this host", args.NetID)
	}
}
```

`Stage.EntityByNetID` is the netIDIndex lookup; verify its name via `grep "EntityByNetID\|byNetID" pkg/universe/netid_index.go`. `coord.KindNameOf(e, stage)` is a small helper on `*Process` that reads the `EntityKind` component and reverse-looks up the kind name from `kindsByName`.

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./pkg/universe/ -run TestEntityDespawn -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_entity.go pkg/universe/builtins_entity_test.go pkg/universe/coordinator.go
git commit -m "feat(universe): add engine entity.despawn command"
```

### Task 13: Add `entity.list`

**Files:**

- Modify: `pkg/universe/builtins_entity.go`.
- Test: `pkg/universe/builtins_entity_test.go`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/builtins_entity_test.go`:

```go
func TestEntityList_AllHostsAggregation(t *testing.T) {
	c, dispatcher := setupTestCluster(t, 2)
	registerEntityCommands(c)
	registerKindForTest(c, "test_dummy")
	for range 5 {
		spawnTestEntityViaCmd(t, dispatcher, "test_dummy", 100, 100)
	}
	for range 3 {
		spawnTestEntityViaCmd(t, dispatcher, "test_dummy",
			float32(coords.CellSize+50), float32(coords.CellSize+50))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatcher.Invoke(ctx, testCaller(), "entity.list", "--kind=test_dummy")
	if err != nil {
		t.Fatalf("entity.list: %v", err)
	}
	got := res.PerTarget[0].Result.(entityListResult)
	if len(got.Entities) != 8 {
		t.Fatalf("Entities=%d, want 8", len(got.Entities))
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/universe/ -run TestEntityList -v
```

Expected: FAIL — `entity.list` not registered.

- [ ] **Step 3: Add the command**

In `pkg/universe/builtins_entity.go`:

```go
type entityListArgs struct {
	Kind string `cmd:"optional,help=filter by registered kind name,complete=kinds"`
}

type entityRow struct {
	NetID  uint32
	Kind   string
	WorldX float32
	WorldY float32
	CellID string
	HostID string
}

type entityListResult struct {
	Entities []entityRow `cmd:"table"`
}
```

In `registerEntityCommands`, after `entity.despawn`:

```go
if err := reg.Register(cmdsys.Command{
	Verb:        "entity.list",
	Capability:  "entity.list",
	Description: "list entities across the cluster (--kind to filter)",
	Route:       cmdsys.RouteAllHosts,
	Args:        entityListArgs{},
	Result:      entityListResult{},
	Handler:     entityListHandler(coord),
}); err != nil {
	return fmt.Errorf("entity.list: %w", err)
}
```

The handler iterates local cells and emits one row per matching entity (using whichever filter API your ECS exposes):

```go
func entityListHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(entityListArgs)
		var rows []entityRow
		for cellID, cell := range coord.Cells {
			cellRows, err := cmdsys.OnLoop(ctx, cell.Engine, func() ([]entityRow, error) {
				return collectEntityRows(coord, cell, cellID, args.Kind), nil
			})
			if err != nil {
				return nil, err
			}
			rows = append(rows, cellRows...)
		}
		return entityListResult{Entities: rows}, nil
	}
}

func collectEntityRows(coord *Process, cell *Cell, cellID, filterKind string) []entityRow {
	var rows []entityRow
	hostID := coord.HostID()
	stage := cell.Stage
	stage.ForEachLiveEntity(func(e ecs.Entity, netID uint32) {
		kindName := coord.KindNameOf(e, stage)
		if filterKind != "" && kindName != filterKind {
			return
		}
		pos := stage.PositionMap().Get(e)
		rows = append(rows, entityRow{
			NetID:  netID,
			Kind:   kindName,
			WorldX: float32(cell.Cell.X)*coords.CellSize + pos.X,
			WorldY: float32(cell.Cell.Y)*coords.CellSize + pos.Y,
			CellID: cellID,
			HostID: hostID,
		})
	})
	return rows
}
```

`Stage.ForEachLiveEntity(fn func(ecs.Entity, uint32))` needs to be added if no equivalent exists. Discover via `grep -n "ForEach\|netIDIndex" pkg/universe/netid_index.go`. The netIDIndex already maintains the live-entity table; expose it as a `Stage` method.

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./pkg/universe/ -run TestEntityList -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_entity.go pkg/universe/builtins_entity_test.go pkg/universe/netid_index.go
git commit -m "feat(universe): add engine entity.list command"
```

### Task 14: Add `entity.tp`

**Files:**

- Modify: `pkg/universe/builtins_entity.go`.
- Test: `pkg/universe/builtins_entity_test.go`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/builtins_entity_test.go`:

```go
func TestEntityTp_SameCell_UpdatesPosition(t *testing.T) {
	c, dispatcher := setupTestCluster(t, 1)
	registerEntityCommands(c)
	registerKindForTest(c, "test_dummy")
	netID := spawnTestEntityViaCmd(t, dispatcher, "test_dummy", 10, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatcher.Invoke(ctx, testCaller(), "entity.tp",
		fmt.Sprintf("%d 100 200", netID))
	if err != nil {
		t.Fatalf("entity.tp: %v", err)
	}
	got := res.PerTarget[0].Result.(entityTpResult)
	if got.NewWorldX != 100 || got.NewWorldY != 200 {
		t.Fatalf("post-TP world pos = (%g,%g), want (100,200)", got.NewWorldX, got.NewWorldY)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/universe/ -run TestEntityTp -v
```

Expected: FAIL — `entity.tp` not registered.

- [ ] **Step 3: Add the command**

In `pkg/universe/builtins_entity.go`:

```go
type entityTpArgs struct {
	NetID uint32  `cmd:"help=entity network ID"`
	X     float32 `cmd:"help=destination world X"`
	Y     float32 `cmd:"help=destination world Y"`
}

type entityTpResult struct {
	NetID      uint32
	Kind       string
	PrevWorldX float32
	PrevWorldY float32
	NewWorldX  float32
	NewWorldY  float32
	HostID     string
}
```

In `registerEntityCommands`, after `entity.list`:

```go
if err := reg.Register(cmdsys.Command{
	Verb:        "entity.tp",
	Capability:  "entity.tp",
	Description: "teleport an entity to absolute world coordinates",
	Route:       cmdsys.RouteEntityOwner,
	Args:        entityTpArgs{},
	Result:      entityTpResult{},
	Handler:     entityTpHandler(coord),
}); err != nil {
	return fmt.Errorf("entity.tp: %w", err)
}
```

Handler:

```go
func entityTpHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(entityTpArgs)
		for _, cell := range coord.Cells {
			res, err := cmdsys.OnLoop(ctx, cell.Engine, func() (entityTpResult, error) {
				e, ok := cell.Stage.EntityByNetID(args.NetID)
				if !ok {
					return entityTpResult{}, nil
				}
				pos := cell.Stage.PositionMap().Get(e)
				prevX := float32(cell.Cell.X)*coords.CellSize + pos.X
				prevY := float32(cell.Cell.Y)*coords.CellSize + pos.Y
				if err := cell.Stage.MoveEntityTo(e, args.X, args.Y, MoveBypassCooldown()); err != nil {
					return entityTpResult{}, err
				}
				return entityTpResult{
					NetID:      args.NetID,
					Kind:       coord.KindNameOf(e, cell.Stage),
					PrevWorldX: prevX,
					PrevWorldY: prevY,
					NewWorldX:  args.X,
					NewWorldY:  args.Y,
					HostID:     coord.HostID(),
				}, nil
			})
			if err != nil {
				return nil, err
			}
			if res.NetID != 0 {
				return res, nil
			}
		}
		return nil, fmt.Errorf("entity %d not found on this host", args.NetID)
	}
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./pkg/universe/ -run TestEntityTp -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_entity.go pkg/universe/builtins_entity_test.go
git commit -m "feat(universe): add engine entity.tp command"
```

### Task 15: Build `player.tp` (online + offline branches)

**Files:**

- Create: `pkg/universe/builtins_player.go`.
- Test: `pkg/universe/builtins_player_test.go`.

- [ ] **Step 1: Write the failing tests**

Create `pkg/universe/builtins_player_test.go`:

```go
package universe

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/zenion/mmokit/pkg/coords"
)

func TestPlayerTp_Online_CrossCell(t *testing.T) {
	c, dispatcher := setupTestCluster(t, 4) // 2x2 cells
	registerPlayerCommands(c)
	connectTestPlayer(t, c, "alice", "cell_0_0")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	farX := float32(coords.CellSize)*1.5 // inside cell_1_0 territory
	_, err := dispatcher.Invoke(ctx, testCaller(), "player.tp",
		fmt.Sprintf("alice %g %g", farX, float32(50)))
	if err != nil {
		t.Fatalf("player.tp: %v", err)
	}

	// Allow the cluster a few ticks to commit the handoff.
	advanceCluster(t, c, 5)

	got := c.ActiveUserHost("alice") // host stays the same in single-host test cluster
	if got == "" {
		t.Fatalf("session lost")
	}
	pos := readPlayerWorldPos(t, c, "alice")
	if pos.X < float32(coords.CellSize) || pos.X > float32(coords.CellSize)*2 {
		t.Fatalf("player did not land in cell_1_0; pos = %+v", pos)
	}
}

func TestPlayerTp_Offline_UpdatesPlayerData(t *testing.T) {
	c, dispatcher := setupTestCluster(t, 1)
	registerPlayerCommands(c)
	repo := newTestPlayerRepo(t, c)
	repo.SeedOffline("bob", 0, 0, 0, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dispatcher.Invoke(ctx, testCaller(), "player.tp",
		"bob 100 200"); err != nil {
		t.Fatalf("player.tp: %v", err)
	}
	pd := repo.Get("bob")
	if pd.CellX != 0 || pd.CellY != 0 || pd.X != 100 || pd.Y != 200 {
		t.Fatalf("PlayerData after offline TP = %+v", pd)
	}
}
```

(The cluster + player + repo helpers `setupTestCluster`, `connectTestPlayer`, `advanceCluster`, `readPlayerWorldPos`, `newTestPlayerRepo` follow the existing patterns in `pkg/universe/s6_gateway_test.go` and `pkg/universe/cmdsys_meshcontrol_test.go`. If a concrete helper isn't there, build one — keep it small.)

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/universe/ -run TestPlayerTp -v
```

Expected: FAIL — `registerPlayerCommands` undefined.

- [ ] **Step 3: Implement `player.tp`**

Create `pkg/universe/builtins_player.go`:

```go
package universe

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/coords"
)

type playerTpArgs struct {
	Username string  `cmd:"help=target username,complete=players"`
	X        float32 `cmd:"help=destination world X"`
	Y        float32 `cmd:"help=destination world Y"`
}

type playerTpResult struct {
	Username   string
	Status     string // "online" or "offline"
	PrevWorldX float32
	PrevWorldY float32
	NewWorldX  float32
	NewWorldY  float32
}

// registerPlayerCommands registers all player.* engine commands on the
// dispatcher's registry. Called from Process.RegisterBuiltins.
func registerPlayerCommands(coord *Process) error {
	reg := coord.CommandRegistry()
	if err := reg.Register(cmdsys.Command{
		Verb:        "player.tp",
		Capability:  "player.tp",
		Description: "teleport a player to absolute world coordinates (online or offline)",
		Route:       cmdsys.RoutePlayerHomeOrOwner,
		Args:        playerTpArgs{},
		Result:      playerTpResult{},
		Handler:     playerTpHandler(coord),
	}); err != nil {
		return fmt.Errorf("player.tp: %w", err)
	}
	return nil
}

func playerTpHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(playerTpArgs)
		username := strings.ToLower(args.Username)
		target := ResolvePlayerTarget(env, username)

		if target.Online != nil && target.Stage != nil {
			return cmdsys.OnLoop(ctx, target.Stage.Engine(), func() (playerTpResult, error) {
				e := target.Online.Entity
				pos := target.Stage.PositionMap().Get(e)
				prevX := float32(target.Stage.Cell().X)*coords.CellSize + pos.X
				prevY := float32(target.Stage.Cell().Y)*coords.CellSize + pos.Y
				if err := target.Stage.MoveEntityTo(e, args.X, args.Y, MoveBypassCooldown()); err != nil {
					return playerTpResult{}, err
				}
				return playerTpResult{
					Username:   username,
					Status:     "online",
					PrevWorldX: prevX,
					PrevWorldY: prevY,
					NewWorldX:  args.X,
					NewWorldY:  args.Y,
				}, nil
			})
		}
		if target.Offline != nil {
			cellX := int32(args.X / coords.CellSize)
			cellY := int32(args.Y / coords.CellSize)
			localX := args.X - float32(cellX)*coords.CellSize
			localY := args.Y - float32(cellY)*coords.CellSize
			prevX := float32(target.Offline.GetCellX())*coords.CellSize + target.Offline.GetX()
			prevY := float32(target.Offline.GetCellY())*coords.CellSize + target.Offline.GetY()
			target.Offline.SetCell(cellX, cellY)
			target.Offline.SetPosition(localX, localY)
			target.DirtyMark()
			return playerTpResult{
				Username:   username,
				Status:     "offline",
				PrevWorldX: prevX,
				PrevWorldY: prevY,
				NewWorldX:  args.X,
				NewWorldY:  args.Y,
			}, nil
		}
		return nil, fmt.Errorf("player %q not found", username)
	}
}
```

`Stage.Engine()` and `Stage.Cell()` need to exist. Discover via `grep -n "func (b \*Stage)\|func (s \*Stage)" pkg/universe/stage.go`. If the engine isn't exposed already, add a simple accessor.

- [ ] **Step 4: Run to verify the tests pass**

```bash
go test ./pkg/universe/ -run TestPlayerTp -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_player.go pkg/universe/builtins_player_test.go pkg/universe/stage.go
git commit -m "feat(universe): add engine player.tp command (online + offline branches)"
```

### Task 16: Add `player.info`

**Files:**

- Modify: `pkg/universe/builtins_player.go`.
- Test: `pkg/universe/builtins_player_test.go`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/builtins_player_test.go`:

```go
func TestPlayerInfo_OnlineReturnsLiveWorldPos(t *testing.T) {
	c, dispatcher := setupTestCluster(t, 1)
	registerPlayerCommands(c)
	connectTestPlayer(t, c, "alice", "cell_0_0")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatcher.Invoke(ctx, testCaller(), "player.info", "alice")
	if err != nil {
		t.Fatalf("player.info: %v", err)
	}
	got := res.PerTarget[0].Result.(playerInfoResult)
	if got.Username != "alice" || got.Status != "online" {
		t.Fatalf("info = %+v", got)
	}
	if got.WorldX == 0 && got.WorldY == 0 {
		t.Fatalf("expected non-zero spawn position; got %+v", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/universe/ -run TestPlayerInfo -v
```

Expected: FAIL — `player.info` not registered.

- [ ] **Step 3: Add the command**

In `pkg/universe/builtins_player.go`:

```go
type playerInfoArgs struct {
	Username string `cmd:"help=username,complete=players"`
}

type playerInfoResult struct {
	Username    string
	Status      string // "online <hostID>" or "offline"
	HostID      string
	CellID      string
	WorldX      float32
	WorldY      float32
	LastLoginRF string // RFC3339 or "never"
}
```

In `registerPlayerCommands`, after `player.tp`:

```go
if err := reg.Register(cmdsys.Command{
	Verb:        "player.info",
	Capability:  "player.info",
	Description: "show player identity, position, and last-login (online + offline)",
	Route:       cmdsys.RoutePlayerHomeOrOwner,
	Args:        playerInfoArgs{},
	Result:      playerInfoResult{},
	Handler:     playerInfoHandler(coord),
}); err != nil {
	return fmt.Errorf("player.info: %w", err)
}
```

Handler:

```go
func playerInfoHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(playerInfoArgs)
		username := strings.ToLower(args.Username)
		target := ResolvePlayerTarget(env, username)

		if target.Online != nil && target.Stage != nil {
			return cmdsys.OnLoop(ctx, target.Stage.Engine(), func() (playerInfoResult, error) {
				e := target.Online.Entity
				pos := target.Stage.PositionMap().Get(e)
				cellX := target.Stage.Cell().X
				cellY := target.Stage.Cell().Y
				return playerInfoResult{
					Username: username,
					Status:   "online",
					HostID:   coord.HostID(),
					CellID:   target.Stage.CellID(),
					WorldX:   float32(cellX)*coords.CellSize + pos.X,
					WorldY:   float32(cellY)*coords.CellSize + pos.Y,
				}, nil
			})
		}
		if target.Offline != nil {
			return playerInfoResult{
				Username: username,
				Status:   "offline",
				WorldX: float32(target.Offline.GetCellX())*coords.CellSize +
					target.Offline.GetX(),
				WorldY: float32(target.Offline.GetCellY())*coords.CellSize +
					target.Offline.GetY(),
			}, nil
		}
		return nil, fmt.Errorf("player %q not found", username)
	}
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./pkg/universe/ -run TestPlayerInfo -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_player.go pkg/universe/builtins_player_test.go
git commit -m "feat(universe): add engine player.info command"
```

### Task 17: Add `player.tpto`

**Files:**

- Modify: `pkg/universe/builtins_player.go`.
- Test: `pkg/universe/builtins_player_test.go`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/builtins_player_test.go`:

```go
func TestPlayerTpto_OnlineTarget_LandsAdjacent(t *testing.T) {
	c, dispatcher := setupTestCluster(t, 4)
	registerPlayerCommands(c)
	connectTestPlayer(t, c, "alice", "cell_0_0")
	connectTestPlayer(t, c, "bob", "cell_1_1")
	advanceCluster(t, c, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dispatcher.Invoke(ctx, testCaller(), "player.tpto",
		"alice bob"); err != nil {
		t.Fatalf("player.tpto: %v", err)
	}
	advanceCluster(t, c, 5)
	alicePos := readPlayerWorldPos(t, c, "alice")
	bobPos := readPlayerWorldPos(t, c, "bob")
	dist := distance(alicePos, bobPos)
	if dist > 200 || dist < 1 {
		t.Fatalf("alice landed %g from bob; want roughly 150 ± offset", dist)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/universe/ -run TestPlayerTpto -v
```

Expected: FAIL.

- [ ] **Step 3: Add the command**

In `pkg/universe/builtins_player.go`:

```go
type playerTptoArgs struct {
	Username string `cmd:"help=player to teleport,complete=players"`
	Target   string `cmd:"help=destination username,complete=players"`
}

type playerTptoResult struct {
	Username   string
	Target     string
	NewWorldX  float32
	NewWorldY  float32
}
```

In `registerPlayerCommands`, after `player.info`:

```go
if err := reg.Register(cmdsys.Command{
	Verb:        "player.tpto",
	Capability:  "player.tpto",
	Description: "teleport <username> next to <target_username>",
	Route:       cmdsys.RoutePlayerHomeOrOwner,
	Args:        playerTptoArgs{},
	Result:      playerTptoResult{},
	Handler:     playerTptoHandler(coord),
}); err != nil {
	return fmt.Errorf("player.tpto: %w", err)
}
```

Handler:

```go
func playerTptoHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(playerTptoArgs)
		// Resolve target's world position via player.info dispatched
		// internally — covers online + offline targets uniformly.
		infoResp, err := coord.InvokeInternal(ctx, env, "player.info",
			playerInfoArgs{Username: args.Target})
		if err != nil {
			return nil, fmt.Errorf("player.tpto: target lookup failed: %w", err)
		}
		var targetWorldX, targetWorldY float32
		for _, tr := range infoResp.PerTarget {
			if tr.OK {
				if pi, ok := tr.Result.(playerInfoResult); ok {
					targetWorldX, targetWorldY = pi.WorldX, pi.WorldY
					break
				}
			}
		}
		if targetWorldX == 0 && targetWorldY == 0 {
			return nil, fmt.Errorf("player.tpto: target %q has no resolvable position", args.Target)
		}

		// Apply small random offset so the source doesn't stack on the target.
		const offsetDist = 150.0
		angle := rand.Float64() * 2 * math.Pi
		nx := targetWorldX + float32(math.Cos(angle)*offsetDist)
		ny := targetWorldY + float32(math.Sin(angle)*offsetDist)

		// Now move the source via player.tp internally — handles online +
		// offline source uniformly.
		_, err = coord.InvokeInternal(ctx, env, "player.tp",
			playerTpArgs{Username: args.Username, X: nx, Y: ny})
		if err != nil {
			return nil, err
		}
		return playerTptoResult{
			Username:  strings.ToLower(args.Username),
			Target:    strings.ToLower(args.Target),
			NewWorldX: nx,
			NewWorldY: ny,
		}, nil
	}
}
```

`coord.InvokeInternal` is the existing `cmdsys.Dispatcher.InvokeInternal` accessor. If `*Process` doesn't expose it directly today, add a thin wrapper:

```go
func (c *Process) InvokeInternal(ctx context.Context, parent *cmdsys.Env, verb string, args any) (cmdsys.Result, error) {
	return c.dispatcher.InvokeInternal(ctx, parent, verb, args)
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./pkg/universe/ -run TestPlayerTpto -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_player.go pkg/universe/builtins_player_test.go pkg/universe/coordinator.go
git commit -m "feat(universe): add engine player.tpto via internal player.info+player.tp dispatch"
```

### Task 18: Add `player.list`

**Files:**

- Modify: `pkg/universe/builtins_player.go`.
- Test: `pkg/universe/builtins_player_test.go`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/builtins_player_test.go`:

```go
func TestPlayerList_OnlineOnly(t *testing.T) {
	c, dispatcher := setupTestCluster(t, 1)
	registerPlayerCommands(c)
	connectTestPlayer(t, c, "alice", "cell_0_0")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatcher.Invoke(ctx, testCaller(), "player.list", "")
	if err != nil {
		t.Fatalf("player.list: %v", err)
	}
	got := res.PerTarget[0].Result.(playerListResult)
	if len(got.Players) != 1 || got.Players[0].Username != "alice" {
		t.Fatalf("player.list = %+v", got.Players)
	}
}

func TestPlayerList_All_MergesOnlineAndOffline(t *testing.T) {
	c, dispatcher := setupTestCluster(t, 1)
	registerPlayerCommands(c)
	repo := newTestPlayerRepo(t, c)
	repo.SeedOffline("offline_user", 0, 0, 0, 0)
	connectTestPlayer(t, c, "alice", "cell_0_0")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := dispatcher.Invoke(ctx, testCaller(), "player.list", "--all")
	if err != nil {
		t.Fatalf("player.list --all: %v", err)
	}
	got := res.PerTarget[0].Result.(playerListResult)
	if len(got.Players) < 2 {
		t.Fatalf("expected ≥2 rows; got %+v", got.Players)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/universe/ -run TestPlayerList -v
```

Expected: FAIL — `player.list` not registered.

- [ ] **Step 3: Add the command**

In `pkg/universe/builtins_player.go`:

```go
type playerListArgs struct {
	All bool `cmd:"optional,name=all,help=include offline players (requires DB)"`
}

type playerListRow struct {
	Username string
	Status   string
	HostID   string
	CellID   string
}

type playerListResult struct {
	Players []playerListRow `cmd:"table"`
}
```

In `registerPlayerCommands`, after `player.tpto`:

```go
if err := reg.Register(cmdsys.Command{
	Verb:        "player.list",
	Capability:  "player.list",
	Description: "list active sessions cluster-wide; --all merges with offline DB",
	Route:       cmdsys.RouteCoordinator,
	Args:        playerListArgs{},
	Result:      playerListResult{},
	Handler:     playerListHandler(coord),
}); err != nil {
	return fmt.Errorf("player.list: %w", err)
}
```

Handler:

```go
func playerListHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(playerListArgs)
		online := coord.ActiveUsers() // map[username]hostID
		out := make(map[string]playerListRow, len(online))
		for username, hostID := range online {
			out[username] = playerListRow{
				Username: username,
				Status:   "online",
				HostID:   hostID,
			}
		}
		if args.All {
			dbHost := coord.PickDBHost()
			if dbHost == "" {
				return nil, fmt.Errorf("--all requires a DB-bearing host; none registered")
			}
			// Fan-out a `player.list_offline` (Hidden) verb to that host so
			// the coord pane can stay in one TraceID.
			res, err := coord.InvokeInternal(ctx, env, "player.list_offline", struct{}{})
			if err != nil {
				return nil, fmt.Errorf("player.list --all offline fetch failed: %w", err)
			}
			for _, tr := range res.PerTarget {
				if !tr.OK {
					continue
				}
				lr, ok := tr.Result.(playerListResult)
				if !ok {
					continue
				}
				for _, row := range lr.Players {
					if existing, has := out[row.Username]; !has {
						out[row.Username] = row
					} else {
						_ = existing // online wins
					}
				}
			}
		}
		rows := make([]playerListRow, 0, len(out))
		for _, r := range out {
			rows = append(rows, r)
		}
		return playerListResult{Players: rows}, nil
	}
}
```

Now register the hidden helper (in the same `registerPlayerCommands`):

```go
if err := reg.Register(cmdsys.Command{
	Verb:        "player.list_offline",
	Capability:  "player.list_offline",
	Description: "internal: enumerate persisted (offline) players from the local DB",
	Route:       cmdsys.RouteLocal,
	Args:        struct{}{},
	Result:      playerListResult{},
	Hidden:      true,
	Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		coord.mu.RLock()
		loc := coord.playerDataLocator
		coord.mu.RUnlock()
		if loc == nil {
			return playerListResult{}, nil
		}
		return loc.ListOffline(), nil // see step 3a
	},
}); err != nil {
	return fmt.Errorf("player.list_offline: %w", err)
}
```

- [ ] **Step 3a: Add `ListOffline` to PlayerDataLocator**

In `pkg/universe/player_target.go`, extend the interface:

```go
type PlayerDataLocator interface {
	Get(username string) (PlayerDataAccessor, func(), bool)
	ListOffline() playerListResult
}
```

(Or, more cleanly, return `[]PlayerDataAccessor` and let the handler shape the rows. Pick whichever feels less coupled. The test imposes no preference.)

In `internal/game/playerdb.go`'s `repoLocator`, implement it:

```go
func (l repoLocator) ListOffline() universe.PlayerListResult {
	all := l.repo.All()
	out := make([]universe.PlayerListRow, 0, len(all))
	for _, pd := range all {
		out = append(out, universe.PlayerListRow{
			Username: pd.Username,
			Status:   "offline",
		})
	}
	return universe.PlayerListResult{Players: out}
}
```

(Promote `playerListResult` and `playerListRow` to exported `PlayerListResult` / `PlayerListRow` so game code can return them. Update the cmdsys command result type accordingly.)

- [ ] **Step 4: Run to verify both tests pass**

```bash
go test ./pkg/universe/ -run TestPlayerList -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_player.go pkg/universe/builtins_player_test.go pkg/universe/player_target.go internal/game/playerdb.go
git commit -m "feat(universe): add engine player.list (online + --all merge with DB)"
```

### Task 19: Add `player.kick`

**Files:**

- Modify: `pkg/universe/builtins_player.go`.
- Test: `pkg/universe/builtins_player_test.go`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/builtins_player_test.go`:

```go
func TestPlayerKick_RemovesSession(t *testing.T) {
	c, dispatcher := setupTestCluster(t, 1)
	registerPlayerCommands(c)
	connectTestPlayer(t, c, "alice", "cell_0_0")
	advanceCluster(t, c, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dispatcher.Invoke(ctx, testCaller(), "player.kick", "alice"); err != nil {
		t.Fatalf("player.kick: %v", err)
	}
	advanceCluster(t, c, 3)
	if got := c.ActiveUserHost("alice"); got != "" {
		t.Fatalf("session still online: %s", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./pkg/universe/ -run TestPlayerKick -v
```

Expected: FAIL.

- [ ] **Step 3: Add the command**

In `pkg/universe/builtins_player.go`:

```go
type playerKickArgs struct {
	Username string `cmd:"help=target username,complete=players"`
}

type playerKickResult struct {
	Username string
	ConnID   uint32
}
```

In `registerPlayerCommands`, after `player.list_offline`:

```go
if err := reg.Register(cmdsys.Command{
	Verb:        "player.kick",
	Capability:  "player.kick",
	Description: "force-disconnect an online player",
	Route:       cmdsys.RoutePlayerOwner,
	Args:        playerKickArgs{},
	Result:      playerKickResult{},
	Handler:     playerKickHandler(coord),
}); err != nil {
	return fmt.Errorf("player.kick: %w", err)
}
```

Handler:

```go
func playerKickHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(playerKickArgs)
		username := strings.ToLower(args.Username)
		target := ResolvePlayerTarget(env, username)
		if target.Online == nil || target.Stage == nil {
			return nil, fmt.Errorf("player %q not online on this host", username)
		}
		return cmdsys.OnLoop(ctx, target.Stage.Engine(), func() (playerKickResult, error) {
			connID := target.Online.ConnID
			target.Stage.Players().Remove(target.Online)
			if remover, ok := target.Stage.Engine().ConnMgr.(interface{ Remove(uint32) }); ok {
				remover.Remove(connID)
			}
			return playerKickResult{Username: username, ConnID: connID}, nil
		})
	}
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./pkg/universe/ -run TestPlayerKick -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/builtins_player.go pkg/universe/builtins_player_test.go
git commit -m "feat(universe): add engine player.kick command"
```

### Task 20: Wire engine builtin registration into `RegisterBuiltins`

**Files:**

- Modify: `pkg/universe/coordinator.go` near line 2103 (the existing `BuiltinOpts` setup).
- Test: existing tests in `pkg/universe/cmdsys_meshcontrol_test.go` should now see entity/player verbs.

- [ ] **Step 1: Find the existing master registration**

```bash
grep -n "registerCellCommands\|registerPerfCommands\|registerLoadCommand" pkg/universe/coordinator.go
```

Note the master function (likely `Process.registerBuiltinCommands` or similar; if the registration spreads across `builtins_*.go` files via init-style calls, instead find the function that calls them).

- [ ] **Step 2: Hook the new registrars in**

Add the calls right next to the existing builtin registrations:

```go
if err := registerEntityCommands(c); err != nil {
	return fmt.Errorf("registerEntityCommands: %w", err)
}
if err := registerPlayerCommands(c); err != nil {
	return fmt.Errorf("registerPlayerCommands: %w", err)
}
```

- [ ] **Step 3: Build and run all tests**

```bash
just build
go test ./pkg/cmdsys/... ./pkg/universe/... ./pkg/mmokit/... ./internal/game/...
```

Expected: PASS, build OK.

- [ ] **Step 4: Smoke check via the running server**

```bash
just run &
sleep 3
# In another terminal: 
# echo "entity.spawn npc 5 0 0" | nc -q1 localhost <admin-port>
# (or use the project's existing console-injection harness — check Justfile)
```

(If no easy injection harness exists, skip step 4 — the integration tests cover the wire path.)

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "feat(universe): register entity.* and player.* engine commands at startup"
```

---

## Phase 3: Space-Game Cleanup

After Phase 3, only the 5 game-specific commands remain in `internal/game/commands/`, all rewritten on top of `mmokit.ResolvePlayerTarget`. The duplicates and broken commands are gone.

### Task 21: Delete subsumed commands

**Files:**

- Delete: `internal/game/commands/tp.go`, `tpto.go`, `kick.go`, `players.go`, `say.go`, `npcs.go`, `spawnnpcs.go`, `resolver.go`.
- Modify: `internal/game/commands/registry.go`.

- [ ] **Step 1: Confirm no test currently references the deleted files**

```bash
grep -l "registerTp\|registerTpTo\|registerKick\|registerPlayers\|registerSay\|registerNPCs\|registerSpawnNPCs\|Resolver{" internal/game/ 2>/dev/null
```

Note any matches; they need updates in this task.

- [ ] **Step 2: Delete the files**

```bash
git rm internal/game/commands/tp.go internal/game/commands/tpto.go internal/game/commands/kick.go internal/game/commands/players.go internal/game/commands/say.go internal/game/commands/npcs.go internal/game/commands/spawnnpcs.go internal/game/commands/resolver.go
```

- [ ] **Step 3: Trim `registry.go`**

Replace `internal/game/commands/registry.go` with:

```go
package commands

import (
	"fmt"

	"github.com/zenion/mmokit/internal/game"
	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// RegisterAll registers space-game admin commands. Generic
// player/entity/cell/cluster commands are registered by the engine
// via mmokit.RegisterBuiltins; only game-specific verbs live here.
func RegisterAll(reg *cmdsys.Registry, coord *mmokit.Process, playerDB *game.PlayerRepo, cfg *game.GameConfig) error {
	cfgPtr := &cfg
	funcs := []func() error{
		func() error { return registerDamage(reg, coord) },
		func() error { return registerHeal(reg, coord) },
		func() error { return registerKill(reg, coord) },
		func() error { return registerGive(reg, coord) },
		func() error { return registerCurrency(reg, coord, playerDB, cfgPtr) },
	}
	for _, fn := range funcs {
		if err := fn(); err != nil {
			return fmt.Errorf("commands.RegisterAll: %w", err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Build to find compile errors**

```bash
just build
```

Expected: errors from `damage.go`, `heal.go`, `kill.go`, `give.go`, `currency.go` referencing the deleted `Resolver` / `ExecOnLoop`. These get fixed in the next tasks.

- [ ] **Step 5: Don't commit yet** — the build is broken. Move on to Task 22.

### Task 22: Rewrite `damage.go`, `heal.go`, `kill.go` on `ResolvePlayerTarget`

**Files:**

- Modify: `internal/game/commands/damage.go`, `heal.go`, `kill.go`.

- [ ] **Step 1: Rewrite `damage.go`**

Replace the file:

```go
package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/mmokit"
)

type DamageArgs struct {
	Username string  `cmd:"help=target username,complete=players"`
	Amount   float32 `cmd:"help=damage amount"`
}

type DamageResult struct {
	Target string
	Dealt  float32
	HP     string
}

func registerDamage(reg *cmdsys.Registry, _ *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.damage",
		Capability:  "player.damage",
		Description: "deal damage to a player's entity (online only)",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        DamageArgs{},
		Result:      DamageResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(DamageArgs)
			username := strings.ToLower(args.Username)
			target := mmokit.ResolvePlayerTarget(env, username)
			if target.Online == nil || target.Stage == nil {
				return nil, fmt.Errorf("player %q not online on this host", username)
			}
			gw := game.UnwrapGameWorld(target.Stage.World())
			if gw == nil {
				return nil, fmt.Errorf("player.damage: not a game-world cell")
			}
			return mmokit.OnLoop(ctx, target.Stage.Engine(), func() (DamageResult, error) {
				e := target.Online.Entity
				if !gw.C.Health.HasAll(e) {
					return DamageResult{}, fmt.Errorf("entity has no health")
				}
				dealt := gw.ApplyDamage(e, args.Amount, 0)
				h := gw.C.Health.Get(e)
				return DamageResult{
					Target: username,
					Dealt:  dealt,
					HP:     fmt.Sprintf("%.0f/%.0f", h.Current, h.Max),
				}, nil
			})
		},
	})
}
```

(`game.UnwrapGameWorld` exists per `internal/game/commands/resolver.go:30`. `Stage.World()` is added in Task 6 if not already present — it returns the `GameWorld` interface implementation backing the cell. If absent, add it as `func (b *Stage) World() any { return b.world }` and have universe set `b.world` from `cell.World` at Stage construction.)

- [ ] **Step 2: Rewrite `heal.go`**

Replace the file with the same shape (minus the damage math):

```go
package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenion/mmokit/internal/game"
	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/mmokit"
)

type HealArgs struct {
	Username string `cmd:"help=target username,complete=players"`
}

type HealResult struct {
	Target string
	OK     bool
}

func registerHeal(reg *cmdsys.Registry, _ *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.heal",
		Capability:  "player.heal",
		Description: "restore full HP and shield (online only)",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        HealArgs{},
		Result:      HealResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(HealArgs)
			username := strings.ToLower(args.Username)
			target := mmokit.ResolvePlayerTarget(env, username)
			if target.Online == nil || target.Stage == nil {
				return nil, fmt.Errorf("player %q not online on this host", username)
			}
			gw := game.UnwrapGameWorld(target.Stage.World())
			if gw == nil {
				return nil, fmt.Errorf("player.heal: not a game-world cell")
			}
			return mmokit.OnLoop(ctx, target.Stage.Engine(), func() (HealResult, error) {
				e := target.Online.Entity
				if gw.C.Health.HasAll(e) {
					h := gw.C.Health.Get(e)
					h.Current = h.Max
				}
				if gw.C.Shield.HasAll(e) {
					s := gw.C.Shield.Get(e)
					s.Current = s.Max
				}
				return HealResult{Target: username, OK: true}, nil
			})
		},
	})
}
```

- [ ] **Step 3: Rewrite `kill.go`**

```go
package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenion/mmokit/internal/game"
	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/mmokit"
)

type KillArgs struct {
	Username string `cmd:"help=target username,complete=players"`
}

type KillResult struct {
	Target string
	OK     bool
}

func registerKill(reg *cmdsys.Registry, _ *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.kill",
		Capability:  "player.kill",
		Description: "instantly kill a player's entity (online only)",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        KillArgs{},
		Result:      KillResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(KillArgs)
			username := strings.ToLower(args.Username)
			target := mmokit.ResolvePlayerTarget(env, username)
			if target.Online == nil || target.Stage == nil {
				return nil, fmt.Errorf("player %q not online on this host", username)
			}
			gw := game.UnwrapGameWorld(target.Stage.World())
			if gw == nil {
				return nil, fmt.Errorf("player.kill: not a game-world cell")
			}
			return mmokit.OnLoop(ctx, target.Stage.Engine(), func() (KillResult, error) {
				gw.MarkPlayerDeath(target.Online.Entity, 0)
				return KillResult{Target: username, OK: true}, nil
			})
		},
	})
}
```

- [ ] **Step 4: Build to verify compile**

```bash
just build
```

Expected: still some errors in `give.go` and `currency.go` (those are next). The three damage/heal/kill files should compile.

- [ ] **Step 5: Don't commit yet** — finish give/currency in Task 23.

### Task 23: Rewrite `give.go` and `currency.go` with offline branches

**Files:**

- Modify: `internal/game/commands/give.go`, `currency.go`.

- [ ] **Step 1: Rewrite `give.go`**

```go
package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenion/mmokit/internal/game"
	"github.com/zenion/mmokit/internal/item"
	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/mmokit"
)

type GiveArgs struct {
	Username string `cmd:"help=target username,complete=players"`
	Item     string `cmd:"help=resource name (ore/crystal/gas/metal)"`
	Qty      int32  `cmd:"help=quantity to give"`
}

type GiveResult struct {
	Target string
	Item   string
	Given  int32
	Added  int32
	Status string // "online" or "offline"
}

func registerGive(reg *cmdsys.Registry, _ *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.give",
		Capability:  "player.give",
		Description: "add a resource item to a player's cargo (online or offline)",
		Route:       cmdsys.RoutePlayerHomeOrOwner,
		Args:        GiveArgs{},
		Result:      GiveResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(GiveArgs)
			username := strings.ToLower(args.Username)
			itemID, ok := resolveResource(args.Item)
			if !ok {
				return nil, fmt.Errorf("unknown resource %q", args.Item)
			}
			target := mmokit.ResolvePlayerTarget(env, username)

			if target.Online != nil && target.Stage != nil {
				gw := game.UnwrapGameWorld(target.Stage.World())
				if gw == nil {
					return nil, fmt.Errorf("player.give: not a game-world cell")
				}
				return mmokit.OnLoop(ctx, target.Stage.Engine(), func() (GiveResult, error) {
					e := target.Online.Entity
					if !gw.C.Inventory.HasAll(e) {
						return GiveResult{}, fmt.Errorf("player has no inventory")
					}
					added := gw.C.Inventory.Get(e).AddItem(itemID, args.Qty)
					name := itemNameOrPlaceholder(itemID)
					return GiveResult{
						Target: username, Item: name, Given: args.Qty, Added: added, Status: "online",
					}, nil
				})
			}
			if target.Offline != nil {
				pdAccessor := target.Offline
				pd, ok := pdAccessor.(*game.PlayerData)
				if !ok {
					return nil, fmt.Errorf("player.give: offline accessor type mismatch")
				}
				if pd.Cargo == nil {
					pd.Cargo = make(map[uint32]int32)
				}
				pd.Cargo[itemID] += args.Qty
				target.DirtyMark()
				name := itemNameOrPlaceholder(itemID)
				return GiveResult{
					Target: username, Item: name, Given: args.Qty, Added: args.Qty, Status: "offline",
				}, nil
			}
			return nil, fmt.Errorf("player %q not found", username)
		},
	})
}

// resolveResource maps short resource names to item IDs.
func resolveResource(input string) (uint32, bool) {
	input = strings.ToLower(input)
	for _, def := range item.All() {
		if def.Category == item.CategoryResource && strings.HasPrefix(strings.ToLower(def.Name), input) {
			return def.ID, true
		}
	}
	return 0, false
}

func itemNameOrPlaceholder(id uint32) string {
	if def := item.Get(id); def != nil {
		return def.Name
	}
	return fmt.Sprintf("item#%d", id)
}
```

- [ ] **Step 2: Rewrite `currency.go`**

```go
package commands

import (
	"context"
	"fmt"
	"strings"

	gamepb "github.com/zenion/mmokit/gen/go/gamepb"
	"github.com/zenion/mmokit/internal/game"
	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/mmokit"
)

type CurrencyArgs struct {
	Username   string `cmd:"help=target username,complete=players"`
	Amount     int64  `cmd:"help=new balance amount"`
	CurrencyID uint32 `cmd:"optional,help=currency ID (default: settlement currency)"`
}

type CurrencyResult struct {
	Target     string
	CurrencyID uint32
	NewBalance int64
	Status     string
}

func registerCurrency(reg *cmdsys.Registry, _ *mmokit.Process, playerDB *game.PlayerRepo, cfg **game.GameConfig) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.currency",
		Capability:  "player.currency",
		Description: "set a player's currency balance (online or offline)",
		Route:       cmdsys.RoutePlayerHomeOrOwner,
		Args:        CurrencyArgs{},
		Result:      CurrencyResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(CurrencyArgs)
			username := strings.ToLower(args.Username)
			curID := args.CurrencyID
			if curID == 0 && cfg != nil && *cfg != nil {
				curID = (*cfg).SettlementCurrencyID
			}
			target := mmokit.ResolvePlayerTarget(env, username)

			pdata := playerDB.GetOrCreate(username)
			if pdata.Currencies == nil {
				pdata.Currencies = make(map[uint32]int64)
			}
			pdata.Currencies[curID] = args.Amount
			playerDB.MarkDirty(username)

			if target.Online != nil && target.Stage != nil {
				_, _ = mmokit.OnLoop(ctx, target.Stage.Engine(), func() (struct{}, error) {
					sendBankContentsAdmin(target.Stage, target.Online.ConnID, pdata, *cfg)
					return struct{}{}, nil
				})
				return CurrencyResult{
					Target: username, CurrencyID: curID, NewBalance: args.Amount, Status: "online",
				}, nil
			}
			if target.Offline != nil {
				return CurrencyResult{
					Target: username, CurrencyID: curID, NewBalance: args.Amount, Status: "offline",
				}, nil
			}
			return nil, fmt.Errorf("player %q not found", username)
		},
	})
}

// sendBankContentsAdmin sends a BankContentsMsg to a player.
// Lifted from the previous currency.go — unchanged except for stage import.
func sendBankContentsAdmin(stage *mmokit.Stage, connID uint32, pdata *game.PlayerData, cfg *game.GameConfig) {
	gw := game.UnwrapGameWorld(stage.World())
	if gw == nil {
		return
	}
	var items []*gamepb.InventoryItem
	for id, qty := range pdata.Bank {
		if qty > 0 {
			items = append(items, &gamepb.InventoryItem{ItemId: id, Quantity: qty})
		}
	}
	var currencies []*gamepb.CurrencyBalance
	for cid, bal := range pdata.Currencies {
		if bal != 0 {
			currencies = append(currencies, &gamepb.CurrencyBalance{CurrencyId: cid, Balance: bal})
		}
	}
	var bankMaxMass float32
	if cfg != nil {
		bankMaxMass = cfg.BankMaxMass
	}
	gw.ServerEvents().Send(gw.Engine().ConnMgr, connID, uint32(gamepb.GameServerEventCode_GSE_BANK_CONTENTS), &gamepb.BankContentsMsg{
		Items:      items,
		TotalMass:  pdata.BankTotalMass(),
		MaxMass:    bankMaxMass,
		Currencies: currencies,
	})
}
```

- [ ] **Step 3: Build the whole project**

```bash
just build
```

Expected: builds clean.

- [ ] **Step 4: Run all tests**

```bash
go test ./pkg/cmdsys/... ./pkg/universe/... ./pkg/mmokit/... ./internal/game/...
```

Expected: PASS.

- [ ] **Step 5: Commit Phase-3 changes as one logical unit**

```bash
git add internal/game/commands/
git commit -m "refactor(game): rewrite damage/heal/kill/give/currency atop ResolvePlayerTarget; delete subsumed commands"
```

---

## Phase 4: Verification

### Task 24: Confirm 4node-basic still works end-to-end

**Files:** none modified.

- [ ] **Step 1: Run the example smoke test**

```bash
go test ./examples/4node-basic/... -v
go test ./pkg/universe/ -run "TestS6|TestS7" -v
```

Expected: PASS.

- [ ] **Step 2: Manual smoke — single-process server**

```bash
just run &
SERVER_PID=$!
sleep 3
# In another terminal: connect a client and TP across cells via the console.
# Stop the server when done:
kill $SERVER_PID
```

(The console commands `entity.spawn npc 5 0 0`, `player.tp <user> 100 100`, `player.list --all` should all work from any pane.)

- [ ] **Step 3: Manual smoke — distributed**

```bash
just distributed &
SERVER_PID=$!
sleep 5
# In each tmux pane, exercise: cell.list, entity.spawn, player.tp, player.tpto.
kill $SERVER_PID
```

- [ ] **Step 4: Commit (no-op if no changes)**

If smoke testing surfaced any tweaks, commit them; otherwise no commit needed.

### Task 25: Add the `just smoke-commands` target

**Files:**

- Modify: `justfile` (top-level).

- [ ] **Step 1: Add the recipe**

In the project's top-level `justfile`, add:

```just
# scripted multi-process smoke for the new entity/player commands
smoke-commands:
    #!/usr/bin/env bash
    set -euo pipefail
    just distributed >/tmp/distributed.log 2>&1 &
    PID=$!
    trap "kill $PID" EXIT
    sleep 5
    # exercise entity.spawn / player.tp / player.list — adjust admin URL to match
    curl -fsSL -X POST localhost:9101/commands/entity.spawn \
        -H "content-type: application/json" \
        -d '{"args":{"Kind":"npc","Count":5,"X":0,"Y":0}}' >/dev/null
    curl -fsSL -X POST localhost:9101/commands/player.list \
        -H "content-type: application/json" \
        -d '{"args":{}}' >/dev/null
    echo "smoke-commands ok"
```

(Adjust the admin URL/port to match `cfg.AdminListen`. If the existing distributed setup uses different paths, tweak accordingly.)

- [ ] **Step 2: Run it**

```bash
just smoke-commands
```

Expected: prints `smoke-commands ok` and exits 0.

- [ ] **Step 3: Commit**

```bash
git add justfile
git commit -m "chore: add just smoke-commands target for distributed-mode regression"
```

### Task 26: Final cluster-wide regression run

**Files:** none modified.

- [ ] **Step 1: Full test sweep**

```bash
go test ./... -count=1
just build
```

Expected: all PASS, build OK.

- [ ] **Step 2: Confirm git log shape**

```bash
git log --oneline main..HEAD
```

Expected: a clean sequence of feature/refactor commits matching the task list, no merge commits or noise.

- [ ] **Step 3: Done — push when ready**

```bash
# Solo-dev workflow: merge to main directly per repo convention.
git push origin main
```

---

## Self-Review Notes

Spec coverage check:

- ✅ `Stage.MoveEntityTo` (Task 9)
- ✅ Generalized `HandoffDriver` (Task 8)
- ✅ Cross-host transport reuses `meshpb.Handoff` (no new task — verified by Task 8 + cluster smoke)
- ✅ Player session follow (no new task — existing PlayerMigrated path covered by integration tests in Tasks 15/17)
- ✅ Failure modes (covered in handler error paths — Tasks 9, 14, 15)
- ✅ `RoutePlayerHomeOrOwner` (Tasks 1, 5)
- ✅ `Coordinator.PickDBHost` + `HasPlayerDB` advertisement (Task 4)
- ✅ `ResolvePlayerTarget` + `LocalProcess` marker (Tasks 2, 6, 7)
- ✅ `entity.*` engine commands (Tasks 11, 12, 13, 14)
- ✅ `player.*` engine commands (Tasks 15, 16, 17, 18, 19)
- ✅ Game-side rewrites (Tasks 22, 23)
- ✅ Subsumed-command deletes (Task 21)
- ✅ Bootstrap wiring (Task 20)
- ✅ Audit/observability (no new task — falls out of routing through existing Dispatcher)
- ✅ Capabilities (no new task — capabilities = verb name on each `Register`)
- ✅ Migration plan (Phase 1 → 4 cleanly mergeable)
- ✅ Testing — unit tests in Tasks 1, 2, 4, 5, 6, 7, 8, 9, 10; integration tests across 11–19; manual smoke in Task 24, 25, 26

Type-consistency check: types `MoveOpt`, `MoveBypassCooldown`, `PlayerTarget`, `PlayerDataAccessor`, `PlayerDataLocator`, `RoutePlayerHomeOrOwner` are introduced in Phase 1 with the names used by every later task.

---

Plan complete and saved to `docs/superpowers/plans/2026-04-29-distributed-commands-and-entity-tp.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
