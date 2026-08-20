package cmdsys

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeLoopRunner satisfies the LoopRunner interface for OnLoop tests.
type fakeLoopRunner struct {
	calls int
}

func (f *fakeLoopRunner) RunOnLoop(ctx context.Context, fn func() error) error {
	f.calls++
	return fn()
}

func TestOnLoop_ReturnsTypedResult(t *testing.T) {
	r := &fakeLoopRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := OnLoop(ctx, r, func() (int, error) { return 42, nil })
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
	if r.calls != 1 {
		t.Errorf("RunOnLoop called %d times, want 1", r.calls)
	}
}

func TestOnLoop_PropagatesInnerError(t *testing.T) {
	r := &fakeLoopRunner{}
	ctx := context.Background()
	wantErr := errors.New("boom")
	got, err := OnLoop(ctx, r, func() (int, error) { return 0, wantErr })
	if err == nil || !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if got != 0 {
		t.Errorf("got %d, want 0 (zero value on error)", got)
	}
}

// fakeRunOnLoopFailingRunner returns an error from RunOnLoop itself —
// e.g. context cancelled before the loop fired, or the loop already
// stopped. TestOnLoop_PropagatesRunnerError asserts fn does not run in
// that case. That was aspirational before CE-008 unit 6: the real
// RunOnLoop returned the context error while leaving the closure queued,
// and it ran on a later tick. The per-job claim makes this fake's
// behaviour the production contract.
type fakeFailingRunner struct{}

func (f *fakeFailingRunner) RunOnLoop(ctx context.Context, fn func() error) error {
	return errors.New("loop unavailable")
}

func TestOnLoop_PropagatesRunnerError(t *testing.T) {
	r := &fakeFailingRunner{}
	got, err := OnLoop(context.Background(), r, func() (int, error) {
		t.Fatal("fn should not run when runner errors")
		return 0, nil
	})
	if err == nil {
		t.Fatal("expected error from runner")
	}
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}
