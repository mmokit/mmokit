package universe

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/metrics"
	"github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/replication"
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

// cellIdentity is a Cell's wire and coordinate identity, held as one
// immutable value so the two can never be observed out of step.
//
// A rename (merge commit, migrate) swaps the whole record atomically rather
// than writing the fields in place. That is the point: the previous shape
// wrote MeshID and Cell separately, off any shared lock, while
// Host.CellByID read MeshID from a gRPC goroutine — a real data race, and
// one where even a correctly-locked reader could have seen the new mesh ID
// paired with the old coordinate.
type cellIdentity struct {
	meshID MeshCellID
	cell   CellID
}

// newCellIdentity builds the initial identity for a Cell being constructed.
// Every Cell must have one installed before it is published anywhere another
// goroutine can reach it.
func newCellIdentity(mesh MeshCellID, id CellID) *cellIdentity {
	return &cellIdentity{meshID: mesh, cell: id}
}

// MeshID returns the wire-format ID for this cell (e.g. "cell_0_0"). Used as
// the key in Process.Cells, Host.OwnedCells, and on the MeshControl wire. For
// display use CellID().String() instead.
//
// Safe from any goroutine. Returns the zero value for a Cell whose identity
// was never installed, which only happens in a malformed test fixture.
func (c *Cell) MeshID() MeshCellID {
	id := c.identity.Load()
	if id == nil {
		return ""
	}
	return id.meshID
}

// CellID returns this cell's coordinate identity. Safe from any goroutine,
// and guaranteed to correspond to the same identity generation as a MeshID()
// read that observed the same swap.
func (c *Cell) CellID() CellID {
	id := c.identity.Load()
	if id == nil {
		return CellID{}
	}
	return id.cell
}

// Identity returns both halves from a SINGLE atomic load, so callers that
// need them together cannot straddle a rename.
func (c *Cell) Identity() (MeshCellID, CellID) {
	id := c.identity.Load()
	if id == nil {
		return "", CellID{}
	}
	return id.meshID, id.cell
}

// setIdentity atomically replaces this cell's identity. The only caller is
// the rename path; it deliberately publishes a fresh record rather than
// mutating the current one, so a concurrent reader sees either the whole old
// identity or the whole new one.
func (c *Cell) setIdentity(mesh MeshCellID, id CellID) {
	c.identity.Store(newCellIdentity(mesh, id))
}

// Cell is a self-contained game simulation owning one cell.
type Cell struct {
	// identity is this cell's (MeshID, CellID) pair. Read it through
	// MeshID() / CellID(); replace it only through setIdentity. Never add a
	// mutable identity field back — off-loop readers (the mesh data plane,
	// admin views, the transfer executor) hold no lock that a rename
	// respects.
	identity atomic.Pointer[cellIdentity]

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
	// next test's setup.
	runMu     sync.Mutex
	runCancel context.CancelFunc // nil until Run starts; reset when Run exits
	runDone   chan struct{}      // closed when Run returns

	// recoveredPanics counts inbox messages whose handling panicked and was
	// recovered — by guardDecode (harmless, one frame lost) or by
	// processMessageGuarded (not harmless; see its doc comment).
	recoveredPanics atomic.Uint64

	// integrityReassert single-flights the post-recovery invariant check so
	// a flood of malformed frames cannot schedule unbounded ECS scans.
	integrityReassert atomic.Bool
}

// NewCell constructs a Cell with its immutable identity installed. Callers
// populate the remaining fields (Engine, Stage, Inbox, ...) before the cell is
// published anywhere another goroutine can reach it.
//
// This exists because identity is deliberately not a settable field: a Cell
// with no identity, or one whose mesh ID and coordinate disagree, is a bug
// that used to be expressible in a struct literal.
func NewCell(mesh MeshCellID, id CellID) *Cell {
	c := &Cell{}
	c.setIdentity(mesh, id)
	return c
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

	c.Log.Log(CatMeshCell, "[%s] cell started for cell %s", c.MeshID(), c.CellID())
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
	c.Log.Log(CatMeshCell, "[%s] cell shutdown complete", c.MeshID())
}

// DrainInbox processes all pending inter-cell messages.
// Called from the game loop via PreTickFunc.
func (c *Cell) DrainInbox() {
	for {
		select {
		case msg := <-c.Inbox:
			c.processMessageGuarded(msg)
		default:
			c.Stage.TickGhosts()
			c.Stage.TickTransferCooldowns()
			return
		}
	}
}

