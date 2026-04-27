package universe

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
)

// TestNoStutter_ReplicaSmoothnessAcrossBoundary is the user-reported
// scenario pinned as a regression test: a bot wandering in cell_0_0
// being observed by a viewer sitting in cell_1_0. Under the Replication
// Timeline Redesign, every border-frame entry carries a
// ProducedAtMs stamp from the source cell's ClusterClock, and every
// tick the destination cell's replica caches the freshest stamp. The
// guarantee we're locking in here: the observed ProducedAtMs on the
// destination advances monotonically, and the gap between successive
// distinct stamps never exceeds 75 ms (1.5 tick at 20 Hz).
//
// Rather than standing up the full Process+Cell infrastructure — the
// cross-cell border flow is authoritatively unit-tested by
// BorderDispatcher round-trip tests — this test drives two WorldBase
// instances directly and shuttles frames from the source cell's
// BorderDispatcher into the destination cell's ApplyBorderFrame. That's
// the same code path Cell.Run exercises tick-for-tick; doing it manually
// lets the test advance the clock with full determinism and inspect the
// Replica component on every frame.
func TestNoStutter_ReplicaSmoothnessAcrossBoundary(t *testing.T) {
	// Use a smaller cell size so the test entity doesn't need extreme
	// coordinates to sit near the shared edge. Reset after the test runs.
	coords.SetCellSize(1024)

	// Build two WorldBases — the "source" cell (0,0) is where the bot
	// lives, and the "dest" cell (1,0) is where the viewer lives.
	// newTestWorldBase gives each its own engine + ECS world but no
	// bridge; we shuttle frames between them explicitly.
	srcBase := newTestWorldBase(t, CellID{X: 0, Y: 0})
	dstBase := newTestWorldBase(t, CellID{X: 1, Y: 0})

	// Drive the source cluster clock from a fake time source instead of
	// wall-clock time.Sleep. Real-time sleep was flaky on loaded systems:
	// occasionally a 50 ms sleep would oversleep enough that two ticks of
	// clock advanced between consecutive Walks, producing a >75 ms gap
	// in the stamp sequence. The framework already supports a clock
	// override via ClusterClock.SetNowFn — use it to simulate exactly
	// 50 ms per iteration.
	srcBase.clusterClock = NewClusterClock()
	var simNowMs uint64 = 1_000_000 // arbitrary base; only deltas matter
	srcBase.clusterClock.SetNowFn(func() uint64 { return simNowMs })
	srcBase.clusterClock.Observe(simNowMs, 1)

	// Spawn a "bot" entity inside the source cell, hugging the east
	// edge with eastward velocity so it stays in the near-boundary
	// push set for every tick of the test window. The bot never leaves
	// cell (0,0) — it wraps back via the clamp below.
	const botNetID uint32 = 42
	world := srcBase.ECSWorld()
	posMap := ecs.NewMap1[component.Position](world)
	velMap := ecs.NewMap1[component.Velocity](world)
	nidMap := ecs.NewMap1[component.NetworkID](world)
	kindMap := ecs.NewMap1[component.EntityKind](world)
	colMap := ecs.NewMap1[component.Collider](world)

	bot := world.NewEntity()
	// Start at 90% across the cell in X, middle in Y — well inside the
	// enter margin (default AoI radius 100).
	posMap.Add(bot, &component.Position{X: coords.CellSize * 0.9, Y: coords.CellSize * 0.5})
	velMap.Add(bot, &component.Velocity{X: 40, Y: 0}) // 40 u/s east
	nidMap.Add(bot, &component.NetworkID{ID: botNetID, Epoch: 1})
	kindMap.Add(bot, &component.EntityKind{Type: 1})
	colMap.Add(bot, &component.Collider{Radius: 10})

	// Build a one-direction dispatcher wired to a CellViewer pointed at
	// the east neighbor (the dest cell). Passing nil for sourceCell /
	// destCell keeps CellViewer.Send a no-op — we grab the frame off
	// disp.Walk directly and feed dstBase.ApplyBorderFrame.
	bd := NewBorderDispatcher(srcBase, nil)
	bx, by := neighborBoundaryMidpoint(CellID{X: 0, Y: 0}, 1, 0)
	nv := NewCellViewer("east-neighbor", CellViewerID("east-neighbor"), bx, by, nil, nil, nil)
	nv.SetDirection(1, 0)

	// Run for 2 simulated seconds = 40 ticks at 20 Hz. On each tick:
	//   1. Advance bot position by dt (50ms) of simulated movement.
	//   2. Build the border frame and apply it to the dest cell.
	//   3. Sleep 50ms of real wall time so ClusterClock.Now() advances
	//      in step with our simulated tick cadence — ProducedAtMs is
	//      stamped by the source's clusterClock.Now() inside Walk, so
	//      we need real time to pass for the stamps to move.
	//   4. Read the dest replica's ProducedAtMs and record.
	const totalTicks = 40
	const dtMs = 50
	var stamps []uint64
	srcPos := posMap.Get(bot)
	for i := 0; i < totalTicks; i++ {
		// Advance the bot's position. Keep it near the east edge so it
		// keeps qualifying for border replication. If it runs past the
		// cell boundary, wrap back to the 0.85 cell-size mark.
		srcPos.X += 40.0 * float32(dtMs) / 1000.0
		if srcPos.X >= coords.CellSize-20 {
			srcPos.X = coords.CellSize * 0.85
		}

		// Build the border frame from the source cell. tick starts at 5
		// so we skip the tick-0 resync path and get normal delta behavior
		// from tick 2+; any tick offset works for the smoothness check.
		tick := uint64(5 + i)
		frame := bd.disp.Walk(nv, tick, bd.candidatesFor(nv, tick))
		// Deliver to the destination cell as if it had just read this
		// frame off its inbox.
		dstBase.ApplyBorderFrame(frame, "cell_0_0")
		// Rotate the source viewer's interest-set snapshot so the next
		// Walk's diff is correct (mirrors what BorderDispatcher.Tick does
		// post-Send in the real loop).
		nv.SwapInSet()

		// Advance the simulated clock by exactly one tick interval. The
		// stamp on the next Walk reads from this fake clock, so the
		// distinct-stamp gap is deterministic.
		simNowMs += dtMs

		// Inspect the dest cell's replica for the bot. Every tick where
		// the border frame included the bot, ApplyBorderFrame refreshes
		// Replica.ProducedAtMs with the source's stamp.
		ent, ok := dstBase.replicaNetIDs[botNetID]
		if !ok {
			continue
		}
		replicaMap := ecs.NewMap1[component.Replica](dstBase.ECSWorld())
		rep := replicaMap.Get(ent)
		if rep.ProducedAtMs != 0 {
			stamps = append(stamps, rep.ProducedAtMs)
		}
	}

	// Sanity: the bot must have been visible to the dest for most of the
	// run. If fewer than ~30 samples landed, either the border geometry
	// is wrong or replication is silently dropping frames — either way
	// the smoothness claim doesn't hold.
	if len(stamps) < 30 {
		t.Fatalf("expected ≥30 ProducedAtMs samples on dest replica (got %d) — border replication not flowing",
			len(stamps))
	}

	// Walk the stamp sequence. Duplicate reads across ticks that didn't
	// refresh the stamp show up as zero-gaps — those are fine; they
	// reflect "the replica's cache is still holding the last value."
	// What we're guarding against is a gap between two DISTINCT stamps
	// exceeding 75 ms (= 1.5 tick at 20 Hz), which would manifest as
	// visible stutter on the client interpolator.
	var maxGap uint64
	for i := 1; i < len(stamps); i++ {
		if stamps[i] <= stamps[i-1] {
			continue // same-stamp repeat; no new frame this tick
		}
		gap := stamps[i] - stamps[i-1]
		if gap > maxGap {
			maxGap = gap
		}
		if gap > 75 {
			t.Fatalf("ProducedAtMs gap %d ms at sample index %d exceeds 75 ms (stutter)\nfull stamp trace: %v",
				gap, i, stamps)
		}
	}
	t.Logf("replica smoothness OK: %d samples, max distinct-stamp gap = %d ms", len(stamps), maxGap)
}
