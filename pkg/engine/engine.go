package engine

import (
	"sync/atomic"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/metrics"
	"github.com/mmokit/mmokit/pkg/net"
)

// Engine holds platform state that any game needs.
type Engine struct {
	ECS     *ecs.World
	ConnMgr net.ConnSender
	Log     *logger.Logger
	Tick    uint32
	Config  Config

	Perf *TickProfile

	// GetNetID is injected by the game layer to map ECS entities to network IDs.
	// Used during entity removal to track which network IDs were despawned.
	GetNetID func(ecs.Entity) (uint32, bool)

	// OnEntityRemoved is called for each entity just before it is removed from
	// the ECS through an Engine removal path. It is an optional lifecycle
	// observer; framework-owned cleanup is wired separately and cannot be
	// replaced by assigning this callback. The callback must not structurally
	// mutate the ECS world.
	OnEntityRemoved func(ecs.Entity)

	// Metrics collects per-cell observability data. Nil until wired by
	// the universe layer or game setup code.
	Metrics *metrics.CellMetrics

	// EntityCounter returns (real, replica, ghost, connected) counts.
	// Injected by the universe layer to avoid importing ECS component types.
	EntityCounter func() (real, replica, ghost, connected int)

	netIDBase uint32
	// cellSize is the world's base cell width/height, injected per cell by the
	// universe layer. It lives here because systems in pkg/system need it and
	// cannot import pkg/universe (that is the direction of the dependency), so
	// the engine is the lowest common seam between them.
	cellSize  float32
	nextNetID atomic.Uint32

	toRemove          []removalRequest
	removalQueueIndex map[ecs.Entity]int

	// RemovedNetIDs is the authoritative tombstone batch available to the
	// replication system in the current publication phase. Call
	// SampleRemovedNetIDs rather than reading it directly in a tick consumer;
	// sampling freezes this batch and routes later removals to the next tick.
	RemovedNetIDs []uint32

	nextRemovedNetIDs  []uint32
	removalsSampled    bool
	entityRemovalClean func(ecs.Entity)

	// loopQ is the queue of jobs scheduled for execution on the game loop.
	// External callers go through RunOnLoop / SubmitLoopJob, not by posting
	// directly. See run_on_loop.go for the contract.
	loopQ *loopQueue

	// loopGID tracks the goroutine ID of the game loop while it is running,
	// enabling on-loop reentrance detection in RunOnLoop.
	loopGID loopGID

	Players *PlayerManager

	// clientInputTick is the per-tick hook for the typed client-input
	// dispatch (channel 0x00 post Plan 1 Phase 5 unification;
	// mmokit.HandleClient). Wired opaquely by universe.Process.createNode
	// (engine doesn't import universe). nil on engines used outside the
	// universe stack.
	clientInputTick func()

	// replicationAck is this cell's ReplicationSystem.AckFrame, installed at
	// system construction via ReplicationConfig.RegisterAck. The typed
	// client-input phase calls it when a client acknowledges a replication
	// frame. nil on engines with no replication system.
	replicationAck func(connID, streamEpoch, seq uint32)
}

// SetReplicationAck installs this cell's explicit frame-ACK sink. Called once
// by NewReplicationSystem via ReplicationConfig.RegisterAck.
func (e *Engine) SetReplicationAck(fn func(connID, streamEpoch, seq uint32)) {
	e.replicationAck = fn
}

// AckReplicationFrame routes a client frame acknowledgement to this cell's
// ReplicationSystem. No-op when replication is absent.
//
// Cell-loop goroutine only. Its only production caller is the typed
// client-input handler, which runs inside GameLoop.tick before the system
// loop — so an ACK that arrives before tick N is committed before tick N's
// frame is built, adding zero latency.
func (e *Engine) AckReplicationFrame(connID, streamEpoch, seq uint32) {
	if e.replicationAck != nil {
		e.replicationAck(connID, streamEpoch, seq)
	}
}

