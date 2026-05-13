# OnResolveSpawn Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `Config.DefaultSpawn` + `Process.SetSpawnResolver` with a single `Process.OnResolveSpawn(func(*PlayerSession) Location)` registration. No engine-side static fallback; if no callback is registered, default to the center of cell `(0,0)` computed from `Config.CellSize`.

**Architecture:** The spawn pipeline shape is unchanged: gateway → resolveSpawn → `CellAtPosition` → `PlayerAssignment` to cell → cell fires `OnPlayerJoin`. The change is signature/wiring only: the resolver takes `*PlayerSession` and returns a single `coords.Location` (no `ok` bool). The Config field disappears; the cached `defaultSpawn` field on Gateway is replaced by a small helper that returns the cell-(0,0) center on demand. Reconnect routing is untouched.

**Tech Stack:** Go 1.x, `pkg/universe`, `pkg/mmokit`, `pkg/coords`, `pkg/engine`.

**Spec reference:** [docs/superpowers/specs/2026-05-13-onresolve-spawn-design.md](../specs/2026-05-13-onresolve-spawn-design.md)

---

## File Map

**Modified:**
- `pkg/universe/coordinator.go` — delete `Config.DefaultSpawn` field + its doc; drop the line copying it into `Gateway.defaultSpawn` (line ~2006).
- `pkg/universe/spawn_resolver.go` — change `SpawnResolver` signature; rename `SetSpawnResolver` → `OnResolveSpawn`; rewrite `resolveSpawn` (no more `defaultSpawn` branch; compute cell-(0,0) center when resolver nil).
- `pkg/universe/gateway.go` — delete `defaultSpawn coords.Location` field; remove the field assignment line.
- `pkg/universe/cell_bridge_impl.go` — `RequestRespawn` calls the resolver via `*PlayerSession`; no `DefaultSpawn` fallback.
- `pkg/universe/mesh_control_server.go` — `handleInboundResolveSpawn` calls the resolver with a `*PlayerSession` built from the RPC fields (UserID + Username).
- `pkg/mmokit/mmokit.go` — fix the `SpawnResolver` type alias doc; add a forwarder for `OnResolveSpawn` if not auto-available via the `*Process` re-export.
- `pkg/universe/universe_test.go` — fixture helper switches to registering a resolver; `TestBridge_RequestRespawn` updates assertion path.
- `pkg/universe/gateway_test.go` — comment-only updates.
- `examples/4node-basic/main.go` — replace `DefaultSpawn:` Config entry with `process.OnResolveSpawn(...)` registration.
- `examples/4node-basic/mesh_e2e_test.go` — two callsites get the same treatment.
- `cmd/server/main.go` — collapse `coordCfg.DefaultSpawn = ...` + existing `coordinator.SetSpawnResolver(...)` into one `coordinator.OnResolveSpawn(...)` call.
- `internal/game/entity_ship.go` — stale comment cleanup.
- `internal/game/entity_station.go` — stale comment cleanup.
- `CLAUDE.md` — update the 4node-basic paragraph that references `Config.DefaultSpawn`.

**Created:** none.

---

## Task 1: Core API change in pkg/universe — signature, registration, fallback

**Files:**
- Modify: `pkg/universe/spawn_resolver.go`
- Modify: `pkg/universe/coordinator.go` (delete `Config.DefaultSpawn`; drop `Gateway.defaultSpawn` field assignment)
- Modify: `pkg/universe/gateway.go` (delete `defaultSpawn` field)
- Modify: `pkg/universe/cell_bridge_impl.go` (`RequestRespawn`)
- Modify: `pkg/universe/mesh_control_server.go` (`handleInboundResolveSpawn`)
- Modify: `pkg/universe/universe_test.go` (fixture helper + `TestBridge_RequestRespawn`)
- Modify: `pkg/universe/gateway_test.go` (comment refs)

This task bundles every change inside `pkg/universe` because the signature change touches several files at once — splitting it would leave the package uncompilable mid-task. Verification runs at the end.

