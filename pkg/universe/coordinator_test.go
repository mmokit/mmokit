package universe

import (
	"sync/atomic"
	"testing"

	"github.com/mmokit/mmokit/pkg/engine"
)

// TestSetProtocolStoresValue verifies that SetProtocol stores the supplied
// schema dumper for later use by dumpSchemaAndExit.
func TestSetProtocolStoresValue(t *testing.T) {
	marker := "my-protocol-marker"
	p := New(Config{Mode: "all"})
	p.SetProtocol(marker)
	if p.protocol != marker {
		t.Errorf("protocol = %v, want %q", p.protocol, marker)
	}
}

// TestProtocolUnsetByDefault verifies that a Process constructed via
// universe.New directly (without mmokit.New) starts with no protocol installed.
func TestProtocolUnsetByDefault(t *testing.T) {
	p := New(Config{Mode: "all"})
	if p.protocol != nil {
		t.Errorf("protocol = %v, want nil", p.protocol)
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

// TestProcess_BusPresentInServiceContext verifies that Process.bus is
// constructed in New() and that serviceContext() injects it into every
// *service.Context handed to a kind's Init.
func TestProcess_BusPresentInServiceContext(t *testing.T) {
	p := New(Config{Headless: true, Mode: "all", CellsX: 1, CellsY: 1})
	if p.bus == nil {
		t.Fatal("Process.bus is nil after New")
	}
	ctx := p.serviceContext("test-kind", "test-instance")
	if ctx.Bus == nil {
		t.Fatal("service.Context.Bus is nil — serviceContext did not inject p.bus")
	}
	if ctx.Bus != p.bus {
		t.Fatal("service.Context.Bus is not the same *Bus held by Process")
	}
}
