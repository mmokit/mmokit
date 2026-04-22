package universe

import (
	"testing"
	"time"
)

// TestClusterClock_NowBeforeObserveFallsBackToLocalWall ensures pre-sync
// code paths still return a usable number (local wall-clock ms). The
// caller is responsible for gating on Observed() before taking any
// action that requires cluster-coherent timestamps.
func TestClusterClock_NowBeforeObserveFallsBackToLocalWall(t *testing.T) {
	c := NewClusterClock()
	if c.Observed() {
		t.Fatal("fresh clock must report Observed()=false")
	}
	before := uint64(time.Now().UnixMilli())
	got := c.Now()
	after := uint64(time.Now().UnixMilli())
	if got < before || got > after {
		t.Fatalf("pre-Observe Now() = %d; want in [%d,%d]", got, before, after)
	}
}

// TestClusterClock_FirstObserveSnapsOffset verifies the first observation
// sets the offset exactly to (coord - local), regardless of EMA alpha.
func TestClusterClock_FirstObserveSnapsOffset(t *testing.T) {
	c := NewClusterClock()
	local := uint64(time.Now().UnixMilli())
	coord := local + 1_000 // coord is 1 second ahead of us
	c.observeAt(coord, 1, local)
	if !c.Observed() {
		t.Fatal("after Observe, Observed() must be true")
	}
	got := c.nowAt(local)
	if got != coord {
		t.Fatalf("Now() after first observe = %d; want %d (coord)", got, coord)
	}
}

// TestClusterClock_EMAConvergesTowardCoord verifies subsequent
// observations converge the offset EMA toward coord's clock.
func TestClusterClock_EMAConvergesTowardCoord(t *testing.T) {
	c := NewClusterClock()
	local := uint64(1_000_000)
	// First observation: offset = 1_000 ms.
	c.observeAt(local+1_000, 1, local)
	// Second observation 10 seconds later: coord drifted to +1_100.
	local2 := local + 10_000
	c.observeAt(local2+1_100, 2, local2)
	got := c.nowAt(local2)
	// Expected EMA with alpha=0.3 against prior 1_000 offset toward 1_100:
	// offset_new = 0.7*1_000 + 0.3*1_100 = 1_030 ms.
	want := local2 + 1_030
	if got < want-5 || got > want+5 {
		t.Fatalf("Now() after EMA step = %d; want ~%d (±5)", got, want)
	}
}

// TestClusterClock_StaleSeqRejected ensures an out-of-order
// CoordTimeSync (seq lower than highest seen) is dropped.
func TestClusterClock_StaleSeqRejected(t *testing.T) {
	c := NewClusterClock()
	c.observeAt(2_000, 5, 1_000)  // offset = 1_000
	c.observeAt(99_999, 3, 1_000) // lower seq — must be dropped
	got := c.nowAt(1_000)
	if got != 2_000 {
		t.Fatalf("stale observe mutated offset: Now=%d, want 2_000", got)
	}
}

// TestClusterClock_CachedOffsetSurvivesCoordDeath models the coord
// dying after the first observation: subsequent Now() calls must
// continue to return (local + cached_offset), no panic, no reset.
func TestClusterClock_CachedOffsetSurvivesCoordDeath(t *testing.T) {
	c := NewClusterClock()
	c.observeAt(2_000, 1, 1_000) // offset = +1_000
	// 5 minutes later, no new observation.
	got := c.nowAt(1_000 + 5*60*1_000)
	want := (1_000 + 5*60*1_000) + 1_000
	if got != uint64(want) {
		t.Fatalf("Now() after 5min idle = %d; want %d", got, want)
	}
}
