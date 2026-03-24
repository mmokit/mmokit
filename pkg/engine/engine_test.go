package engine

import (
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

type testComp struct{ ID uint32 }

func newTestEngine() *Engine {
	log := logger.New()
	connMgr := net.NewConnManager()
	return New(Config{TickRate: 20}, connMgr, log)
}

func TestNextNetID(t *testing.T) {
	tests := []struct {
		name     string
		base     uint32
		calls    int
		wantLast uint32
	}{
		{"first call returns base+1", 0, 1, 1},
		{"three calls increment", 0, 3, 3},
		{"with base offset", 1000, 3, 1003},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := newTestEngine()
			eng.SetNetIDBase(tt.base)
			var got uint32
			for i := 0; i < tt.calls; i++ {
				got = eng.NextNetID()
			}
			if got != tt.wantLast {
				t.Errorf("NextNetID() after %d calls = %d, want %d", tt.calls, got, tt.wantLast)
			}
		})
	}
}

func TestNextNetID_Successive(t *testing.T) {
	eng := newTestEngine()
	first := eng.NextNetID()
	second := eng.NextNetID()
	if first != 1 {
		t.Errorf("first NextNetID() = %d, want 1", first)
	}
	if second != 2 {
		t.Errorf("second NextNetID() = %d, want 2", second)
	}
}

func TestSetNetIDBase(t *testing.T) {
	eng := newTestEngine()
	eng.SetNetIDBase(5000)
	got := eng.NextNetID()
	if got != 5001 {
		t.Errorf("NextNetID() after SetNetIDBase(5000) = %d, want 5001", got)
	}
}

func TestFlushRemovals(t *testing.T) {
	eng := newTestEngine()
	mapper := ecs.NewMap1[testComp](eng.ECS)
	entity := mapper.NewEntity(&testComp{ID: 42})

	eng.MarkForRemoval(entity)

	eng.GetNetID = func(e ecs.Entity) (uint32, bool) {
		comp := mapper.Get(e)
		return comp.ID, true
	}

	eng.RemovedNetIDs = eng.RemovedNetIDs[:0]
	eng.FlushRemovals()

	if len(eng.RemovedNetIDs) != 1 || eng.RemovedNetIDs[0] != 42 {
		t.Errorf("RemovedNetIDs = %v, want [42]", eng.RemovedNetIDs)
	}
	if eng.ECS.Alive(entity) {
		t.Error("entity should be removed from ECS world")
	}
}

func TestFlushRemovals_SkipsDeadEntities(t *testing.T) {
	eng := newTestEngine()
	mapper := ecs.NewMap1[testComp](eng.ECS)
	entity := mapper.NewEntity(&testComp{ID: 99})

	// Remove entity directly first, so it's dead when FlushRemovals runs
	eng.ECS.RemoveEntity(entity)
	eng.MarkForRemoval(entity)

	eng.GetNetID = func(e ecs.Entity) (uint32, bool) {
		return 99, true
	}
	eng.RemovedNetIDs = eng.RemovedNetIDs[:0]
	eng.FlushRemovals()

	if len(eng.RemovedNetIDs) != 0 {
		t.Errorf("RemovedNetIDs = %v, want empty (dead entity should be skipped)", eng.RemovedNetIDs)
	}
}

// --- TickProfile tests ---

func TestTickProfile_EmptyStats(t *testing.T) {
	tp := NewTickProfile([]string{"A", "B"})
	stats := tp.Stats()
	if stats.SampleCount != 0 {
		t.Errorf("SampleCount = %d, want 0", stats.SampleCount)
	}
	if len(stats.SystemNames) != 2 {
		t.Errorf("SystemNames len = %d, want 2", len(stats.SystemNames))
	}
}

func TestTickProfile_SingleSample(t *testing.T) {
	tp := NewTickProfile([]string{"Sys"})
	tp.Record([]time.Duration{5 * time.Millisecond}, 10*time.Millisecond)

	stats := tp.Stats()
	if stats.SampleCount != 1 {
		t.Fatalf("SampleCount = %d, want 1", stats.SampleCount)
	}
	if stats.Total.Latest != 10*time.Millisecond {
		t.Errorf("Total.Latest = %v, want 10ms", stats.Total.Latest)
	}
	// With a single sample, all percentiles should equal the same value
	if stats.Total.P50 != 10*time.Millisecond {
		t.Errorf("Total.P50 = %v, want 10ms", stats.Total.P50)
	}
	if stats.Total.P95 != 10*time.Millisecond {
		t.Errorf("Total.P95 = %v, want 10ms", stats.Total.P95)
	}
	if stats.Total.P99 != 10*time.Millisecond {
		t.Errorf("Total.P99 = %v, want 10ms", stats.Total.P99)
	}
	if stats.Systems[0].Latest != 5*time.Millisecond {
		t.Errorf("Systems[0].Latest = %v, want 5ms", stats.Systems[0].Latest)
	}
}

func TestTickProfile_MultipleSamples(t *testing.T) {
	tp := NewTickProfile([]string{"Sys"})
	// Record 100 samples with known durations: 1ms, 2ms, ..., 100ms
	for i := 1; i <= 100; i++ {
		d := time.Duration(i) * time.Millisecond
		tp.Record([]time.Duration{d}, d)
	}

	stats := tp.Stats()
	if stats.SampleCount != 100 {
		t.Fatalf("SampleCount = %d, want 100", stats.SampleCount)
	}
	if stats.Total.Latest != 100*time.Millisecond {
		t.Errorf("Total.Latest = %v, want 100ms", stats.Total.Latest)
	}
	// P50 of 1..100: percentileIndex(100, 50) = (100*50-1)/100 = 49 -> sorted[49] = 50ms
	if stats.Total.P50 != 50*time.Millisecond {
		t.Errorf("Total.P50 = %v, want 50ms", stats.Total.P50)
	}
	// P95: percentileIndex(100, 95) = (9500-1)/100 = 94 -> sorted[94] = 95ms
	if stats.Total.P95 != 95*time.Millisecond {
		t.Errorf("Total.P95 = %v, want 95ms", stats.Total.P95)
	}
	// P99: percentileIndex(100, 99) = (9900-1)/100 = 98 -> sorted[98] = 99ms
	if stats.Total.P99 != 99*time.Millisecond {
		t.Errorf("Total.P99 = %v, want 99ms", stats.Total.P99)
	}
	if stats.Total.Max != 100*time.Millisecond {
		t.Errorf("Total.Max = %v, want 100ms", stats.Total.Max)
	}
}

func TestTickProfile_Reset(t *testing.T) {
	tp := NewTickProfile([]string{"Sys"})
	tp.Record([]time.Duration{1 * time.Millisecond}, 2*time.Millisecond)
	tp.Reset()

	stats := tp.Stats()
	if stats.SampleCount != 0 {
		t.Errorf("SampleCount after Reset = %d, want 0", stats.SampleCount)
	}
}