// processMessageGuarded is the last-resort barrier around one inbox message.
//
// It exists because gl.tick runs bare: DrainInbox is reached from
// cellBridge.PreTick, so a panic anywhere under processMessage unwinds the cell
// loop goroutine and kills the process. A peer frame is the input, so that is a
// remote kill.
//
// Chosen semantics, stated because the safe scope here is not obvious:
//
//   - The DECODE steps carry their own scoped barriers (guardDecode below).
//     Those are pure — they touch no engine state — so recovering one loses
//     exactly one frame and nothing else is in doubt.
//   - This outer barrier can only fire for a panic in a MUTATING arm, and a
//     mid-handler unwind there is genuinely dangerous: MsgSpawnTransfer
//     registers a player and then mutates the returned session, and MsgHandoff
//     acks, marks handoffAccepted, pre-registers the session, and queues a
//     promote in sequence. Unwinding between any two of those leaves a
//     half-registered player, or a netID that will hold both a Live and a
//     Replica slot.
//
// So a recovery here is NOT treated as handled. It bumps a counter and forces
// a cluster-integrity re-assert through the configured InvariantMode, which
// under the dev/test default (InvariantPanic) fails loudly and under the
// production default (InvariantLog) records the violation. The alternative —
// swallowing it — trades a visible crash for an invisible split-brain, which is
// the worse failure. Keeping the loop alive is what buys the process the chance
// to report the inconsistency at all.
func (c *Cell) processMessageGuarded(msg CellMessage) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		c.recoveredPanics.Add(1)
		metrics.Ingress().RecordRejected(metrics.SurfaceMesh, metrics.ReasonPanicRecovered)
		c.Log.Log(CatMeshMsg,
			"[%s] processMessage panic (type=%d from=%s): %v — cell loop survived, forcing integrity re-assert",
			c.MeshID(), msg.Type, msg.FromCellID, r)
		c.requestIntegrityReassert(msg.Type)
	}()
	c.processMessage(msg)
}

// RecoveredMessagePanics returns how many inbox messages this cell recovered a
// panic from. Non-zero means at least one mesh message left engine state in an
// unverified condition; see processMessageGuarded.
func (c *Cell) RecoveredMessagePanics() uint64 { return c.recoveredPanics.Load() }

// requestIntegrityReassert schedules a cluster-integrity check after a
// recovered handler panic.
//
// Off the cell loop on purpose: defaultInvariants scans every cell's ECS
// through RunOnLoop with a one-second timeout each, which must not run inside
// the tick it is reporting on. Single-flight on purpose too: the panics that
// reach here are peer-triggered, so an unguarded check-per-panic would let a
// malformed-frame flood schedule unbounded scans — converting a DoS fix into a
// DoS vector.
func (c *Cell) requestIntegrityReassert(msgType MsgType) {
	if c.Stage == nil || c.Stage.coord == nil {
		return
	}
	if !c.integrityReassert.CompareAndSwap(false, true) {
		return
	}
	proc := c.Stage.coord
	meshID := c.MeshID()
	go func() {
		defer c.integrityReassert.Store(false)
		proc.CheckInvariants(defaultInvariants,
			fmt.Sprintf("recovered processMessage panic on %s (msg type %d)", meshID, msgType))
	}()
}

