package universe

import (
	"context"
	"sync"
	"time"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/metrics"
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/replication"
)

// pendingPromote is a destination-side promote queued for a specific
// cluster-tick. Cell.drainPendingPromotes drains due entries at tick
// start and either (a) promotes an existing border-replica in place
// via PromoteReplicaToLive (the common case — the entity has been
// visible as a replica through border frames for some time before
// the handoff), or (b) materializes a fresh Live entity via
// SpawnLiveFromTransfer when no replica exists yet (the fast-mover
// case, or cross-boundary spawn).
type pendingPromote struct {
	netID        uint32
	epoch        uint32
	transferBlob []byte
	connID       uint32
	fromCellID   string
}

// Cell is a self-contained game simulation owning one cell.
type Cell struct {
	ID      string
	Cell    CellID
	Engine  *engine.Engine
	World   GameWorld
	Base    *WorldBase // direct access for infrastructure methods
	Loop    *engine.GameLoop
	Bridge  Bridge
	Metrics *metrics.CellMetrics

	Inbox     chan CellMessage
	Events    chan net.PlayerEvent
	Neighbors map[string]*Cell
	Log       *logger.Logger

	// pendingPromotes is keyed by CommitTick. The MsgHandoff handler
	// enqueues; drainPendingPromotes (called at PostSystems tick start)
	// drains every entry with key <= currentClusterTick. A slipped
	// commit-tick still commits on the next pass.
	pendingPromotes map[uint64][]pendingPromote

	// onMessage is an optional hook called for every inbox message before
	// the cell's processMessage handler runs. Set from tests (same-package
	// _test.go files) to observe delivered messages without racing against
	// the game loop. Nil in production. Must be set before Run() is called
	// or under external synchronization.
	onMessage func(CellMessage)

	// runMu guards runCancel / runDone. Run() initializes them on entry,
	// Shutdown() reads + acts on them to cancel the game loop and block
	// until it has actually exited. This is how S7-T10 stops cell
	// goroutines from leaking past a Shutdown call and racing with the
	// next test's setup (coords.SetCellSize, etc.).
	runMu     sync.Mutex
	runCancel context.CancelFunc // nil until Run starts; reset when Run exits
	runDone   chan struct{}      // closed when Run returns
}

// Run starts the cell's game loop. Blocks until context is cancelled OR
// Shutdown() is called on this cell. The ctx argument is wrapped in a
// derived context whose cancel is owned by the cell, so Shutdown() can
// stop the loop without touching the caller's ctx.
func (c *Cell) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.runMu.Lock()
	c.runCancel = cancel
	c.runDone = done
	c.runMu.Unlock()

	defer func() {
		c.runMu.Lock()
		c.runCancel = nil
		c.runMu.Unlock()
		close(done)
	}()

	c.Log.Log(CatMeshCell, "[%s] cell started for cell %s", c.ID, c.Cell)
	c.Loop.Run(ctx)
}

// Shutdown saves all state on this cell. If Run is currently active, it
// cancels the cell's derived game-loop context and BLOCKS until the game
// loop goroutine has exited before saving state and returning. Idempotent:
// safe to call multiple times and safe to call before Run starts.
func (c *Cell) Shutdown() {
	c.runMu.Lock()
	cancel := c.runCancel
	done := c.runDone
	c.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	c.World.Shutdown()
	c.Log.Log(CatMeshCell, "[%s] cell shutdown complete", c.ID)
}

// DrainInbox processes all pending inter-cell messages.
// Called from the game loop via PreTickFunc.
func (c *Cell) DrainInbox() {
	for {
		select {
		case msg := <-c.Inbox:
			c.processMessage(msg)
		default:
			c.Base.TickGhosts()
			c.Base.TickTransferCooldowns()
			return
		}
	}
}

