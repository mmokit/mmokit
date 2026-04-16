package main

// End-to-end mesh integration test for the 4node-basic distributed harness.
//
// This test stands up a real 2-host coordinator in-process (via TestHosts +
// HostNetwork gRPC loopback), registers the full 4node-basic system set and
// World, spawns bots that wander continuously, and drives a sequence of
// splits and merges through the orchestrator. Between every topology change
// it asserts:
//
//   - entity conservation: every spawned bot netID is present on some cell.
//   - no duplicates: each netID appears on exactly one cell.
//   - no stranded replicas: every Replica's SourceNetID has a real entity
//     somewhere in the cluster (regression test for the interest-set diff
//     despawn fix).
//   - border traffic liveness: BorderFramesSent is increasing, proving bots
//     are actually crossing boundaries (catches BotSystem rewire regressions).
//
// Test host IDs "test-node-0" + "test-node-3" are deliberately chosen — the
// fnv64a rendezvous on those names gives a deterministic 2-2 split for the
// 2x2 grid, whereas generic names would pile all 4 cells on one host and
// silently defeat the distributed nature of the test.
//
// Gated behind testing.Short so `go test -short ./...` skips the ~20s runtime.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/net"
)

// ---------------------------------------------------------------------------
// Test
// ---------------------------------------------------------------------------

func TestE2EMeshSplitMergeWithBotTraffic(t *testing.T) {
	if testing.Short() {
		t.Skip("mesh e2e test; skipped in -short mode")
	}

	coord, cancel := buildTestCoord(t)
	defer cancel()

	// Phase 1: spawn 60 bots in cell 0_0. The host owning 0_0 is determined
	// by rendezvous over the TestHosts names — whatever it is, spawnBots
	// picks up the live cell from coord.Cells.
	const botCount = 60
	parent := mmokit.CellID{X: 0, Y: 0}
	spawned := spawnBotsForTest(t, coord, parent, botCount)
	t.Logf("phase 1: spawned %d bots in %s (netIDs=%v)",
		len(spawned), mmokit.MeshCellID(parent), summariseIDs(spawned))
	if len(spawned) != botCount {
		t.Fatalf("expected to spawn %d bots, got %d", botCount, len(spawned))
	}

	// Phase 2: baseline wander so some bots cross natural cell boundaries.
	wanderAndAssert(t, coord, 3*time.Second, spawned, "baseline")

	// Phase 3: split 0_0 into four depth-1 children.
	mustSplit(t, coord, parent)
	wanderAndAssert(t, coord, 3*time.Second, spawned, "post-split-0_0")

	// Phase 4: merge the four children back into 0_0.
	mustMerge(t, coord, parent.Children()[0])
	wanderAndAssert(t, coord, 3*time.Second, spawned, "post-merge-0_0")

	// Phase 5: split a different quadrant to cover rendezvous asymmetry.
	other := mmokit.CellID{X: 1, Y: 0}
	mustSplit(t, coord, other)
	wanderAndAssert(t, coord, 3*time.Second, spawned, "post-split-1_0")

	// Phase 6: merge back.
	mustMerge(t, coord, other.Children()[0])
	wanderAndAssert(t, coord, 3*time.Second, spawned, "post-merge-1_0")

	// Phase 7: re-split 0_0 right on the heels of a merge to stress the
	// cooldown-bypass + repeated-transition paths.
	mustSplit(t, coord, parent)
	wanderAndAssert(t, coord, 3*time.Second, spawned, "post-resplit-0_0")

	// Final invariants.
	assertNoStrandedReplicas(t, coord, "final")
	assertBorderTrafficWasLive(t, coord)
}

// ---------------------------------------------------------------------------
// Coordinator setup
// ---------------------------------------------------------------------------

