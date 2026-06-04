package mmokit_test

import (
	"encoding/binary"
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

func decodeTicks(b []byte) uint64 {
	if len(b) != 8 {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

// readShieldCurrent returns the Current of the single spawned Shield entity.
func readShieldCurrent(stage *pkguniverse.Stage) float32 {
	var out float32
	mmokit.ForEach1(stage, func(_ mmokit.Entity, sh *gamecomp.Shield) { out = sh.Current })
	return out
}

func TestWasmSwap_PreservesStateAndBehavior(t *testing.T) {
	wasmPath := buildWasmModule(t, "../../examples/4node-basic/wasmmods/shieldregen")

	stage, eng := newTestStage(t)
	stage.Spawn(mmokit.Position{}, gamecomp.Shield{Current: 50, Max: 100, RegenRate: 10})

	def := mmokit.NewWasmSystem[gamecomp.Shield](wasmPath)

	// v1: tick twice. Each tick regenerates RegenRate*dt = 10*0.5 = 5.
	sys1 := def.Factory()
	mmokit.WireSystem(sys1, stage.ECSWorld(), eng, stage)
	stage.TickOne(sys1, 0.5) // Current 50 -> 55
	stage.TickOne(sys1, 0.5) // Current 55 -> 60 ; internal ticks == 2
	if got := readShieldCurrent(stage); got != 60 {
		t.Fatalf("after 2 ticks Current=%v want 60", got)
	}

	// Swap: snapshot v1, build a fresh v2, restore state into it.
	swap1, ok := sys1.(mmokit.SwappableSystem)
	if !ok {
		t.Fatal("wasm system does not implement SwappableSystem")
	}
	state, err := swap1.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	sys2 := def.Factory()
	swap2 := sys2.(mmokit.SwappableSystem)
	if err := swap2.Restore(state); err != nil {
		t.Fatalf("restore: %v", err)
	}
	mmokit.WireSystem(sys2, stage.ECSWorld(), eng, stage)
	stage.TickOne(sys2, 0.5) // Current 60 -> 65 ; internal ticks 2 -> 3
	if got := readShieldCurrent(stage); got != 65 {
		t.Fatalf("after swap+tick Current=%v want 65", got)
	}

	// Internal ticks must have carried 2 across the swap, now 3.
	state2, err := swap2.Snapshot()
	if err != nil {
		t.Fatalf("snapshot2: %v", err)
	}
	if got := decodeTicks(state2); got != 3 {
		t.Fatalf("internal ticks after swap = %d, want 3 (2 pre-swap + 1 post)", got)
	}
}
