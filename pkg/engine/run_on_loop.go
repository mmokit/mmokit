package engine

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"
)

// ErrLoopStopped is returned by RunOnLoop / SubmitLoopJob when the engine's
// game loop is not running (or has already exited) and there is no goroutine
// to drain the queue.
var ErrLoopStopped = errors.New("engine: game loop not running")

// loopJob is a single unit of work queued for execution on the game loop.
// done is closed after fn has run (or the job was abandoned); err carries
// the result back to the caller.
type loopJob struct {
	fn   func() error
	done chan struct{}
	err  error
}

// loopQueue is the unexported channel the game loop drains every tick.
// External callers go through RunOnLoop / SubmitLoopJob instead of posting
// directly — that is the contract that lets the engine enforce deadlines,
// detect on-loop reentrance, and apply the admin drain budget.
type loopQueue struct {
	ch chan *loopJob
}

func newLoopQueue(buf int) *loopQueue {
	return &loopQueue{ch: make(chan *loopJob, buf)}
}

// RunOnLoop runs fn on the engine's game loop goroutine and returns its
// error. If the caller is already running on the loop goroutine, fn is
// invoked inline with zero scheduling — preventing the nested-schedule
// deadlock that kills the sim when a handler tries to post to its own loop.
//
// When called from any other goroutine, fn is queued and the caller blocks
// until the loop drains it or ctx is cancelled. A nil ctx is treated as
// context.Background(); passing a context with a deadline is strongly
// recommended so stuck callers do not pile up against the queue buffer.
//
// The returned error is whatever fn returned, or ctx.Err() if the caller
// was cancelled before the loop picked up the job, or ErrLoopStopped if
// the loop exited before the job ran.
func (e *Engine) RunOnLoop(ctx context.Context, fn func() error) error {
	if fn == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if e.IsLoopGoroutine() {
		return fn()
	}
	job := &loopJob{fn: fn, done: make(chan struct{})}
	select {
	case e.loopQ.ch <- job:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-job.done:
		return job.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SubmitLoopJob queues fn for execution on the game loop without blocking
// the caller. Useful for fire-and-forget scheduling (config change fan-out,
// neighbor rewires, cross-cell gossip). Returns true if the job was queued
// or run inline, false if the queue was full and the job was dropped.
//
// When called from the loop goroutine itself, fn runs inline immediately.
// Errors returned by fn are discarded — callers that need to observe errors
// must use RunOnLoop instead.
func (e *Engine) SubmitLoopJob(fn func() error) bool {
	if fn == nil {
		return true
	}
	if e.IsLoopGoroutine() {
		_ = fn()
		return true
	}
	job := &loopJob{fn: fn, done: make(chan struct{})}
	select {
	case e.loopQ.ch <- job:
		return true
	default:
		return false
	}
}

// loopGID stores the goroutine ID of the game loop while it is running.
// Zero means the loop is not currently running.
type loopGID struct {
	id atomic.Uint64
}

// markLoopGoroutine stashes the current goroutine's ID as the loop's owner.
// Called once at the start of GameLoop.Run.
func (g *loopGID) mark() {
	g.id.Store(currentGoroutineID())
}

// clear resets the loop goroutine ID. Called on loop exit.
func (g *loopGID) clear() {
	g.id.Store(0)
}

// isCurrent reports whether the calling goroutine is the loop goroutine.
func (g *loopGID) isCurrent() bool {
	want := g.id.Load()
	if want == 0 {
		return false
	}
	return currentGoroutineID() == want
}

// IsLoopGoroutine reports whether the caller is currently executing on this
// engine's game loop goroutine. Used internally by RunOnLoop / SubmitLoopJob;
// exported for games and tests that need to gate reentrant behavior.
func (e *Engine) IsLoopGoroutine() bool {
	return e.loopGID.isCurrent()
}

// currentGoroutineID extracts the calling goroutine's ID from the runtime
// stack header. This is the standard stdlib-only trick: `runtime.Stack` writes
// the header "goroutine 12345 [status]:\n…" and we parse the second field.
// Costs ~150-300 ns per call; acceptable for the admin / command hot paths
// where RunOnLoop is used.
func currentGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	fields := bytes.Fields(buf[:n])
	if len(fields) < 2 {
		return 0
	}
	id, err := strconv.ParseUint(string(fields[1]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// loopJobBudget is the soft per-tick budget the loop spends draining queued
// jobs. Jobs that overrun this by themselves log a warning; the drain loop
// stops pulling new jobs once the cumulative budget is exceeded and resumes
// next tick. Kept generous by default so interactive commands feel instant,
// but small enough that a slow job does not eat the tick.
const loopJobBudget = 8 * time.Millisecond

// loopJobSlowThreshold is the per-job wall-time above which the drain loop
// logs a warning. Used to catch handlers that pretend to be fast but aren't.
const loopJobSlowThreshold = 5 * time.Millisecond
