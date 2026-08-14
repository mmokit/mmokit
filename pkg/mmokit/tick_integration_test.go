// Real-loop integration test: drives Process.Start in a goroutine and
// verifies that OnWorldTickAll callbacks fire through the actual
// mergedHooks.PreFlush wiring in coordinator.go (not just through a
// hand-rolled stage.TickCallbacks() drive). Closes the gap noted in
// the foundation review for the entity message-passing API.
package mmokit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/mmokit"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

func TestOnWorldTickAll_FiresThroughRealLoop(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
		HTTPPort: -1, // disable HTTP listener so parallel tests don't collide on :8080
	})

	var ticks atomic.Int32
	mmokit.OnWorldTickAll(p, func(_ *pkguniverse.Stage, _ float32) {
		ticks.Add(1)
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Start(ctx)
	}()

	// Tick rate defaults to 20Hz (50ms per tick). Wait long enough for ≥3 ticks
	// plus startup overhead, then shut down.
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done

	got := ticks.Load()
	if got < 3 {
		t.Fatalf("OnWorldTickAll fired %d times in ~300ms; expected at least 3", got)
	}
}
