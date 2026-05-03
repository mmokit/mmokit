package mmokit_test

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

func TestOnWorldTick_FiresOncePerTick(t *testing.T) {
	stage, _ := newTestStage(t)
	var ticks int
	var lastDt float32
	mmokit.OnWorldTick(stage, func(dt float32) {
		ticks++
		lastDt = dt
	})
	runTicks(t, stage, 3)
	if ticks != 3 {
		t.Fatalf("ticks = %d, want 3", ticks)
	}
	if lastDt <= 0 {
		t.Fatalf("dt should be positive, got %v", lastDt)
	}
}
