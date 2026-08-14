# Connection Proxy & DefaultNode Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the DefaultNode concept from mmokit's Coordinator and replace it with a connection proxy layer that processes logins at the coordinator level and routes authenticated players to game-determined target nodes.

**Architecture:** The Coordinator becomes a connection proxy — it owns all WebSocket connections, buffers them until login completes, then routes authenticated players to the appropriate node via a game-provided `PlayerRouter` callback. Login parsing moves from per-node `PlayerManager.processLogins()` to a coordinator-level `LoginService`. Session tracking (active users, disconnected users) lives on the Coordinator for global duplicate detection and reconnection routing.

**Tech Stack:** Go, protobuf, ECS (Ark), WebSocket

---

### Task 1: Create LoginService

**Files:**
- Create: `pkg/universe/login.go`

This is the new coordinator-level login processing service. It buffers pending connections and calls the game-provided login handler each tick.

- [ ] **Step 1: Create `pkg/universe/login.go`**

```go
package universe

import (
	"errors"
	"time"

	"github.com/zenion/mmokit/pkg/net"
)

// LoginHandler parses login messages and returns the username.
// Returns ErrLoginPending if no valid login message found yet.
// Returns other errors for rejected logins (error message sent to client).
type LoginHandler func(connID uint32, messages [][]byte) (username string, err error)

// PlayerRouter determines which node should host a player after login.
// Called with the authenticated username. Returns a nodeID.
type PlayerRouter func(username string) string

// loginService manages pre-node login processing on the coordinator.
type loginService struct {
	handler      LoginHandler
	onRejected   func(connID uint32, reason string)
	loginTimeout time.Duration
	pending      map[uint32]*pendingConn
}

type pendingConn struct {
	connID    uint32
	createdAt time.Time
}

func newLoginService(handler LoginHandler, timeout time.Duration) *loginService {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &loginService{
		handler:      handler,
		loginTimeout: timeout,
		pending:      make(map[uint32]*pendingConn),
	}
}

func (ls *loginService) addPending(connID uint32) {
	ls.pending[connID] = &pendingConn{
		connID:    connID,
		createdAt: time.Now(),
	}
}

func (ls *loginService) removePending(connID uint32) {
	delete(ls.pending, connID)
}

// loginResult is returned by processLogins for each successfully authenticated player.
type loginResult struct {
	connID   uint32
	username string
}

// processLogins drains input for all pending connections and attempts login.
// Returns successfully authenticated players. Removes timed-out connections.
func (ls *loginService) processLogins(connMgr *net.ConnManager) (results []loginResult, timedOut []uint32) {
	now := time.Now()
	for connID, pc := range ls.pending {
		// Check timeout
		if now.Sub(pc.createdAt) > ls.loginTimeout {
			timedOut = append(timedOut, connID)
			delete(ls.pending, connID)
			continue
		}

		msgs := connMgr.DrainInput(connID)
		if len(msgs) == 0 {
			continue
		}

		username, err := ls.handler(connID, msgs)
		if err != nil {
			if errors.Is(err, ErrLoginPending) {
				continue
			}
			// Login rejected
			if ls.onRejected != nil {
				ls.onRejected(connID, err.Error())
			}
			delete(ls.pending, connID)
			continue
		}

		results = append(results, loginResult{connID: connID, username: username})
		delete(ls.pending, connID)
	}
	return results, timedOut
}

func (ls *loginService) pendingCount() int {
	return len(ls.pending)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./pkg/universe/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/login.go
git commit -m "feat: add LoginService for coordinator-level login processing"
```

---

### Task 2: Add ErrLoginPending to universe package

**Files:**
- Modify: `pkg/universe/login.go`

The `ErrLoginPending` error is currently defined in `pkg/engine/player_manager.go`. The login service needs access to it. Rather than importing engine (which would create a circular dependency risk), re-export it.

- [ ] **Step 1: Add ErrLoginPending to login.go**

Add at the top of `pkg/universe/login.go`, after the imports:

```go
import (
	"errors"
	"time"

	"github.com/zenion/mmokit/pkg/engine"
	"github.com/zenion/mmokit/pkg/net"
)

// ErrLoginPending re-exports the engine's ErrLoginPending for use in LoginHandler implementations.
var ErrLoginPending = engine.ErrLoginPending
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./pkg/universe/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/login.go
git commit -m "feat: re-export ErrLoginPending in universe package"
```

---

### Task 3: Add session tracking maps and LoginHandler/PlayerRouter to Coordinator

**Files:**
- Modify: `pkg/universe/coordinator.go:28-81` (Config struct and Coordinator struct)

Add new Config fields for login handling and new Coordinator fields for session tracking.

- [ ] **Step 1: Add Config fields**

In `pkg/universe/coordinator.go`, add to the `Config` struct (after the `DebugTopology` field at line 43):

```go
	LoginHandler    LoginHandler  // required: parses login messages, returns username
	LoginRejected   func(connID uint32, reason string) // optional: called on rejected login
	LoginTimeout    time.Duration // max time for login before disconnect (0 = 30s)
```

- [ ] **Step 2: Add Coordinator fields**

In the `Coordinator` struct (after `playerNode` at line 80), add:

```go
	activeUsers    map[string]string // username -> nodeID (for dupe detection)
	disconnected   map[string]string // username -> nodeID (for reconnection)
	loginSvc       *loginService
	playerRouter   PlayerRouter
```

- [ ] **Step 3: Add SetPlayerRouter method**

After the `OnConsoleReady` method (line 153), add:

```go
// SetPlayerRouter sets the callback that determines which node hosts a player.
// Called after successful login with the authenticated username.
// Must return a valid nodeID. Must be called before Start().
func (c *Coordinator) SetPlayerRouter(router PlayerRouter) {
	c.playerRouter = router
}

// NodeAtPosition returns the nodeID that owns the given world-space position.
// Handles dynamic cells — always finds the correct subcell.
// Returns "" if no node owns the position.
func (c *Coordinator) NodeAtPosition(worldX, worldY float32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for cell, nodeID := range c.NodeOwner {
		minX, minY, maxX, maxY := cell.WorldBounds(coords.CellSize)
		if worldX >= minX && worldX < maxX && worldY >= minY && worldY < maxY {
			return nodeID
		}
	}
	return ""
}
```

- [ ] **Step 4: Initialize new fields in NewCoordinator**

In `NewCoordinator` (line 111-119), add the new maps to the return struct:

