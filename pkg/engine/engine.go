package engine

import (
	"sync/atomic"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// AdminCmd is a command to execute on the game loop goroutine.
type AdminCmd struct {
	Fn func()
}

// Engine holds platform state that any game needs.
type Engine struct {
	ECS     *ecs.World
	ConnMgr *net.ConnManager
	Log     *logger.Logger
	Tick    uint32
	Config  Config

	Perf *TickProfile

	netIDBase     uint32
	nextNetID     atomic.Uint32
	toRemove      []ecs.Entity
	RemovedNetIDs []uint32

	PendingAdminCmds chan AdminCmd
}

// SetNetIDBase sets the base offset for NetworkID allocation.
// Each node should have a unique base to prevent ID collisions.
func (e *Engine) SetNetIDBase(base uint32) {
	e.netIDBase = base
}

// New creates a new Engine.
func New(cfg Config, connMgr *net.ConnManager, log *logger.Logger) *Engine {
	return &Engine{
		ECS:              ecs.NewWorld(1024),
		ConnMgr:          connMgr,
		Log:              log,
		Config:           cfg,
		toRemove:         make([]ecs.Entity, 0, 64),
		PendingAdminCmds: make(chan AdminCmd, 32),
	}
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
// The getNetID callback lets the game provide the NetworkID lookup without
// the engine knowing about game-specific components.
func (e *Engine) FlushRemovals(getNetID func(ecs.Entity) (uint32, bool)) {
	for _, entity := range e.toRemove {
		if e.ECS.Alive(entity) {
			if id, ok := getNetID(entity); ok {
				e.RemovedNetIDs = append(e.RemovedNetIDs, id)
			}
			e.ECS.RemoveEntity(entity)
		}
	}
	e.toRemove = e.toRemove[:0]
}
