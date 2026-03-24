package engine

import (
	"context"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

type mockSystem struct {
	name string
	log  *[]string
}

func (m *mockSystem) Name() string { return m.name }
func (m *mockSystem) Update(dt float32) {
	*m.log = append(*m.log, m.name)
}

func TestGameLoop_SystemOrder(t *testing.T) {
	eng := newLoopTestEngine()
	var callLog []string

	systems := []System{
		&mockSystem{name: "A", log: &callLog},
		&mockSystem{name: "B", log: &callLog},
		&mockSystem{name: "C", log: &callLog},
	}

	gl := NewGameLoop(eng, systems, Hooks{})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	gl.Run(ctx)

	if eng.Tick == 0 {
		t.Fatal("expected at least one tick to run")
	}
	// Check that systems ran in order for the first tick (pattern repeats)
	if len(callLog) < 3 {
		t.Fatalf("callLog has %d entries, want at least 3", len(callLog))
	}
	if callLog[0] != "A" || callLog[1] != "B" || callLog[2] != "C" {
		t.Errorf("first tick system order = %v, want [A B C]", callLog[:3])
	}
}

func TestGameLoop_HookOrder(t *testing.T) {
	eng := newLoopTestEngine()
	var hookLog []string
	var sysLog []string

	systems := []System{
		&mockSystem{name: "sys", log: &sysLog},
	}

	hooks := Hooks{
		ClearTickState: func() { hookLog = append(hookLog, "ClearTickState") },
		OnConnect:      func(id uint32) {},
		OnDisconnect:   func(id uint32) {},
		ProcessLogins:  func() { hookLog = append(hookLog, "ProcessLogins") },
		PreFlush:       func() { hookLog = append(hookLog, "PreFlush") },
		PostFlush:      func() { hookLog = append(hookLog, "PostFlush") },
		PostTick:       func() { hookLog = append(hookLog, "PostTick") },
	}

	gl := NewGameLoop(eng, systems, hooks)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	gl.Run(ctx)

	if eng.Tick == 0 {
		t.Fatal("expected at least one tick to run")
	}

	// Verify first tick hook order
	wantPerTick := []string{"ClearTickState", "ProcessLogins", "PreFlush", "PostFlush", "PostTick"}
	if len(hookLog) < len(wantPerTick) {
		t.Fatalf("hookLog has %d entries, want at least %d: %v", len(hookLog), len(wantPerTick), hookLog)
	}
	for i, want := range wantPerTick {
		if hookLog[i] != want {
			t.Errorf("hookLog[%d] = %q, want %q", i, hookLog[i], want)
		}
	}

	// Systems should have run between ProcessLogins and PreFlush
	if len(sysLog) == 0 {
		t.Error("system was never called")
	}
}

func TestGameLoop_ContextCancellation(t *testing.T) {
	eng := newLoopTestEngine()
	gl := NewGameLoop(eng, nil, Hooks{})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		gl.Run(ctx)
		close(done)
	}()

	// Cancel immediately
	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func newLoopTestEngine() *Engine {
	log := logger.New()
	connMgr := net.NewConnManager()
	return New(Config{TickRate: 20}, connMgr, log)
}