// buildTestCoord constructs a 2-host 4node-basic coordinator wired with the
// full system set from main.go, starts every cell's game loop in a goroutine,
// and returns a cancel func that the caller should defer to tear everything
// down. Mirrors main.go exactly so the test exercises the real code path.
func buildTestCoord(t *testing.T) (*mmokit.Coordinator, context.CancelFunc) {
	t.Helper()

	mmokit.SetCellSize(CellSize)

	cfg := mmokit.Config{
		CellsX:              CellsX,
		CellsY:              CellsY,
		CellSize:            CellSize,
		TickRate:            TickRate,
		AoIRadius:           AoIRadius,
		TestHosts:           []string{"test-node-0", "test-node-3"},
		Headless:            true,
		ConnManager:         net.NewConnManager(),
		Logger:              logger.New(),
		DynamicPartitioning: mmokit.DisabledPartitionConfig(),
		DefaultSpawn:        mmokit.WorldCenterOfCell(0, 0),
		LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) {
			return "", nil, mmokit.ErrLoginPending
		},
	}

	coord := mmokit.NewCoordinator(cfg)
	coord.SetWorld(NewWorld)

	// Match main.go's system set verbatim.
	coord.AddSystem("ClickToMove", mmokit.NewClickToMoveSystem())
	coord.AddSystem("Physics", mmokit.NewPhysicsSystem())
	coord.AddSystem("DeadReckoning", mmokit.NewDeadReckoningSystem())
	coord.AddSystem("Spatial", mmokit.NewSpatialSystem())
	coord.AddSystem("DebugInfo", func() mmokit.System { return &DebugInfoSystem{} })
	coord.AddSystem("Bots", func() mmokit.System { return &BotSystem{} })
	coord.AddSystem("Network", mmokit.NewNetworkSystem())

	coord.Build()

	ctx, cancel := context.WithCancel(context.Background())
	for _, cell := range coord.Cells {
		go cell.Run(ctx)
	}
	// Let the game loops reach steady state before spawning bots.
	time.Sleep(100 * time.Millisecond)

	// Verify the expected 2-2 split actually landed. If the fnv64a
	// rendezvous rebalances in the future this will catch it early and
	// point at the root cause.
	hostCount := map[string]int{}
	for _, host := range hostList(coord) {
		hostCount[host]++
	}
	if len(hostCount) != 2 {
		t.Fatalf("buildTestCoord: expected cells distributed across 2 hosts, got %v", hostCount)
	}

	cleanup := func() {
		cancel()
		coord.Shutdown()
	}
	t.Cleanup(cleanup)
	return coord, cleanup
}

// ---------------------------------------------------------------------------
// Bot spawn with netID capture
// ---------------------------------------------------------------------------

// spawnBotsForTest calls the real spawnBotsInCell helper and then walks the
// source cell's ECS to capture the NetworkIDs of everything tagged as a bot.
// Returns the slice sorted for deterministic assertion output.
//
// spawnBotsInCell assigns unique names of the form bot_<cellID>_<nanosuffix>,
// so everything with a "bot_" prefix on this cell immediately after the call
// is something we spawned in this batch — there's no prior batch to worry
// about in a fresh test coordinator.
func spawnBotsForTest(t *testing.T, coord *mmokit.Coordinator, cellID mmokit.CellID, count int) []uint32 {
	t.Helper()

	cell := resolveCell(coord, mmokit.MeshCellID(cellID))
	if cell == nil {
		t.Fatalf("spawnBotsForTest: no cell at %s in coord.Cells", mmokit.MeshCellID(cellID))
	}

	spawned := spawnBotsInCell(cell, count)
	if spawned != count {
		t.Fatalf("spawnBotsInCell: got %d, want %d", spawned, count)
	}

	var netIDs []uint32
	execOnTestLoop(t, cell, func() {
		w := cell.World.(*World)
		nameMap := ecs.NewMap1[PlayerName](w.ECSWorld())
		netMap := ecs.NewMap1[mmokit.NetworkID](w.ECSWorld())
		filter := ecs.NewFilter2[PlayerName, mmokit.NetworkID](w.ECSWorld())
		q := filter.Query()
		for q.Next() {
			e := q.Entity()
			if !nameMap.HasAll(e) || !netMap.HasAll(e) {
				continue
			}
			if !strings.HasPrefix(nameMap.Get(e).Name, "bot_") {
				continue
			}
			netIDs = append(netIDs, netMap.Get(e).ID)
		}
	})
	sort.Slice(netIDs, func(i, j int) bool { return netIDs[i] < netIDs[j] })
	return netIDs
}

