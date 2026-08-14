package engine

import (
	"context"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/net"
)

type mockSystem struct {
	name string
	log  *[]string
}

type updateFunc func(float32)

func (f updateFunc) Update(dt float32) { f(dt) }

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
	names := []string{"A", "B", "C"}

	gl := NewGameLoop(eng, systems, names, Hooks{})

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
	names := []string{"sys"}

	hooks := Hooks{
		ClearTickState: func() { hookLog = append(hookLog, "ClearTickState") },
		OnConnect:      func(id uint32) {},
		OnDisconnect:   func(id uint32) {},
		PreFlush:       func() { hookLog = append(hookLog, "PreFlush") },
		PostFlush:      func() { hookLog = append(hookLog, "PostFlush") },
		PostTick:       func() { hookLog = append(hookLog, "PostTick") },
	}

	gl := NewGameLoop(eng, systems, names, hooks)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	gl.Run(ctx)

	if eng.Tick == 0 {
		t.Fatal("expected at least one tick to run")
	}

	// Verify first tick hook order
	wantPerTick := []string{"ClearTickState", "PreFlush", "PostFlush", "PostTick"}
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

func TestGameLoop_DespawnAfterRemovalSamplingPublishesNextTick(t *testing.T) {
	eng := newLoopTestEngine()
	mapper := ecs.NewMap1[testComp](eng.ECS)
	entity := mapper.NewEntity(&testComp{ID: 73})
	eng.GetNetID = func(e ecs.Entity) (uint32, bool) {
		return mapper.Get(e).ID, true
	}

	var sampled [][]uint32
	sample := updateFunc(func(float32) {
		sampled = append(sampled, append([]uint32(nil), eng.SampleRemovedNetIDs()...))
	})
	despawn := updateFunc(func(float32) {
		eng.RemoveEntityNow(entity)
	})
	gl := NewGameLoop(eng, []System{sample, despawn}, []string{"replication", "late-despawn"}, Hooks{})

	gl.tick(0.05)
	gl.tick(0.05)

	if len(sampled) != 2 {
		t.Fatalf("sample count = %d, want 2", len(sampled))
	}
	if len(sampled[0]) != 0 {
		t.Fatalf("first tick sample = %v, want empty", sampled[0])
	}
	if len(sampled[1]) != 1 || sampled[1][0] != 73 {
		t.Fatalf("second tick sample = %v, want [73]", sampled[1])
	}
}

func TestGameLoop_EndTickRemovalAfterSamplingPublishesNextTick(t *testing.T) {
	eng := newLoopTestEngine()
	mapper := ecs.NewMap1[testComp](eng.ECS)
	entity := mapper.NewEntity(&testComp{ID: 74})
	eng.GetNetID = func(e ecs.Entity) (uint32, bool) {
		return mapper.Get(e).ID, true
	}

	var sampled [][]uint32
	sample := updateFunc(func(float32) {
		sampled = append(sampled, append([]uint32(nil), eng.SampleRemovedNetIDs()...))
	})
	queueRemoval := updateFunc(func(float32) {
		eng.MarkForRemoval(entity)
	})
	gl := NewGameLoop(eng, []System{sample, queueRemoval}, []string{"replication", "queue-removal"}, Hooks{})

	// FlushRemovals runs after both systems, hence after the tick-one sample.
	gl.tick(0.05)
	gl.tick(0.05)

	if len(sampled) != 2 {
		t.Fatalf("sample count = %d, want 2", len(sampled))
	}
	if len(sampled[0]) != 0 {
		t.Fatalf("first tick sample = %v, want empty", sampled[0])
	}
	if len(sampled[1]) != 1 || sampled[1][0] != 74 {
		t.Fatalf("second tick sample = %v, want [74]", sampled[1])
	}
}

func TestGameLoop_ContextCancellation(t *testing.T) {
	eng := newLoopTestEngine()
	gl := NewGameLoop(eng, nil, nil, Hooks{})

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
