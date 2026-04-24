package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/zenion/mmoserver/pkg/net"
)

// Hooks allows the game to inject behavior into the engine's tick loop.
// All hooks are nil-safe — if a hook is nil, it is simply skipped.
type Hooks struct {
	OnConnect      func(connID uint32)
	OnDisconnect   func(connID uint32)
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

// Systems returns the slice of systems registered on this game loop.
// Used by schema introspection to walk systems and extract router metadata.
func (gl *GameLoop) Systems() []System { return gl.systems }

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

	// Stash this goroutine's ID so RunOnLoop can detect reentrance from
	// handlers already running on the loop and short-circuit safely.
	gl.engine.loopGID.mark()
	defer gl.engine.loopGID.clear()

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

	// Process logins from pending connections (engine-internal, not a game hook)
	eng.Players.processPendingSessions()

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

// processAdminCmds drains the loop queue with a per-tick time budget. Jobs
// that overrun loopJobSlowThreshold log a warning; once the cumulative budget
// is exceeded, the remaining jobs stay queued until next tick. This keeps a
// slow admin job from eating the entire tick budget while still giving fast
// interactive commands instant turnaround.
func (gl *GameLoop) processAdminCmds() {
	deadline := time.Now().Add(loopJobBudget)
	for {
		select {
		case job := <-gl.engine.loopQ.ch:
			start := time.Now()
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("loop job panicked: %v", r)
					}
				}()
				if job.fn != nil {
					err = job.fn()
				}
			}()
			job.err = err
			close(job.done)
			if d := time.Since(start); d > loopJobSlowThreshold {
				gl.engine.Log.Log("engine:loop", "slow admin job: %v", d)
			}
			if time.Now().After(deadline) {
				return
			}
		default:
			return
		}
	}
}

func (gl *GameLoop) processEvents() {
	var ch <-chan net.PlayerEvent
	if gl.eventsCh != nil {
		ch = gl.eventsCh
	} else if ev, ok := gl.engine.ConnMgr.(interface{ Events() <-chan net.PlayerEvent }); ok {
		// Fallback path for engines constructed without SetEventsCh — typically
		// test harnesses that hold a real *ConnManager. Normal coordinator-mode
		// loops get their events channel via SetEventsCh during bridge wiring;
		// node-mode loops backed by VirtualConnManager (T6+) also use SetEventsCh.
		ch = ev.Events()
	} else {
		return
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
