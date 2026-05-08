package universe

import (
	"sync/atomic"
	"testing"

	"github.com/zenion/mmoserver/pkg/engine"
)

// TestConfigProtocolRoundTrip verifies that Config.Protocol passes through
// Process.Protocol() unchanged.
func TestConfigProtocolRoundTrip(t *testing.T) {
	marker := "my-protocol-marker"
	p := New(Config{
		Mode:     "all",
		Protocol: marker,
	})
	if p.Protocol() != marker {
		t.Errorf("Protocol() = %v, want %q", p.Protocol(), marker)
	}
}

// TestConfigProtocolUnset verifies that Process.Protocol() returns nil when
// Config.Protocol is not set.
func TestConfigProtocolUnset(t *testing.T) {
	p := New(Config{Mode: "all"})
	if p.Protocol() != nil {
		t.Errorf("Protocol() = %v, want nil", p.Protocol())
	}
}

// countingSystem increments a shared counter on Init.
type countingSystem struct {
	engine.SystemBase
	counter *int32
}

func (s *countingSystem) Init() { atomic.AddInt32(s.counter, 1) }

// TestSystemInitRunsOnceAfterConstruction verifies that a system's Init()
// fires exactly once per cell during cell construction. Replaces the
// pre-C-full TestConfigOnInitRunsOnceAfterConstruction.
func TestSystemInitRunsOnceAfterConstruction(t *testing.T) {
	var calls int32
	mmo := New(Config{
		CellsX: 2, CellsY: 2, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	// Each cell gets a fresh countingSystem instance; they all share &calls.
	mmo.AddSystem(engine.SystemDef{
		Name: "OnceInitTracker",
		Factory: func() engine.System {
			return &countingSystem{counter: &calls}
		},
	})
	mmo.Build()
	t.Cleanup(mmo.Shutdown)

	expected := int32(4) // 2x2 cells, Init() per cell
	if atomic.LoadInt32(&calls) != expected {
		t.Errorf("Init fired %d times across 4 cells, expected %d", atomic.LoadInt32(&calls), expected)
	}
}

// TestOnStageInit_FiresOnEveryStage verifies that OnStageInit fires once
// per cell created during Build().
func TestOnStageInit_FiresOnEveryStage(t *testing.T) {
	p := New(Config{
		Mode:     "all",
		CellsX:   2,
		CellsY:   1,
		Headless: true,
	})

	var seen []*Stage
	p.OnStageInit(func(s *Stage) {
		seen = append(seen, s)
	})

	p.Build()

	if len(seen) != 2 {
		t.Fatalf("OnStageInit fired %d times, want 2 (one per cell)", len(seen))
	}
	// Each Stage must be a real cell's Stage.
	stageSet := map[*Stage]bool{}
	for _, s := range seen {
		stageSet[s] = true
	}
	for id, cell := range p.Cells {
		if !stageSet[cell.Stage] {
			t.Errorf("cell %s Stage not in seen set", id)
		}
	}
}

// TestOnStageInit_LateRegistrationCatchesUp verifies that an OnStageInit
// callback registered AFTER Build() still fires for every existing
// stage. This makes registration order forgiving for game code.
func TestOnStageInit_LateRegistrationCatchesUp(t *testing.T) {
	p := New(Config{
		Mode:     "all",
		CellsX:   2,
		CellsY:   1,
		Headless: true,
	})

	p.Build()

	var seen int
	p.OnStageInit(func(s *Stage) {
		seen++
	})

	if seen != 2 {
		t.Fatalf("late OnStageInit caught up on %d stages, want 2", seen)
	}
}
