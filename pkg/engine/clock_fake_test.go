package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTickSource is a tickSource whose ticks fire only when a test says so.
// The channel is unbuffered, so fire returns only once the loop has taken the
// tick — which is what lets a test assert an exact tick count with no sleeps.
type fakeTickSource struct {
	ch      chan time.Time
	stopped atomic.Bool
}

func newFakeTickSource() *fakeTickSource {
	return &fakeTickSource{ch: make(chan time.Time)}
}

func (f *fakeTickSource) C() <-chan time.Time { return f.ch }
func (f *fakeTickSource) Stop()               { f.stopped.Store(true) }
func (f *fakeTickSource) fire(at time.Time)   { f.ch <- at }

// manualClock is the loop's wall clock under test control.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newManualClock() *manualClock {
	return &manualClock{t: time.Unix(1700000000, 0)}
}

func (c *manualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// loopHarness runs a GameLoop against a fake tick source and a manual clock.
// Tick delivery is a handshake rather than a wait: tick() returns only once
// the loop has run the tick through its PostTick hook, so a test can assert
// what a single tick did without sleeping. Everything after PostTick — the
// perf record and the metrics sample — is settled by stop() instead.
type loopHarness struct {
	t         *testing.T
	eng       *Engine
	gl        *GameLoop
	src       *fakeTickSource
	clock     *manualClock
	requested chan time.Duration
	ticked    chan struct{}
	cancel    context.CancelFunc
	done      chan struct{}
}

func newLoopHarness(t *testing.T, eng *Engine, systems []System, names []string, hooks Hooks) *loopHarness {
	t.Helper()
	h := &loopHarness{
		t:         t,
		eng:       eng,
		src:       newFakeTickSource(),
		clock:     newManualClock(),
		requested: make(chan time.Duration, 1),
		ticked:    make(chan struct{}),
		done:      make(chan struct{}),
	}
	// Wrap PostTick — the last hook in a tick — so tick() has a completion
	// barrier. The channel is unbuffered, so the loop waits here until the
	// test collects the tick.
	inner := hooks.PostTick
	hooks.PostTick = func() {
		if inner != nil {
			inner()
		}
		h.ticked <- struct{}{}
	}
	h.gl = NewGameLoop(eng, systems, names, hooks)
	h.gl.newTickSource = func(d time.Duration) tickSource {
		h.requested <- d
		return h.src
	}
	h.gl.now = h.clock.now
	return h
}

// start runs the loop and returns the period it asked its tick source for.
// Receiving that period also establishes the happens-before edge the rest of
// the harness relies on.
func (h *loopHarness) start() time.Duration {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		h.gl.Run(ctx)
		close(h.done)
	}()
	select {
	case d := <-h.requested:
		return d
	case <-time.After(2 * time.Second):
		h.t.Fatal("loop did not build a tick source")
		return 0
	}
}

// tick delivers one tick and returns once the loop has run it through
// PostTick.
func (h *loopHarness) tick() {
	h.t.Helper()
	select {
	case h.src.ch <- h.clock.now():
	case <-h.done:
		h.t.Fatal("loop exited before accepting a tick")
	case <-time.After(2 * time.Second):
		h.t.Fatal("loop did not accept a tick")
	}
	select {
	case <-h.ticked:
	case <-h.done:
		h.t.Fatal("loop exited mid-tick")
	case <-time.After(2 * time.Second):
		h.t.Fatal("loop did not complete a tick")
	}
}

// stop cancels the loop and waits for Run to return. Every tick delivered
// before it is complete once it returns.
func (h *loopHarness) stop() {
	h.t.Helper()
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		h.t.Fatal("loop did not return after cancellation")
	}
}

// TestLoopHarness_ExactTickCount pins the seam itself: N deliveries produce
// exactly N ticks, and they cost no wall time. Before this seam the only way
// to observe a tick was to sleep for one — loop_test.go's 150ms window asserts
// merely that Tick != 0, and 1000 ticks at 20Hz would have cost 50 seconds.
func TestLoopHarness_ExactTickCount(t *testing.T) {
	eng := newLoopTestEngine()
	h := newLoopHarness(t, eng, nil, nil, Hooks{})
	h.start()

	const want = 1000
	started := time.Now()
	for i := 0; i < want; i++ {
		h.tick()
	}
	h.stop()

	if eng.Tick != want {
		t.Fatalf("eng.Tick = %d, want exactly %d", eng.Tick, want)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("%d ticks took %v; the loop is still waiting on wall time", want, elapsed)
	}
	if !h.src.stopped.Load() {
		t.Error("tick source was not stopped when Run returned")
	}
}

// TestLoopHarness_ManualClockDrivesTimings proves the manual clock reaches the
// per-system timing path, so tick-budget behaviour is testable without sleeps.
func TestLoopHarness_ManualClockDrivesTimings(t *testing.T) {
	eng := newLoopTestEngine()
	var h *loopHarness
	slow := updateFunc(func(float32) { h.clock.advance(7 * time.Millisecond) })
	h = newLoopHarness(t, eng, []System{slow}, []string{"slow"}, Hooks{})
	h.start()
	h.tick()
	h.stop()

	if got := h.gl.sysTimings[0]; got != 7*time.Millisecond {
		t.Fatalf("sysTimings[0] = %v, want 7ms from the manual clock", got)
	}
}