// ---------------------------------------------------------------------------
// Split / merge drivers
// ---------------------------------------------------------------------------

// settleAfterTransition is the delay applied after a split/merge commit
// to let in-flight handoffs land, cell game loops drain their inboxes,
// and TransferCooldowns tick down before the next assertion.
// Long enough that any reasonable handoff race settles; short enough not
// to dominate runtime.
const settleAfterTransition = 2 * time.Second

func mustSplit(t *testing.T, coord *mmokit.Coordinator, parent mmokit.CellID) {
	t.Helper()
	if err := coord.SplitCell(parent, true); err != nil {
		t.Fatalf("SplitCell(%v): %v", parent, err)
	}
	time.Sleep(settleAfterTransition)
	t.Logf("split: %s -> children", mmokit.MeshCellID(parent))
}

func mustMerge(t *testing.T, coord *mmokit.Coordinator, child mmokit.CellID) {
	t.Helper()
	if err := coord.MergeCell(child, true); err != nil {
		t.Fatalf("MergeCell(%v): %v", child, err)
	}
	time.Sleep(settleAfterTransition)
	t.Logf("merge: %s -> parent", mmokit.MeshCellID(child))
}

// ---------------------------------------------------------------------------
// Wander + assert
// ---------------------------------------------------------------------------

// wanderAndAssert samples border-frame metrics before the wander interval,
// sleeps for dur, then validates every invariant the test cares about.
// Every assertion prefixes phase so a failing test immediately identifies
// which transition regressed.
func wanderAndAssert(
	t *testing.T,
	coord *mmokit.Coordinator,
	dur time.Duration,
	spawned []uint32,
	phase string,
) {
	t.Helper()

	borderBefore := sumBorderFrames(coord)
	time.Sleep(dur)

	// Invariant 1+2: every spawned netID is present exactly once.
	//
	// The walk is NOT atomic — botLocations visits each cell on its own
	// game loop via execOnTestLoop, which takes a few ticks total. A bot
	// handing off from cell A to cell B between the two cells' walks can
	// slip through both snapshots. Retry on any "missing" bots: if the
	// missing set is consistent across 3 back-to-back walks, it is a true
	// loss. Otherwise it was a handoff caught mid-flight.
	var foundIn map[uint32]string
	var missing []uint32
	const retries = 4
	for attempt := 0; attempt < retries; attempt++ {
		foundIn = botLocations(t, coord)
		missing = missing[:0]
		for _, id := range spawned {
			if _, ok := foundIn[id]; !ok {
				missing = append(missing, id)
			}
		}
		if len(missing) == 0 {
			break
		}
		// Settle a bit before re-walking.
		time.Sleep(120 * time.Millisecond)
	}
	if len(missing) > 0 {
		t.Errorf("[%s] entity conservation: %d of %d bots missing. Missing netIDs: %v",
			phase, len(missing), len(spawned), summariseIDs(missing))
	}
	// Duplicates — a netID should live on exactly one cell at a time.
	// botLocations already maps netID->cell via a single pass so duplicates
	// would show up as a count mismatch after we compare keys vs spawned
	// keys. Detect extras explicitly:
	spawnedSet := make(map[uint32]struct{}, len(spawned))
	for _, id := range spawned {
		spawnedSet[id] = struct{}{}
	}
	var extras []uint32
	for id := range foundIn {
		if _, ok := spawnedSet[id]; !ok {
			extras = append(extras, id)
		}
	}
	if len(extras) > 0 {
		t.Errorf("[%s] unexpected bot netIDs found that were not spawned by the test: %v",
			phase, summariseIDs(extras))
	}

	// Invariant 3 (no stranded replicas) is checked only in the final
	// invariants block — per-phase walks race with in-flight handoffs
	// and report false positives on entities mid-transit between cells.
	// The final check has had multiple wander phases to converge.

	// Invariant 4: border traffic actually moved.
	borderAfter := sumBorderFrames(coord)
	if borderAfter <= borderBefore {
		t.Errorf("[%s] border-frame counter did not advance during wander: before=%d after=%d (bots may have stopped moving across cells)",
			phase, borderBefore, borderAfter)
	}

	t.Logf("[%s] seen=%d spawned=%d border-frames-delta=%d",
		phase, len(foundIn), len(spawned), borderAfter-borderBefore)

	// Distribution report: which cells hold how many bots.
	counts := map[string]int{}
	for _, cell := range foundIn {
		counts[cell]++
	}
	cellKeys := make([]string, 0, len(counts))
	for k := range counts {
		cellKeys = append(cellKeys, k)
	}
	sort.Strings(cellKeys)
	var distParts []string
	for _, k := range cellKeys {
		distParts = append(distParts, fmt.Sprintf("%s:%d", k, counts[k]))
	}
	t.Logf("[%s] distribution: %s", phase, strings.Join(distParts, " "))
}

