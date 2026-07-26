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
	Stage   *Stage // direct access for infrastructure methods
	Loop    *engine.GameLoop
	Bridge  Bridge
	Metrics *metrics.CellMetrics

	Inbox  chan CellMessage
	Events chan net.PlayerEvent
	// Neighbors maps mesh-form neighbor cell IDs to their *Cell pointer.
	// Runtime reads and writes are guarded by Process.mu; initial Build wiring
	// happens before cell loops start. Key form matches Process.Cells keys so
	// cross-cell ops (replication, handoff) share the same identifiers.
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
	c.Stage.Shutdown()
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
				// De-stale state beyond motion: the blob captured every
				// component at crossing-tick (commitTick−lead), but border
				// replication kept the replica current up to commit. Capture
				// the replica's fresh values for ALL border-replicated
				// components so the first post-handoff frame doesn't serve a
				// stale value that then snaps — e.g. a WASM-animated
				// Collider.Radius (the pulse demo's visible "size jumps at
				// the cell line" bug) or game Health/Shield.
				recentRadius    float32
				hasRecentRadius bool
				recentComps     []ComponentSlice
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
				// Collider: copy only Radius — border frames carry only the
				// (animatable) radius, so the replica's Width/Height/Layer/
				// Shape are zero. The blob has the correct static fields; we
				// only de-stale the animated radius.
				if c.Stage.colliderMap.HasAll(ent) {
					recentRadius = c.Stage.colliderMap.Get(ent).Radius
					hasRecentRadius = true
				}
				// Every non-core registered component the replica carries was
				// refreshed by the border-frame component tail each tick, so
				// the replica's value is fresher than the blob's. Capture it
				// for re-apply after the blob spawn. (Core components — Pos/
				// Vel/Rot/CellCoord — are handled by the motion capture above.)
				if reg := c.Stage.ReplicationRegistry(); reg != nil {
					for _, rep := range reg.All() {
						if rep.IsTransferCore || rep.Scan == nil {
							continue
						}
						if d := rep.Scan(ent); d != nil {
							recentComps = append(recentComps, ComponentSlice{ID: rep.ID, Data: d})
						}
					}
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
			// De-stale non-motion components from the replica's tip too — the
			// blob's values are crossing-tick stale (commitTick−lead). Without
			// this, any per-tick-changing component is served stale on the
			// first post-handoff frame and snaps a tick later (the pulse
			// demo's visible size jump on its WASM-animated Collider.Radius).
			if hasRecentRadius && c.Stage.colliderMap.HasAll(newEnt) {
				c.Stage.colliderMap.Get(newEnt).Radius = recentRadius
			}
			if len(recentComps) > 0 {
				if reg := c.Stage.ReplicationRegistry(); reg != nil {
					for _, cs := range recentComps {
						if rep := reg.Get(cs.ID); rep != nil && rep.Apply != nil {
							rep.Apply(newEnt, cs.Data)
						}
					}
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

// handoffDriver returns this cell's HandoffDriver, or nil when the bridge
// does not host one (NoopBridge, test bridges). Nil-safe on every field.
func (c *Cell) handoffDriver() *HandoffDriver {
	if c == nil || c.Bridge == nil {
		return nil
	}
	if h, ok := c.Bridge.(handoffDriverHost); ok {
		return h.HandoffDriver()
	}
	return nil
}

// processMessage handles a single inter-cell message.
func (c *Cell) processMessage(msg CellMessage) {
	if c.onMessage != nil {
		c.onMessage(msg)
	}
	switch msg.Type {
	case MsgSpawnTransfer:
		if msg.Spawn == nil {
			return
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgSpawnTransfer from=%s conn=%d user=%s", c.MeshID, msg.FromCellID, msg.Spawn.ConnID, msg.Spawn.Username)
		c.Engine.Players.RegisterPlayer(msg.Spawn.ConnID, msg.Spawn.Username)
		if s := c.Engine.Players.ByConnID(msg.Spawn.ConnID); s != nil {
			s.StreamGeneration = msg.Spawn.StreamGeneration
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
				existing.StreamGeneration = msg.Assignment.StreamGeneration
				existing.UserID = msg.Assignment.UserID
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
					s.StreamGeneration = msg.Assignment.StreamGeneration
					s.UserID = msg.Assignment.UserID
					s.SpawnLocation = msg.Assignment.SpawnLocation
				}
			}
		} else {
			c.Engine.Players.RegisterPlayer(msg.Assignment.ConnID, msg.Assignment.Username)
			if s := c.Engine.Players.ByConnID(msg.Assignment.ConnID); s != nil {
				s.StreamGeneration = msg.Assignment.StreamGeneration
				s.UserID = msg.Assignment.UserID
				s.SpawnLocation = msg.Assignment.SpawnLocation
			}
		}

	case MsgCrossCellAction:
		if msg.Action == nil {
			return
		}
		c.Log.Log(CatMeshAction, "[%s] cross-cell action from=%s type=%d targetNetID=%d", c.MeshID, msg.FromCellID, msg.Action.Type, msg.Action.TargetNetID)
		if !c.Stage.HandleEngineAction(msg.Action) {
			c.Log.Log(CatMeshAction, "[%s] cross-cell action: unhandled type=%d from=%s (no engine handler)", c.MeshID, msg.Action.Type, msg.FromCellID)
		}

	case MsgSessionTransfer:
		for _, st := range msg.Sessions {
			c.Log.Log(CatMeshMsg, "[%s] msg MsgSessionTransfer conn=%d user=%s state=%s",
				c.MeshID, st.ConnID, st.Username, st.StateTag)
			localID, err := destinationConnIDForSessionTransfer(c, st)
			if err != nil {
				c.Log.Log(CatMeshMsg, "[%s] MsgSessionTransfer remap failed: %v", c.MeshID, err)
				continue
			}
			c.Engine.Players.RegisterSessionTransfer(localID, st.Username, st.StateTag, st.Data)
			if sess := c.Engine.Players.ByConnID(localID); sess != nil {
				sess.StreamGeneration = st.StreamGeneration
				sess.UserID = st.UserID
			}
			if c.Stage != nil && c.Stage.coord != nil {
				c.Stage.coord.touchActiveUser(st.UserID, st.Username, st.GatewayID, st.GatewayConnID,
					c.Stage.coord.HostForCellID(c.MeshID), c.MeshID)
			}
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

		ack := &HandoffAckPayload{
			NetID:      msg.Handoff.NetID,
			Epoch:      msg.Handoff.Epoch,
			CommitTick: msg.Handoff.CommitTick,
		}

		// No bridge means no way to ack, and an unacknowledged promote would
		// make this cell authoritative with the source unable to learn it.
		if c.Bridge == nil {
			c.Log.Log(CatMeshTransfer,
				"[%s] handoff dropped (no bridge to ack through): netID=%d epoch=%d from=%s",
				c.MeshID, msg.Handoff.NetID, msg.Handoff.Epoch, msg.FromCellID)
			return
		}

		// Dedup on (NetID, Epoch). The source re-sends the identical payload
		// every HandoffAcceptRetryTicks until it sees an ack, so duplicates
		// are the normal case under ack loss — not an error. Re-ack (the ack
		// is idempotent) and do nothing else: in particular do NOT re-run the
		// eager session pre-register below against a session that may already
		// have transferred out again.
		if c.Stage != nil && c.Stage.handoffAlreadyAccepted(msg.Handoff.NetID, msg.Handoff.Epoch) {
			c.Bridge.SendHandoffAccepted(msg.FromCellID, ack)
			c.Log.Log(CatMeshTransfer,
				"[%s] duplicate handoff re-acked: netID=%d epoch=%d from=%s",
				c.MeshID, msg.Handoff.NetID, msg.Handoff.Epoch, msg.FromCellID)
			return
		}

		// Send the acceptance BEFORE committing to the promote. If the ack
		// cannot be enqueued, this cell must not become authoritative: the
		// source would never learn to demote and the entity would be Live on
		// two cells with no protocol left to retire either. Dropping here is
		// safe and self-healing — the source retries the identical Handoff.
		if !c.Bridge.SendHandoffAccepted(msg.FromCellID, ack) {
			c.Log.Log(CatMeshTransfer,
				"[%s] handoff ack undeliverable — dropping, source will retry: netID=%d epoch=%d from=%s",
				c.MeshID, msg.Handoff.NetID, msg.Handoff.Epoch, msg.FromCellID)
			return
		}
		if c.Stage != nil {
			c.Stage.markHandoffAccepted(msg.Handoff.NetID, msg.Handoff.Epoch)
		}

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
		if srcConnID, streamGeneration, gwID, gwConnID, username, userID := PeekTransferPlayer(msg.Handoff.TransferBlob); srcConnID != 0 {
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
			// Stamp the auth identity on the just-registered session.
			// Without this, the next handoff out of this cell panics in
			// PlayerRepo.Bind on a zero UserID (the source-cell
			// removeFromWorld action calls SavePlayerState, which Binds).
			if s := c.Engine.Players.ByConnID(localID); s != nil {
				s.StreamGeneration = streamGeneration
				s.UserID = userID
			}
			// Refresh the cluster-wide UUID-keyed activeUsers entry so
			// a tab refresh post-transfer routes to the reconnect path
			// instead of fresh-login spawn (which would duplicate the
			// player entity for the 30-second grace window).
			if c.Stage != nil && c.Stage.coord != nil {
				c.Stage.coord.touchActiveUser(userID, username, gwID, gwConnID, c.Stage.coord.HostForCellID(c.MeshID), c.MeshID)
			}
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

	case MsgHandoffAccepted:
		if msg.HandoffAck == nil {
			return
		}
		// Runs on the cell loop via PreTick -> DrainInbox, strictly before
		// PostSystems -> HandoffDriver.Tick, so an ack ingested this tick can
		// arm a demote in the same tick.
		hd := c.handoffDriver()
		if hd == nil {
			c.Log.Log(CatMeshTransfer,
				"[%s] handoff-ack dropped (no handoff driver): netID=%d epoch=%d from=%s",
				c.MeshID, msg.HandoffAck.NetID, msg.HandoffAck.Epoch, msg.FromCellID)
			return
		}
		hd.OnHandoffAccepted(msg.HandoffAck.NetID, msg.HandoffAck.Epoch, msg.HandoffAck.CommitTick, msg.FromCellID)

	case MsgForwardInput:
		if msg.ForwardInput == nil {
			return
		}
		localConnID := msg.ForwardInput.ConnID
		if msg.ForwardInput.GatewayID != "" {
			if c.Stage == nil || c.Stage.coord == nil || c.Stage.coord.vcm == nil {
				c.Log.Log(CatMeshMsg, "[%s] MsgForwardInput has gateway session but no VCM: gw=%s conn=%d",
					c.MeshID, msg.ForwardInput.GatewayID, msg.ForwardInput.ConnID)
				return
			}
			var ok bool
			localConnID, ok = c.Stage.coord.vcm.LookupByKey(SessionKey{
				GatewayID: msg.ForwardInput.GatewayID,
				ConnID:    msg.ForwardInput.ConnID,
			})
			if !ok {
				c.Log.Log(CatMeshMsg, "[%s] MsgForwardInput for unknown destination session: gw=%s conn=%d",
					c.MeshID, msg.ForwardInput.GatewayID, msg.ForwardInput.ConnID)
				return
			}
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgForwardInput from=%s conn=%d bytes=%d",
			c.MeshID, msg.FromCellID, localConnID, len(msg.ForwardInput.InputBlob))
		// Cross-host forwards use the same session-epoch gate as ordinary
		// gateway traffic. Same-host/in-process frames have no GatewayID and
		// already carry a destination-local connID.
		if msg.ForwardInput.GatewayID != "" && c.Stage.coord.vcm != nil {
			c.Stage.coord.vcm.InjectForwardedInputWithEpoch(
				localConnID,
				msg.ForwardInput.InputBlob,
				msg.ForwardInput.SessionEpoch,
				net.ChannelEvent,
			)
		} else if c.Engine.ConnMgr != nil {
			c.Engine.ConnMgr.InjectInput(localConnID, msg.ForwardInput.InputBlob)
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
