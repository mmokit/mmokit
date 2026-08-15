package facadetest

import (
	"context"
	"testing"

	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/internal/wasmfixtures"
	"github.com/mmokit/mmokit/pkg/wasmabi"
	"github.com/mmokit/mmokit/pkg/wasmhost"
)

// TestTintModule_LoadsAndDeclaresQuery builds the visible Tint demo module
// (which animates the replicated entity color) and confirms it loads as a
// reactor with the expected ABI version and a ReadWrite column declaration.
// Guards the same init()-vs-main() / -buildmode=c-shared pitfalls as the
// shield smoke.
func TestTintModule_LoadsAndDeclaresQuery(t *testing.T) {
	wasmPath := wasmfixtures.Build(t, "examples/4node-basic/wasmmods/tint")

	ctx := context.Background()
	rt := wasmhost.New(ctx)
	defer rt.Close(ctx)

	m, err := wasmhost.Load(ctx, rt, readFile(t, wasmPath))
	if err != nil {
		t.Fatalf("load tint module: %v", err)
	}
	defer m.Close(ctx)

	if v := m.ABIVersion(ctx); v != wasmabi.ABIVersion {
		t.Fatalf("ABIVersion=%d want %d", v, wasmabi.ABIVersion)
	}
	id, rw := m.Query(ctx)
	if want := wasmabi.ElemSize[mmokit.Tint](); id != want || !rw {
		t.Fatalf("Query=(%d,%v) want (%d,true)  [Tint elem size, ReadWrite]", id, rw, want)
	}
}

// TestTintSystem_DrivesEntityColor runs the tint module through the real
// adapter over a Tint column — a sub-word-size POD (3 bytes) — and confirms
// the active rainbow-sweep variant overwrites the spawn color with the
// phase-0 color: hue 0 ⇒ pure red (255,0,0).
//
// The hue phase derives from the stage's cluster clock, so we pin it to t=0
// to land on hue 0 deterministically.
func TestTintSystem_DrivesEntityColor(t *testing.T) {
	wasmPath := wasmfixtures.Build(t, "examples/4node-basic/wasmmods/tint")

	stage, eng := newTestStage(t)
	stage.ClusterClock().SetNowFn(func() uint64 { return 0 })
	stage.Spawn(mmokit.Position{}, mmokit.Tint{R: 1, G: 2, B: 3})

	sys := mmokit.NewWasmSystem[mmokit.Tint](wasmPath).Factory()
	mmokit.WireSystem(sys, stage.ECSWorld(), eng, stage)
	stage.TickOne(sys, 0.05)

	var got mmokit.Tint
	mmokit.ForEach1(stage, func(_ mmokit.Entity, c *mmokit.Tint) { got = *c })

	if got != (mmokit.Tint{R: 255, G: 0, B: 0}) {
		t.Fatalf("Tint=%+v want {255 0 0} (rainbow sweep at phase 0 = hue 0 = red)", got)
	}
}

// TestTintSystem_PhaseFollowsClusterTimeNotCell is the regression guard for
// the cell-boundary animation stutter: the hue phase must come from the
// cluster-coherent clock, NOT a cell-local tick counter. Two independent
// stages (cells) are pinned to the SAME cluster time but ticked a DIFFERENT
// number of times; their colors must match exactly. Under a cell-local
// `Ticks++` design the cell ticked more often would be at a different hue —
// exactly the discontinuity an entity saw when it crossed into another cell.
func TestTintSystem_PhaseFollowsClusterTimeNotCell(t *testing.T) {
	wasmPath := wasmfixtures.Build(t, "examples/4node-basic/wasmmods/tint")

	// A non-zero, tick-aligned time so the hue is off the phase-0 red — if the
	// guest ignored cluster time both cells would still match at red, hiding
	// the bug. At t=1s (Rate=0.2 ⇒ 4 rad/s) the hue is ~0.64, not 0.
	const nowMs uint64 = 1000

	colorAfter := func(ticks int) mmokit.Tint {
		stage, eng := newTestStage(t)
		stage.ClusterClock().SetNowFn(func() uint64 { return nowMs })
		stage.Spawn(mmokit.Position{}, mmokit.Tint{})
		sys := mmokit.NewWasmSystem[mmokit.Tint](wasmPath).Factory()
		mmokit.WireSystem(sys, stage.ECSWorld(), eng, stage)
		for range ticks {
			stage.TickOne(sys, 0.05)
		}
		var got mmokit.Tint
		mmokit.ForEach1(stage, func(_ mmokit.Entity, c *mmokit.Tint) { got = *c })
		return got
	}

	cellA := colorAfter(1)  // crossed in early — one tick of local history
	cellB := colorAfter(11) // long-resident cell — many local ticks

	if cellA != cellB {
		t.Fatalf("color diverged across cells at equal cluster time: cellA=%+v cellB=%+v "+
			"(phase is leaking cell-local tick count instead of cluster time)", cellA, cellB)
	}
	if cellA == (mmokit.Tint{R: 255, G: 0, B: 0}) {
		t.Fatalf("color is phase-0 red — cluster time (%dms) was not applied", nowMs)
	}
}
