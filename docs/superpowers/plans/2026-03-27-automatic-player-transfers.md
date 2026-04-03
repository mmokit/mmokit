# Automatic Player Transfers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the mmokit framework automatically handle player session management during cross-node entity transfers, so games get correct behavior for free without manual `RegisterPendingLogin` / `OnPlayerTransfer` boilerplate.

**Architecture:** Two framework-level changes: (1) the generic `BoundarySystem` detects player entities during transfer and cleans up the source node's session + updates coordinator routing, (2) `node.processMessage` creates a session on the destination node before calling `SpawnFromTransfer`, so the game's spawn code can find and wire the session. A `Username` field is added to `TransferFrame` to carry player identity. After both framework changes, redundant manual code is removed from the space game and slither.

**Tech Stack:** Go, Ark ECS, mmokit framework (`pkg/universe/`, `pkg/engine/`)

---

### Task 1: Add Username to TransferFrame

**Files:**
- Modify: `pkg/universe/transfer.go:14-174`
- Modify: `pkg/universe/world_base.go:212-243`

- [ ] **Step 1: Add Username field to TransferFrame struct**

In `pkg/universe/transfer.go`, add `Username` after `ConnID`:

```go
type TransferFrame struct {
	NetworkID  uint32
	EntityType uint8
	ConnID     uint32 // 0 for non-player entities
	Username   string // player username (empty for non-player entities)
	PosX, PosY float32
	// ... rest unchanged
}
```

- [ ] **Step 2: Update MarshalTransferFrame to encode Username**

Insert username encoding after the ConnID write (line 63). Username is length-prefixed with a uint8:

```go
// After: binary.LittleEndian.PutUint32(buf[off:], f.ConnID); off += 4
buf[off] = uint8(len(f.Username))
off++
copy(buf[off:], f.Username)
off += len(f.Username)
```

Update the header size calculation. The fixed header grows by 1 byte (username length prefix). The variable-length username bytes are added to `size`:

```go
const headerSize = 4 + 1 + 4 + 1 + 4 + 4 + 4 + 4 + 4 + 14 + 4 + 4 + 2 // 54

size := headerSize + len(f.Username)
```

Update the wire format comment to include `[1] UsernameLen` and `[N] Username` after ConnID.

- [ ] **Step 3: Update UnmarshalTransferFrame to decode Username**

After reading ConnID, read the username length and string:

```go
// After: f.ConnID = binary.LittleEndian.Uint32(data[off:]); off += 4
nameLen := int(data[off])
off++
if off+nameLen > len(data) {
	return nil, fmt.Errorf("transfer frame: truncated username")
}
f.Username = string(data[off : off+nameLen])
off += nameLen
```

Update the minimum-length check to use the new `headerSize` (54).

- [ ] **Step 4: Add PeekTransferPlayer helper**

Add a function that reads just ConnID and Username from transfer bytes without full deserialization. The node uses this to set up the session before calling `SpawnFromTransfer`:

```go
// PeekTransferPlayer extracts the ConnID and Username from raw transfer bytes
// without fully deserializing. Returns (0, "") for non-player entities.
func PeekTransferPlayer(data []byte) (connID uint32, username string) {
	// Layout: NetworkID[4] + EntityType[1] + ConnID[4] + UsernameLen[1] + Username[N]
	const connIDOffset = 4 + 1 // after NetworkID + EntityType
	if len(data) < connIDOffset+4+1 {
		return 0, ""
	}
	connID = binary.LittleEndian.Uint32(data[connIDOffset:])
	if connID == 0 {
		return 0, ""
	}
	nameLen := int(data[connIDOffset+4])
	nameStart := connIDOffset + 4 + 1
	if nameStart+nameLen > len(data) {
		return connID, ""
	}
	return connID, string(data[nameStart : nameStart+nameLen])
}
```

- [ ] **Step 5: Populate Username in SerializeEntityCore**

