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
	ConnMgr *net.ConnManager
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

	// Metrics collects per-node observability data. Nil until wired by
	// the universe layer or game setup code.
	Metrics *metrics.NodeMetrics

	// EntityCounter returns (real, replica, ghost, connected) counts.
	// Injected by the universe layer to avoid importing ECS component types.
	EntityCounter func() (real, replica, ghost, connected int)

	netIDBase     uint32
	nextNetID     atomic.Uint32
	toRemove      []ecs.Entity
	RemovedNetIDs []uint32

	PendingAdminCmds chan func()

	Players *PlayerManager
}

// SetNetIDBase sets the base offset for NetworkID allocation.
// Each node should have a unique base to prevent ID collisions.
func (e *Engine) SetNetIDBase(base uint32) {
	e.netIDBase = base
}

// NetIDBase returns the base offset for NetworkID allocation.
func (e *Engine) NetIDBase() uint32 {
	return e.netIDBase
}

// New creates a new Engine.
func New(cfg Config, connMgr *net.ConnManager, log *logger.Logger) *Engine {
	eng := &Engine{
		ECS:              ecs.NewWorld(1024),
		ConnMgr:          connMgr,
		Log:              log,
		Config:           cfg,
		toRemove:         make([]ecs.Entity, 0, 64),
		PendingAdminCmds: make(chan func(), 32),
	}
	eng.Players = NewPlayerManager()
	eng.Players.eng = eng
	return eng
}

// NextNetID allocates and returns the next network entity ID.
func (e *Engine) NextNetID() uint32 {
	return e.netIDBase + e.nextNetID.Add(1)
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
