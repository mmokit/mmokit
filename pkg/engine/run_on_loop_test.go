package engine

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitForQueued blocks until n jobs are sitting in the loop queue. The send
// into the buffered queue has no receiver, so there is no handshake to wait
// on; the condition is exact and the deadline only bounds a failure.
func waitForQueued(t *testing.T, eng *Engine, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(eng.loopQ.ch) != n {
		if time.Now().After(deadline) {
			t.Fatalf("queue holds %d jobs, want %d", len(eng.loopQ.ch), n)
		}
		runtime.Gosched()
	}
}

// TestRunOnLoop_AbandonedJobNeverExecutes is the direct regression for
// CE-008's third defect. A caller whose context expires while its job is
// queued used to return to its own error handling while the closure stayed in
// the queue and ran on a later tick — applying a mutation on behalf of a
// request that had already given up. Admin verbs derive their context from the
// HTTP request, so closing a browser tab was enough to trigger it.
func TestRunOnLoop_AbandonedJobNeverExecutes(t *testing.T) {
	eng := newLoopTestEngine()
	h := newLoopHarness(t, eng, nil, nil, Hooks{})
	h.start()

	var ran atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.RunOnLoop(ctx, func() error {
			ran.Add(1)
			return nil
		})
	}()
	waitForQueued(t, eng, 1)

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnLoop returned %v, want context.Canceled", err)
	}

	// Two ticks: the first drains the abandoned job, the second proves it was
	// discarded rather than deferred.
	h.tick()
	h.tick()
	h.stop()

	if got := ran.Load(); got != 0 {
		t.Fatalf("abandoned job executed %d times, want 0", got)
	}
	if n := len(eng.loopQ.ch); n != 0 {
		t.Fatalf("queue still holds %d jobs, want 0", n)
	}
}

// TestRunOnLoop_LoopDeliversResult is the counterpart: a job the loop claims
// runs exactly once and its error reaches the caller.
func TestRunOnLoop_LoopDeliversResult(t *testing.T) {
	eng := newLoopTestEngine()
	h := newLoopHarness(t, eng, nil, nil, Hooks{})
	h.start()

	want := errors.New("job failed")
	var ran atomic.Int32
	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.RunOnLoop(context.Background(), func() error {
			ran.Add(1)
			return want
		})
	}()
	waitForQueued(t, eng, 1)

	h.tick()
	if err := <-errCh; !errors.Is(err, want) {
		t.Fatalf("RunOnLoop returned %v, want %v", err, want)
	}
	h.stop()

	if got := ran.Load(); got != 1 {
		t.Fatalf("job ran %d times, want 1", got)
	}
}