In `pkg/universe/world_base.go`, `SerializeEntityCore`, after setting ConnID, look up the username from the PlayerManager:

```go
if b.playerMap.HasAll(entity) {
	f.ConnID = b.playerMap.Get(entity).ConnID
	if f.ConnID != 0 {
		if s := b.eng.Players.ByConnID(f.ConnID); s != nil {
			f.Username = s.Username
		}
	}
}
```

- [ ] **Step 6: Verify compilation**

Run: `go vet ./...`
Expected: clean output, no errors.

- [ ] **Step 7: Commit**

```
feat(universe): add Username field to TransferFrame

Carries player identity during cross-node transfers so the destination
node can create a properly named session automatically.
```

---

### Task 2: Auto-register player session on destination node

**Files:**
- Modify: `pkg/universe/node.go:56-76`

- [ ] **Step 1: Pre-create session before SpawnFromTransfer**

In `node.processMessage`, `MsgTransfer` case, after the replica removal and before `SpawnFromTransfer`, peek at the transfer data. If it's a player entity, register a pending login so the game's `SpawnFromTransfer` can find the session via `ByConnID`:

```go
case MsgTransfer:
	if msg.Transfer == nil {
		return
	}
	// Remove any pre-existing replica with the same NetworkID
	if msg.TransferNetID != 0 {
		n.World.RemoveReplicaByNetID(msg.TransferNetID)
	}

	// Pre-create player session so SpawnFromTransfer can wire s.Entity.
	connID, username := PeekTransferPlayer(msg.Transfer)
	if connID != 0 {
		n.Engine.Players.RegisterPendingLogin(connID, username)
	}

	netID, _, err := n.World.SpawnFromTransfer(msg.Transfer)
	if err != nil {
		return
	}

	// Send arrival confirmation back to source node
	n.Bridge.SendArrivalConfirm(msg.FromNodeID, &ArrivalConfirmMsg{
		NetworkID: netID,
		ConnID:    connID,
	})
```

