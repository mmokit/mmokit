package engine

import (
	"context"
	"time"

	"github.com/zenion/mmoserver/pkg/net"
)

// Hooks allows the game to inject behavior into the engine's tick loop.
// All hooks are nil-safe — if a hook is nil, it is simply skipped.
type Hooks struct {
	OnConnect      func(connID uint32)
	OnDisconnect   func(connID uint32)
	ProcessLogins  func()
	PreFlush       func()
	PostFlush      func()
	ClearTickState func()
	PostTick       func()
}

// GameLoop runs the fixed-timestep tick loop.
type GameLoop struct {
	engine     *Engine
	systems    []System
	hooks      Hooks
	sysTimings []time.Duration         // reusable scratch buffer
	eventsCh   <-chan net.PlayerEvent   // per-node events (nil = use ConnMgr.Events())
}

// SetEventsCh sets a per-node events channel. When set, processEvents
// drains from this channel instead of ConnMgr.Events().
func (gl *GameLoop) SetEventsCh(ch <-chan net.PlayerEvent) {
	gl.eventsCh = ch
}

// NewGameLoop creates a game loop with the given systems and lifecycle hooks.
// names provides profiling labels for each system (must match len(systems)).
func NewGameLoop(eng *Engine, systems []System, names []string, hooks Hooks) *GameLoop {
	perf := NewTickProfile(names)
	eng.Perf = perf

	// Merge PlayerManager hooks (first) with game hooks (second)
	pmHooks := eng.Players.hooks()
	merged := Hooks{
		OnConnect: func(connID uint32) {
			pmHooks.OnConnect(connID)
			if hooks.OnConnect != nil {
				hooks.OnConnect(connID)
			}
		},
		OnDisconnect: func(connID uint32) {
			pmHooks.OnDisconnect(connID)
			if hooks.OnDisconnect != nil {
				hooks.OnDisconnect(connID)
			}
		},
		ProcessLogins: func() {
			pmHooks.ProcessLogins()
			if hooks.ProcessLogins != nil {
				hooks.ProcessLogins()
			}
		},
		PreFlush:       hooks.PreFlush,
		PostFlush:      hooks.PostFlush,
		ClearTickState: hooks.ClearTickState,
		PostTick: func() {
			if hooks.PostTick != nil {
				hooks.PostTick()
			}
			pmHooks.PostTick()
		},
	}

	return &GameLoop{
		engine:     eng,
		systems:    systems,
		hooks:      merged,
		sysTimings: make([]time.Duration, len(systems)),
	}
}

// Run starts the fixed-timestep game loop. Blocks until ctx is cancelled.
func (gl *GameLoop) Run(ctx context.Context) {
	tickInterval := time.Duration(1000/gl.engine.Config.TickRate) * time.Millisecond
	dt := float32(tickInterval.Seconds())
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	gl.engine.Log.Log("engine:loop", "game loop started: %dHz (%.0fms per tick)", gl.engine.Config.TickRate, tickInterval.Seconds()*1000)

	for {
		select {
		case <-ctx.Done():
			gl.engine.Log.Log("engine:loop", "game loop stopped")
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
	if gl.hooks.ClearTickState != nil {
		gl.hooks.ClearTickState()
	}

	// Process connect/disconnect events
	gl.processEvents()

	// Drain admin commands from console
	gl.processAdminCmds()

	// Process logins from pending connections
	if gl.hooks.ProcessLogins != nil {
		gl.hooks.ProcessLogins()
	}

	// Run all systems in order, measuring each
	for i, sys := range gl.systems {
		sysStart := time.Now()
		sys.Update(dt)
		gl.sysTimings[i] = time.Since(sysStart)
	}

	// Pre-flush: pre-removal notifications
	if gl.hooks.PreFlush != nil {
		gl.hooks.PreFlush()
	}

	// Flush entity removals (clear + repopulate RemovedNetIDs so NetworkSystem
	// can read the previous tick's removals to distinguish removals from AoI exits)
	eng.RemovedNetIDs = eng.RemovedNetIDs[:0]
	eng.FlushRemovals()

	// Post-flush: post-removal work (spawns, state changes)
	if gl.hooks.PostFlush != nil {
		gl.hooks.PostFlush()
	}

	// Post-tick: periodic saves, etc.
	if gl.hooks.PostTick != nil {
		gl.hooks.PostTick()
	}

	tickTotal := time.Since(tickStart)
	eng.Perf.Record(gl.sysTimings, tickTotal)

	if eng.Metrics != nil {
		var real, replica, ghost, connected int
		if eng.EntityCounter != nil {
			real, replica, ghost, connected = eng.EntityCounter()
		}
		eng.Metrics.RecordTick(tickTotal, real, replica, ghost, connected)
	}
}

func (gl *GameLoop) processAdminCmds() {
	for {
		select {
		case cmd := <-gl.engine.PendingAdminCmds:
			cmd()
		default:
			return
		}
	}
}

func (gl *GameLoop) processEvents() {
	var ch <-chan net.PlayerEvent
	if gl.eventsCh != nil {
		ch = gl.eventsCh
	} else {
		ch = gl.engine.ConnMgr.Events()
	}
	for {
		select {
		case evt := <-ch:
			if evt.Connected {
				if gl.hooks.OnConnect != nil {
					gl.hooks.OnConnect(evt.ConnID)
				}
			} else {
				if gl.hooks.OnDisconnect != nil {
					gl.hooks.OnDisconnect(evt.ConnID)
				}
			}
		default:
			return
		}
	}
}
