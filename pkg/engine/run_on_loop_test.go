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
	if ok := eng.SubmitLoopJob(func() error { ran.Add(1); return nil }); !ok {
		t.Fatal("SubmitLoopJob reported the job was not queued")
	}
	waitForQueued(t, eng, 1)
	h.tick()
	h.stop()

	if got := ran.Load(); got != 1 {
		t.Fatalf("submitted job ran %d times, want 1", got)
	}
}
