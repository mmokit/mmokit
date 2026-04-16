package engine

import (
	"strings"
	"testing"
	"time"
)

func TestFormatPerfSnapshotTextContainsKeyLabels(t *testing.T) {
	snap := PerfSnapshotText{
		TickHz:   20,
		BudgetMS: 50,
		Tick: TimingStats{
			Avg: 12 * time.Millisecond,
			P50: 11 * time.Millisecond,
			P95: 15 * time.Millisecond,
			P99: 18 * time.Millisecond,
			Max: 23 * time.Millisecond,
		},
		SystemNames:   []string{"Phys"},
		SystemTimings: []TimingStats{{Avg: 3 * time.Millisecond, P95: 4 * time.Millisecond}},
		EntitiesReal:  100,
		Connections:   5,
	}

	out := FormatPerfSnapshotText(snap)

	for _, want := range []string{"Tick (20Hz, budget 50ms):", "Phys", "100 real", "5 conns"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- full output ---\n%s", want, out)
		}
	}
}
