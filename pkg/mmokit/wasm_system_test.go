package mmokit_test

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/mmokit"
)

// PodVal has identical layout to a single float32, so the echomod fixture
// (which adds 1.0 to each float32 in its column) increments PodVal.X per tick.
type PodVal struct{ X float32 }

func TestWasmSystem_BridgesColumnThroughModule(t *testing.T) {
	wasmPath := buildWasmModule(t, "../wasmhost/internal/echomod")

	stage, eng := newTestStage(t)
	const n = 5
	for i := 0; i < n; i++ {
		stage.Spawn(mmokit.Position{}, PodVal{X: float32(i)})
	}

	def := mmokit.NewWasmSystem[PodVal](wasmPath)
	sys := def.Factory()
	mmokit.WireSystem(sys, stage.ECSWorld(), eng, stage)
	stage.TickOne(sys, 0.05)

	// After one tick every X should have incremented by 1: the set becomes {1,2,3,4,5}.
	// Use a set so the assertion is independent of ECS iteration order.
	got := map[float32]bool{}
	mmokit.ForEach1(stage, func(_ mmokit.Entity, p *PodVal) { got[p.X] = true })
	for i := 0; i < n; i++ {
		want := float32(i) + 1
		if !got[want] {
			t.Fatalf("expected a PodVal with X=%v after one tick; got %v", want, got)
		}
	}
}