- [ ] **Step 1: Update `SpawnResolver` type and `OnResolveSpawn` registration**

File: `pkg/universe/spawn_resolver.go`

Replace lines 15–33 (the godoc block + type + `SetSpawnResolver` method) with:

```go
// SpawnResolver decides the world-space spawn location for a player session
// at login (or post-death respawn). Called once per login on the process that
// owns the coordinator. The returned Location is fed into CellAtPosition so
// the gateway can route the PlayerAssignment to the owning cell.
//
// The resolver is topology-blind: it returns world-space coords only. The
// gateway calls CellAtPosition(loc.X, loc.Y) at dispatch time to pick the
// current owning cell, so split/merge between resolver call and dispatch is
// handled naturally.
//
// Games own the entire decision: DB lookup, faction-based zones, group
// respawn — whatever logic the game needs lives inside this callback. If no
// resolver is registered, the engine defaults to the center of cell (0,0)
// derived from Config.CellSize.
type SpawnResolver func(session *engine.PlayerSession) coords.Location

// OnResolveSpawn registers the spawn resolver on the coordinator. Must be
// called before Start(). At most one resolver is registered; later calls
// overwrite earlier ones.
func (c *Process) OnResolveSpawn(r SpawnResolver) {
	c.mu.Lock()
	c.spawnResolver = r
	c.mu.Unlock()
}
```

Add the import for `engine` at the top of the file if not already present:

```go
import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)
```

- [ ] **Step 2: Rewrite `resolveSpawn` to drop `defaultSpawn`**

File: `pkg/universe/spawn_resolver.go`

Replace the `resolveSpawn` function (lines 61–106) with:

```go
// resolveSpawn returns the spawn location for the (userID, username) pair and
// (in standalone mode) any reconnect-routing override discovered via the
// coordinator's activeUsers index.
//
//  1. Embedded coordinator with resolver → call inline (zero RPC overhead).
//     Reconnect detection happens later in dispatchPlayerAssignment.
//  2. Standalone gateway → send ResolveSpawn RPC with 2s deadline. The
//     coordinator runs the resolver and may piggyback reconnect info.
//  3. No resolver registered or RPC fails → fall back to the engine default
//     (center of cell (0,0), computed from cfg.CellSize).
func (g *Gateway) resolveSpawn(ctx context.Context, userID uuid.UUID, username string) spawnResolution {
	if g.coord != nil {
		g.coord.mu.RLock()
		resolver := g.coord.spawnResolver
		g.coord.mu.RUnlock()
		if resolver != nil {
			session := &engine.PlayerSession{UserID: userID, Username: username}
			return spawnResolution{Location: resolver(session)}
		}
		return spawnResolution{Location: defaultSpawnLocation(g.coord.cfg.CellSize)}
	}

	if g.controlClient != nil {
		rpcCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		resp, err := g.spawnOrch.send(rpcCtx, g.controlClient, g.id, userID, username)
		if err == nil && resp != nil {
			out := spawnResolution{
				IsReconnect:  resp.IsReconnect,
				TargetHostID: resp.TargetHostId,
				TargetCellID: resp.TargetCellId,
			}
			if resp.Ok {
				out.Location = coords.Location{X: resp.WorldX, Y: resp.WorldY}
			} else {
				out.Location = defaultSpawnLocation(g.cfg.CellSize)
			}
			return out
		}
		if err != nil {
			g.log.Log(CatNetConn, "gateway: resolveSpawn RPC failed for %s: %v — using engine default", username, err)
		}
	}
	return spawnResolution{Location: defaultSpawnLocation(g.cfg.CellSize)}
}

// defaultSpawnLocation returns the center of cell (0,0) for the given cell
// size. Used when no SpawnResolver is registered (or the standalone RPC
// fails). Topology-blind — the gateway still routes via CellAtPosition.
func defaultSpawnLocation(cellSize float32) coords.Location {
	return coords.Location{X: cellSize / 2, Y: cellSize / 2}
}
```

Note the change to the standalone branch: `g.defaultSpawn` is gone; we compute the default from `g.cfg.CellSize`.

