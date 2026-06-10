package mmokit_test

import (
	"context"
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/wasmabi"
	"github.com/zenion/mmoserver/pkg/wasmhost"
)

// TestPulseModule_LoadsAndDeclaresQuery builds the visible Pulse demo module
// (which oscillates Collider.Radius) and confirms it loads as a reactor with
// the expected ABI version and a ReadWrite column declaration. Guards the same
// init()-vs-main() / -buildmode=c-shared pitfalls as the shield smoke.
func TestPulseModule_LoadsAndDeclaresQuery(t *testing.T) {
	wasmPath := buildWasmModule(t, "../../examples/4node-basic/wasmmods/pulse")

	ctx := context.Background()
	rt := wasmhost.New(ctx)
	defer rt.Close(ctx)

	m, err := wasmhost.Load(ctx, rt, readFile(t, wasmPath))
	if err != nil {
		t.Fatalf("load pulse module: %v", err)
	}
	defer m.Close(ctx)

	if v := m.ABIVersion(ctx); v != wasmabi.ABIVersion {
		t.Fatalf("ABIVersion=%d want %d", v, wasmabi.ABIVersion)
	}
	id, rw := m.Query(ctx)
	if want := wasmabi.ElemSize[mmokit.Collider](); id != want || !rw {
		t.Fatalf("Query=(%d,%v) want (%d,true)  [Collider elem size, ReadWrite]", id, rw, want)
	}
}

// TestPulseSystem_DrivesColliderRadius runs the pulse module through the real
// adapter over a Collider — a MIXED-TYPE POD (float32s + uint8s + padding) — and
// confirms (a) it mutates Radius to the phase-0 value (27 = 6 + (48-6)*0.5), and
// (b) every non-radius field survives the gather/scatter byte round-trip
// unchanged. This is a stricter layout check than the all-float32 Shield.
//
// The pulse phase now derives from the stage's cluster clock, so we pin it to
// t=0 to land on phase 0 deterministically (sin(0)=0 → osc=0.5 → r=27).
func TestPulseSystem_DrivesColliderRadius(t *testing.T) {
	wasmPath := buildWasmModule(t, "../../examples/4node-basic/wasmmods/pulse")

	stage, eng := newTestStage(t)
	stage.ClusterClock().SetNowFn(func() uint64 { return 0 })
	stage.Spawn(mmokit.Position{}, mmokit.Collider{Radius: 20, Width: 5, Height: 6, Layer: 2, Shape: 1})

	sys := mmokit.NewWasmSystem[mmokit.Collider](wasmPath).Factory()
	mmokit.WireSystem(sys, stage.ECSWorld(), eng, stage)
	stage.TickOne(sys, 0.05)

	var got mmokit.Collider
	mmokit.ForEach1(stage, func(_ mmokit.Entity, c *mmokit.Collider) { got = *c })

	if got.Radius != 27 {
		t.Fatalf("Radius=%v want 27 (pulse oscillation at phase 0)", got.Radius)
	}
	if got.Width != 5 || got.Height != 6 || got.Layer != 2 || got.Shape != 1 {
		t.Fatalf("non-radius Collider fields corrupted by round-trip: %+v", got)
	}
}

// TestPulseSystem_PhaseFollowsClusterTimeNotCell is the regression guard for
// the cell-boundary animation stutter: the pulse phase must come from the
// cluster-coherent clock, NOT a cell-local tick counter. Two independent
// stages (cells) are pinned to the SAME cluster time but ticked a DIFFERENT
// number of times; their radii must match exactly. Under the old cell-local
// `Ticks++` design the cell ticked more often would be at a different phase —
// exactly the discontinuity an entity saw when it crossed into another cell.
func TestPulseSystem_PhaseFollowsClusterTimeNotCell(t *testing.T) {
	wasmPath := buildWasmModule(t, "../../examples/4node-basic/wasmmods/pulse")

	// A non-zero, tick-aligned time so the phase is off the sin(0)=0 zero — if
	// the guest ignored cluster time both cells would still match at 27, hiding
	// the bug. At t=1s (Rate=0.5 ⇒ 10 rad/s) the radius is ~15.6, not 27.
	const nowMs uint64 = 1000

	radiusAfter := func(ticks int) float32 {
		stage, eng := newTestStage(t)
		stage.ClusterClock().SetNowFn(func() uint64 { return nowMs })
		stage.Spawn(mmokit.Position{}, mmokit.Collider{Radius: 20})
		sys := mmokit.NewWasmSystem[mmokit.Collider](wasmPath).Factory()
		mmokit.WireSystem(sys, stage.ECSWorld(), eng, stage)
		for range ticks {
			stage.TickOne(sys, 0.05)
		}
		var got mmokit.Collider
		mmokit.ForEach1(stage, func(_ mmokit.Entity, c *mmokit.Collider) { got = *c })
		return got.Radius
	}

	cellA := radiusAfter(1)  // crossed in early — one tick of local history
	cellB := radiusAfter(11) // long-resident cell — many local ticks

	if cellA != cellB {
		t.Fatalf("radius diverged across cells at equal cluster time: cellA=%v cellB=%v "+
			"(phase is leaking cell-local tick count instead of cluster time)", cellA, cellB)
	}
	if cellA == 27 {
		t.Fatalf("radius=27 means phase 0 — cluster time (%dms) was not applied", nowMs)
	}
}
