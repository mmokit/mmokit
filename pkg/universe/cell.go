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
	fromCellID   MeshCellID
}

// Cell is a self-contained game simulation owning one cell.
type Cell struct {
	// MeshID is the wire-format ID for this cell (e.g. "cell_0_0"). Used
	// as the key in Process.Cells, Host.OwnedCells, and on the MeshControl
	// wire. For display use cell.Cell.String() instead.
	MeshID  MeshCellID
	Cell    CellID
	Engine  *engine.Engine
	World   GameWorld
	Stage   *Stage // direct access for infrastructure methods
	Loop    *engine.GameLoop
	Bridge  Bridge
	Metrics *metrics.CellMetrics

	Inbox  chan CellMessage
	Events chan net.PlayerEvent
	// Neighbors maps mesh-form neighbor cell IDs to their *Cell pointer.
	// Populated during topology setup; key form matches Process.Cells keys
	// so cross-cell ops (replication, handoff) share the same identifiers.
	Neighbors map[MeshCellID]*Cell
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

	c.Log.Log(CatMeshCell, "[%s] cell started for cell %s", c.MeshID, c.Cell)
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
	c.Log.Log(CatMeshCell, "[%s] cell shutdown complete", c.MeshID)
}

// DrainInbox processes all pending inter-cell messages.
// Called from the game loop via PreTickFunc.
func (c *Cell) DrainInbox() {
	for {
		select {
		case msg := <-c.Inbox:
			c.processMessage(msg)
		default:
			c.Stage.TickGhosts()
			c.Stage.TickTransferCooldowns()
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
			// Materialize from the TransferBlob, which carries authoritative
			// full state from SerializeEntity on the source — crucially
			// including PlayerConn so the engine's input router can find the
			// entity for this connID. A bare Replica→Live promote would drop
			// PlayerConn (not on the border-push set) and leave the player
			// frozen from the client's perspective.
			//
			// BUT: the blob was serialized at crossing-tick (commitTick−2).
			// Between then and now the source has continued to simulate the
			// entity, and the border-dispatcher has been pushing updated
			// Position/Velocity onto the Replica each tick. Naively
			// re-spawning from the blob would rubber-band the client ~2
			// ticks backward.
			//
			// So: capture the Replica's current Position/Velocity/Rotation
			// FIRST, then replace-spawn from the blob, then overwrite with
			// the captured tip-of-motion. Fast-mover case (no pre-existing
			// replica) falls through to plain spawn-from-blob.
			if len(p.transferBlob) == 0 {
				c.Log.Log(CatMeshTransfer,
					"[%s] commit-tick spawn skipped: netID=%d empty transfer blob",
					c.MeshID, p.netID)
				continue
			}
			var (
				hasRecent      bool
				recentPosX     float32
				recentPosY     float32
				recentVelX     float32
				recentVelY     float32
				recentAngle    float32
				hasRecentRot   bool
				recentCellX    int32
				recentCellY    int32
				hasRecentCC    bool
				recentStampMs  uint64
				hasRecentStamp bool
			)
			if ent, presence, ok := c.Stage.LookupNetID(p.netID); ok && presence == PresenceReplica {
				if c.Stage.posMap.HasAll(ent) {
					pos := c.Stage.posMap.Get(ent)
					recentPosX = pos.X
					recentPosY = pos.Y
					hasRecent = true
				}
				if c.Stage.velMap.HasAll(ent) {
					vel := c.Stage.velMap.Get(ent)
					recentVelX = vel.X
					recentVelY = vel.Y
				}
				if c.Stage.rotMap.HasAll(ent) {
					recentAngle = c.Stage.rotMap.Get(ent).Angle
					hasRecentRot = true
				}
				if c.Stage.cellMap.HasAll(ent) {
					cc := c.Stage.cellMap.Get(ent)
					recentCellX = cc.CellX
					recentCellY = cc.CellY
					hasRecentCC = true
				}
				if c.Stage.replicaMap.HasAll(ent) {
					rep := c.Stage.replicaMap.Get(ent)
					recentStampMs = rep.ProducedAtMs
					hasRecentStamp = rep.ProducedAtMs > 0
				}
				c.Stage.RemoveReplicaByNetID(p.netID)
			}
			newEnt, err := c.Stage.SpawnLiveFromTransfer(p.netID, p.epoch, p.transferBlob)
			if err != nil {
				c.Log.Log(CatMeshTransfer,
					"[%s] commit-tick spawn-from-transfer failed: netID=%d err=%v",
					c.MeshID, p.netID, err)
				continue
			}
			// Overwrite the blob's stale motion state with the Replica's
			// tip-of-motion so the client experiences no rubber-band at
			// the commit boundary.
			//
			// Then sub-tick forward extrapolation: the captured tip is
			// only as recent as the most recent border-push that
			// reached dest's inbox before dest's PostSystems ran. If
			// dest's tick fired before source's tick-C push landed,
			// the tip is a full tick behind — dest would spawn at
			// source_pos_(C-1), physics-advance one tick on dest's
			// next Systems pass, and emit source_pos_(C) to the client
			// at stamp (C+1)·50. That matches source's last sample at
			// (C·50, source_pos_C) → client sees 50 ms of stamp time
			// with zero motion, the visible stutter.
			//
			// ClusterClock.TickTime(now) − Replica.ProducedAtMs tells
			// us how stale the tip is; advance pos by velocity·that
			// delta. For the own-player (constant velocity) this lands
			// the spawn at source's commit-tick pos exactly. For a
			// decelerating bot the extrapolation overshoots by
			// deceleration·(stagger²)/2 — bounded by one tick interval
			// of stagger (<50 ms), so worst-case overshoot is a
			// fraction of a unit.
			if hasRecent {
				posX := recentPosX
				posY := recentPosY
				if hasRecentStamp && c.Stage.clusterClock != nil {
					now := c.Stage.clusterClock.TickTime(c.Stage.eng.TickIntervalMs())
					if now > recentStampMs {
						aheadS := float32(now-recentStampMs) / 1000.0
						posX += recentVelX * aheadS
						posY += recentVelY * aheadS
					}
				}
				if c.Stage.posMap.HasAll(newEnt) {
					pos := c.Stage.posMap.Get(newEnt)
					pos.X = posX
					pos.Y = posY
				}
				if c.Stage.velMap.HasAll(newEnt) {
					vel := c.Stage.velMap.Get(newEnt)
					vel.X = recentVelX
					vel.Y = recentVelY
				}
				if hasRecentRot && c.Stage.rotMap.HasAll(newEnt) {
					c.Stage.rotMap.Get(newEnt).Angle = recentAngle
				}
				if hasRecentCC && c.Stage.cellMap.HasAll(newEnt) {
					cc := c.Stage.cellMap.Get(newEnt)
					cc.CellX = recentCellX
					cc.CellY = recentCellY
				}
			}
			c.Log.Log(CatMeshTransfer,
				"[%s] handoff committed: netID=%d commitTick=%d from=%s",
				c.MeshID, p.netID, commitTick, p.fromCellID)
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
		c.Log.Log(CatMeshMsg, "[%s] msg MsgChat from=%s user=%s", c.MeshID, msg.FromCellID, msg.Chat.Username)
		c.World.DispatchChat(msg.Chat.Username, msg.Chat.Text)

	case MsgSpawnTransfer:
		if msg.Spawn == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgSpawnTransfer from=%s conn=%d user=%s", c.MeshID, msg.FromCellID, msg.Spawn.ConnID, msg.Spawn.Username)
		c.Engine.Players.RegisterPlayer(msg.Spawn.ConnID, msg.Spawn.Username)
		if s := c.Engine.Players.ByConnID(msg.Spawn.ConnID); s != nil {
			s.SpawnLocation = msg.Spawn.SpawnLocation
		}

	case MsgPlayerAssignment:
		if msg.Assignment == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgPlayerAssignment conn=%d user=%s reconnect=%v",
			c.MeshID, msg.Assignment.ConnID, msg.Assignment.Username, msg.Assignment.IsReconnect)
		if msg.Assignment.IsReconnect {
			existing := c.Engine.Players.ByUsername(msg.Assignment.Username)
			if existing != nil && existing.State == engine.StateDisconnected {
				existing.ConnID = msg.Assignment.ConnID
				existing.DisconnectTime = time.Time{}
				existing.SpawnLocation = msg.Assignment.SpawnLocation
				c.Engine.Players.ReconnectSession(existing)
				// ReconnectSession transitions to PriorState. For PriorState =
				// StateActive, OnEnter(StateActive) fires the join hooks
				// (which call game-specific reconnectPlayer / send
				// SE_PLAYER_SPAWNED). For non-Active resume states (Docked,
				// Dead, Docking) OnEnter doesn't fire the join hooks, so the
				// game would never get a chance to send welcome-back
				// messages — the client would sit there with no entity ID
				// and no state-specific UI cue (bank panel, dead screen,
				// etc.). Fire the hooks explicitly so the game can dispatch
				// per-state on reconnect.
				if existing.State != engine.StateActive && c.Stage != nil && c.Stage.coord != nil {
					c.Stage.coord.fireJoinHooks(existing, c.Stage)
				}
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
				s.UserID = msg.Assignment.UserID
				s.SpawnLocation = msg.Assignment.SpawnLocation
			}
		}

	case MsgCrossCellAction:
		if msg.Action == nil {
			return
		}
		c.Log.Log(CatMeshAction, "[%s] cross-cell action from=%s type=%d targetNetID=%d", c.MeshID, msg.FromCellID, msg.Action.Type, msg.Action.TargetNetID)
		result := c.World.HandleCrossCellAction(msg.Action)
		if result != nil {
			c.Bridge.SendActionResult(msg.FromCellID, result)
		}

	case MsgActionResult:
		if msg.ActionResult == nil {
			return
		}
		c.Log.Log(CatMeshAction, "[%s] action result from=%s type=%d", c.MeshID, msg.FromCellID, msg.ActionResult.Type)
		c.World.HandleActionResult(msg.ActionResult)

	case MsgSessionTransfer:
		for _, st := range msg.Sessions {
			c.Log.Log(CatMeshMsg, "[%s] msg MsgSessionTransfer conn=%d user=%s state=%s",
				c.MeshID, st.ConnID, st.Username, st.StateTag)
			c.Engine.Players.RegisterSessionTransfer(st.ConnID, st.Username, st.StateTag, st.Data)
		}

	case MsgBorderFrame:
		if msg.BorderFrame == nil {
			return
		}
		byteCount := len(msg.BorderFrame)
		frame, err := replication.DecodeFrame(msg.BorderFrame)
		if err != nil {
			c.Log.Log(CatMeshMsg, "[%s] MsgBorderFrame decode error from=%s: %v", c.MeshID, msg.FromCellID, err)
			return
		}
		if c.Metrics != nil {
			c.Metrics.RecordBorderFrameRecv(byteCount)
		}
		c.Stage.ApplyBorderFrame(frame, msg.FromCellID)

	case MsgHandoff:
		if msg.Handoff == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgHandoff from=%s netID=%d epoch=%d commitTick=%d",
			c.MeshID, msg.FromCellID, msg.Handoff.NetID, msg.Handoff.Epoch, msg.Handoff.CommitTick)

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
			if gwConnID != 0 && c.Stage != nil && c.Stage.coord != nil && c.Stage.coord.vcm != nil {
				key := SessionKey{GatewayID: gwID, ConnID: gwConnID}
				// Pass epoch=0: this is an entity handoff, not a session
				// authority change. Session epoch is owned by sessionRoutes
				// (bumped by Migrate + SessionRegister dispatch). VCM's
				// "never downgrade" logic preserves the real session epoch.
				localID = c.Stage.coord.vcm.RegisterSession(key, username, 0, c.MeshID)
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
			c.MeshID, msg.Handoff.NetID, msg.Handoff.Epoch, msg.Handoff.CommitTick, msg.FromCellID)

	case MsgForwardInput:
		if msg.ForwardInput == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgForwardInput from=%s conn=%d bytes=%d",
			c.MeshID, msg.FromCellID, msg.ForwardInput.ConnID, len(msg.ForwardInput.InputBlob))
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
			c.MeshID, msg.Disconnect.ConnID, msg.Disconnect.Reason)
		// Push a synthetic disconnect event to the cell's Events channel — same
		// path as in-process WebSocket disconnects so the engine's grace-period
		// state machine fires unchanged.
		select {
		case c.Events <- net.PlayerEvent{ConnID: msg.Disconnect.ConnID, Disconnect: true}:
		default:
			c.Log.Log(CatMeshMsg, "[%s] events channel full, dropping MsgPlayerDisconnected conn=%d",
				c.MeshID, msg.Disconnect.ConnID)
		}
	}
}
