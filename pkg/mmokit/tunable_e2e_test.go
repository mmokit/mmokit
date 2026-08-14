package mmokit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/cmdsys"
	"github.com/mmokit/mmokit/pkg/universe"
)

// buildWavetuneWasm compiles the wavetune test fixture to a wasip1 reactor
// .wasm (c-shared so //go:wasmexport is reachable) and returns its path. The
// temp dir is cleaned up by the test framework, so no stray .wasm lands in the
// repo. Mirrors buildShieldWasmForTest in wasm_manager_verbs_internal_test.go.
func buildWavetuneWasm(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "wavetune.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, ".")
	cmd.Dir = "internal/testmods/wavetune"
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wavetune wasm: %v\n%s", err, b)
	}
	return out
}

// readSingleY returns the Position.Y of the one spawned entity, read on the
// cell's loop goroutine via RunOnLoop (all ECS access stays on-loop).
func readSingleY(t *testing.T, cell *universe.Cell) float32 {
	t.Helper()
	var y float32
	if err := cell.Engine.RunOnLoop(context.Background(), func() error {
		ForEach1(cell.Stage, func(_ Entity, p *Position) { y = p.Y })
		return nil
	}); err != nil {
		t.Fatalf("read Position.Y on loop: %v", err)
	}
	return y
}

// waitForY polls Position.Y (on-loop) until it equals want or the deadline
// passes. The wavetune Update writes Offset into every entity's Y every tick,
// so once a couple of ticks elapse Y settles to the current Offset. Returns the
// last observed value for diagnostics on failure.
func waitForY(t *testing.T, cell *universe.Cell, want float32) (float32, bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last float32
	for time.Now().Before(deadline) {
		last = readSingleY(t, cell)
		if last == want {
			return last, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return last, false
}

// TestTunableEndToEnd_WasmViaTuneSet is the end-to-end proof of the mmokit
// tunable layer over a wasm system: AddWasmSystem registers a tune-tagged wasm
// system, Build() fires OnCellSystemsReady → SyncCellTunables which harvests the
// guest's Offset tunable (default 10) into the per-process registry and applies
// it to the live instance. We then drive the tune.set verb through the real
// cmdsys.Dispatcher and confirm the new value reaches the running guest, mutating
// entity state on the next tick.
//
// Two non-negotiable assertions:
//  1. After boot, the wavetune system writes Offset's tag default (10) into Y.
//  2. After `tune set wavetune Offset 50`, it writes 50.
func TestTunableEndToEnd_WasmViaTuneSet(t *testing.T) {
	wasmPath := buildWavetuneWasm(t)

	// Full Process build so the C5 hook fires and tune.* verbs are registered.
	proc := New(Config{
		CellsX: 1, CellsY: 1, CellSize: 1000, TickRate: 20, AoIRadius: 100,
		Headless: true,
	})
	// Register the wasm system BEFORE Build so it boots into the cell AND is
	// recorded for the tune verbs. The logical name derives from the filename:
	// "wavetune.wasm" -> "wavetune".
	AddWasmSystem[Position](proc, wasmPath)
	proc.Build()
	t.Cleanup(proc.Shutdown)

	// Grab the single cell.
	var cell *universe.Cell
	for _, c := range proc.Cells {
		cell = c
		break
	}
	if cell == nil {
		t.Fatal("no cells after Build")
	}

	// Start the cell's game loop live. tune.set routes through CmdOnLoop →
	// RunOnLoop, which blocks until the loop drains the queue — so the loop must
	// actually be ticking. Stopped via Cleanup.
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

	// (1) After boot+ticks, the guest applies Offset's tag default (10) to Y.
	// If this fails (e.g. Y stays 0), it's a real C5/C1 integration bug — the
	// hook didn't fire, harvestParams returned no schema, or the registry wasn't
	// seeded. Do NOT weaken the assertion to make it pass.
	if got, ok := waitForY(t, cell, 10); !ok {
		t.Fatalf("default Offset not applied: Position.Y=%v want 10 "+
			"(C5 hook / harvestParams / registry-seed integration bug)", got)
	}

	// (2) Drive tune.set through the real cmdsys.Dispatcher to set Offset=50.
	ctx, cancelCtx := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCtx()
	caller := cmdsys.NewOperatorIdentity("test-op")
	res, err := proc.CmdDispatcher().Invoke(ctx, caller, "tune.set", tuneSetArgs{
		System: "wavetune",
		Field:  "Offset",
		Value:  "50",
	})
	if err != nil {
		t.Fatalf("tune.set dispatch: %v", err)
	}
	if len(res.PerTarget) == 0 {
		t.Fatalf("tune.set produced no per-target results: %+v", res)
	}
	for _, tr := range res.PerTarget {
		if !tr.OK {
			t.Fatalf("tune.set target %q not OK: %s", tr.TargetID, tr.Error)
		}
	}

	// (3) The running guest now writes 50 into Y on subsequent ticks.
	if got, ok := waitForY(t, cell, 50); !ok {
		t.Fatalf("tune.set Offset=50 did not reach guest: Position.Y=%v want 50", got)
	}
}