// guardDecode runs a pure decode/deserialize step behind a recover barrier and
// reports whether it completed. Scoped to steps that read wire bytes and touch
// no engine state, so a recovered panic costs exactly the offending frame and
// leaves nothing half-applied — unlike the mutating arms, which fall through to
// processMessageGuarded and its integrity re-assert.
func (c *Cell) guardDecode(step string, fn func()) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			c.recoveredPanics.Add(1)
			metrics.Ingress().RecordRejected(metrics.SurfaceMesh, metrics.ReasonPanicRecovered)
			c.Log.Log(CatMeshMsg, "[%s] %s decode panic, dropping frame: %v", c.MeshID(), step, r)
			ok = false
		}
	}()
	fn()
	return true
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
					c.MeshID(), p.netID)
				continue
			}
			var (
				hasRecent      bool
				recentPosX     float32
				recentPosY     float32
				recentPosZ     float32
				recentVelX     float32
				recentVelY     float32
				recentVelZ     float32
				recentRot      component.Rotation
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
					recentPosZ = pos.Z
					hasRecent = true
				}
				if c.Stage.velMap.HasAll(ent) {
					vel := c.Stage.velMap.Get(ent)
					recentVelX = vel.X
					recentVelY = vel.Y
					recentVelZ = vel.Z
				}
				if c.Stage.rotMap.HasAll(ent) {
					// The WHOLE rotation, not its yaw. Capturing yaw here and
					// SetYaw-ing it below destroyed pitch and roll on every
					// boundary handoff — invisible in a 2D profile, total
					// orientation loss at every cell line in a 3D one.
					recentRot = *c.Stage.rotMap.Get(ent)
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
					c.MeshID(), p.netID, err)
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
				posZ := recentPosZ
				if hasRecentStamp && c.Stage.clusterClock != nil {
					now := c.Stage.clusterClock.TickTime(c.Stage.eng.TickIntervalMs())
					if now > recentStampMs {
						aheadS := float32(now-recentStampMs) / 1000.0
						posX += recentVelX * aheadS
						posY += recentVelY * aheadS
						posZ += recentVelZ * aheadS
					}
				}
				if c.Stage.posMap.HasAll(newEnt) {
					pos := c.Stage.posMap.Get(newEnt)
					pos.X = posX
					pos.Y = posY
					pos.Z = posZ
				}
				if c.Stage.velMap.HasAll(newEnt) {
					vel := c.Stage.velMap.Get(newEnt)
					vel.X = recentVelX
					vel.Y = recentVelY
					vel.Z = recentVelZ
				}
				if hasRecentRot && c.Stage.rotMap.HasAll(newEnt) {
					*c.Stage.rotMap.Get(newEnt) = recentRot
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
							// De-staling is best-effort: a refused blob leaves
							// this one component at its (older) transfer-blob
							// value rather than the replica's tip, which is
							// exactly the divergence noteComponentDecodeDrop
							// trades for keeping the promoted entity.
							if err := rep.Apply(newEnt, cs.Data); err != nil {
								noteComponentDecodeDrop("handoff de-stale", cs.ID, err)
							}
						}
					}
				}
			}
			c.Log.Log(CatMeshTransfer,
				"[%s] handoff committed: netID=%d commitTick=%d from=%s",
				c.MeshID(), p.netID, commitTick, p.fromCellID)
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
		c.Log.Log(CatMeshMsg, "[%s] msg MsgSpawnTransfer from=%s conn=%d user=%s", c.MeshID(), msg.FromCellID, msg.Spawn.ConnID, msg.Spawn.Username)
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
			c.MeshID(), msg.Assignment.ConnID, msg.Assignment.Username, msg.Assignment.IsReconnect)
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
		c.Log.Log(CatMeshAction, "[%s] cross-cell action from=%s type=%d targetNetID=%d", c.MeshID(), msg.FromCellID, msg.Action.Type, msg.Action.TargetNetID)
		if !c.Stage.HandleEngineAction(msg.Action) {
			c.Log.Log(CatMeshAction, "[%s] cross-cell action: unhandled type=%d from=%s (no engine handler)", c.MeshID(), msg.Action.Type, msg.FromCellID)
		}

	case MsgSessionTransfer:
		for _, st := range msg.Sessions {
			c.Log.Log(CatMeshMsg, "[%s] msg MsgSessionTransfer conn=%d user=%s state=%s",
				c.MeshID(), st.ConnID, st.Username, st.StateTag)
			localID, err := destinationConnIDForSessionTransfer(c, st)
			if err != nil {
				c.Log.Log(CatMeshMsg, "[%s] MsgSessionTransfer remap failed: %v", c.MeshID(), err)
				continue
			}
			c.Engine.Players.RegisterSessionTransfer(localID, st.Username, st.StateTag, st.Data)
			if sess := c.Engine.Players.ByConnID(localID); sess != nil {
				sess.StreamGeneration = st.StreamGeneration
				sess.UserID = st.UserID
			}
			if c.Stage != nil && c.Stage.coord != nil {
				c.Stage.coord.touchActiveUser(st.UserID, st.Username, st.GatewayID, st.GatewayConnID,
					c.Stage.coord.HostForCellID(c.MeshID()), c.MeshID())
			}
		}

	case MsgBorderFrame:
		if msg.BorderFrame == nil {
			return
		}
		byteCount := len(msg.BorderFrame)
		var frame replication.Frame
		var err error
		// Pure decode: DecodeFrame reads wire-supplied lengths and touches
		// no engine state, so a recovered panic here costs one border frame
		// and nothing else. The neighbour re-sends every tick.
		if !c.guardDecode("MsgBorderFrame", func() {
			frame, err = replication.DecodeFrame(msg.BorderFrame)
		}) {
			return
		}
		if err != nil {
			c.Log.Log(CatMeshMsg, "[%s] MsgBorderFrame decode error from=%s: %v", c.MeshID(), msg.FromCellID, err)
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
			c.MeshID(), msg.FromCellID, msg.Handoff.NetID, msg.Handoff.Epoch, msg.Handoff.CommitTick)

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
				c.MeshID(), msg.Handoff.NetID, msg.Handoff.Epoch, msg.FromCellID)
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
				c.MeshID(), msg.Handoff.NetID, msg.Handoff.Epoch, msg.FromCellID)
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
				c.MeshID(), msg.Handoff.NetID, msg.Handoff.Epoch, msg.FromCellID)
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
		// Pure decode of the transfer blob's session prefix, scoped so a
		// malformed blob loses this handoff instead of unwinding between
		// the ack we just sent and the promote queued below — the exact
		// mid-handler unwind that would leave a half-registered player.
		var srcConnID, streamGeneration, gwConnID uint32
		var gwID, username string
		var userID uuid.UUID
		if !c.guardDecode("MsgHandoff transfer blob", func() {
			srcConnID, streamGeneration, gwID, gwConnID, username, userID = PeekTransferPlayer(msg.Handoff.TransferBlob)
		}) {
			return
		}
		if srcConnID != 0 {
			localID := srcConnID
			if gwConnID != 0 && c.Stage != nil && c.Stage.coord != nil && c.Stage.coord.vcm != nil {
				key := SessionKey{GatewayID: gwID, ConnID: gwConnID}
				// Pass epoch=0: this is an entity handoff, not a session
				// authority change. Session epoch is owned by sessionRoutes
				// (bumped by Migrate + SessionRegister dispatch). VCM's
				// "never downgrade" logic preserves the real session epoch.
				localID = c.Stage.coord.vcm.RegisterSession(key, username, 0, c.MeshID())
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
				c.Stage.coord.touchActiveUser(userID, username, gwID, gwConnID, c.Stage.coord.HostForCellID(c.MeshID()), c.MeshID())
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
			c.MeshID(), msg.Handoff.NetID, msg.Handoff.Epoch, msg.Handoff.CommitTick, msg.FromCellID)

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
				c.MeshID(), msg.HandoffAck.NetID, msg.HandoffAck.Epoch, msg.FromCellID)
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
					c.MeshID(), msg.ForwardInput.GatewayID, msg.ForwardInput.ConnID)
				return
			}
			var ok bool
			localConnID, ok = c.Stage.coord.vcm.LookupByKey(SessionKey{
				GatewayID: msg.ForwardInput.GatewayID,
				ConnID:    msg.ForwardInput.ConnID,
			})
			if !ok {
				c.Log.Log(CatMeshMsg, "[%s] MsgForwardInput for unknown destination session: gw=%s conn=%d",
					c.MeshID(), msg.ForwardInput.GatewayID, msg.ForwardInput.ConnID)
				return
			}
		}
		c.Log.Log(CatMeshMsg, "[%s] msg MsgForwardInput from=%s conn=%d bytes=%d",
			c.MeshID(), msg.FromCellID, localConnID, len(msg.ForwardInput.InputBlob))
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
			c.MeshID(), msg.Disconnect.ConnID, msg.Disconnect.Reason)
		// Push a synthetic disconnect event to the cell's Events channel — same
		// path as in-process WebSocket disconnects so the engine's grace-period
		// state machine fires unchanged.
		select {
		case c.Events <- net.PlayerEvent{ConnID: msg.Disconnect.ConnID, Disconnect: true}:
		default:
			c.Log.Log(CatMeshMsg, "[%s] events channel full, dropping MsgPlayerDisconnected conn=%d",
				c.MeshID(), msg.Disconnect.ConnID)
		}
	}
}