// drainPendingPromotes applies every queued destination-side promote
// whose commit-tick has arrived (CommitTick <= currentClusterTick).
//
// Called from cellBridge.PostSystems at the start of the post-systems
// phase, BEFORE BorderDispatcher.Tick and HandoffDriver.Tick, so the
// promoted entity is included in the first outbound border frame of
// the commit tick — this is the frame carrying the destination's first
// authoritative sample that clients lerp toward after the seam.
//
// For each due entry:
//   - If a border-replica already exists for netID, promote it in
//     place via PromoteReplicaToLive (same ECS entity; preserves
//     component tail smoothing state).
//   - Otherwise spawn a fresh Live entity from the TransferBlob via
//     SpawnLiveFromTransfer (fast-mover case or cross-boundary spawn).
//
// Slipped CommitTick (the cell didn't tick exactly at CommitTick, e.g.
// under CPU pressure) commits on the next pass — the drain uses <=, not
// ==, so no pending entry is ever stranded.
func (c *Cell) drainPendingPromotes(currentClusterTick uint64) {
	if len(c.pendingPromotes) == 0 {
		return
	}
	for commitTick, list := range c.pendingPromotes {
		if commitTick > currentClusterTick {
			continue
		}
		for _, p := range list {
			// Always materialize from the TransferBlob. If a border-replica
			// for netID already exists on this cell, remove it first so
			// SpawnLiveFromTransfer installs cleanly.
			//
			// Why not promote-in-place? The border-replica holds only the
			// subset of components the source pushes via border-dispatcher
			// (position, velocity, collider, entity kind, replicated game
			// components). It does NOT carry PlayerConn, TransferCooldown,
			// Rotation, or the full TransferFrame-serialized component
			// graph. Promoting in place would leave a player entity without
			// its PlayerConn link — the engine's input router would then
			// fail to find the entity for the connID and the player would
			// appear frozen from the client's perspective. The TransferBlob
			// carries authoritative full state (produced by SerializeEntity
			// on the source at handoff time) and is the single source of
			// truth at commit.
			if len(p.transferBlob) == 0 {
				c.Log.Log(CatMeshTransfer,
					"[%s] commit-tick spawn skipped: netID=%d empty transfer blob",
					c.ID, p.netID)
				continue
			}
			if _, presence, ok := c.Base.LookupNetID(p.netID); ok && presence == PresenceReplica {
				c.Base.RemoveReplicaByNetID(p.netID)
			}
			if _, err := c.Base.SpawnLiveFromTransfer(p.netID, p.epoch, p.transferBlob); err != nil {
				c.Log.Log(CatMeshTransfer,
					"[%s] commit-tick spawn-from-transfer failed: netID=%d err=%v",
					c.ID, p.netID, err)
				continue
			}
			c.Log.Log(CatMeshTransfer,
				"[%s] handoff committed: netID=%d commitTick=%d from=%s",
				c.ID, p.netID, commitTick, p.fromCellID)
		}
		delete(c.pendingPromotes, commitTick)
	}
}

// handoffDriverHost is implemented by bridges that host a HandoffDriver
// (cellBridge and grpcBridge). Used by processMessage to reach the driver
// without importing the concrete bridge type.
type handoffDriverHost interface {
	HandoffDriver() *HandoffDriver
}