```go
	return &Coordinator{
		Nodes:        make(map[string]*Node),
		NodeOwner:    make(map[CellID]string),
		ConnMgr:      cfg.ConnManager,
		Log:          cfg.Logger,
		defaultCell:  cfg.DefaultCell,
		playerNode:   make(map[uint32]string),
		activeUsers:  make(map[string]string),
		disconnected: make(map[string]string),
		cfg:          cfg,
	}
```

- [ ] **Step 5: Verify it compiles**

Run: `go vet ./pkg/universe/`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "feat: add LoginHandler, PlayerRouter, and session tracking to Coordinator"
```

---

### Task 4: Add session tracking callbacks

**Files:**
- Modify: `pkg/universe/coordinator.go`

Add methods for nodes to notify the coordinator about session state changes (active, disconnected, removed). These are called from node game loops, so they need locking.

- [ ] **Step 1: Add session notification methods**

Add after `NodeAtPosition`:

```go
// notifySessionActive is called when a player transitions to active on a node.
// Thread-safe — called from node game loops.
func (c *Coordinator) notifySessionActive(username, nodeID string) {
	c.mu.Lock()
	c.activeUsers[username] = nodeID
	delete(c.disconnected, username)
	c.mu.Unlock()
}

// notifySessionDisconnected is called when a player disconnects (enters grace period).
// Thread-safe — called from node game loops.
func (c *Coordinator) notifySessionDisconnected(username, nodeID string) {
	c.mu.Lock()
	c.disconnected[username] = nodeID
	delete(c.activeUsers, username)
	c.mu.Unlock()
}

