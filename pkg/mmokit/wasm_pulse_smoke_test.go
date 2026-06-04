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

	if v := m.ABIVersion(ctx); v != 1 {
		t.Fatalf("ABIVersion=%d want 1", v)
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
func TestPulseSystem_DrivesColliderRadius(t *testing.T) {
	wasmPath := buildWasmModule(t, "../../examples/4node-basic/wasmmods/pulse")

	stage, eng := newTestStage(t)
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