// processMessage handles a single inter-cell message.
func (c *Cell) processMessage(msg CellMessage) {
	if c.onMessage != nil {
		c.onMessage(msg)
	}
	switch msg.Type {
	case MsgChat:
		if msg.Chat == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgChat from=%s user=%s", c.ID, msg.FromCellID, msg.Chat.Username)
		c.World.DispatchChat(msg.Chat.Username, msg.Chat.Text)

	case MsgSpawnTransfer:
		if msg.Spawn == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgSpawnTransfer from=%s conn=%d user=%s", c.ID, msg.FromCellID, msg.Spawn.ConnID, msg.Spawn.Username)
		c.Engine.Players.RegisterPlayer(msg.Spawn.ConnID, msg.Spawn.Username)
		if s := c.Engine.Players.ByConnID(msg.Spawn.ConnID); s != nil {
			s.SpawnLocation = msg.Spawn.SpawnLocation
		}

	case MsgPlayerAssignment:
		if msg.Assignment == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgPlayerAssignment conn=%d user=%s reconnect=%v",
			c.ID, msg.Assignment.ConnID, msg.Assignment.Username, msg.Assignment.IsReconnect)
		if msg.Assignment.IsReconnect {
			existing := c.Engine.Players.ByUsername(msg.Assignment.Username)
			if existing != nil && existing.State == engine.StateDisconnected {
				existing.ConnID = msg.Assignment.ConnID
				existing.DisconnectTime = time.Time{}
				existing.SpawnLocation = msg.Assignment.SpawnLocation
				c.Engine.Players.ReconnectSession(existing)
			} else {
				// Lingering session gone — treat as fresh login
				c.Engine.Players.RegisterPlayer(msg.Assignment.ConnID, msg.Assignment.Username)
				if s := c.Engine.Players.ByConnID(msg.Assignment.ConnID); s != nil {
					s.SpawnLocation = msg.Assignment.SpawnLocation
				}
			}
		} else {
			c.Engine.Players.RegisterPlayer(msg.Assignment.ConnID, msg.Assignment.Username)
			if s := c.Engine.Players.ByConnID(msg.Assignment.ConnID); s != nil {
				s.SpawnLocation = msg.Assignment.SpawnLocation
				// Set optional session data from login handler (e.g., skin selection)
				if msg.Assignment.Data != nil {
					s.Data = msg.Assignment.Data
				}
			}
		}

	case MsgCrossCellAction:
		if msg.Action == nil {
			return
		}
		c.Log.Log(CatMeshAction, "[%s] cross-cell action from=%s type=%d targetNetID=%d", c.ID, msg.FromCellID, msg.Action.Type, msg.Action.TargetNetID)
		result := c.World.HandleCrossCellAction(msg.Action)
		if result != nil {
			c.Bridge.SendActionResult(msg.FromCellID, result)
		}

	case MsgActionResult:
		if msg.ActionResult == nil {
			return
		}
		c.Log.Log(CatMeshAction, "[%s] action result from=%s type=%d", c.ID, msg.FromCellID, msg.ActionResult.Type)
		c.World.HandleActionResult(msg.ActionResult)

	case MsgSessionTransfer:
		for _, st := range msg.Sessions {
			c.Log.Log(CatMeshMsg, "[%s] msg MsgSessionTransfer conn=%d user=%s state=%s",
				c.ID, st.ConnID, st.Username, st.StateTag)
			c.Engine.Players.RegisterSessionTransfer(st.ConnID, st.Username, st.StateTag, st.Data)
		}

	case MsgBorderFrame:
		if msg.BorderFrame == nil {
			return
		}
		byteCount := len(msg.BorderFrame)
		frame, err := replication.DecodeFrame(msg.BorderFrame)
		if err != nil {
			c.Log.Log(CatMeshMsg, "[%s] MsgBorderFrame decode error from=%s: %v", c.ID, msg.FromCellID, err)
			return
		}
		if c.Metrics != nil {
			c.Metrics.RecordBorderFrameRecv(byteCount)
		}
		c.Base.ApplyBorderFrame(frame, msg.FromCellID)

	case MsgHandoff:
		if msg.Handoff == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgHandoff from=%s netID=%d epoch=%d commitTick=%d",
			c.ID, msg.FromCellID, msg.Handoff.NetID, msg.Handoff.Epoch, msg.Handoff.CommitTick)

		// Pre-register the player session NOW (before commit-tick) so
		// ClientInput frames arriving in the lead-time window have a
		// destination-local ID to route to. The entity itself is still
		// authoritative on the source until commitTick, but session
		// wiring can happen eagerly — the duplicate-spawn check in
		// SpawnLiveFromTransfer handles the edge case where the commit
		// races with a late input.
		//
		// In node mode, we ALSO need to register a VCM session on this
		// host so subsequent ClientInput frames from the gateway route
		// to the freshly transferred player. The frame carries the
		// client's SessionKey (GatewayID + GatewayConnID) from the
		// source's VCM; we register it here to allocate a DESTINATION-
		// local ID, then use that local ID for the engine player
		// session — NOT the source's local ID, which means nothing on
		// this host. SpawnFromTransferCore performs the matching remap
		// when it decodes the blob (idempotent RegisterSession returns
		// the same localID for the same key).
		if srcConnID, gwID, gwConnID, username := PeekTransferPlayer(msg.Handoff.TransferBlob); srcConnID != 0 {
			localID := srcConnID
			if gwConnID != 0 && c.Base != nil && c.Base.coord != nil && c.Base.coord.vcm != nil {
				key := SessionKey{GatewayID: gwID, ConnID: gwConnID}
				// Pass epoch=0: this is an entity handoff, not a session
				// authority change. Session epoch is owned by sessionRoutes
				// (bumped by Migrate + SessionRegister dispatch). VCM's
				// "never downgrade" logic preserves the real session epoch.
				localID = c.Base.coord.vcm.RegisterSession(key, username, 0, c.ID)
			}
			c.Engine.Players.RegisterTransferSession(localID, username)
		}

		// H2 hard-cut: queue a promote for the CommitTick carried in the
		// payload. drainPendingPromotes (called at the start of PostSystems)
		// drains this when the cluster clock catches up to commitTick, at
		// which point either the existing border-replica is promoted in
		// place or a fresh Live entity is spawned from the TransferBlob.
		if c.pendingPromotes == nil {
			c.pendingPromotes = make(map[uint64][]pendingPromote)
		}
		c.pendingPromotes[msg.Handoff.CommitTick] = append(
			c.pendingPromotes[msg.Handoff.CommitTick],
			pendingPromote{
				netID:        msg.Handoff.NetID,
				epoch:        msg.Handoff.Epoch,
				transferBlob: msg.Handoff.TransferBlob,
				connID:       msg.Handoff.ConnID,
				fromCellID:   msg.FromCellID,
			},
		)
		c.Log.Log(CatMeshTransfer,
			"[%s] handoff queued: netID=%d epoch=%d commitTick=%d from=%s",
			c.ID, msg.Handoff.NetID, msg.Handoff.Epoch, msg.Handoff.CommitTick, msg.FromCellID)

	case MsgForwardInput:
		if msg.ForwardInput == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgForwardInput from=%s conn=%d bytes=%d",
			c.ID, msg.FromCellID, msg.ForwardInput.ConnID, len(msg.ForwardInput.InputBlob))
		// Inject the forwarded input into the local ConnManager's input
		// buffer so it gets processed by the engine's input router on the
		// next tick, as if it had arrived from the player's connection.
		if c.Engine.ConnMgr != nil {
			c.Engine.ConnMgr.InjectInput(msg.ForwardInput.ConnID, msg.ForwardInput.InputBlob)
		}

	case MsgPlayerDisconnected:
		if msg.Disconnect == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgPlayerDisconnected conn=%d reason=%s",
			c.ID, msg.Disconnect.ConnID, msg.Disconnect.Reason)
		// Push a synthetic disconnect event to the cell's Events channel — same
		// path as in-process WebSocket disconnects so the engine's grace-period
		// state machine fires unchanged.
		select {
		case c.Events <- net.PlayerEvent{ConnID: msg.Disconnect.ConnID, Disconnect: true}:
		default:
			c.Log.Log(CatMeshMsg, "[%s] events channel full, dropping MsgPlayerDisconnected conn=%d",
				c.ID, msg.Disconnect.ConnID)
		}
	}
}