// SetClientInputTick wires the per-tick typed-client-input dispatch hook
// (channel 0x00 post Plan 1 Phase 5; mmokit.HandleClient). Called once
// by universe.Process.createNode at cell creation. Idempotent — re-wiring
// with the same fn is allowed; nil clears the hook.
func (e *Engine) SetClientInputTick(fn func()) {
	e.clientInputTick = fn
}

// SetNetIDBase sets the base offset for NetworkID allocation.
// Each cell must receive a unique range base to prevent ID collisions.
func (e *Engine) SetNetIDBase(base uint32) {
	e.netIDBase = base
}

// SetCellSize injects the world's base cell size. Called once per cell by the
// universe layer, before any system runs.
func (e *Engine) SetCellSize(size float32) { e.cellSize = size }

// CellSize returns the world's base cell width/height. Zero only on an engine
// built outside the universe layer, which is a test or benchmark harness.
func (e *Engine) CellSize() float32 { return e.cellSize }

// NetIDBase returns the base offset for NetworkID allocation.
func (e *Engine) NetIDBase() uint32 {
	return e.netIDBase
}

// New creates a new Engine.
func New(cfg Config, connMgr net.ConnSender, log *logger.Logger) *Engine {
	eng := &Engine{
		ECS:               ecs.NewWorld(1024),
		ConnMgr:           connMgr,
		Log:               log,
		Config:            cfg,
		toRemove:          make([]removalRequest, 0, 64),
		removalQueueIndex: make(map[ecs.Entity]int, 64),
		RemovedNetIDs:     make([]uint32, 0, 64),
		nextRemovedNetIDs: make([]uint32, 0, 64),
		loopQ:             newLoopQueue(64),
	}
	eng.Players = NewPlayerManager()
	eng.Players.eng = eng
	return eng
}

// RemovalPolicy controls whether an ECS teardown is a genuine authoritative
// destruction that clients must observe, or only the removal of a local
// representation such as a border replica or transfer ghost.
type RemovalPolicy uint8

const (
	// RemovalAuthoritative publishes the entity's network ID as a client
	// tombstone after cleanup and ECS removal complete.
	RemovalAuthoritative RemovalPolicy = iota

	// RemovalLocalOnly performs the same mandatory cleanup and ECS removal but
	// does not publish a client tombstone. Replica eviction, ghost expiry, and
	// rejected speculative rows use this policy.
	RemovalLocalOnly
)

type removalRequest struct {
	entity ecs.Entity
	policy RemovalPolicy
}

// SetEntityRemovalCleanup installs the framework-owned cleanup primitive run
// for every engine-mediated removal while the entity is still alive. It is
// separate from OnEntityRemoved so optional observers cannot accidentally
// replace mandatory spatial/index cleanup.
func (e *Engine) SetEntityRemovalCleanup(fn func(ecs.Entity)) {
	e.entityRemovalClean = fn
}

// BeginRemovalTick advances the authoritative-removal publication phase. A
// batch sampled last tick is retired and replaced by removals that happened
// after that sample. If no consumer sampled the batch, it remains pending so
// removals cannot disappear merely because replication was absent or skipped.
//
// The game loop calls this exactly once at the beginning of each tick.
func (e *Engine) BeginRemovalTick() {
	if e.removalsSampled {
		e.RemovedNetIDs, e.nextRemovedNetIDs = e.nextRemovedNetIDs, e.RemovedNetIDs[:0]
	}
	e.removalsSampled = false
}

// SampleRemovedNetIDs returns the stable authoritative-removal batch for this
// tick and closes its publication phase. Removals produced after this call are
// retained for the next tick, making removal delivery independent of system
// registration order. The returned slice is valid until BeginRemovalTick.
func (e *Engine) SampleRemovedNetIDs() []uint32 {
	e.removalsSampled = true
	return e.RemovedNetIDs
}

