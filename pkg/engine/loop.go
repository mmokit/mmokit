package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/mmokit/mmokit/pkg/net"
)

// Hooks allows the game to inject behavior into the engine's tick loop.
// All hooks are nil-safe — if a hook is nil, it is simply skipped.
type Hooks struct {
	OnConnect      func(connID uint32)
	OnDisconnect   func(connID uint32)
	ClearTickState func()
	AfterSystem    func() // called after each system's Update returns
	PreFlush       func()
	PostFlush      func()
	PostTick       func()
}

// tickSource is the loop's tick clock, narrowed to what Run uses so a test
// can drive ticks explicitly instead of waiting for wall time.
type tickSource interface {
	C() <-chan time.Time
	Stop()
}

// realTickSource is the production implementation: a plain time.Ticker.
type realTickSource struct{ t *time.Ticker }

func (r realTickSource) C() <-chan time.Time { return r.t.C }
func (r realTickSource) Stop()               { r.t.Stop() }

// GameLoop runs the fixed-timestep tick loop.
type GameLoop struct {
	engine      *Engine
	systems     []System
	hooks       Hooks
	sysTimings  []time.Duration        // reusable scratch buffer
	systemNames []string               // profiling labels, parallel to systems
	eventsCh    <-chan net.PlayerEvent // per-cell events (nil = use ConnMgr.Events())

	// newTickSource and now are the loop's only two reads of wall time.
	// NewGameLoop installs the real implementations; tests substitute
	// deterministic ones. A GameLoop built as a struct literal must set
	// both before it ticks.
	newTickSource func(time.Duration) tickSource
	now           func() time.Time
}

// SetEventsCh sets a per-cell events channel. When set, processEvents
// drains from this channel instead of ConnMgr.Events().
func (gl *GameLoop) SetEventsCh(ch <-chan net.PlayerEvent) {
	gl.eventsCh = ch
}

// Systems returns the slice of systems registered on this game loop.
// Used by schema introspection to walk systems and extract router metadata.
func (gl *GameLoop) Systems() []System { return gl.systems }

// EachSystem calls fn for every system in tick order with its registered name
// (parallel to systemNames). Call on the loop goroutine.
func (gl *GameLoop) EachSystem(fn func(name string, s System)) {
	for i, s := range gl.systems {
		name := ""
		if i < len(gl.systemNames) {
			name = gl.systemNames[i]
		}
		fn(name, s)
	}
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
		ClearTickState: hooks.ClearTickState,
		AfterSystem:    hooks.AfterSystem,
		PreFlush:       hooks.PreFlush,
		PostFlush:      hooks.PostFlush,
		PostTick: func() {
			if hooks.PostTick != nil {
				hooks.PostTick()
			}
			pmHooks.PostTick()
		},
	}

	return &GameLoop{
		engine:      eng,
		systems:     systems,
		hooks:       merged,
		sysTimings:  make([]time.Duration, len(systems)),
		systemNames: names,
		newTickSource: func(d time.Duration) tickSource {
			return realTickSource{t: time.NewTicker(d)}
		},
		now: time.Now,
	}
}

// AddSystemLive appends a system to the running loop. MUST be called on the
// loop goroutine (e.g. via engine.RunOnLoop). It resizes the timing buffer and
// rebuilds the profiler so the new system appears in perf output.
func (gl *GameLoop) AddSystemLive(name string, s System) {
	gl.systems = append(gl.systems, s)
	gl.systemNames = append(gl.systemNames, name)
	gl.sysTimings = make([]time.Duration, len(gl.systems))
	gl.engine.Perf = NewTickProfile(gl.systemNames)
}

// RemoveSystemLive removes the first system registered under name. Returns
// false if no such system exists. MUST be called on the loop goroutine.
func (gl *GameLoop) RemoveSystemLive(name string) bool {
	for i, n := range gl.systemNames {
		if n == name {
			gl.systems = append(gl.systems[:i], gl.systems[i+1:]...)
			gl.systemNames = append(gl.systemNames[:i], gl.systemNames[i+1:]...)
			gl.sysTimings = make([]time.Duration, len(gl.systems))
			gl.engine.Perf = NewTickProfile(gl.systemNames)
			return true
		}
	}
	return false
}