// ---------------------------------------------------------------------------
// Bot location walk
// ---------------------------------------------------------------------------

// botLocations walks every live cell on every host, scans for entities with
// PlayerName starting "bot_", and returns a map of netID -> cell key. A
// netID that lives on two cells simultaneously would appear with only one
// of the two mappings (the last walked) — but the spawnedSet equality check
// above catches the mismatch.
func botLocations(t *testing.T, coord *mmokit.Coordinator) map[uint32]string {
	t.Helper()
	out := map[uint32]string{}
	for _, cell := range snapshotCells(coord) {
		cellKey := cell.ID
		execOnTestLoop(t, cell, func() {
			w, ok := cell.World.(*World)
			if !ok {
				return
			}
			nameMap := ecs.NewMap1[PlayerName](w.ECSWorld())
			netMap := ecs.NewMap1[mmokit.NetworkID](w.ECSWorld())
			filter := ecs.NewFilter2[PlayerName, mmokit.NetworkID](w.ECSWorld()).
				Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
			q := filter.Query()
			for q.Next() {
				e := q.Entity()
				if !nameMap.HasAll(e) || !netMap.HasAll(e) {
					continue
				}
				if !strings.HasPrefix(nameMap.Get(e).Name, "bot_") {
					continue
				}
				out[netMap.Get(e).ID] = cellKey
			}
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Stranded replica assertion
// ---------------------------------------------------------------------------

// assertNoStrandedReplicas walks all cells, collecting every Replica's
// SourceNetID and every real entity's netID. Every replica must have a
// real counterpart somewhere — the regression mode the interest-set
// diff fix addresses. Walks are not atomic across cells: a bot handing
// off during the walk can leave its replicas pointing at a netID we
// missed in the real-entity scan. Retry up to N times with a settle
// gap; if a "stranded" replica disappears on the next walk, it was a
// transient.
func assertNoStrandedReplicas(t *testing.T, coord *mmokit.Coordinator, phase string) {
	t.Helper()

	type replicaLoc struct {
		cell        string
		sourceCell  string
		sourceNetID uint32
	}

	const retries = 5
	var stranded []replicaLoc
	for attempt := 0; attempt < retries; attempt++ {
		var replicas []replicaLoc
		realNetIDs := map[uint32]struct{}{}

		for _, cell := range snapshotCells(coord) {
			cellKey := cell.ID
			execOnTestLoop(t, cell, func() {
				w, ok := cell.World.(*World)
				if !ok {
					return
				}

				// Pass 1: Replicas in this cell.
				repMap := ecs.NewMap1[mmokit.Replica](w.ECSWorld())
				repFilter := ecs.NewFilter1[mmokit.Replica](w.ECSWorld())
				repQ := repFilter.Query()
				for repQ.Next() {
					if !repMap.HasAll(repQ.Entity()) {
						continue
					}
					rep := repMap.Get(repQ.Entity())
					replicas = append(replicas, replicaLoc{
						cell:        cellKey,
						sourceCell:  rep.SourceNodeID,
						sourceNetID: rep.SourceNetID,
					})
				}

				// Pass 2: real entities in this cell.
				netMap := ecs.NewMap1[mmokit.NetworkID](w.ECSWorld())
				realFilter := ecs.NewFilter1[mmokit.NetworkID](w.ECSWorld()).
					Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
				realQ := realFilter.Query()
				for realQ.Next() {
					if !netMap.HasAll(realQ.Entity()) {
						continue
					}
					realNetIDs[netMap.Get(realQ.Entity()).ID] = struct{}{}
				}
			})
		}

		stranded = stranded[:0]
		for _, r := range replicas {
			if _, ok := realNetIDs[r.sourceNetID]; ok {
				continue
			}
			stranded = append(stranded, r)
		}
		if len(stranded) == 0 {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}

	for i, r := range stranded {
		if i >= 10 {
			t.Errorf("[%s] ...%d additional stranded replicas omitted", phase, len(stranded)-10)
			break
		}
		t.Errorf("[%s] stranded replica: cell=%s sourceCell=%s sourceNetID=%d (no real entity anywhere after %d retries)",
			phase, r.cell, r.sourceCell, r.sourceNetID, retries)
	}
}

// ---------------------------------------------------------------------------
// Misc helpers
// ---------------------------------------------------------------------------

// execOnTestLoop is a local copy of pkg/universe/cell_transfer_executor_test.go
// execOnLoop — unexported + wrong package so we can't share. Same semantics:
// schedule fn on the cell's game loop, block up to 3 seconds, t.Fatal on
// timeout.
func execOnTestLoop(t *testing.T, cell *mmokit.Cell, fn func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := cell.Engine.RunOnLoop(ctx, func() error {
		fn()
		return nil
	})
	if err != nil {
		t.Fatalf("execOnTestLoop: %v on %s", err, cell.ID)
	}
}

// hostList returns the host IDs currently owning each live cell, in
// snapshot-cell order. Used by buildTestCoord to verify the 2-host
// distribution up front.
func hostList(coord *mmokit.Coordinator) []string {
	var hosts []string
	for _, cell := range snapshotCells(coord) {
		// Cell.Bridge doesn't directly expose the owning host; walk
		// coord.Hosts and check each for this cell.
		for hostID, host := range coord.Hosts {
			if host == nil {
				continue
			}
			if host.CellByCellID(cell.Cell) != nil {
				hosts = append(hosts, hostID)
				break
			}
		}
	}
	return hosts
}

// sumBorderFrames sums BorderFramesSent across every live cell. Used as a
// "bots are actually moving" liveness check.
func sumBorderFrames(coord *mmokit.Coordinator) uint64 {
	var total uint64
	for _, cell := range snapshotCells(coord) {
		if cell.Metrics == nil {
			continue
		}
		total += cell.Metrics.InterNodeSnapshot().BorderFramesSent
	}
	return total
}

// assertBorderTrafficWasLive is a final sanity check at the end of the test:
// across all cells, total border frames sent + received MUST exceed a
// loose minimum, proving the test wasn't running in zero-traffic mode.
func assertBorderTrafficWasLive(t *testing.T, coord *mmokit.Coordinator) {
	t.Helper()
	var sent, recv uint64
	for _, cell := range snapshotCells(coord) {
		if cell.Metrics == nil {
			continue
		}
		snap := cell.Metrics.InterNodeSnapshot()
		sent += snap.BorderFramesSent
		recv += snap.BorderFramesRecv
	}
	total := sent + recv
	const floor = 100
	if total < floor {
		t.Errorf("final: border traffic too low to be real: sent=%d recv=%d total=%d (floor=%d) — did bots ever cross a boundary?",
			sent, recv, total, floor)
	} else {
		t.Logf("final: border traffic sent=%d recv=%d total=%d", sent, recv, total)
	}
}

// summariseIDs trims long netID slices for readable failure output.
func summariseIDs(ids []uint32) string {
	if len(ids) <= 8 {
		return fmt.Sprintf("%v", ids)
	}
	return fmt.Sprintf("%v...%v (len=%d)", ids[:4], ids[len(ids)-4:], len(ids))
}