Note: `connID` from Peek replaces the returned connID from `SpawnFromTransfer` (they're the same value). The `_` discards the duplicate.

- [ ] **Step 2: Verify compilation**

Run: `go vet ./...`
Expected: clean output.

- [ ] **Step 3: Commit**

```
feat(universe): auto-register player session on transfer destination

The node now peeks at ConnID/Username in the transfer payload and calls
RegisterPendingLogin before SpawnFromTransfer, so games find the session
via ByConnID without manual boilerplate.
```

---

### Task 3: Auto-handle player transfer on source node (BoundarySystem)

**Files:**
- Modify: `pkg/universe/boundary_system.go:23-157`

- [ ] **Step 1: Add playerConnMap field to BoundarySystem**

```go
type BoundarySystem struct {
	world        *ecs.World
	bw           BoundaryWorld
	filter       *ecs.Filter2[component.Position, component.SectorCoord]
	playerMap    *ecs.Map1[component.PlayerConn]
}
```

- [ ] **Step 2: Initialize playerMap lazily in Update**

At the top of `Update`, where the filter is initialized, also initialize the player map:

```go
func (s *BoundarySystem) Update(dt float32) {
	if s.filter == nil {
		s.filter = ecs.NewFilter2[component.Position, component.SectorCoord](s.world).
			Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.TransferCooldown]())
		s.playerMap = ecs.NewMap1[component.PlayerConn](s.world)
	}
```

- [ ] **Step 3: After sending transfer, handle player session cleanup + routing**

In the transfer loop (after `SendTransfer` and `Ghost.Add`), add player handling. The BoundarySystem now checks for PlayerConn, transitions the session, and calls `OnPlayerTransfer`:

```go
s.bw.Bridge().SendTransfer(t.destNodeID, data, netID)

// Player transfer: clean up session on source node and update routing.
if s.playerMap.HasAll(t.entity) {
	playerConnID := s.playerMap.Get(t.entity).ConnID
	if playerConnID != 0 {
		if eng := s.bw.Engine(); eng != nil {
			if sess := eng.Players.ByConnID(playerConnID); sess != nil {
				eng.Players.Transition(sess, engine.StateTransferring)
				eng.Players.Remove(sess)
			}
		}
		s.bw.Bridge().OnPlayerTransfer(playerConnID, t.destNodeID)
	}
}
```

- [ ] **Step 4: Add Engine() to BoundaryWorld interface**

The BoundarySystem needs access to the PlayerManager via the Engine. Add `Engine()` to `BoundaryWorld`:

```go
type BoundaryWorld interface {
	SerializeEntity(entity ecs.Entity) ([]byte, error)
	Bridge() NodeBridge
	NodeID() string
	Sector() coords.SectorCoord
	GhostMap() *ecs.Map1[component.Ghost]
	Engine() *engine.Engine
}
```

`WorldBase` already has `func (b *WorldBase) Engine() *engine.Engine`, so it satisfies this automatically. The space game's `gameWorldAdapter` also embeds `WorldBase`, so it satisfies this too.

- [ ] **Step 5: Import engine package in boundary_system.go**

Add `"github.com/zenion/mmoserver/pkg/engine"` to the imports for `engine.StateTransferring`.

- [ ] **Step 6: Verify compilation**

Run: `go vet ./...`
Expected: clean output.

- [ ] **Step 7: Commit**

```
feat(universe): BoundarySystem auto-handles player session transfer

When a player entity crosses a sector boundary, the generic BoundarySystem
now cleans up the session on the source node and notifies the coordinator,
removing the need for games to do this manually.
```

---

### Task 4: Clean up space game — remove redundant transfer code

**Files:**
- Modify: `internal/system/sector_boundary.go:140-194`
- Modify: `internal/game/transfer.go:216-225`

- [ ] **Step 1: Remove manual OnPlayerTransfer + session cleanup from space game boundary system**

In `internal/system/sector_boundary.go`, remove the entire `if isPlayer` block (lines 180-193) that manually calls `Transition`, `Remove`, and `OnPlayerTransfer`. Also remove the `isPlayer`, `connID`, and `username` variables and their computation (lines 149-157) since they're no longer needed. The `username` was only used in the log message — update the log to remove `player=%s`:

Before (lines 149-193):
```go
isPlayer := s.gw.C.PlayerConn.HasAll(t.entity)
var connID uint32
var username string
if isPlayer {
	connID = s.gw.C.PlayerConn.Get(t.entity).ConnID
	if sess := s.gw.Players.ByConnID(connID); sess != nil {
		username = sess.Username
	}
}

s.gw.Log.Log(game.CatTransfer, "cross-node transfer: netID=%d type=%d dest=%s sector=(%d,%d) player=%s",
	payload.NetworkID, payload.EntityType, t.destNodeID, t.newSector.SX, t.newSector.SY, username)

// ... marshal, ghost, send ...

if isPlayer {
	if sess := s.gw.Players.ByConnID(connID); sess != nil {
		s.gw.Players.Transition(sess, mmokit.StateTransferring)
		s.gw.Players.Remove(sess)
	}
	_ = sec
	s.gw.Bridge.OnPlayerTransfer(connID, t.destNodeID)
}
```

After:
```go
s.gw.Log.Log(game.CatTransfer, "cross-node transfer: netID=%d type=%d dest=%s sector=(%d,%d)",
	payload.NetworkID, payload.EntityType, t.destNodeID, t.newSector.SX, t.newSector.SY)

// ... marshal, ghost, send — unchanged ...
// (player session cleanup + routing is now handled by BoundarySystem automatically)
```

- [ ] **Step 2: Remove RegisterPendingLogin from spawnShipFromTransfer**

In `internal/game/transfer.go`, the session registration (lines 220-225) is now handled by the framework. Remove the `RegisterPendingLogin` and `ByConnID` + entity assignment lines. Keep the game-specific sector change message and map data send:

Before (lines 220-225):
```go
if p.ConnID != 0 {
	gw.Players.RegisterPendingLogin(p.ConnID, p.Username)
	if s := gw.Players.ByConnID(p.ConnID); s != nil {
		s.Entity = entity
	}
	gw.Log.Log(CatTransfer, "ship transfer wired: conn=%d username=%s", p.ConnID, p.Username)
```

After:
```go
if p.ConnID != 0 {
	// Session is auto-registered by the framework (RegisterPendingLogin in node.processMessage).
	// Wire the entity to the pre-created session.
	if s := gw.Players.ByConnID(p.ConnID); s != nil {
		s.Entity = entity
	}
	gw.Log.Log(CatTransfer, "ship transfer wired: conn=%d username=%s", p.ConnID, p.Username)
```

- [ ] **Step 3: Verify compilation**

Run: `go vet ./...`
Expected: clean output.

- [ ] **Step 4: Commit**

```
refactor(game): remove manual player transfer boilerplate

Session registration and coordinator routing are now handled by the
framework automatically. The space game only needs to wire s.Entity
and send game-specific messages (sector change, map data).
```

---

### Task 5: Clean up slither — remove redundant transfer code

**Files:**
- Modify: `examples/slither/world.go:315-322`

- [ ] **Step 1: Simplify SpawnFromTransfer player handling**

The framework now handles `RegisterPendingLogin` and `OnPlayerTransfer`. Slither's `SpawnFromTransfer` just needs to wire `s.Entity` and send the spawned message:

Before (lines 315-322):
```go
// Re-register player if this is a player snake
if frame.ConnID != 0 {
	if s := gw.Engine().Players.ByConnID(frame.ConnID); s != nil {
		s.Entity = entity
	}
	gw.SendSpawnedMsg(frame.ConnID, frame.NetworkID) // Tell client about new sector
	log.Printf("[%s] player transfer received: connID=%d netID=%d", gw.NodeID(), frame.ConnID, frame.NetworkID)
}
```

After:
```go
// Wire entity to the pre-created session and notify the client.
// Session registration + coordinator routing are handled by the framework.
if frame.ConnID != 0 {
	if s := gw.Engine().Players.ByConnID(frame.ConnID); s != nil {
		s.Entity = entity
	}
	gw.SendSpawnedMsg(frame.ConnID, frame.NetworkID)
	log.Printf("[%s] player transfer received: connID=%d netID=%d", gw.NodeID(), frame.ConnID, frame.NetworkID)
}
```

This is a comment-only change — the code was already correct because the framework now creates the session before calling `SpawnFromTransfer`.

- [ ] **Step 2: Verify compilation**

Run: `go vet ./...`
Expected: clean output.

- [ ] **Step 3: Commit**

```
refactor(slither): rely on framework for player transfer session management
```

---

### Task 6: Run tests and verify end-to-end

**Files:**
- Test: `pkg/universe/universe_test.go`
- Test: `internal/game/transfer_test.go`

- [ ] **Step 1: Run existing universe tests**

Run: `go test ./pkg/universe/ -v`
Expected: all existing transfer/drain inbox tests pass.

- [ ] **Step 2: Run existing game transfer tests**

Run: `go test ./internal/game/ -v -run Transfer`
Expected: all existing transfer tests pass.

- [ ] **Step 3: Run full test suite**

Run: `go test ./... 2>&1 | tail -30`
Expected: all tests pass.

- [ ] **Step 4: Manual verification — run slither with multi-node grid**

Run: `cd examples/slither && go run . -grid 2`
Test: Connect via browser, move snake across sector boundary. Verify:
- No crash on transfer
- Snake continues moving on new node
- Head is at the front of the body (not lagging behind)
- Other snakes/bots remain visible

- [ ] **Step 5: Final commit if any fixes needed, then done**