- [ ] **Step 3: Drop `defaultSpawn` field from `Gateway` struct**

File: `pkg/universe/gateway.go`

Delete lines 69–72 (the four-line block):

```go
	// defaultSpawn is the fallback spawn position when no resolver is registered
	// or the resolver returns ok=false. Copied from Config.DefaultSpawn at
	// Gateway construction time (standalone mode) or read via coord.cfg (embedded).
	defaultSpawn coords.Location
```

There's no other reference to `g.defaultSpawn` after Step 2 lands.

- [ ] **Step 4: Delete `Config.DefaultSpawn` field**

File: `pkg/universe/coordinator.go`

Delete lines 218–222 (the godoc + field):

```go
	// DefaultSpawn is the world-space login/respawn location used when no
	// SpawnResolver is registered or the resolver returns ok=false.
	// Topology-independent: the gateway resolves the current owning cell
	// via CellAtPosition at dispatch time.
	DefaultSpawn coords.Location
```

Also delete the line that copies it into Gateway (around line 2006):

```go
		defaultSpawn: cfg.DefaultSpawn,
```

The Gateway struct literal at line ~1995 + the second one at line ~1685 should both no longer reference `defaultSpawn`. (The earlier struct literal didn't set the field, so only the standalone-gateway literal needs the edit.)

- [ ] **Step 5: Update `cell_bridge_impl.go::RequestRespawn`**

File: `pkg/universe/cell_bridge_impl.go`

Find the function starting at line 176. Replace lines 178–188 (the resolver+fallback block) with:

```go
	b.coord.mu.RLock()
	resolver := b.coord.spawnResolver
	cellSize := b.coord.cfg.CellSize
	b.coord.mu.RUnlock()

	var loc coords.Location
	if resolver != nil {
		// Respawn: we don't have UserID at this layer (the cell bridge takes
		// only connID + username). The resolver still gets a session with the
		// fields available; games that need UserID for respawn should add it
		// at a higher level.
		session := &engine.PlayerSession{ConnID: connID, Username: username}
		loc = resolver(session)
	} else {
		loc = defaultSpawnLocation(cellSize)
	}
```

Add the `engine` import if not already present:

```go
"github.com/zenion/mmoserver/pkg/engine"
```

- [ ] **Step 6: Update `mesh_control_server.go::handleInboundResolveSpawn`**

File: `pkg/universe/mesh_control_server.go`

Replace lines 771–798 (the function body) with:

```go
func (s *meshControlServer) handleInboundResolveSpawn(gatewayID string, req *meshpb.ResolveSpawn) {
	s.coord.mu.RLock()
	resolver := s.coord.spawnResolver
	cellSize := s.coord.cfg.CellSize
	s.coord.mu.RUnlock()

	resp := &meshpb.SpawnResolved{RequestId: req.RequestId, Ok: true}
	var session engine.PlayerSession
	session.Username = req.Username
	if req.UserId != "" {
		if uid, err := uuid.Parse(req.UserId); err == nil {
			session.UserID = uid
		}
	}

	var loc coords.Location
	if resolver != nil {
		loc = resolver(&session)
	} else {
		loc = defaultSpawnLocation(cellSize)
	}
	resp.WorldX = loc.X
	resp.WorldY = loc.Y

	if req.UserId != "" {
		if uid, err := uuid.Parse(req.UserId); err == nil && uid != uuid.Nil {
			s.coord.applyResolveSpawnReconnect(uid, resp)
		}
	}

	if err := s.sendCoordMessageToGateway(gatewayID, &meshpb.CoordMessage{
		Msg: &meshpb.CoordMessage_SpawnResolved{SpawnResolved: resp},
	}); err != nil {
		s.log.Log(CatMeshMsg, "coordinator: SpawnResolved to gateway %s failed: %v", gatewayID, err)
	}
}
```

Confirm the `engine` import is already present (the file should import `pkg/engine` already; if not, add it). The previous code's `resp.Error = "no spawn resolver registered on coordinator"` branch is gone — the engine default is a valid resolution.

- [ ] **Step 7: Update `universe_test.go` fixture and `TestBridge_RequestRespawn`**

File: `pkg/universe/universe_test.go`

Replace lines 42–52 (the `newTestCoordinator` tail) with:

```go
	c := New(cfg)
	c.Build()
	// Tests rely on the engine default (center of cell (0,0)) — no resolver
	// is registered. Any test that needs a specific location calls
	// c.OnResolveSpawn(...) explicitly.
	return c
}
```

Replace the `TestBridge_RequestRespawn` body (lines 530–565). The new test registers a resolver instead of setting a Config field:

```go
func TestBridge_RequestRespawn(t *testing.T) {
	grid := Config{CellsX: 2, CellsY: 1}
	c := newTestCoordinator(grid)

	targetID := string(CellID{X: 0, Y: 0}.MeshID())
	targetCell, err := ParseCellID(targetID)
	if err != nil {
		t.Fatalf("ParseCellID: %v", err)
	}
	minX, minY, maxX, maxY := targetCell.WorldBounds(coords.CellSize)
	want := coords.Location{X: (minX + maxX) / 2, Y: (minY + maxY) / 2}
	c.OnResolveSpawn(func(_ *engine.PlayerSession) coords.Location { return want })

	otherID := string(CellID{X: 1, Y: 0}.MeshID())
	other := c.Cells[MeshCellID(otherID)]
	target := c.Cells[MeshCellID(targetID)]

	other.Bridge.RequestRespawn(77, "charlie")

	select {
	case msg := <-target.Inbox:
		if msg.Type != MsgSpawnTransfer {
			t.Fatalf("expected MsgSpawnTransfer, got %d", msg.Type)
		}
		if msg.Spawn.ConnID != 77 || msg.Spawn.Username != "charlie" {
			t.Fatalf("unexpected spawn: %+v", msg.Spawn)
		}
		if msg.Spawn.SpawnLocation != want {
			t.Fatalf("SpawnLocation = %+v, want %+v",
				msg.Spawn.SpawnLocation, want)
		}
	default:
		t.Fatal("no message in target node inbox")
	}
}
```

Add the `engine` import to this test file if missing:

```go
"github.com/zenion/mmoserver/pkg/engine"
```

- [ ] **Step 8: Add two new tests for the resolver / default behavior**

File: `pkg/universe/universe_test.go`

Append these two tests at the end of the file:

```go
// TestOnResolveSpawn_DefaultLocation verifies the engine default (center of
// cell (0,0)) is used when no SpawnResolver is registered.
func TestOnResolveSpawn_DefaultLocation(t *testing.T) {
	c := newTestCoordinator(Config{CellsX: 2, CellsY: 2})

	got := defaultSpawnLocation(c.cfg.CellSize)
	want := coords.Location{X: c.cfg.CellSize / 2, Y: c.cfg.CellSize / 2}
	if got != want {
		t.Fatalf("defaultSpawnLocation(%v) = %+v, want %+v", c.cfg.CellSize, got, want)
	}
}

// TestOnResolveSpawn_ReceivesSession verifies the registered resolver is
// called with a PlayerSession carrying UserID + Username at login. Drives
// the RequestRespawn path (the simplest call site exercising the resolver).
func TestOnResolveSpawn_ReceivesSession(t *testing.T) {
	c := newTestCoordinator(Config{CellsX: 1, CellsY: 1})

	var gotSession *engine.PlayerSession
	want := coords.Location{X: 100, Y: 200}
	c.OnResolveSpawn(func(s *engine.PlayerSession) coords.Location {
		gotSession = s
		return want
	})

	cellID := CellID{X: 0, Y: 0}.MeshID()
	cell := c.Cells[cellID]
	cell.Bridge.RequestRespawn(42, "alice")

	select {
	case msg := <-cell.Inbox:
		if msg.Spawn.SpawnLocation != want {
			t.Fatalf("SpawnLocation = %+v, want %+v", msg.Spawn.SpawnLocation, want)
		}
		if gotSession == nil {
			t.Fatal("resolver was not invoked")
		}
		if gotSession.Username != "alice" {
			t.Fatalf("session.Username = %q, want alice", gotSession.Username)
		}
		if gotSession.ConnID != 42 {
			t.Fatalf("session.ConnID = %d, want 42", gotSession.ConnID)
		}
	default:
		t.Fatal("no message in cell inbox")
	}
}
```

- [ ] **Step 9: Update gateway_test.go comment-only references**

File: `pkg/universe/gateway_test.go`

Line 38, replace `// DefaultSpawn from examples/4node-basic/main.go post-Task-13:` with:

```go
		// Spawn from examples/4node-basic/main.go (registered via OnResolveSpawn):
```

Line 82, replace `// The original bug: DefaultSpawn from mmokit.WorldCenterOfCell(0, 0)` with:

```go
		// The original bug: spawn from mmokit.WorldCenterOfCell(0, 0)
```

No functional change; comments only.

- [ ] **Step 10: Run vet + the universe package tests**

Run:

```bash
just build
```

Expected: build succeeds, `bin/server` produced. (`just build` runs `go vet ./...` under the hood and refuses to build into the root — this is the canonical compile check per `CLAUDE.md`.)

Then:

```bash
go test ./pkg/universe/...
```

Expected: all tests pass, including the two new ones.

If any callsite outside `pkg/universe` blocks the build, that's a callsite covered by a later task. The build break should clear after the remaining tasks (mmokit facade, examples, space game) land. If you see breaks inside `pkg/universe` itself, fix them now before committing.

- [ ] **Step 11: Commit**

```bash
git add pkg/universe/spawn_resolver.go pkg/universe/coordinator.go pkg/universe/gateway.go pkg/universe/cell_bridge_impl.go pkg/universe/mesh_control_server.go pkg/universe/universe_test.go pkg/universe/gateway_test.go
git commit -m "$(cat <<'EOF'
refactor(universe): collapse DefaultSpawn + SetSpawnResolver into OnResolveSpawn

Single game-owned callback (*PlayerSession → Location) decides the entire
spawn location. No static Config field; engine defaults to the center of
cell (0,0) when no resolver is registered.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

Note: this commit leaves the wider workspace in a broken state (examples and `cmd/server` still reference the removed symbols). That's intentional — the remaining tasks land file-by-file. The `pkg/universe` package itself compiles and its tests pass.

---

## Task 2: Update mmokit facade

**Files:**
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Update the `SpawnResolver` doc + add `OnResolveSpawn` facade**

File: `pkg/mmokit/mmokit.go`

Replace lines 429–433:

```go
// SpawnResolver decides the world-space spawn location for a player session
// at login (or post-death respawn). Games own the full decision (DB lookup,
// faction-based zones, fallback). Register via Process.OnResolveSpawn. If no
// resolver is registered, the engine defaults to the center of cell (0,0).
type SpawnResolver = universe.SpawnResolver
```

The `OnResolveSpawn` method is on `*universe.Process`, which is type-aliased to `*mmokit.Process`, so it's automatically callable as `process.OnResolveSpawn(...)`. No extra wrapper needed. Verify by grepping for the existing `OnPlayerJoin` alias pattern — same shape.

- [ ] **Step 2: Verify the facade builds standalone**

Run:

```bash
go vet ./pkg/mmokit/...
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/mmokit.go
git commit -m "$(cat <<'EOF'
refactor(mmokit): update SpawnResolver doc for OnResolveSpawn API

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Update 4node-basic example + e2e test

**Files:**
- Modify: `examples/4node-basic/main.go`
- Modify: `examples/4node-basic/mesh_e2e_test.go`

- [ ] **Step 1: Replace `DefaultSpawn` Config entry with `OnResolveSpawn` registration**

File: `examples/4node-basic/main.go`

Remove line 38 from the `mmokit.New` Config literal:

```go
		DefaultSpawn:     mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85},
```

After the `process := mmokit.New(...)` block (before the first `process.OnConsoleReady` call at line 41), insert:

```go
	process.OnResolveSpawn(func(s *mmokit.PlayerSession) mmokit.Location {
		return mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85}
	})
```

`mmokit.PlayerSession` is already aliased to `engine.PlayerSession` in `pkg/mmokit/mmokit.go:179`, and `mmokit.Process` is a type alias for `universe.Process` (line 293), so `process.OnResolveSpawn(...)` is callable directly without facade work.

- [ ] **Step 2: Replace `DefaultSpawn` in mesh_e2e_test.go (coordinator literal)**

File: `examples/4node-basic/mesh_e2e_test.go`

Line 177 — delete the `DefaultSpawn:` field from the coordinator's `mmokit.Config` literal.

After `coord.Build()` (line 179), insert a resolver registration:

```go
	coord.OnResolveSpawn(func(s *mmokit.PlayerSession) mmokit.Location {
		return mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85}
	})
```

Wait — `OnResolveSpawn` must be called before `Build()` per the godoc in Task 1 Step 1. Move the registration BEFORE `coord.Build()`. Concretely: after `mmokit.New(...)` returns and is assigned to `coord`, but before `coord.Build()`.

- [ ] **Step 3: Replace `DefaultSpawn` in mesh_e2e_test.go (host loop)**

File: `examples/4node-basic/mesh_e2e_test.go`

Line 206 — delete `DefaultSpawn:` from the host's Config literal inside the `for _, hid := range hostIDs` loop.

Host processes don't run the resolver (only the coord does), so no `OnResolveSpawn` call is needed on the host. Just remove the field.

- [ ] **Step 4: Run the 4node-basic tests**

Run:

```bash
go test ./examples/4node-basic/...
```

Expected: all tests pass. If `mesh_e2e_test.go` fails because the resolver registration happens at the wrong lifecycle point, re-verify Step 2 placement.

- [ ] **Step 5: Commit**

```bash
git add examples/4node-basic/main.go examples/4node-basic/mesh_e2e_test.go
git commit -m "$(cat <<'EOF'
refactor(4node-basic): migrate to OnResolveSpawn

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Update cmd/server (space game)

**Files:**
- Modify: `cmd/server/main.go`

The space game today has TWO knobs: a `coordCfg.DefaultSpawn` (line 142) for new players AND a `coordinator.SetSpawnResolver(...)` (line 319) for DB-saved positions. The refactor collapses them into one resolver where the fallback logic lives inside the callback.

- [ ] **Step 1: Capture the default spawn formula as a local function**

File: `cmd/server/main.go`

Delete the `coordCfg.DefaultSpawn = coords.Location{...}` block (lines 138–145). In its place, leave a comment placeholder that the resolver registration below uses the same formula. The actual code change happens in Step 2.

- [ ] **Step 2: Rewrite the `SetSpawnResolver` block as `OnResolveSpawn` with the inline fallback**

File: `cmd/server/main.go`

Replace lines 319–329 (the `coordinator.SetSpawnResolver(...)` block):

```go
		coordinator.OnResolveSpawn(func(s *mmokit.PlayerSession) coords.Location {
			pdata := playerDB.Get(s.Username)
			if pdata != nil && pdata.HasSave {
				return coords.Location{
					X: float32(pdata.CellX)*coords.CellSize + pdata.X,
					Y: float32(pdata.CellY)*coords.CellSize + pdata.Y,
					// Facing + Tag not yet persisted; leave zero.
				}
			}
			// New player (no saved location). Spawn 30 units east of the
			// trade station — outside DockRange (13.3) so the player sees
			// the station and decides to dock instead of being auto-pulled.
			return coords.Location{
				X: float32(gameCfg.StationCell.CellX)*coords.CellSize + game.StationLocalX + 30,
				Y: float32(gameCfg.StationCell.CellY)*coords.CellSize + game.StationLocalY,
			}
		})
```

Verify the `mmokit` import alias and `coords` import are present at the top of the file (they should be — both are used elsewhere). `mmokit.PlayerSession` must be available (added to the facade in Task 3 if not already there).

- [ ] **Step 3: Build the space-game binary**

Run:

```bash
just build
```

Expected: success. If `gameCfg` or `playerDB` aren't in scope inside the resolver closure, double-check that the closure is declared inside the `if needsGameState` block where both are defined.

- [ ] **Step 4: Commit**

```bash
git add cmd/server/main.go
git commit -m "$(cat <<'EOF'
refactor(server): migrate space game to OnResolveSpawn with inline fallback

Collapses DefaultSpawn + SetSpawnResolver: DB-saved position is the primary
return; the new-player station-offset spawn moves into the same callback as
the explicit fallback branch.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Stale comment cleanup

**Files:**
- Modify: `internal/game/entity_ship.go`
- Modify: `internal/game/entity_station.go`
- Modify: `CLAUDE.md`

- [ ] **Step 1: entity_ship.go comment fix**

File: `internal/game/entity_ship.go`

Find line 69:

```go
		// Use gateway-resolved spawn (Config.DefaultSpawn for new players),
```

Replace with:

```go
		// Use gateway-resolved spawn (Process.OnResolveSpawn callback),
```

- [ ] **Step 2: entity_station.go comment fix**

File: `internal/game/entity_station.go`

Find line 21:

```go
// derive Config.DefaultSpawn from StationCell + this offset.
```

Replace with:

```go
// derive the resolver's new-player fallback from StationCell + this offset.
```

- [ ] **Step 3: CLAUDE.md update**

File: `CLAUDE.md`

Find the 4node-basic paragraph that says:

> Spawn position is pinned via `Config.DefaultSpawn = mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85}` in `main.go`; the game-side `OnEnter` hook calls `gw.SpawnAtLocation(s.SpawnLocation, ...)` so the entity lands where the gateway resolved.

Replace with:

> Spawn position is registered via `process.OnResolveSpawn(func(s *mmokit.PlayerSession) mmokit.Location { return mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85} })` in `main.go`; the game-side `OnEnter` hook calls `gw.SpawnAtLocation(s.SpawnLocation, ...)` so the entity lands where the resolver chose. When no resolver is registered, the engine defaults to the center of cell (0,0).

- [ ] **Step 4: Commit**

```bash
git add internal/game/entity_ship.go internal/game/entity_station.go CLAUDE.md
git commit -m "$(cat <<'EOF'
docs: refresh DefaultSpawn references for OnResolveSpawn

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Final verification

- [ ] **Step 1: Verify no stale references remain**

Run:

```bash
grep -rn "DefaultSpawn\|SetSpawnResolver" --include="*.go" --include="*.md" .
```

Expected: empty output. Any hit is a missed reference — fix it before continuing.

- [ ] **Step 2: Full build + vet**

Run:

```bash
just build
```

Expected: builds cleanly, produces `bin/server`.

- [ ] **Step 3: Full test suite**

Run:

```bash
go test ./...
```

Expected: all tests pass. Pay particular attention to:
- `pkg/universe/` — new tests + updated `TestBridge_RequestRespawn`.
- `examples/4node-basic/` — e2e tests with the new resolver.
- `internal/game/` — should be untouched functionally; comment-only changes shouldn't affect tests.

- [ ] **Step 4: lint-no-ark gate**

Run:

```bash
just lint-no-ark
```

Expected: clean. (No new ark imports were introduced by this refactor.)

- [ ] **Step 5: Manual smoke (optional but recommended)**

In one terminal:

```bash
cd examples/4node-basic && just dev
```

Connect a client (or use the web client at `http://localhost:8080`). Verify a fresh player spawns at `(CellSize * 0.85, CellSize * 0.85)` — the same corner of cell (0,0) the demo used before the refactor.

Stop the server when satisfied. (Don't leave a background process running per memory: "no leftover processes".)

- [ ] **Step 6: No additional commit**

Task 6 is verification only. If the previous tasks were committed cleanly, there's nothing more to commit here.