// notifySessionRemoved is called when a player session is fully removed from a node.
// Thread-safe — called from node game loops.
func (c *Coordinator) notifySessionRemoved(username string) {
	c.mu.Lock()
	delete(c.disconnected, username)
	delete(c.activeUsers, username)
	c.mu.Unlock()
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./pkg/universe/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "feat: add session notification methods for coordinator-level tracking"
```

---

### Task 5: Add PlayerManager session notification hooks

**Files:**
- Modify: `pkg/engine/player_manager.go:27-44` (struct), `pkg/engine/player_manager.go:192-232` (Transition, Remove)

PlayerManager needs callbacks to notify the coordinator about state changes.

- [ ] **Step 1: Add callback fields to PlayerManager**

In the `PlayerManager` struct (after `eng *Engine` at line 44), add:

```go
	onSessionActive       func(username string) // called when player enters Active
	onSessionDisconnected func(username string) // called when player enters Disconnected
	onSessionRemoved      func(username string) // called when session is removed
```

- [ ] **Step 2: Add setter methods**

After `SetLoginRejectedHandler` (line 248), add:

```go
// SetSessionCallbacks sets coordinator-level session tracking callbacks.
// These are called during state transitions and session removal.
func (pm *PlayerManager) SetSessionCallbacks(
	onActive func(username string),
	onDisconnected func(username string),
	onRemoved func(username string),
) {
	pm.onSessionActive = onActive
	pm.onSessionDisconnected = onDisconnected
	pm.onSessionRemoved = onRemoved
}
```

- [ ] **Step 3: Fire onSessionActive in Transition**

In `PlayerManager.Transition()` (around line 214), after the successful transition and OnEnter callback, add a check:

Find the section where `s.State = to` is set and OnEnter is called. After the OnEnter call, add:

```go
	if to == StateActive && pm.onSessionActive != nil && s.Username != "" {
		pm.onSessionActive(s.Username)
	}
	if to == StateDisconnected && pm.onSessionDisconnected != nil && s.Username != "" {
		pm.onSessionDisconnected(s.Username)
	}
```

- [ ] **Step 4: Fire onSessionRemoved in Remove**

In `PlayerManager.Remove()` (around line 232), before the deletes, add:

```go
	if pm.onSessionRemoved != nil && s.Username != "" {
		pm.onSessionRemoved(s.Username)
	}
```

- [ ] **Step 5: Verify it compiles**

Run: `go vet ./pkg/engine/`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add pkg/engine/player_manager.go
git commit -m "feat: add session notification callbacks to PlayerManager"
```

---

### Task 6: Wire session callbacks in createNode

**Files:**
- Modify: `pkg/universe/coordinator.go:280-419` (createNode)

Wire the session callbacks when creating each node so the coordinator tracks all sessions globally.

- [ ] **Step 1: Wire callbacks after engine creation**

In `createNode`, after `eng.SetNetIDBase(c.netIDAlloc.Allocate())` (line 286), add:

```go
	nodeID := id // capture for closures
	eng.Players.SetSessionCallbacks(
		func(username string) { c.notifySessionActive(username, nodeID) },
		func(username string) { c.notifySessionDisconnected(username, nodeID) },
		func(username string) { c.notifySessionRemoved(username) },
	)
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./pkg/universe/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "feat: wire session callbacks in createNode for global tracking"
```

---

### Task 7: Add PlayerAssignment event type and node handling

**Files:**
- Modify: `pkg/universe/node.go:112-117` (message handling)
- Modify: `pkg/universe/message.go`

Add a new message type for the coordinator to send authenticated players to nodes.

- [ ] **Step 1: Add MsgPlayerAssignment to message.go**

In `pkg/universe/message.go`, add a new message type and struct:

```go
	MsgPlayerAssignment MsgType = 8 // coordinator assigns authenticated player to node
```

Add to `NodeMessage` struct:

```go
	Assignment *PlayerAssignment
```

Add the struct:

```go
// PlayerAssignment is sent by the coordinator to a node after successful login.
type PlayerAssignment struct {
	ConnID      uint32
	Username    string
	IsReconnect bool
}
```

- [ ] **Step 2: Handle MsgPlayerAssignment in node.go**

In `pkg/universe/node.go`, in the `processMessage` method, add a new case after `MsgSpawnTransfer`:

```go
	case MsgPlayerAssignment:
		if msg.Assignment == nil {
			return
		}
		n.Log.Log(CatMeshMsg, "[%s] msg MsgPlayerAssignment conn=%d user=%s reconnect=%v",
			n.ID, msg.Assignment.ConnID, msg.Assignment.Username, msg.Assignment.IsReconnect)
		if msg.Assignment.IsReconnect {
			// Reconnect: find lingering session, assign new connID
			existing := n.Engine.Players.ByUsername(msg.Assignment.Username)
			if existing != nil && existing.State == engine.StateDisconnected {
				existing.ConnID = msg.Assignment.ConnID
				existing.DisconnectTime = time.Time{}
				n.Engine.Players.ReconnectSession(existing)
			} else {
				// Lingering session gone — treat as fresh login
				n.Engine.Players.RegisterPendingLogin(msg.Assignment.ConnID, msg.Assignment.Username)
			}
		} else {
			n.Engine.Players.RegisterPendingLogin(msg.Assignment.ConnID, msg.Assignment.Username)
		}
```

- [ ] **Step 3: Add ReconnectSession to PlayerManager**

In `pkg/engine/player_manager.go`, add:

```go
// ReconnectSession re-activates a disconnected session with a new connection ID.
// Called by the coordinator when routing a reconnecting player to the correct node.
func (pm *PlayerManager) ReconnectSession(s *PlayerSession) {
	pm.byConnID[s.ConnID] = s
	if err := pm.Transition(s, s.PriorState); err != nil {
		pm.Remove(s)
	}
}
```

- [ ] **Step 4: Add time import to node.go if not present**

Check if `time` is imported in `pkg/universe/node.go`. If not, add it.

- [ ] **Step 5: Verify it compiles**

Run: `go vet ./pkg/universe/ && go vet ./pkg/engine/`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/message.go pkg/universe/node.go pkg/engine/player_manager.go
git commit -m "feat: add MsgPlayerAssignment for coordinator-to-node player routing"
```

---

### Task 8: Move sendServerConfig to Coordinator

**Files:**
- Modify: `pkg/universe/coordinator.go`

The `sendServerConfig` method currently lives on PlayerManager and sends tick rate on connect. Move it to Coordinator since the Coordinator now owns connections pre-login.

- [ ] **Step 1: Add sendServerConfig to Coordinator**

Add to `pkg/universe/coordinator.go`:

```go
// sendServerConfig sends the server configuration (tick rate) to a newly connected client.
func (c *Coordinator) sendServerConfig(connID uint32) {
	msg := &enginepb.ServerConfigMsg{
		TickRate: uint32(c.cfg.TickRate),
	}
	inner, err := proto.Marshal(msg)
	if err != nil {
		return
	}
	evt := &enginepb.ServerEvent{
		Code: uint32(enginepb.ServerEventCode_SE_SERVER_CONFIG),
		Data: inner,
	}
	evtData, err := proto.Marshal(evt)
	if err != nil {
		return
	}
	frame := make([]byte, 1+len(evtData))
	frame[0] = 0x00 // event channel
	copy(frame[1:], evtData)
	c.ConnMgr.Send(connID, frame)
}
```

Add `"google.golang.org/protobuf/proto"` to the imports if not already present.

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./pkg/universe/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "feat: move sendServerConfig to Coordinator"
```

---

### Task 9: Rewrite routeEvents to use LoginService

**Files:**
- Modify: `pkg/universe/coordinator.go:424-441` (Start), `pkg/universe/coordinator.go:655-683` (routeEvents)

This is the core change. `routeEvents` no longer routes connections to DefaultNode. Instead it buffers them and processes logins on a ticker.

- [ ] **Step 1: Initialize loginService in Build()**

In `Build()`, after the dynamic partitioning init (around line 212), add:

```go
	// Initialize login service if LoginHandler is provided.
	if cfg.LoginHandler != nil {
		c.loginSvc = newLoginService(cfg.LoginHandler, cfg.LoginTimeout)
		c.loginSvc.onRejected = cfg.LoginRejected
	}
```

- [ ] **Step 2: Rewrite routeEvents**

Replace the entire `routeEvents` method (lines 655-683) with:

```go
// routeEvents drains ConnManager.Events() and processes logins.
// New connections are buffered in the login service. Authenticated players
// are routed to the appropriate node via the PlayerRouter.
func (c *Coordinator) routeEvents(ctx context.Context) {
	events := c.ConnMgr.Events()

	// Login processing ticker — same rate as game loop
	tickInterval := time.Duration(1000/c.cfg.TickRate) * time.Millisecond
	loginTicker := time.NewTicker(tickInterval)
	defer loginTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case evt := <-events:
			if evt.Connected {
				if c.loginSvc != nil {
					c.loginSvc.addPending(evt.ConnID)
					c.sendServerConfig(evt.ConnID)
					c.Log.Log(CatNetConn, "coordinator: conn %d pending login", evt.ConnID)
				} else {
					// No login handler — legacy single-node path (shouldn't happen in production)
					c.Log.Log(CatNetConn, "coordinator: conn %d but no login handler configured", evt.ConnID)
				}
			} else {
				// Disconnect: route to the node that owns this player
				nodeID := c.getPlayerNode(evt.ConnID)
				if nodeID != "" {
					if node, ok := c.getNode(nodeID); ok {
						node.Events <- evt
					}
					c.removePlayerNode(evt.ConnID)
				} else {
					// Player was still in pending login — just remove
					if c.loginSvc != nil {
						c.loginSvc.removePending(evt.ConnID)
					}
				}
			}

		case <-loginTicker.C:
			c.processLogins()
		}
	}
}
```

- [ ] **Step 3: Add processLogins method**

Add after `routeEvents`:

```go
// processLogins processes all pending login attempts on the coordinator goroutine.
func (c *Coordinator) processLogins() {
	if c.loginSvc == nil {
		return
	}

	results, timedOut := c.loginSvc.processLogins(c.ConnMgr)

	// Disconnect timed-out connections
	for _, connID := range timedOut {
		c.Log.Log(CatNetConn, "coordinator: login timeout conn=%d", connID)
		c.ConnMgr.Close(connID)
	}

	for _, r := range results {
		c.routeAuthenticatedPlayer(r.connID, r.username)
	}
}

// routeAuthenticatedPlayer routes a successfully authenticated player to the correct node.
func (c *Coordinator) routeAuthenticatedPlayer(connID uint32, username string) {
	// 1. Check for reconnection (lingering disconnected session)
	c.mu.RLock()
	reconnectNodeID := c.disconnected[username]
	existingNodeID := c.activeUsers[username]
	c.mu.RUnlock()

	if existingNodeID != "" {
		// Duplicate username — reject
		c.Log.Log(CatNetConn, "coordinator: duplicate username %q conn=%d (active on %s)", username, connID, existingNodeID)
		if c.loginSvc.onRejected != nil {
			c.loginSvc.onRejected(connID, "Username already connected")
		}
		c.ConnMgr.Close(connID)
		return
	}

	if reconnectNodeID != "" {
		// Reconnect to the node with the lingering session
		if node, ok := c.getNode(reconnectNodeID); ok {
			c.setPlayerNode(connID, reconnectNodeID)
			node.Events <- net.PlayerEvent{ConnID: connID, Connected: true}
			node.Inbox <- NodeMessage{
				Type: MsgPlayerAssignment,
				Assignment: &PlayerAssignment{
					ConnID:      connID,
					Username:    username,
					IsReconnect: true,
				},
			}
			c.Log.Log(CatNetConn, "coordinator: reconnect conn=%d user=%s -> %s", connID, username, reconnectNodeID)
			return
		}
		// Node gone (e.g., merged) — fall through to fresh login
	}

	// 2. Route via PlayerRouter
	var targetNodeID string
	if c.playerRouter != nil {
		targetNodeID = c.playerRouter(username)
	}
	if targetNodeID == "" {
		// Fallback: pick any node
		for id := range c.Nodes {
			targetNodeID = id
			break
		}
	}

	node, ok := c.getNode(targetNodeID)
	if !ok {
		c.Log.Log(CatNetConn, "coordinator: no node %s for conn=%d user=%s", targetNodeID, connID, username)
		c.ConnMgr.Close(connID)
		return
	}

	c.setPlayerNode(connID, targetNodeID)
	node.Events <- net.PlayerEvent{ConnID: connID, Connected: true}
	node.Inbox <- NodeMessage{
		Type: MsgPlayerAssignment,
		Assignment: &PlayerAssignment{
			ConnID:   connID,
			Username: username,
		},
	}
	c.Log.Log(CatNetConn, "coordinator: conn=%d user=%s -> %s", connID, username, targetNodeID)
}
```

- [ ] **Step 4: Add net import**

Add `"github.com/zenion/mmokit/pkg/net"` to coordinator.go imports if not present (needed for `net.PlayerEvent`).

- [ ] **Step 5: Verify it compiles**

Run: `go vet ./pkg/universe/`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "feat: rewrite routeEvents to process logins at coordinator level"
```

---

### Task 10: Decouple Console from Engine

**Files:**
- Modify: `pkg/engine/console.go:57-90` (struct, constructor)
- Modify: `pkg/engine/console.go:270-283` (ExecOnGameLoop)
- Modify: `pkg/engine/console.go:350-423` (registerPlatformCommands)

Console no longer takes an `*Engine`. It takes a `Logger` and an optional `ExecFunc` for game-loop proxying. The `perf` and `load` commands become node builtins registered by the coordinator, not platform commands.

- [ ] **Step 1: Change Console struct**

Replace the `engine *Engine` field (line 64) with:

```go
	execFunc func(fn func() string) string // optional: proxy to a game loop
```

- [ ] **Step 2: Change NewConsole signature**

Change `NewConsole` (line 73) from:

```go
func NewConsole(eng *Engine, gameLog *logger.Logger) *Console {
```

to:

```go
func NewConsole(gameLog *logger.Logger) *Console {
```

Remove `engine: eng` from the struct initialization inside.

- [ ] **Step 3: Add SetExecFunc method**

```go
// SetExecFunc sets the function used to execute closures on a game loop.
// Used by commands that need thread-safe ECS access.
func (c *Console) SetExecFunc(fn func(func() string) string) {
	c.execFunc = fn
}
```

- [ ] **Step 4: Update ExecOnGameLoop**

Replace the body of `ExecOnGameLoop` (lines 272-283):

```go
func (c *Console) ExecOnGameLoop(fn func() string) string {
	if c.execFunc != nil {
		return c.execFunc(fn)
	}
	return "  no game loop connected\n"
}
```

- [ ] **Step 5: Remove perf and load from registerPlatformCommands**

Remove the `perf` command (lines 382-403) and `load` command (lines 405-423) from `registerPlatformCommands()`. These will be re-registered as node builtins by the coordinator in Task 11.

Keep `help`, `quit`, and the `log` command group.

- [ ] **Step 6: Verify it compiles**

Run: `go vet ./pkg/engine/`
Expected: no errors (perf/load are removed, callers updated in next task)

- [ ] **Step 7: Commit**

```bash
git add pkg/engine/console.go
git commit -m "refactor: decouple Console from Engine, remove perf/load platform commands"
```

---

### Task 11: Update Coordinator console setup

**Files:**
- Modify: `pkg/universe/coordinator.go:466-507` (startConsole)

Update `startConsole` to create Console without an Engine reference. Re-register perf/load as node-scoped builtins.

- [ ] **Step 1: Rewrite startConsole**

Replace lines 466-507:

```go
func (c *Coordinator) startConsole(ctx context.Context) {
	c.console = engine.NewConsole(c.Log)

	// Set exec func to proxy to the first node (for commands that need it).
	// Node-specific commands use their own ExecOnGameLoop.
	for _, node := range c.Nodes {
		eng := node.Engine
		c.console.SetExecFunc(func(fn func() string) string {
			result := make(chan string, 1)
			eng.PendingAdminCmds <- func() {
				result <- fn()
			}
			select {
			case r := <-result:
				return r
			case <-time.After(5 * time.Second):
				return "  game loop not responding (timeout)\n"
			}
		})
		break
	}

	// Auto-wire node builtins from coordinator's node map.
	nodeRefs := c.buildNodeRefs()

	builtinOpts := engine.BuiltinOpts{
		Nodes: nodeRefs,
	}

	// Merge game-provided builtins if Console was set.
	co := c.consoleOpts
	if co != nil {
		builtinOpts.Config = co.Config
		builtinOpts.ConfigSave = co.ConfigSave
		builtinOpts.ConfigReset = co.ConfigReset
		builtinOpts.Entities = co.Entities
		builtinOpts.Registry = co.Registry
	}

	// Auto-wire default entity commands if game didn't provide its own.
	if builtinOpts.Entities == nil {
		// Pick any node for entity opts
		for _, node := range c.Nodes {
			builtinOpts.Entities = c.defaultEntityOpts(node)
			break
		}
	}

	c.console.RegisterBuiltins(builtinOpts)

	// Register perf command (uses first node by default)
	c.registerPerfCommands(c.console)

	// Register cell commands if dynamic partitioning is enabled.
	if c.cfg.DynamicPartitioning != nil {
		c.registerCellCommands(c.console)
	}

	// Let game register custom commands.
	onReady := c.onConsoleReady
	if onReady != nil {
		onReady(c.console)
	}

	c.console.Run(ctx)
}
```

- [ ] **Step 2: Add registerPerfCommands**

Add a new method:

```go
// registerPerfCommands registers perf and load as coordinator-level commands.
func (c *Coordinator) registerPerfCommands(console *engine.Console) {
	// Pick first node for default perf display
	var defaultEng *engine.Engine
	for _, node := range c.Nodes {
		defaultEng = node.Engine
		break
	}
	if defaultEng == nil {
		return
	}

	console.Register(engine.Command{
		Name: "perf", Aliases: []string{"p"},
		Category: "perf", Usage: "perf [reset]", Description: "show tick timing, entities, network, load",
		Complete: func(args []string) []string {
			if len(args) == 0 {
				return []string{"reset"}
			}
			return nil
		},
		Fn: func(args []string) {
			eng := defaultEng
			if len(args) > 0 && args[0] == "reset" {
				output := console.ExecOnGameLoop(func() string {
					eng.Perf.Reset()
					return "  perf counters reset\n"
				})
				fmt.Print(output)
				return
			}
			output := console.ExecOnGameLoop(func() string { return engine.FormatPerfOutput(eng) })
			fmt.Print(output)
		},
	})

	console.Register(engine.Command{
		Name: "load",
		Category: "perf", Usage: "load", Description: "show composite load score",
		Fn: func(args []string) {
			eng := defaultEng
			output := console.ExecOnGameLoop(func() string {
				if eng.Metrics == nil {
					return "  metrics not wired\n"
				}
				snap := eng.Metrics.Snapshot()
				tickBudget := time.Duration(1000/eng.Config.TickRate) * time.Millisecond
				return fmt.Sprintf("  load: %.2f (tick=%.1f%% entity=%.1f%%)\n",
					snap.CompositeLoad,
					float64(snap.Tick.AvgDuration)/float64(tickBudget)*100,
					float64(snap.Entities.Real)/1000.0*100,
				)
			})
			fmt.Print(output)
		},
	})
}
```

- [ ] **Step 3: Export formatPerfOutput**

In `pkg/engine/console.go`, rename the `formatPerfOutput` function to `FormatPerfOutput` (exported) so the coordinator can call it.

- [ ] **Step 4: Add fmt import to coordinator.go if needed**

- [ ] **Step 5: Verify it compiles**

Run: `go vet ./pkg/universe/ && go vet ./pkg/engine/`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/coordinator.go pkg/engine/console.go
git commit -m "refactor: decouple console from DefaultNode, register perf as coordinator commands"
```

---

### Task 12: Remove DefaultNode / DefaultCell

**Files:**
- Modify: `pkg/universe/coordinator.go`

Remove all DefaultNode/DefaultCell methods and fields. This is the "rip the band-aid" step.

- [ ] **Step 1: Remove from Config**

In the `Config` struct, remove:

```go
	DefaultCell       CellID
```

- [ ] **Step 2: Remove from Coordinator struct**

Remove:

```go
	defaultCell  CellID
```

- [ ] **Step 3: Remove from NewCoordinator**

Remove `defaultCell: cfg.DefaultCell,` from the struct literal.

- [ ] **Step 4: Remove methods**

Delete these methods entirely:
- `DefaultNode()` (lines 692-694)
- `findDefaultNode()` (lines 699-720)
- `DefaultCell()` (lines 723-725)

- [ ] **Step 5: Verify it compiles**

Run: `go vet ./pkg/universe/`
Expected: errors from callers that still use DefaultNode/DefaultCell — these are fixed in subsequent tasks.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "refactor: remove DefaultNode, DefaultCell, findDefaultNode from Coordinator"
```

---

### Task 13: Replace RequestSpawnOnNode with RequestRespawn

**Files:**
- Modify: `pkg/universe/bridge.go`
- Modify: `pkg/universe/node_bridge_impl.go:95-104`

- [ ] **Step 1: Update bridge interface**

In `pkg/universe/bridge.go`, replace:

```go
	// RequestSpawnOnNode transfers a player spawn to the station node.
	RequestSpawnOnNode(connID uint32, username string)
```

with:

```go
	// RequestRespawn asks the coordinator to route a player respawn.
	// The coordinator calls PlayerRouter to determine the target node.
	RequestRespawn(connID uint32, username string)
```

- [ ] **Step 2: Update NoopNodeBridge**

Replace:
```go
func (NoopNodeBridge) RequestSpawnOnNode(uint32, string)            {}
```
with:
```go
func (NoopNodeBridge) RequestRespawn(uint32, string)                {}
```

- [ ] **Step 3: Update nodeBridge implementation**

In `pkg/universe/node_bridge_impl.go`, replace the `RequestSpawnOnNode` method (lines 95-104):

```go
func (b *nodeBridge) RequestRespawn(connID uint32, username string) {
	b.node.Log.Log(CatMeshMsg, "[%s] requesting respawn: conn=%d user=%s", b.node.ID, connID, username)
	// Route through coordinator's player router
	var targetNodeID string
	if b.coord.playerRouter != nil {
		targetNodeID = b.coord.playerRouter(username)
	}
	if targetNodeID == "" {
		// Fallback: route to first node
		for id := range b.coord.Nodes {
			targetNodeID = id
			break
		}
	}
	if targetNodeID == "" {
		return
	}
	dest, ok := b.coord.Nodes[targetNodeID]
	if !ok {
		return
	}
	dest.Inbox <- NodeMessage{
		Type:       MsgSpawnTransfer,
		FromNodeID: b.node.ID,
		Spawn:      &SpawnTransfer{ConnID: connID, Username: username},
	}
	b.coord.setPlayerNode(connID, targetNodeID)
}
```

- [ ] **Step 4: Verify it compiles**

Run: `go vet ./pkg/universe/`
Expected: errors from callers using old name — fixed in next tasks.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/bridge.go pkg/universe/node_bridge_impl.go
git commit -m "refactor: replace RequestSpawnOnNode with RequestRespawn"
```

---

### Task 14: Update space game for new APIs

**Files:**
- Modify: `cmd/server/main.go:102-134`
- Modify: `internal/game/game.go:98-131` (login handler)
- Modify: `internal/game/lifecycle.go:125-134` (respawn routing)
- Modify: `internal/game/factory.go`

- [ ] **Step 1: Move login handler to main.go Config**

In `cmd/server/main.go`, update the coordinator config (lines 102-110) to include `LoginHandler` and `LoginRejected`:

```go
	coordinator = mmokit.NewCoordinator(mmokit.Config{
		CellsX:     gameCfg.MeshCellsX,
		CellsY:     gameCfg.MeshCellsY,
		TickRate:    platformCfg.TickRate,
		ConnManager: connMgr,
		Logger:      gameLog,
		LoginHandler: func(connID uint32, msgs [][]byte) (string, error) {
			for _, data := range msgs {
				var evt enginepb.ClientEvent
				if err := proto.Unmarshal(data, &evt); err != nil {
					continue
				}
				if enginepb.ClientEventCode(evt.Code) == enginepb.ClientEventCode_CE_LOGIN {
					var login enginepb.LoginMsg
					if err := proto.Unmarshal(evt.Data, &login); err != nil {
						continue
					}
					username := strings.ToLower(login.Username)
					if username == "" {
						continue
					}
					return username, nil
				}
			}
			return "", mmokit.ErrLoginPending
		},
		LoginRejected: func(connID uint32, reason string) {
			gameLog.Log(game.CatPlayerConnect, "login rejected: conn=%d reason=%s", connID, reason)
			rejectData := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_LOGIN_REJECTED), &enginepb.LoginRejectedMsg{
				Reason: reason,
			})
			if rejectData != nil {
				connMgr.SendReliable(connID, rejectData)
			}
		},
	})
```

Remove `DefaultCell` from the config.

- [ ] **Step 2: Add PlayerRouter**

After `game.GameSetup(...)` (line 135), add:

```go
	coordinator.SetPlayerRouter(func(username string) string {
		if pdata := playerDB.Get(username); pdata != nil {
			worldX := float32(pdata.CellX)*coords.CellSize + pdata.X
			worldY := float32(pdata.CellY)*coords.CellSize + pdata.Y
			nodeID := coordinator.NodeAtPosition(worldX, worldY)
			if nodeID != "" {
				return nodeID
			}
		}
		// New player or invalid saved position — spawn at station
		stationWorldX := float32(gameCfg.StationCell.CellX)*coords.CellSize + coords.CellSize/2
		stationWorldY := float32(gameCfg.StationCell.CellY)*coords.CellSize + coords.CellSize/2
		return coordinator.NodeAtPosition(stationWorldX, stationWorldY)
	})
```

Add `"github.com/zenion/mmokit/pkg/coords"` and `"strings"` to main.go imports.

- [ ] **Step 3: Remove login handler from game.go**

In `internal/game/game.go`, remove the `SetLoginHandler` call (lines 98-120) and the `SetLoginRejectedHandler` call (lines 123-131). The login handler is now on the coordinator, not per-node.

- [ ] **Step 4: Update respawn routing in lifecycle.go**

In `internal/game/lifecycle.go:127`, replace:

```go
		if gw.Cell != gw.Config.StationCell {
			gw.Log.Log(CatPlayerConnect, "respawn transfer: conn=%d username=%s -> station node", connID, s.Username)
			gw.Bridge.RequestSpawnOnNode(connID, s.Username)
```

with:

```go
		if !gw.hasStation() {
			gw.Log.Log(CatPlayerConnect, "respawn transfer: conn=%d username=%s -> station node", connID, s.Username)
			gw.Bridge.RequestRespawn(connID, s.Username)
```

- [ ] **Step 5: Add hasStation method**

In `internal/game/game.go` or a relevant file, add:

```go
// hasStation returns true if this node has a station entity.
func (gw *GameWorld) hasStation() bool {
	filter := ecs.NewFilter1[gamecomp.Station](gw.ECS)
	query := filter.Query()
	has := query.Next()
	return has
}
```

- [ ] **Step 6: Update OnConsoleReady in main.go**

Replace `coordinator.DefaultNode().World` (line 121) with a direct lookup from any node. The console setup doesn't need a "default" world — it just needs any world for config access:

```go
	coordinator.OnConsoleReady(func(console *mmokit.Console) {
		// Build node info list for cross-node admin commands
		var allNodes []game.NodeInfo
		var anyWorld *game.GameWorld
		for _, node := range coordinator.Nodes {
			gw := game.UnwrapGameWorld(node.World)
			allNodes = append(allNodes, game.NodeInfo{
				ID:    node.ID,
				Cell:  node.Cell,
				World: gw,
			})
			if anyWorld == nil {
				anyWorld = &gw
			}
		}

		// Register game builtins (config, entity)
		console.RegisterBuiltins(mmokit.BuiltinOpts{
			Config:      &(*anyWorld).Config,
			ConfigSave:  func() error { return game.SaveConfig(store, &(*anyWorld).Config) },
			ConfigReset: func() { (*anyWorld).Config = game.DefaultGameConfig() },
			Registry:    (*anyWorld).Registry,
			Entities:    game.BuildEntityOpts(*anyWorld),
		})

		// Register game-specific commands (players, damage, etc.)
		game.RegisterCommands(console, *anyWorld, store, allNodes)
	})
```

- [ ] **Step 7: Verify it compiles**

Run: `go vet ./...`
Expected: may still have errors from examples — fixed next.

- [ ] **Step 8: Commit**

```bash
git add cmd/server/main.go internal/game/game.go internal/game/lifecycle.go
git commit -m "feat: update space game for coordinator-level login and PlayerRouter"
```

---

### Task 15: Update 4node-basic example

**Files:**
- Modify: `examples/4node-basic/main.go`
- Modify: `examples/4node-basic/world.go:61-86` (login handler)

- [ ] **Step 1: Add LoginHandler to Config in main.go**

In `examples/4node-basic/main.go`, update the config (lines 27-35):

```go
	cfg := mmokit.Config{
		CellsX:        CellsX,
		CellsY:        CellsY,
		CellSize:      CellSize,
		TickRate:      TickRate,
		AoIRadius:     AoIRadius,
		DebugTopology: true,
		LogCategories: *logFlag,
		LoginHandler: func(connID uint32, msgs [][]byte) (string, error) {
			for _, data := range msgs {
				var evt enginepb.ClientEvent
				if err := proto.Unmarshal(data, &evt); err != nil {
					continue
				}
				if evt.Code == uint32(basicpb.ClientEventCode_BCE_LOGIN) {
					var login basicpb.LoginMsg
					if err := proto.Unmarshal(evt.Data, &login); err != nil {
						continue
					}
					name := strings.ToLower(strings.TrimSpace(login.Name))
					if name == "" || len(name) > 20 {
						continue
					}
					return name, nil
				}
			}
			return "", mmokit.ErrLoginPending
		},
	}
```

Add necessary imports: `enginepb`, `basicpb`, `proto`, `strings`.

- [ ] **Step 2: Add PlayerRouter**

After `coord.SetWorld(NewWorld)` (line 42), add:

```go
	coord.SetPlayerRouter(func(username string) string {
		// All players start at cell (0,0)
		return coord.NodeAtPosition(0, 0)
	})
```

- [ ] **Step 3: Remove login handler from world.go**

In `examples/4node-basic/world.go`, remove the login handler setup (lines 62-86):

```go
	// Remove this block:
	pm := gw.Engine().Players
	pm.SetLoginHandler(func(s *mmokit.PlayerSession, pm *mmokit.PlayerManager) error {
		...
	})
```

Keep the `pm.OnState(mmokit.StateActive, ...)` callbacks.

- [ ] **Step 4: Verify it compiles**

Run: `go vet ./examples/4node-basic/`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add examples/4node-basic/main.go examples/4node-basic/world.go
git commit -m "feat: update 4node-basic for coordinator-level login"
```

---

### Task 16: Update slither example

**Files:**
- Modify: `examples/slither/main.go:29-49`

- [ ] **Step 1: Check slither's login handler location**

Read `examples/slither/world.go` to find where the login handler is set. Move it to `main.go` Config.

- [ ] **Step 2: Add LoginHandler to Config**

Update the coordinator config in `examples/slither/main.go` to include `LoginHandler`. The slither login handler parses `CE_LOGIN` from enginepb. Move the handler closure to the config.

- [ ] **Step 3: Add PlayerRouter**

Add after coord setup:

```go
	coord.SetPlayerRouter(func(username string) string {
		// All players start at cell (0,0)
		return coord.NodeAtPosition(0, 0)
	})
```

- [ ] **Step 4: Replace DefaultNode() usage**

Replace line 43:
```go
	gw := coord.DefaultNode().World.(*SlitherWorld)
```

The console setup needs a world reference without DefaultNode. Use:

```go
	coord.OnConsoleReady(func(console *mmokit.Console) {
		var gw *SlitherWorld
		for _, node := range coord.Nodes {
			gw = node.World.(*SlitherWorld)
			break
		}
		registry := buildEntityRegistry(gw)
		console.RegisterBuiltins(mmokit.BuiltinOpts{
			Registry: registry,
			Entities: buildEntityOpts(gw, registry),
		})
	})
```

- [ ] **Step 5: Remove login handler from slither world Init**

- [ ] **Step 6: Verify it compiles**

Run: `go vet ./examples/slither/`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add examples/slither/
git commit -m "feat: update slither for coordinator-level login"
```

---

### Task 17: Update mmokit facade

**Files:**
- Modify: `pkg/mmokit/mmokit.go`

Re-export new types so games import from `mmokit` not `universe` directly.

- [ ] **Step 1: Add type aliases/re-exports**

In `pkg/mmokit/mmokit.go`, add re-exports for the new types:

```go
type LoginHandler = universe.LoginHandler
type PlayerRouter = universe.PlayerRouter

var ErrLoginPending = universe.ErrLoginPending
```

Remove any existing `ErrLoginPending` re-export from engine if it exists (check current re-exports).

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./pkg/mmokit/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/mmokit.go
git commit -m "feat: re-export LoginHandler, PlayerRouter, ErrLoginPending from mmokit"
```

---

### Task 18: Update tests

**Files:**
- Modify: `pkg/universe/universe_test.go`
- Modify: `internal/game/node_test.go`

- [ ] **Step 1: Remove TestCoordinator_DefaultNode**

Delete the `TestCoordinator_DefaultNode` test (lines 550-563).

- [ ] **Step 2: Update TestBridge_RequestSpawnOnNode**

Rename to `TestBridge_RequestRespawn`. Update to use the new API:

```go
func TestBridge_RequestRespawn(t *testing.T) {
	grid := Config{CellsX: 2, CellsY: 1}
	c, _ := newTestCoordinator(grid)

	// Set a player router that always routes to node_0_0
	targetID := MeshNodeID(CellID{X: 0, Y: 0})
	c.playerRouter = func(username string) string {
		return targetID
	}

	otherID := MeshNodeID(CellID{X: 1, Y: 0})
	other := c.Nodes[otherID]
	target := c.Nodes[targetID]

	other.Bridge.RequestRespawn(77, "charlie")

	select {
	case msg := <-target.Inbox:
		if msg.Type != MsgSpawnTransfer {
			t.Fatalf("expected MsgSpawnTransfer, got %d", msg.Type)
		}
		if msg.Spawn.ConnID != 77 || msg.Spawn.Username != "charlie" {
			t.Fatalf("unexpected spawn: %+v", msg.Spawn)
		}
	default:
		t.Fatal("no message in target node inbox")
	}
}
```

- [ ] **Step 3: Update newTestCoordinator if needed**

If `newTestCoordinator` sets `DefaultCell`, remove that. Add a default `LoginHandler` and `PlayerRouter` if the coordinator now requires them:

```go
func newTestCoordinator(cfg Config) (*Coordinator, map[CellID]*mockWorld) {
	worlds := make(map[CellID]*mockWorld)
	if cfg.ConnManager == nil {
		cfg.ConnManager = net.NewConnManager()
	}
	if cfg.Logger == nil {
		cfg.Logger = logger.New()
	}
	if cfg.LoginHandler == nil {
		cfg.LoginHandler = func(connID uint32, msgs [][]byte) (string, error) {
			return "", ErrLoginPending
		}
	}
	c := NewCoordinator(cfg)
	c.SetWorld(func(base *WorldBase) GameWorld {
		mw := &mockWorld{spawnNetID: 100, spawnConnID: 42}
		worlds[base.Cell()] = mw
		return mw
	})
	if c.playerRouter == nil {
		c.playerRouter = func(username string) string {
			for id := range c.Nodes {
				return id
			}
			return ""
		}
	}
	c.Build()
	return c, worlds
}
```

- [ ] **Step 4: Update internal/game/node_test.go**

Check for `MsgSpawnTransfer` usage. Update any `RequestSpawnOnNode` calls to `RequestRespawn`.

- [ ] **Step 5: Run all tests**

Run: `go test ./pkg/universe/ ./pkg/engine/ ./internal/game/ -v -count=1`
Expected: all tests pass

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/universe_test.go internal/game/node_test.go
git commit -m "test: update tests for DefaultNode removal and RequestRespawn"
```

---

### Task 19: Remove processLogins from engine hooks

**Files:**
- Modify: `pkg/engine/loop.go:12-20` (Hooks struct)
- Modify: `pkg/engine/loop.go:58-63` (hook merging)
- Modify: `pkg/engine/loop.go:119-122` (tick)
- Modify: `pkg/engine/player_manager.go:282-321` (hooks method)

Login processing no longer happens per-node. Remove the `ProcessLogins` hook from the engine loop.

- [ ] **Step 1: Remove ProcessLogins from Hooks struct**

In `pkg/engine/loop.go`, remove from the `Hooks` struct:

```go
	ProcessLogins  func()
```

- [ ] **Step 2: Remove from NewGameLoop merged hooks**

In `NewGameLoop` (lines 58-63), remove the `ProcessLogins` merging.

- [ ] **Step 3: Remove from tick()**

In `tick()` (lines 119-122), remove:

```go
	// Process logins from pending connections
	if gl.hooks.ProcessLogins != nil {
		gl.hooks.ProcessLogins()
	}
```

- [ ] **Step 4: Remove from PlayerManager.hooks()**

In `pkg/engine/player_manager.go`, in the `hooks()` method (lines 314-316), remove:

```go
		ProcessLogins: func() {
			pm.processLogins()
		},
```

- [ ] **Step 5: Keep processLogins method but mark it as node-local only**

The `processLogins` method on PlayerManager is still needed for handling `RegisterPendingLogin` calls (from transfers and player assignments). It should still run but via the node's event processing, not the engine hooks.

Actually, `RegisterPendingLogin` sets `isTransferLogin = true` on the session. The `processLogins` method handles these by directly setting state to Active (line 337-342). This still needs to run each tick for transfer logins to be processed.

Keep `ProcessLogins` in the hook but remove the `onLogin` handler path from `processLogins` — only handle `isTransferLogin` sessions:

```go
func (pm *PlayerManager) processLogins() {
	var pending []*PlayerSession
	for _, s := range pm.sessions {
		if s.State == StatePending {
			pending = append(pending, s)
		}
	}

	for _, s := range pending {
		if _, ok := pm.sessions[s.ID]; !ok {
			continue
		}

		if s.isTransferLogin {
			s.isTransferLogin = false
			s.State = StateActive
			pm.byUsername[s.Username] = s
			if pm.onSessionActive != nil && s.Username != "" {
				pm.onSessionActive(s.Username)
			}
			continue
		}

		// Non-transfer pending sessions are handled by coordinator-level login.
		// If they arrive here with a username set (from MsgPlayerAssignment),
		// transition to Active.
		if s.Username != "" {
			pm.byUsername[s.Username] = s
			if err := pm.Transition(s, StateActive); err != nil {
				pm.Remove(s)
			}
		}
	}
}
```

Actually, let me reconsider. The flow for `MsgPlayerAssignment` is:
1. Coordinator sends `MsgPlayerAssignment` to node inbox
2. Node processes it: calls `RegisterPendingLogin(connID, username)`
3. Next tick: `processLogins()` finds the pending session with username set
4. Transitions to Active

This is the same path as transfer logins. So `processLogins` is still needed. The only change is removing the `onLogin` handler call (login parsing now happens at coordinator level).

- [ ] **Step 5 (revised): Simplify processLogins**

Replace the `processLogins` method body to remove the `onLogin` handler path:

```go
func (pm *PlayerManager) processLogins() {
	var pending []*PlayerSession
	for _, s := range pm.sessions {
		if s.State == StatePending {
			pending = append(pending, s)
		}
	}

	for _, s := range pending {
		if _, ok := pm.sessions[s.ID]; !ok {
			continue
		}

		if s.isTransferLogin {
			s.isTransferLogin = false
			// Set state directly — skip OnEnter. The entity is already created
			// by SpawnFromTransfer; firing OnEnter would spawn a duplicate.
			s.State = StateActive
			continue
		}

		// Sessions with username set (from coordinator MsgPlayerAssignment)
		if s.Username == "" {
			continue
		}

		// Check for reconnection (disconnected session with same username)
		if existing := pm.byUsername[s.Username]; existing != nil && existing != s && existing.State == StateDisconnected {
			existing.ConnID = s.ConnID
			existing.DisconnectTime = time.Time{}
			pm.byConnID[s.ConnID] = existing
			delete(pm.sessions, s.ID)
			if err := pm.Transition(existing, existing.PriorState); err != nil {
				pm.Remove(existing)
			}
			continue
		}

		// Duplicate username check (coordinator should prevent this, but guard anyway)
		if existing := pm.byUsername[s.Username]; existing != nil && existing != s {
			if pm.onLoginRejected != nil && s.ConnID != 0 {
				pm.onLoginRejected(s.ConnID, "Username already connected")
			}
			pm.Remove(s)
			continue
		}

		pm.byUsername[s.Username] = s
		if err := pm.Transition(s, StateActive); err != nil {
			pm.Remove(s)
		}
	}
}
```

- [ ] **Step 6: Verify it compiles**

Run: `go vet ./pkg/engine/ && go vet ./pkg/universe/`
Expected: no errors

- [ ] **Step 7: Run tests**

Run: `go test ./pkg/engine/ ./pkg/universe/ -v -count=1`
Expected: all pass

- [ ] **Step 8: Commit**

```bash
git add pkg/engine/loop.go pkg/engine/player_manager.go
git commit -m "refactor: simplify processLogins, remove onLogin handler path"
```

---

### Task 20: Remove SetLoginHandler from PlayerManager

**Files:**
- Modify: `pkg/engine/player_manager.go`

- [ ] **Step 1: Remove SetLoginHandler method and onLogin field**

Remove from struct:
```go
	onLogin         func(s *PlayerSession, pm *PlayerManager) error
```

Remove the method:
```go
func (pm *PlayerManager) SetLoginHandler(fn func(s *PlayerSession, pm *PlayerManager) error) {
	pm.onLogin = fn
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./...`
Expected: no errors (all callers should have been updated in previous tasks)

- [ ] **Step 3: Run all tests**

Run: `go test ./... -count=1`
Expected: all pass

- [ ] **Step 4: Commit**

```bash
git add pkg/engine/player_manager.go
git commit -m "cleanup: remove SetLoginHandler from PlayerManager"
```

---

### Task 21: Full integration test

**Files:** None (verification only)

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -count=1 -timeout=60s`
Expected: all tests pass

- [ ] **Step 2: Run vet on entire codebase**

Run: `go vet ./...`
Expected: no errors

- [ ] **Step 3: Build all binaries**

Run: `make build`
Expected: successful compilation

- [ ] **Step 4: Verify examples build**

Run: `cd examples/4node-basic && go build . && cd ../slither && go build .`
Expected: successful compilation

- [ ] **Step 5: Commit any remaining fixes**

If any issues were found and fixed, commit them.