// TestRunOnLoop_ClaimedJobOutlivesItsCaller pins the deliberate consequence of
// the claim: once the loop owns a job, a caller whose context expires still
// waits for it. Returning early is what allowed a cancelled cross-cell
// transfer to tear its cell down while an adopted-user closure was still
// writing to it, so the deadline is soft by at most one job's runtime.
func TestRunOnLoop_ClaimedJobOutlivesItsCaller(t *testing.T) {
	eng := newLoopTestEngine()
	gl := NewGameLoop(eng, nil, nil, Hooks{})

	started := make(chan struct{})
	release := make(chan struct{})
	var ran atomic.Bool

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.RunOnLoop(ctx, func() error {
			close(started)
			<-release
			ran.Store(true)
			return nil
		})
	}()
	waitForQueued(t, eng, 1)

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		gl.processAdminCmds()
	}()
	<-started // the drain owns the job and is inside fn

	// The caller now gives up. Its job is already claimed, so it must wait.
	cancel()
	select {
	case err := <-errCh:
		t.Fatalf("caller returned %v while its claimed job was still running", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-errCh; err != nil {
		t.Fatalf("caller returned %v, want the job's nil result", err)
	}
	<-drained
	if !ran.Load() {
		t.Fatal("claimed job did not run")
	}
}

// TestLoopJobClaim_ExactlyOnce pins the primitive itself under contention.
func TestLoopJobClaim_ExactlyOnce(t *testing.T) {
	job := &loopJob{done: make(chan struct{})}
	if !job.claim() {
		t.Fatal("first claim failed")
	}
	if job.claim() {
		t.Fatal("second claim succeeded; the job would run twice")
	}

	const racers = 32
	for round := 0; round < 100; round++ {
		j := &loopJob{done: make(chan struct{})}
		var wins atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if j.claim() {
					wins.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if got := wins.Load(); got != 1 {
			t.Fatalf("round %d: %d goroutines claimed the job, want exactly 1", round, got)
		}
	}
}

// TestSubmitLoopJob_NeverAbandoned records that fire-and-forget jobs have no
// caller to abandon them, so the claim never blocks their execution.
func TestSubmitLoopJob_NeverAbandoned(t *testing.T) {
	eng := newLoopTestEngine()
	h := newLoopHarness(t, eng, nil, nil, Hooks{})
	h.start()

	var ran atomic.Int32
	if err := eng.SubmitLoopJob(func() error { ran.Add(1); return nil }); err != nil {
		t.Fatalf("SubmitLoopJob returned %v, want nil", err)
	}
	waitForQueued(t, eng, 1)
	h.tick()
	h.stop()

	if got := ran.Load(); got != 1 {
		t.Fatalf("submitted job ran %d times, want 1", got)
	}
}

// TestRunOnLoop_StoppedLoopReturnsErrLoopStopped uses context.Background()
// deliberately. Four production callers — internal/tunectl and
// internal/wasmctl — pass no deadline at all, so before the gate existed
// their jobs queued into a channel nobody would ever drain and the caller
// blocked forever. With no deadline here, a regression hangs to the go test
// timeout instead of passing quietly.
func TestRunOnLoop_StoppedLoopReturnsErrLoopStopped(t *testing.T) {
	eng := newLoopTestEngine()
	h := newLoopHarness(t, eng, nil, nil, Hooks{})
	h.start()
	h.tick()
	h.stop()

	var ran atomic.Int32
	err := eng.RunOnLoop(context.Background(), func() error {
		ran.Add(1)
		return nil
	})
	if !errors.Is(err, ErrLoopStopped) {
		t.Fatalf("RunOnLoop returned %v, want ErrLoopStopped", err)
	}
	if got := ran.Load(); got != 0 {
		t.Fatalf("job ran %d times on a stopped loop, want 0", got)
	}

	if err := eng.SubmitLoopJob(func() error { ran.Add(1); return nil }); !errors.Is(err, ErrLoopStopped) {
		t.Fatalf("SubmitLoopJob returned %v, want ErrLoopStopped", err)
	}
	if got := ran.Load(); got != 0 {
		t.Fatalf("job ran %d times on a stopped loop, want 0", got)
	}
}

// TestRunOnLoop_NeverStartedStillQueues pins the other half of the
// distinction. Cells schedule work into the window between construction and
// the first tick on purpose — cell_transfer_executor's populate closure and
// partition's rewire directives both do — so "no loop running" must not mean
// "will never run". A caller here waits for its context, not ErrLoopStopped.
func TestRunOnLoop_NeverStartedStillQueues(t *testing.T) {
	eng := newLoopTestEngine()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.RunOnLoop(ctx, func() error { return nil })
	}()
	waitForQueued(t, eng, 1)
	cancel()

	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnLoop on a never-started loop returned %v, want context.Canceled", err)
	}
	if errors.Is(<-func() chan error {
		c := make(chan error, 1)
		c <- eng.SubmitLoopJob(func() error { return nil })
		return c
	}(), ErrLoopStopped) {
		t.Fatal("SubmitLoopJob refused a never-started loop; it must queue")
	}
}

// TestLoopQueue_StopFailsQueuedJobs covers the shutdown drain itself, with no
// callers racing it: every job still queued when the loop exits is failed
// with ErrLoopStopped, not silently orphaned and not run. Before this,
// ErrLoopStopped was declared and returned by nothing.
func TestLoopQueue_StopFailsQueuedJobs(t *testing.T) {
	q := newLoopQueue(8)
	var ran atomic.Int32
	jobs := make([]*loopJob, 3)
	for i := range jobs {
		jobs[i] = &loopJob{fn: func() error { ran.Add(1); return nil }, done: make(chan struct{})}
		q.ch <- jobs[i]
	}

	if n := q.stop(); n != 3 {
		t.Fatalf("stop() failed %d jobs, want 3", n)
	}
	for i, job := range jobs {
		select {
		case <-job.done:
		default:
			t.Fatalf("job %d was left unresolved by the drain", i)
		}
		if !errors.Is(job.err, ErrLoopStopped) {
			t.Errorf("job %d err = %v, want ErrLoopStopped", i, job.err)
		}
	}
	if got := ran.Load(); got != 0 {
		t.Fatalf("%d queued jobs ran during shutdown, want 0", got)
	}
}

// TestLoopQueue_StopReleasesWaiters is the same shutdown seen from the
// callers' side. It deliberately does not assert stop()'s return: a caller
// whose post-send gate re-check fires first reclaims its own job, so the
// drain's count is a lower bound. What must hold is that every waiter is
// released with ErrLoopStopped and no closure runs.
func TestLoopQueue_StopReleasesWaiters(t *testing.T) {
	eng := newLoopTestEngine()

	const waiters = 3
	var ran atomic.Int32
	errs := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			errs <- eng.RunOnLoop(context.Background(), func() error {
				ran.Add(1)
				return nil
			})
		}()
	}
	waitForQueued(t, eng, waiters)

	eng.loopQ.stop()
	for i := 0; i < waiters; i++ {
		if err := <-errs; !errors.Is(err, ErrLoopStopped) {
			t.Fatalf("waiter %d returned %v, want ErrLoopStopped", i, err)
		}
	}
	if got := ran.Load(); got != 0 {
		t.Fatalf("%d queued jobs ran during shutdown, want 0", got)
	}
}

