package mmokit_test

import (
	"fmt"
	"testing"

	"github.com/mmokit/mmokit/pkg/mmokit"
	"github.com/mmokit/mmokit/pkg/mmokit/internal/testmods/podcomp"
)

// benchShield runs b.N ticks of either the native ShieldRegen loop or the
// wasm-backed system over n non-saturating Shield entities. Max is huge so
// every tick does real regen work on both paths (avoids the Current==Max
// short-circuit skewing the native baseline).
func benchShield(b *testing.B, n int, useWasm bool) {
	stage, eng := newTestStage(b)
	for i := 0; i < n; i++ {
		stage.Spawn(mmokit.Position{}, podcomp.Shield{Current: 0, Max: 1e30, RegenRate: 1})
	}
	const dt = float32(0.05)

	var tick func()
	if useWasm {
		wasmPath := buildWasmModule(b, "internal/testmods/shieldregen")
		sys := mmokit.NewWasmSystem[podcomp.Shield](wasmPath).Factory()
		mmokit.WireSystem(sys, stage.ECSWorld(), eng, stage)
		tick = func() { sys.Update(dt) }
	} else {
		// Native ShieldRegen, identical math to internal/game/system_shieldregen.go.
		tick = func() {
			mmokit.ForEach1(stage, func(_ mmokit.Entity, sh *podcomp.Shield) {
				if sh.DamageCooldown > 0 {
					sh.DamageCooldown -= dt
					return
				}
				if sh.Current < sh.Max {
					sh.Current = min(sh.Current+sh.RegenRate*dt, sh.Max)
				}
			})
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tick()
	}
}

func BenchmarkShield(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("native/%d", n), func(b *testing.B) { benchShield(b, n, false) })
		b.Run(fmt.Sprintf("wasm/%d", n), func(b *testing.B) { benchShield(b, n, true) })
	}
}
