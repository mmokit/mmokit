// Real-loop parity test for CE-008: a system's dt and a per-tick callback's
// dt are the same number. Both reach the game through the actual
// mergedHooks wiring in coordinator.go — the callback via PreFlush, the
// system via GameLoop.tick — and before CE-008 the coordinator derived its
// own exact 1.0/TickRate while the loop truncated, so the two disagreed at
// any rate that does not divide 1000.
package facadetest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mmokit/mmokit"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// dtProbeSink is package-level because NewSystem's argument is a prototype
// for type information only — the framework builds a fresh zero value per
// cell, so a field on the prototype is nil by the time Update runs.
var (
	dtProbeMu   sync.Mutex
	dtProbeSeen []float32
)

type dtProbeSystem struct {
	mmokit.SystemBase
}

func (s *dtProbeSystem) Update(dt float32) {
	dtProbeMu.Lock()
	dtProbeSeen = append(dtProbeSeen, dt)
	dtProbeMu.Unlock()
}

func TestSystemAndTickCallbackShareOneDt(t *testing.T) {
	// 60Hz is deliberate: it does not divide 1000, so it is the rate where
	// the two derivations used to differ (0.016 vs 0.0166667). At the
	// default 20Hz this test would pass on the unfixed tree.
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		TickRate: 60,
		Headless: true,
		HTTPPort: -1,
	})

	dtProbeMu.Lock()
	dtProbeSeen = nil
	dtProbeMu.Unlock()
	p.AddSystem(mmokit.NewSystem(&dtProbeSystem{}))

	var callbackMu sync.Mutex
	var callbackDt []float32
	mmokit.OnWorldTickAll(p, func(_ *pkguniverse.Stage, dt float32) {
		callbackMu.Lock()
		callbackDt = append(callbackDt, dt)
		callbackMu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Start(ctx)
	}()
	// 17ms per tick; 300ms is comfortably more than the 3 ticks asserted.
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done

	dtProbeMu.Lock()
	systemDt := append([]float32(nil), dtProbeSeen...)
	dtProbeMu.Unlock()
	callbackMu.Lock()
	callbackDt = append([]float32(nil), callbackDt...)
	callbackMu.Unlock()

	if len(systemDt) < 3 || len(callbackDt) < 3 {
		t.Fatalf("system ticked %d times, callback %d; want at least 3 each",
			len(systemDt), len(callbackDt))
	}
	// 17ms is what a 60Hz request schedules; see pkg/engine/tick_schedule.go.
	const want float32 = 0.017
	if systemDt[0] != want {
		t.Errorf("system dt = %v, want %v", systemDt[0], want)
	}
	if callbackDt[0] != want {
		t.Errorf("tick callback dt = %v, want %v", callbackDt[0], want)
	}
	if systemDt[0] != callbackDt[0] {
		t.Errorf("system dt %v != tick callback dt %v inside the same loop",
			systemDt[0], callbackDt[0])
	}
}