// SystemByName returns the first system registered under name. MUST be called
// on the loop goroutine.
func (gl *GameLoop) SystemByName(name string) (System, bool) {
	for i, n := range gl.systemNames {
		if n == name {
			return gl.systems[i], true
		}
	}
	return nil, false
}

// ReplaceSystemLive swaps the system registered under name in place, preserving
// its tick-order slot and timing/profiler indices (name and count unchanged).
// Returns false if no such system exists. MUST be called on the loop goroutine.
func (gl *GameLoop) ReplaceSystemLive(name string, s System) bool {
	for i, n := range gl.systemNames {
		if n == name {
			gl.systems[i] = s
			return true
		}
	}
	return false
}

// Run starts the fixed-timestep game loop. Blocks until ctx is cancelled.
func (gl *GameLoop) Run(ctx context.Context) {
	sched := newTickSchedule(gl.engine.Config.TickRate)
	src := gl.newTickSource(sched.Period)
	defer src.Stop()

	// Stash this goroutine's ID so RunOnLoop can detect reentrance from
	// handlers already running on the loop and short-circuit safely.
	gl.engine.loopGID.mark()
	defer gl.engine.loopGID.clear()

	gl.logSchedule(sched)

	for {
		select {
		case <-ctx.Done():
			gl.engine.Log.Log("engine:loop", "game loop stopped")
			return
		case <-src.C():
			gl.tick(sched.Dt)
		}
	}
}

// logSchedule reports what the loop will actually run at. It names the
// requested rate and the effective one separately whenever they differ,
// because a line that prints only the configured rate next to a rounded
// period states a contradiction and invites the reader past it.
func (gl *GameLoop) logSchedule(s tickSchedule) {
	switch {
	case s.Clamped:
		gl.engine.Log.Log("engine:loop",
			"game loop: tick rate %d is out of range; substituting %.0fHz (%dms per tick)",
			s.Rate, s.EffectiveRate(), s.PeriodMs)
	case !s.Exact:
		gl.engine.Log.Log("engine:loop",
			"game loop started: %dHz requested -> %.2fHz effective (%dms per tick, dt=%.6f); tick periods quantize to whole milliseconds",
			s.Rate, s.EffectiveRate(), s.PeriodMs, s.Dt)
		return
	}
	gl.engine.Log.Log("engine:loop", "game loop started: %.0fHz (%dms per tick, dt=%.6f)",
		s.EffectiveRate(), s.PeriodMs, s.Dt)
}

func (gl *GameLoop) tick(dt float32) {
	tickStart := gl.now()
	eng := gl.engine
	eng.Tick++
	eng.BeginRemovalTick()

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

	// Drain typed client-input frames (channel 0x00 post Plan 1
	// Phase 5 unification) → dispatch through the mmokit.HandleClient
	// registry via the universe-side typed message dispatcher.
	// Engine-internal phase: every input for tick N is visible to
	// every system that runs below.
	if eng.clientInputTick != nil {
		eng.clientInputTick()
	}

	// Run all systems in order, measuring each
	for i, sys := range gl.systems {
		sysStart := gl.now()
		sys.Update(dt)
		if gl.hooks.AfterSystem != nil {
			gl.hooks.AfterSystem()
		}
		gl.sysTimings[i] = gl.now().Sub(sysStart)
	}

	// Pre-flush: pre-removal notifications
	if gl.hooks.PreFlush != nil {
		gl.hooks.PreFlush()
	}

	// Flush entity removals. If replication already sampled this tick's
	// tombstones, these removals are published next tick; otherwise they remain
	// in the current pending batch until a consumer samples them.
	eng.FlushRemovals()

	// Post-flush: post-removal work (spawns, state changes)
	if gl.hooks.PostFlush != nil {
		gl.hooks.PostFlush()
	}

	// Post-tick: periodic saves, etc.
	if gl.hooks.PostTick != nil {
		gl.hooks.PostTick()
	}

	tickTotal := gl.now().Sub(tickStart)
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
	deadline := gl.now().Add(loopJobBudget)
	for {
		select {
		case job := <-gl.engine.loopQ.ch:
			start := gl.now()
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
			if d := gl.now().Sub(start); d > loopJobSlowThreshold {
				gl.engine.Log.Log("engine:loop", "slow admin job: %v", d)
			}
			if gl.now().After(deadline) {
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