func (e *Engine) publishRemovedNetID(netID uint32) {
	if e.removalsSampled {
		e.nextRemovedNetIDs = append(e.nextRemovedNetIDs, netID)
		return
	}
	e.RemovedNetIDs = append(e.RemovedNetIDs, netID)
}

// NextNetID allocates and returns the next network entity ID.
func (e *Engine) NextNetID() uint32 {
	return e.netIDBase + e.nextNetID.Add(1)
}

// TickIntervalMs returns the configured game-loop tick interval in
// milliseconds (1000 / TickRate). Used by cluster-tick computations
// in the handoff path where CommitTick must be a cluster-coherent
// integer derived from ClusterClock.Now() divided by the tick
// interval. Falls back to 50ms (20Hz) if TickRate is unset.
func (e *Engine) TickIntervalMs() uint64 {
	if e.Config.TickRate <= 0 {
		return 50
	}
	return uint64(1000 / e.Config.TickRate)
}

// MarkForRemoval queues an entity for removal at the end of the tick.
func (e *Engine) MarkForRemoval(entity ecs.Entity) {
	e.MarkForRemovalWithPolicy(entity, RemovalAuthoritative)
}

// MarkForRemovalWithPolicy queues an entity for removal at the end of the
// tick using policy. Local representations must use RemovalLocalOnly.
func (e *Engine) MarkForRemovalWithPolicy(entity ecs.Entity, policy RemovalPolicy) {
	if idx, ok := e.removalQueueIndex[entity]; ok {
		// Authoritative destruction is the stronger request. This makes mixed
		// duplicate requests deterministic when, for example, lifecycle and
		// gameplay systems both classify the same entity in one tick.
		if policy == RemovalAuthoritative {
			e.toRemove[idx].policy = RemovalAuthoritative
		}
		return
	}
	e.removalQueueIndex[entity] = len(e.toRemove)
	e.toRemove = append(e.toRemove, removalRequest{entity: entity, policy: policy})
}

// RemoveEntityNow immediately removes an entity through the authoritative
// lifecycle path. It records the entity's network ID and invokes cleanup
// hooks exactly once while the entity is still alive. Returns true when the
// entity was removed, or false when the handle was already dead.
//
// Call only on the ECS owner goroutine at a structurally safe point (outside
// a locked query). Commands.Flush and FlushRemovals are the normal callers.
// This represents genuine entity destruction; replica eviction and authority
// transfer teardown must use their dedicated paths so they are not published
// to clients as authoritative removals.
func (e *Engine) RemoveEntityNow(entity ecs.Entity) bool {
	return e.RemoveEntityNowWithPolicy(entity, RemovalAuthoritative)
}

// RemoveEntityNowWithPolicy immediately removes an entity using the shared
// cleanup path and publishes a tombstone only for RemovalAuthoritative.
func (e *Engine) RemoveEntityNowWithPolicy(entity ecs.Entity, policy RemovalPolicy) bool {
	if !e.ECS.Alive(entity) {
		return false
	}
	var netID uint32
	var hasNetID bool
	if e.GetNetID != nil {
		netID, hasNetID = e.GetNetID(entity)
	}
	if e.entityRemovalClean != nil {
		e.entityRemovalClean(entity)
	}
	if e.OnEntityRemoved != nil {
		e.OnEntityRemoved(entity)
	}
	e.ECS.RemoveEntity(entity)
	if policy == RemovalAuthoritative && hasNetID {
		e.publishRemovedNetID(netID)
	}
	return true
}

// FlushRemovals removes all entities marked for removal.
// Uses the Engine's GetNetID callback to map entities to network IDs for
// despawn tracking. If GetNetID is nil, entities are removed silently.
func (e *Engine) FlushRemovals() {
	for _, req := range e.toRemove {
		e.RemoveEntityNowWithPolicy(req.entity, req.policy)
		delete(e.removalQueueIndex, req.entity)
	}
	e.toRemove = e.toRemove[:0]
}
