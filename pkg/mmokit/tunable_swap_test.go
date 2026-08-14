package mmokit

import (
	"context"
	"testing"
	"time"

	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/universe"
)

// TestTunableSwap_ValuePreservedAcrossWasmSwap is the regression guard for the
// wasm load/swap tunable re-apply fix: a `tune set` value MUST survive a
// `wasm swap`. Without the syncSource re-apply in wasm_manager.go, the freshly
// rebuilt instance's Init()→harvestParams seeds the adapter from the GUEST
// DEFAULT (Offset=10), and the operator's tune-set value (50) is silently
// dropped even though the per-process tune registry still holds it. With the
// fix, syncSource writes the registry's intended value back onto the new
// instance after wiring, so Y stays 50.
func TestTunableSwap_ValuePreservedAcrossWasmSwap(t *testing.T) {
	wasmPath := buildWavetuneWasm(t)

	proc := New(Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	// Register the wasm system BEFORE Build so it boots into the cell AND is
	// recorded for both the tune verbs and the wasm.swap verb.
	AddWasmSystem[Position](proc, wasmPath)
	proc.Build()
	t.Cleanup(proc.Shutdown)

	var cell *universe.Cell
	for _, c := range proc.Cells {
		cell = c
		break
	}
	if cell == nil {
		t.Fatal("no cells after Build")
	}

	// Start the cell loop live so on-loop dispatch (tune.set / wasm.swap) drains.
	loopCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		cell.Run(loopCtx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// Spawn ONE entity at Y=0 on the loop goroutine.
	if err := cell.Engine.RunOnLoop(context.Background(), func() error {
		cell.Stage.Spawn(Position{X: 0, Y: 0})
		return nil
	}); err != nil {
		t.Fatalf("spawn entity: %v", err)
	}

	// (1) After boot, the guest applies Offset's tag default (10) to Y.
	if got, ok := waitForY(t, cell, 10); !ok {
		t.Fatalf("default Offset not applied: Position.Y=%v want 10", got)
	}

	caller := cmdsys.NewOperatorIdentity("test-op")

	// (2) tune.set Offset=50; the running guest now writes 50 into Y.
	setCtx, cancelSet := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSet()
	res, err := proc.CmdDispatcher().Invoke(setCtx, caller, "tune.set", tuneSetArgs{
		System: "wavetune",
		Field:  "Offset",
		Value:  "50",
	})
	if err != nil {
		t.Fatalf("tune.set dispatch: %v", err)
	}
	for _, tr := range res.PerTarget {
		if !tr.OK {
			t.Fatalf("tune.set target %q not OK: %s", tr.TargetID, tr.Error)
		}
	}
	if got, ok := waitForY(t, cell, 50); !ok {
		t.Fatalf("tune.set Offset=50 did not reach guest: Position.Y=%v want 50", got)
	}

	// (3) wasm.swap rebuilds the SAME module from disk and hot-swaps it in.
	swapCtx, cancelSwap := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelSwap()
	swapRes, err := proc.CmdDispatcher().Invoke(swapCtx, caller, "wasm.swap", wasmNameArgs{
		Name: "wavetune",
	})
	if err != nil {
		t.Fatalf("wasm.swap dispatch: %v", err)
	}
	for _, tr := range swapRes.PerTarget {
		if !tr.OK {
			t.Fatalf("wasm.swap target %q not OK: %s", tr.TargetID, tr.Error)
		}
	}

	// (4) REGRESSION ASSERTION: after the swap, Y must STILL be 50 — the
	// registry's tune-set value, NOT the guest default of 10. Without the
	// Step-1 re-apply this reverts to 10 and this fails.
	if got, ok := waitForY(t, cell, 50); !ok {
		t.Fatalf("wasm.swap reverted tunable to guest default: Position.Y=%v want 50 "+
			"(syncSource re-apply on the live load/swap path is missing)", got)
	}
}
