package engine

import (
	"sync/atomic"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/metrics"
	"github.com/zenion/mmoserver/pkg/net"
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
	// the ECS during FlushRemovals. Use to deregister from spatial grid, etc.
	OnEntityRemoved func(ecs.Entity)

	// Metrics collects per-cell observability data. Nil until wired by
	// the universe layer or game setup code.
	Metrics *metrics.CellMetrics

	// EntityCounter returns (real, replica, ghost, connected) counts.
	// Injected by the universe layer to avoid importing ECS component types.
	EntityCounter func() (real, replica, ghost, connected int)

	netIDBase     uint32
	nextNetID     atomic.Uint32
	toRemove      []ecs.Entity
	RemovedNetIDs []uint32

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

// NetIDBase returns the base offset for NetworkID allocation.
func (e *Engine) NetIDBase() uint32 {
	return e.netIDBase
}

// New creates a new Engine.
func New(cfg Config, connMgr net.ConnSender, log *logger.Logger) *Engine {
	eng := &Engine{
		ECS:      ecs.NewWorld(1024),
		ConnMgr:  connMgr,
		Log:      log,
		Config:   cfg,
		toRemove: make([]ecs.Entity, 0, 64),
		loopQ:    newLoopQueue(64),
	}
	eng.Players = NewPlayerManager()
	eng.Players.eng = eng
	return eng
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
	e.toRemove = append(e.toRemove, entity)
}

// FlushRemovals removes all entities marked for removal.
// Uses the Engine's GetNetID callback to map entities to network IDs for
// despawn tracking. If GetNetID is nil, entities are removed silently.
func (e *Engine) FlushRemovals() {
	for _, entity := range e.toRemove {
		if e.ECS.Alive(entity) {
			if e.GetNetID != nil {
				if id, ok := e.GetNetID(entity); ok {
					e.RemovedNetIDs = append(e.RemovedNetIDs, id)
				}
			}
			if e.OnEntityRemoved != nil {
				e.OnEntityRemoved(entity)
			}
			e.ECS.RemoveEntity(entity)
		}
	}
	e.toRemove = e.toRemove[:0]
}
