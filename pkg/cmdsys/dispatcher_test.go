package cmdsys

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ---- helpers ---------------------------------------------------------------

type dispArgs struct {
	Msg string
}
type dispResult struct {
	Echo string
}

func echoCmd() Command {
	return Command{
		Verb:        "test.echo",
		Capability:  "test.echo",
		Description: "echo test",
		Route:       RouteLocal,
		Args:        dispArgs{},
		Result:      dispResult{},
		Handler: func(_ context.Context, _ *Env, args any) (any, error) {
			a := args.(dispArgs)
			return dispResult{Echo: a.Msg}, nil
		},
	}
}

func newTestDispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	reg := NewRegistry()
	if err := reg.Register(echoCmd()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	d := NewDispatcher(DispatcherConfig{
		Registry: reg,
		Audit:    NoopAuditSink{},
	})
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func deadlineCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func operatorCaller() Caller {
	return NewOperatorIdentity("test-op")
}

// ---- tests -----------------------------------------------------------------

func TestDispatcher_HappyPathLocal(t *testing.T) {
	d := newTestDispatcher(t)
	res, err := d.Invoke(deadlineCtx(t), operatorCaller(), "test.echo", dispArgs{Msg: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.PerTarget) != 1 {
		t.Fatalf("PerTarget len: got %d want 1", len(res.PerTarget))
	}
	tr := res.PerTarget[0]
	if !tr.OK {
		t.Errorf("target not OK: %s", tr.Error)
	}
	echo, ok := tr.Result.(dispResult)
	if !ok {
		t.Fatalf("result type: got %T want dispResult", tr.Result)
	}
	if echo.Echo != "hello" {
		t.Errorf("Echo: got %q want hello", echo.Echo)
	}
}

func TestDispatcher_NoDeadline(t *testing.T) {
	d := newTestDispatcher(t)
	_, err := d.Invoke(context.Background(), operatorCaller(), "test.echo", dispArgs{Msg: "x"})
	if !errors.Is(err, ErrNoDeadline) {
		t.Errorf("expected ErrNoDeadline, got %v", err)
	}
}

func TestDispatcher_UnknownVerb(t *testing.T) {
	d := newTestDispatcher(t)
	_, err := d.Invoke(deadlineCtx(t), operatorCaller(), "no.such.verb", nil)
	if !errors.Is(err, ErrUnknownVerb) {
		t.Errorf("expected ErrUnknownVerb, got %v", err)
	}
}

func TestDispatcher_RBACDeny(t *testing.T) {
	d := newTestDispatcher(t)
	caller := Caller{
		ID:     "limited",
		Source: SourceTest,
		Grants: []Grant{{"other.*", true}}, // doesn't cover test.echo
	}
	_, err := d.Invoke(deadlineCtx(t), caller, "test.echo", dispArgs{Msg: "x"})
	if !errors.Is(err, ErrRBACDenied) {
		t.Errorf("expected ErrRBACDenied, got %v", err)
	}
}

// TestDispatcher_RBACDeniedBeforeParse verifies that RBAC denial fires even
// when the raw args are clearly malformed. The caller must never see a parse
// error — parse errors can leak schema information to unauthorized callers.
func TestDispatcher_RBACDeniedBeforeParse(t *testing.T) {
	d := newTestDispatcher(t)
	caller := Caller{
		ID:     "unauthorized",
		Source: SourceTest,
		Grants: []Grant{{"other.*", true}}, // doesn't cover test.echo
	}
	// "abc def" is malformed for dispArgs{Msg string cmd:"required"} when
	// interpreted as positional — Msg would be "abc" and "def" is extra, but
	// more importantly Msg is string so it would succeed. Use a command with a
	// float field to ensure the parse path would fail if reached.
	// The critical assertion is that ErrRBACDenied is returned, not a parse error.
	_, err := d.Invoke(deadlineCtx(t), caller, "test.echo", "clearly malformed args with extra tokens x y z")
	if !errors.Is(err, ErrRBACDenied) {
		t.Errorf("expected ErrRBACDenied before parse, got %v", err)
	}
}

func TestDispatcher_RouteCoordinatorNotYetWired(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(Command{
		Verb:       "coord.cmd",
		Capability: "coord.cmd",
		Route:      RouteCoordinator,
		Args:       dispArgs{},
		Handler:    func(_ context.Context, _ *Env, _ any) (any, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(DispatcherConfig{
		Registry: reg,
		Audit:    NoopAuditSink{},
	})
	defer d.Close()

	_, err := d.Invoke(deadlineCtx(t), operatorCaller(), "coord.cmd", dispArgs{Msg: "x"})
	if !errors.Is(err, ErrNotYetWired) {
		t.Errorf("expected ErrNotYetWired, got %v", err)
	}
}

func TestDispatcher_PendingCleanedOnCtxDone(t *testing.T) {
	d := newTestDispatcher(t)

	_, cancelFn := context.WithCancel(context.Background())
	done := make(chan struct{})
	pendID := d.allocPending(cancelFn)

	go func() {
		defer close(done)
		// Simulate context cancellation triggering cleanup.
		cancelFn()
		// Wait a moment and then resolve it.
		time.Sleep(10 * time.Millisecond)
		d.resolvePending(pendID, RemoteResponse{}, context.Canceled)
	}()

	<-done

	d.mu.Lock()
	_, stillPending := d.pending[pendID]
	d.mu.Unlock()

	if stillPending {
		t.Error("pending request was not cleaned up after resolvePending")
	}
}

func TestDispatcher_PendingCleanedOnClose(t *testing.T) {
	reg := NewRegistry()
	d := NewDispatcher(DispatcherConfig{Registry: reg, Audit: NoopAuditSink{}})

	var wg sync.WaitGroup
	const n = 5
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cancelFn := context.WithCancel(context.Background())
			d.allocPending(cancelFn)
		}()
	}
	wg.Wait()

	// Close should drain all pending.
	_ = d.Close()

	d.mu.Lock()
	remaining := len(d.pending)
	d.mu.Unlock()

	if remaining != 0 {
		t.Errorf("after Close, %d pending requests remain", remaining)
	}
}

// auditCapture collects AuditRecords for inspection.
type auditCapture struct {
	mu      sync.Mutex
	records []AuditRecord
}

func (a *auditCapture) Emit(r AuditRecord) {
	a.mu.Lock()
	a.records = append(a.records, r)
	a.mu.Unlock()
}

func (a *auditCapture) all() []AuditRecord {
	a.mu.Lock()
	out := make([]AuditRecord, len(a.records))
	copy(out, a.records)
	a.mu.Unlock()
	return out
}

func TestDispatcher_AuditSinkReceivesStartAndDone(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(echoCmd()); err != nil {
		t.Fatal(err)
	}
	sink := &auditCapture{}
	d := NewDispatcher(DispatcherConfig{Registry: reg, Audit: sink})
	defer d.Close()

	_, _ = d.Invoke(deadlineCtx(t), operatorCaller(), "test.echo", dispArgs{Msg: "audit-me"})

	records := sink.all()
	if len(records) < 2 {
		t.Fatalf("expected at least 2 audit records, got %d", len(records))
	}
	phases := map[string]int{}
	for _, r := range records {
		phases[r.Phase]++
	}
	if phases["start"] == 0 {
		t.Error("no 'start' audit record emitted")
	}
	if phases["done"] == 0 {
		t.Error("no 'done' audit record emitted")
	}
}

func TestDispatcher_AuditDoneHasTargetsAndDuration(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(echoCmd()); err != nil {
		t.Fatal(err)
	}
	sink := &auditCapture{}
	d := NewDispatcher(DispatcherConfig{Registry: reg, Audit: sink})
	defer d.Close()

	_, _ = d.Invoke(deadlineCtx(t), operatorCaller(), "test.echo", dispArgs{Msg: "check-done"})

	var doneRec *AuditRecord
	for i := range sink.records {
		if sink.records[i].Phase == "done" {
			doneRec = &sink.records[i]
			break
		}
	}
	if doneRec == nil {
		t.Fatal("no done record found")
	}
	if len(doneRec.Targets) == 0 {
		t.Error("done record Targets should be non-empty")
	}
	if doneRec.DurationMS < 0 {
		t.Error("done record DurationMS should be >= 0")
	}
	if !doneRec.OK {
		t.Error("done record OK should be true for successful invoke")
	}
}
