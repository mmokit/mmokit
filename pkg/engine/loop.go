package engine

import (
	"context"
	"log"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// Hooks allows the game to inject behavior into the engine's tick loop.
type Hooks struct {
	OnConnect      func(connID uint32)
	OnDisconnect   func(connID uint32)
	ProcessLogins  func()
	PreFlush       func()
	GetNetID       func(ecs.Entity) (uint32, bool)
	PostFlush      func()
	ClearTickState func()
	PostTick       func()
}

// GameLoop runs the fixed-timestep tick loop.
type GameLoop struct {
	engine       *Engine
	systems      []System
	systemNames  []string
	hooks        Hooks
	sysTimings   []time.Duration // reusable scratch buffer
}

// NewGameLoop creates a game loop with the given systems and lifecycle hooks.
func NewGameLoop(eng *Engine, systems []System, systemNames []string, hooks Hooks) *GameLoop {
	perf := NewTickProfile(systemNames)
	eng.Perf = perf
	return &GameLoop{
		engine:      eng,
		systems:     systems,
		systemNames: systemNames,
		hooks:       hooks,
		sysTimings:  make([]time.Duration, len(systems)),
	}
}

// Run starts the fixed-timestep game loop. Blocks until ctx is cancelled.
func (gl *GameLoop) Run(ctx context.Context) {
	tickInterval := time.Duration(1000/gl.engine.Config.TickRate) * time.Millisecond
	dt := float32(tickInterval.Seconds())
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	log.Printf("game loop started: %dHz (%.0fms per tick)", gl.engine.Config.TickRate, tickInterval.Seconds()*1000)

	for {
		select {
		case <-ctx.Done():
			log.Println("game loop stopped")
			return
		case <-ticker.C:
			gl.tick(dt)
		}
	}
}

func (gl *GameLoop) tick(dt float32) {
	tickStart := time.Now()
	eng := gl.engine
	eng.Tick++

	// Clear per-tick state
	gl.hooks.ClearTickState()

	// Process connect/disconnect events
	gl.processEvents()

	// Drain admin commands from console
	gl.processAdminCmds()

	// Process logins from pending connections
	gl.hooks.ProcessLogins()

	// Run all systems in order, measuring each
	for i, sys := range gl.systems {
		sysStart := time.Now()
		sys.Update(dt)
		gl.sysTimings[i] = time.Since(sysStart)
	}

	// Pre-flush: death notifications, pre-removal work
	gl.hooks.PreFlush()

	// Flush entity removals (clear + repopulate RemovedNetIDs so NetworkSystem
	// can read the previous tick's removals to distinguish kills from AoI exits)
	eng.RemovedNetIDs = eng.RemovedNetIDs[:0]
	eng.FlushRemovals(gl.hooks.GetNetID)

	// Post-flush: loot spawns, respawns
	gl.hooks.PostFlush()

	// Post-tick: periodic saves, etc.
	if gl.hooks.PostTick != nil {
		gl.hooks.PostTick()
	}

	eng.Perf.Record(gl.sysTimings, time.Since(tickStart))
}

func (gl *GameLoop) processAdminCmds() {
	for {
		select {
		case cmd := <-gl.engine.PendingAdminCmds:
			cmd.Fn()
		default:
			return
		}
	}
}

func (gl *GameLoop) processEvents() {
	for {
		select {
		case event := <-gl.engine.ConnMgr.Events():
			if event.Connected {
				gl.hooks.OnConnect(event.ConnID)
			}
			if event.Disconnect {
				gl.hooks.OnDisconnect(event.ConnID)
			}
		default:
			return
		}
	}
}
