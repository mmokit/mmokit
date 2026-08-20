package engine

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ErrLoopStopped is returned by RunOnLoop / SubmitLoopJob once the engine's
// game loop has exited, and to every job still queued when it exits. It does
// NOT cover a loop that has not started yet: cells routinely schedule work
// into the gap between construction and the first tick, and that work runs.
// The distinction is "will never run", not "is not running right now" —
// which is why it cannot be derived from HasLoopRunning.
var ErrLoopStopped = errors.New("engine: game loop stopped")

// ErrLoopQueueFull is returned by SubmitLoopJob when the queue is at capacity
// and the job was dropped. RunOnLoop blocks instead, up to its context.
var ErrLoopQueueFull = errors.New("engine: loop job queue full")

// loopJob is a single unit of work queued for execution on the game loop.
// done is closed after fn has run; err carries the result back to the caller.
//
// claimed is the ownership handshake between the waiting caller and the
// drain. Exactly one of them wins it. The winner decides the job's fate: the
// loop runs fn and closes done, or the caller abandons the job and the drain
// skips it. Without it a caller whose context expired returned to its own
// error handling while its closure stayed queued and ran a tick later,
// mutating ECS state on behalf of a request that had already given up.
type loopJob struct {
	fn      func() error
	done    chan struct{}
	err     error
	claimed atomic.Bool
}

// claim takes ownership of the job. It returns true exactly once, to
// whichever of the caller and the loop gets there first.
func (j *loopJob) claim() bool { return j.claimed.CompareAndSwap(false, true) }

// loopQueue is the unexported channel the game loop drains every tick.
// External callers go through RunOnLoop / SubmitLoopJob instead of posting
// directly — that is the contract that lets the engine enforce deadlines,
// detect on-loop reentrance, and apply the admin drain budget.
//
// stopped is the queue's lifecycle gate. Open means jobs may still run: the
// loop is running, or has not started yet. Closed means no job will ever run
// again. It is a channel rather than a flag so callers can select on it, and
// the mutex guards only the channel pointer across a restart — it is never
// held across a send, so a blocked sender cannot deadlock a stopping loop.
type loopQueue struct {
	ch chan *loopJob

	mu      sync.Mutex
	stopped chan struct{}
	closed  bool
}

func newLoopQueue(buf int) *loopQueue {
	return &loopQueue{ch: make(chan *loopJob, buf), stopped: make(chan struct{})}
}

// gate returns the current stop channel.
func (q *loopQueue) gate() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stopped
}

// start reopens the gate. Called at the top of every GameLoop.Run, because a
// cell's loop can be run again on the same engine after a stop and a
// per-engine latch would leave a restarted cell permanently refusing jobs.
func (q *loopQueue) start() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		q.stopped = make(chan struct{})
		q.closed = false
	}
}

// stop closes the gate and fails every job still queued, returning how many.
// Queued jobs are failed rather than run: after the last tick there is no
// AfterSystem flush, no FlushRemovals and no PostTick, so a job running here
// would mutate a world whose per-tick pipeline has already stopped — and by
// the time a cell's loop exits the cell is gone from the ownership maps and
// its netID range may already be released.
func (q *loopQueue) stop() int {
	q.mu.Lock()
	if !q.closed {
		close(q.stopped)
		q.closed = true
	}
	q.mu.Unlock()

	failed := 0
	for {
		select {
		case job := <-q.ch:
			if !job.claim() {
				continue // the caller abandoned it, or the loop already ran it
			}
			job.err = ErrLoopStopped
			close(job.done)
			failed++
		default:
			return failed
		}
	}
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
// The returned error is whatever fn returned, ctx.Err() if the caller was
// cancelled before the loop picked up the job, or ErrLoopStopped if the loop
// had already exited or exited while the job was queued. A job the loop never
// gets to never runs.
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
	if e.loopQ == nil {
		// An Engine built as a struct literal has no queue. There is
		// nothing to drain it, so say so instead of dereferencing nil.
		return ErrLoopStopped
	}
	gate := e.loopQ.gate()
	select {
	case <-gate:
		return ErrLoopStopped
	default:
	}
	job := &loopJob{fn: fn, done: make(chan struct{})}
	select {
	case e.loopQ.ch <- job:
	case <-gate:
		return ErrLoopStopped
	case <-ctx.Done():
		return ctx.Err()
	}
	// select picks uniformly among ready cases, so the send above can win
	// against an already-closed gate. Re-check: if the drain has been and
	// gone, claim the job back rather than waiting for a tick that will
	// never come.
	select {
	case <-gate:
		if job.claim() {
			return ErrLoopStopped
		}
	default:
	}
	select {
	case <-job.done:
		return job.err
	case <-gate:
		if job.claim() {
			return ErrLoopStopped
		}
		<-job.done
		return job.err
	case <-ctx.Done():
		// Abandon the job if the loop has not started it. If the loop got
		// there first the job is already running and its writes must
		// complete before this caller proceeds — returning early here is
		// what let a cancelled transfer tear a cell down underneath a
		// closure still writing to it. So the deadline is soft by at most
		// one job's runtime, deliberately.
		if job.claim() {
			return ctx.Err()
		}
		<-job.done
		return job.err
	}
}

// SubmitLoopJob queues fn for execution on the game loop without blocking
// the caller. Useful for fire-and-forget scheduling (config change fan-out,
// neighbor rewires, cross-cell gossip).
//
// It returns nil if the job was queued or run inline, ErrLoopQueueFull if the
// queue was at capacity, and ErrLoopStopped if the loop has exited. The last
// case is why this returns an error rather than a bool: "queued" on a stopped
// loop is indistinguishable from "queued" on a live one, and a caller that
// owes a client a response would leave it pending forever.
//
// When called from the loop goroutine itself, fn runs inline immediately.
// Errors returned by fn are discarded — callers that need to observe errors
// must use RunOnLoop instead.
func (e *Engine) SubmitLoopJob(fn func() error) error {
	if fn == nil {
		return nil
	}
	if e.IsLoopGoroutine() {
		_ = fn()
		return nil
	}
	if e.loopQ == nil {
		return ErrLoopStopped
	}
	gate := e.loopQ.gate()
	select {
	case <-gate:
		return ErrLoopStopped
	default:
	}
	job := &loopJob{fn: fn, done: make(chan struct{})}
	select {
	case e.loopQ.ch <- job:
	default:
		return ErrLoopQueueFull
	}
	// Same race as RunOnLoop. Report ErrLoopStopped only if claiming the job
	// back succeeded — if the loop already owns it, it ran or will run, and
	// "queued" is the truthful answer.
	select {
	case <-gate:
		if job.claim() {
			return ErrLoopStopped
		}
	default:
	}
	return nil
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

// HasLoopRunning reports whether this engine's game loop is currently active
// (i.e. GameLoop.Run is executing on some goroutine). Callers that want to
// skip the queue for read-only work — perf snapshots on a headless fixture —
// check this and call directly instead.
//
// It is not the same question as "will a queued job run". A loop that has not
// started yet reports false and still drains its queue once it does; a loop
// that has exited reports false and never will. RunOnLoop distinguishes the
// two with the queue's gate and returns ErrLoopStopped only for the second.
func (e *Engine) HasLoopRunning() bool {
	return e.loopGID.id.Load() != 0
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