// TestLoopQueue_StopSkipsAbandonedJobs — the drain must not resolve a job its
// caller already claimed; doing so would close an already-settled done.
func TestLoopQueue_StopSkipsAbandonedJobs(t *testing.T) {
	eng := newLoopTestEngine()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.RunOnLoop(ctx, func() error { return nil })
	}()
	waitForQueued(t, eng, 1)
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller returned %v, want context.Canceled", err)
	}

	if n := eng.loopQ.stop(); n != 0 {
		t.Fatalf("stop() failed %d jobs, want 0 — the caller already owned it", n)
	}
}

// TestSubmitLoopJob_ErrorsOnStoppedAndFull pins the reason this returns an
// error rather than a bool. The dangerous old answer was `true` on a dead
// engine: op_dispatch_cell read it as queued and left the client's typed-op
// promise pending forever.
func TestSubmitLoopJob_ErrorsOnStoppedAndFull(t *testing.T) {
	eng := newLoopTestEngine()
	eng.loopQ = newLoopQueue(1)

	if err := eng.SubmitLoopJob(func() error { return nil }); err != nil {
		t.Fatalf("first submit returned %v, want nil", err)
	}
	if err := eng.SubmitLoopJob(func() error { return nil }); !errors.Is(err, ErrLoopQueueFull) {
		t.Fatalf("submit into a full queue returned %v, want ErrLoopQueueFull", err)
	}

	eng.loopQ.stop()
	if err := eng.SubmitLoopJob(func() error { return nil }); !errors.Is(err, ErrLoopStopped) {
		t.Fatalf("submit into a stopped queue returned %v, want ErrLoopStopped", err)
	}
}

// TestLoopQueue_RestartAfterStop — Cell.Run is re-entered on the same engine,
// so the gate is per-run. A per-engine latch would leave a restarted cell
// permanently refusing jobs.
func TestLoopQueue_RestartAfterStop(t *testing.T) {
	eng := newLoopTestEngine()

	h := newLoopHarness(t, eng, nil, nil, Hooks{})
	h.start()
	h.stop()
	if err := eng.RunOnLoop(context.Background(), func() error { return nil }); !errors.Is(err, ErrLoopStopped) {
		t.Fatalf("after stop, RunOnLoop returned %v, want ErrLoopStopped", err)
	}

	h2 := newLoopHarness(t, eng, nil, nil, Hooks{})
	h2.start()
	var ran atomic.Int32
	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.RunOnLoop(context.Background(), func() error { ran.Add(1); return nil })
	}()
	waitForQueued(t, eng, 1)
	h2.tick()
	if err := <-errCh; err != nil {
		t.Fatalf("after restart, RunOnLoop returned %v, want nil", err)
	}
	h2.stop()
	if got := ran.Load(); got != 1 {
		t.Fatalf("job ran %d times after restart, want 1", got)
	}
}

// TestRunOnLoop_NilQueue — six Engines in this repository are built as struct
// literals and carry no queue. They reach RunOnLoop only through gates today;
// answering ErrLoopStopped is cheaper than a nil dereference behind a gate.
func TestRunOnLoop_NilQueue(t *testing.T) {
	eng := &Engine{}
	if err := eng.RunOnLoop(context.Background(), func() error { return nil }); !errors.Is(err, ErrLoopStopped) {
		t.Fatalf("RunOnLoop on a queueless engine returned %v, want ErrLoopStopped", err)
	}
	if err := eng.SubmitLoopJob(func() error { return nil }); !errors.Is(err, ErrLoopStopped) {
		t.Fatalf("SubmitLoopJob on a queueless engine returned %v, want ErrLoopStopped", err)
	}
}

// TestLoopQueue_ConcurrentStopRace races callers against the drain with no
// deadline anywhere, so a caller that fails to resolve hangs the test rather
// than reporting a plausible error. Aimed at the -race gate.
func TestLoopQueue_ConcurrentStopRace(t *testing.T) {
	for round := 0; round < 20; round++ {
		eng := newLoopTestEngine()
		var ranAfterStop atomic.Int32
		var stopped atomic.Bool

		const callers = 64
		var wg sync.WaitGroup
		start := make(chan struct{})
		results := make(chan error, callers)
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				results <- eng.RunOnLoop(context.Background(), func() error {
					if stopped.Load() {
						ranAfterStop.Add(1)
					}
					return nil
				})
			}()
		}
		var stopWG sync.WaitGroup
		stopWG.Add(1)
		go func() {
			defer stopWG.Done()
			<-start
			eng.loopQ.stop()
			stopped.Store(true)
		}()
		close(start)
		stopWG.Wait()
		// No second stop() and no drain: a caller whose send won the race
		// against the closed gate has to resolve itself, via the post-send
		// re-check or the gate arm of its wait select. If it cannot, this
		// hangs to the test timeout.
		wg.Wait()
		close(results)

		for err := range results {
			if err != nil && !errors.Is(err, ErrLoopStopped) {
				t.Fatalf("round %d: caller returned %v, want nil or ErrLoopStopped", round, err)
			}
		}
		if got := ranAfterStop.Load(); got != 0 {
			t.Fatalf("round %d: %d jobs ran after stop() returned", round, got)
		}
	}
}
