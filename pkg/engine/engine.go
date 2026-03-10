package engine

import (
	"sync/atomic"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/gameserver/pkg/logger"
	"github.com/zenion/gameserver/pkg/net"
	"github.com/zenion/gameserver/pkg/spatial"
)

// AdminCmd is a command to execute on the game loop goroutine.
type AdminCmd struct {
	Fn func()
}

// Engine holds platform state that any game needs.
type Engine struct {
	ECS     *ecs.World
	ConnMgr *net.ConnManager
	Grid    *spatial.Grid
	Log     *logger.Logger
	Tick    uint32
	Config  Config

	nextNetID     atomic.Uint32
	toRemove      []ecs.Entity
	RemovedNetIDs []uint32

	PendingAdminCmds chan AdminCmd
}

// New creates a new Engine.
func New(cfg Config, connMgr *net.ConnManager, grid *spatial.Grid, log *logger.Logger) *Engine {
	return &Engine{
		ECS:              ecs.NewWorld(1024),
		ConnMgr:          connMgr,
		Grid:             grid,
		Log:              log,
		Config:           cfg,
		toRemove:         make([]ecs.Entity, 0, 64),
		PendingAdminCmds: make(chan AdminCmd, 32),
	}
}

// NextNetID allocates and returns the next network entity ID.
func (e *Engine) NextNetID() uint32 {
	return e.nextNetID.Add(1)
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
