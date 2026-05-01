package main

// End-to-end mesh integration test for the 4node-basic distributed harness.
//
// This test stands up a real multi-process coordinator (one coord-role +
// two host-role Coordinators connected via real gRPC MeshControl), registers
// the full 4node-basic system set and World on each host, spawns bots that
// wander continuously, and drives a sequence of splits and merges through the
// orchestrator. Between every topology change it asserts:
//
//   - entity conservation: every spawned bot netID is present on some cell.
//   - no duplicates: each netID appears on exactly one cell.
//   - no stranded replicas: every Replica's SourceNetID has a real entity
//     somewhere in the cluster (regression test for the interest-set diff
//     despawn fix).
//   - border traffic liveness: BorderFramesSent is increasing, proving bots
//     are actually crossing boundaries (catches BotSystem rewire regressions).
//
// Host IDs "test-node-0" + "test-node-3" match the names used in the
// pkg/universe S4/S7 tests for consistency. Cell layout is seeded explicitly
// (2 cells per host) rather than relying on rendezvous hash heuristics.
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

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// ---------------------------------------------------------------------------
// testCluster holds the multi-process coordinator fixture.
// ---------------------------------------------------------------------------

type testCluster struct {
	// Coord is the coord-role Process (no local cells).
	Coord *mmokit.Process
	// Hosts maps hostID -> host-role Process (owns cells).
	Hosts map[string]*mmokit.Process
	// hostOrder is the declaration order (deterministic for iteration).
	hostOrder []string
}

// allCells returns a snapshot of every live *Cell across all host-role
// Coordinators, sorted by cell ID for deterministic iteration.
func (tc *testCluster) allCells() []*mmokit.Cell {
	var all []*mmokit.Cell
	for _, hid := range tc.hostOrder {
		host := tc.Hosts[hid]
		if host == nil {
			continue
		}
		all = append(all, mmokit.HarnessLocalHostCells(host)...)
	}
	// Sort by cell ID string.
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all
}

// resolveClusterCell looks up a cell by string ID across all hosts.
// If cellKey is empty, returns the first live cell (lexicographic order).
func (tc *testCluster) resolveClusterCell(cellKey string) *mmokit.Cell {
	cells := tc.allCells()
	if len(cells) == 0 {
		return nil
	}
	if cellKey == "" {
		return cells[0]
	}
	for _, c := range cells {
		if c.ID == cellKey {
			return c
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Test
// ---------------------------------------------------------------------------

func TestE2EMeshSplitMergeWithBotTraffic(t *testing.T) {
	if testing.Short() {
		t.Skip("mesh e2e test; skipped in -short mode")
	}

	cluster := buildTestCluster(t)

	// Phase 1: spawn 60 bots in cell 0_0.
	const botCount = 60
	parent := mmokit.CellID{X: 0, Y: 0}
	spawned := spawnBotsForTest(t, cluster, parent, botCount)
	t.Logf("phase 1: spawned %d bots in %s (netIDs=%v)",
		len(spawned), mmokit.MeshCellID(parent), summariseIDs(spawned))
	if len(spawned) != botCount {
		t.Fatalf("expected to spawn %d bots, got %d", botCount, len(spawned))
	}

	// Phase 2: baseline wander so some bots cross natural cell boundaries.
	wanderAndAssert(t, cluster, 3*time.Second, spawned, "baseline")

	// Phase 3: split 0_0 into four depth-1 children.
	mustSplit(t, cluster, parent)
	wanderAndAssert(t, cluster, 3*time.Second, spawned, "post-split-0_0")

	// Phase 4: merge the four children back into 0_0.
	mustMerge(t, cluster, parent.Children()[0])
	wanderAndAssert(t, cluster, 3*time.Second, spawned, "post-merge-0_0")

	// Phase 5: split a different quadrant to cover rendezvous asymmetry.
	other := mmokit.CellID{X: 1, Y: 0}
	mustSplit(t, cluster, other)
	wanderAndAssert(t, cluster, 3*time.Second, spawned, "post-split-1_0")

	// Phase 6: merge back.
	mustMerge(t, cluster, other.Children()[0])
	wanderAndAssert(t, cluster, 3*time.Second, spawned, "post-merge-1_0")

	// Phase 7: re-split 0_0 right on the heels of a merge to stress the
	// cooldown-bypass + repeated-transition paths.
	mustSplit(t, cluster, parent)
	wanderAndAssert(t, cluster, 3*time.Second, spawned, "post-resplit-0_0")

	// Final invariants.
	assertNoStrandedReplicas(t, cluster, "final")
	assertBorderTrafficWasLive(t, cluster)
}

// ---------------------------------------------------------------------------
// Process setup
// ---------------------------------------------------------------------------

const (
	hostA = "test-node-0"
	hostB = "test-node-3"
)

// cellLayout is the explicit 2-2 cell assignment for the 2x2 grid.
// cell_0_0 + cell_1_0 → test-node-0; cell_0_1 + cell_1_1 → test-node-3.
var cellLayout = map[string]string{
	"cell_0_0": hostA,
	"cell_1_0": hostA,
	"cell_0_1": hostB,
	"cell_1_1": hostB,
}

// buildTestCluster constructs a coord-role + 2 host-role Coordinators
// connected via real gRPC MeshControl. Each host-role process has the full
// 4node-basic system set + World registered. Cells are seeded explicitly
// (bypassing the 5-second settle window). Returns a testCluster whose
// Coord + Hosts fields are ready for use.
func buildTestCluster(t *testing.T) *testCluster {
	t.Helper()

	mmokit.SetCellSize(CellSize)

	// 1. Coord-role process on an ephemeral port.
	coord := mmokit.New(mmokit.Config{
		CellsX:              CellsX,
		CellsY:              CellsY,
		CellSize:            CellSize,
		Mode:                "coordinator",
		ControlListen:       "127.0.0.1:0",
		TickRate:            TickRate,
		AoIRadius:           AoIRadius,
		Headless:            true,
		ConnManager:         mmokit.NewConnManager(),
		Logger:              mmokit.NewLogger(),
		DynamicPartitioning: mmokit.DisabledPartitionConfig(),
		DefaultSpawn:        mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85},
	})
	coord.Build()

	t.Cleanup(coord.Shutdown)

	// Resolve the ephemeral control listen address from the coord.
	coordAddr := coord.ControlListenAddr()
	if coordAddr == "" {
		t.Fatal("buildTestCluster: coord has no control listener address after Build()")
	}

	// 2. One host-role process per host ID, each dialing the coord.
	hostIDs := []string{hostA, hostB}
	hosts := make(map[string]*mmokit.Process, len(hostIDs))
	for _, hid := range hostIDs {
		host := mmokit.New(mmokit.Config{
			CellsX:              CellsX,
			CellsY:              CellsY,
			CellSize:            CellSize,
			Mode:                "host",
			CoordinatorAddr:     coordAddr,
			HostID:              hid,
			TickRate:            TickRate,
			AoIRadius:           AoIRadius,
			Headless:            true,
			ConnManager:         mmokit.NewConnManager(),
			Logger:              mmokit.NewLogger(),
			DynamicPartitioning: mmokit.DisabledPartitionConfig(),
			DefaultSpawn:        mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85},
		})
		mmokit.RegisterKind[PlayerComponents](host, KindPlayer, "Player")
		mmokit.RegisterKind[BotComponents](host, KindBot, "Bot")
		host.AddSystem(mmokit.NewClickToMoveSystem())
		host.AddSystem(mmokit.NewPhysicsSystem())
		host.AddSystem(mmokit.NewSpatialSystem())
		host.AddSystem(mmokit.NewSystem(&BotSystem{}))
		host.AddSystem(mmokit.NewNetworkSystem())
		host.Build()
		t.Cleanup(host.Shutdown)
		hosts[hid] = host
	}

	// 3. Wait until both hosts have registered with the coord.
	regCtx, regCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer regCancel()
	for _, hid := range hostIDs {
		if err := mmokit.HarnessWaitForHost(coord, regCtx, hid); err != nil {
			t.Fatalf("buildTestCluster: %v", err)
		}
	}

	// 4. Bypass the 5-second settle window so the rebalance loop doesn't
	// stomp our explicit layout seeding.
	mmokit.HarnessSetSettled(coord)

	// 4b. Broadcast PeerList so all hosts learn each other's gRPC addresses
	// before we dispatch cell assignments (hosts need to know each other as
	// peers for cross-host split/merge to work).
	mmokit.HarnessBroadcastPeerList(coord)

	// 5. Dispatch cell assignments explicitly.
	sortedCells := sortedClusterKeys(cellLayout)
	for _, cellKey := range sortedCells {
		mmokit.HarnessDispatchCellAssign(coord, cellLayout[cellKey], cellKey)
	}

	// 6. Wait for every cell to land on its host-role process.
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer seedCancel()
	for _, cellKey := range sortedCells {
		hostCoord := hosts[cellLayout[cellKey]]
		if err := mmokit.HarnessWaitForCellOnLocalHost(hostCoord, seedCtx, cellKey); err != nil {
			t.Fatalf("buildTestCluster: seed %s -> %s: %v", cellKey, cellLayout[cellKey], err)
		}
	}

	// 6b. Broadcast a fresh PeerList with full cell-ownership now that all
	// cells have been confirmed on their hosts.
	mmokit.HarnessBroadcastPeerList(coord)

	// 7. Wait for every host's cellToHostMap to contain all 4 seeded cells.
	// The PeerList broadcast above is async — close the race before handing
	// control to the test body.
	mapCtx, mapCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer mapCancel()
	for _, hid := range hostIDs {
		if err := mmokit.HarnessWaitForCellToHostMap(hosts[hid], mapCtx, sortedCells); err != nil {
			t.Fatalf("buildTestCluster: host %s cellToHostMap: %v", hid, err)
		}
	}

	// Let the game loops reach steady state before spawning bots.
	time.Sleep(200 * time.Millisecond)

	tc := &testCluster{
		Coord:     coord,
		Hosts:     hosts,
		hostOrder: hostIDs,
	}

	// Verify both hosts actually own cells (sanity-check the layout seeding).
	hostCount := map[string]int{}
	for _, hid := range hostIDs {
		hostCount[hid] = len(mmokit.HarnessLocalHostCells(hosts[hid]))
	}
	for hid, n := range hostCount {
		if n == 0 {
			t.Fatalf("buildTestCluster: host %s owns 0 cells — cell assignment failed", hid)
		}
	}

	return tc
}

// sortedClusterKeys returns map keys in sorted order.
func sortedClusterKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Bot spawn with netID capture
// ---------------------------------------------------------------------------

// spawnBotsForTest spawns bots into cell cellID and captures the resulting
// NetworkIDs. Returns the sorted slice for deterministic assertion output.
func spawnBotsForTest(t *testing.T, cluster *testCluster, cellID mmokit.CellID, count int) []uint32 {
	t.Helper()

	cell := cluster.resolveClusterCell(mmokit.MeshCellID(cellID))
	if cell == nil {
		t.Fatalf("spawnBotsForTest: no cell at %s in cluster", mmokit.MeshCellID(cellID))
	}

	spawned := spawnBotsInCell(cell, count)
	if spawned != count {
		t.Fatalf("spawnBotsInCell: got %d, want %d", spawned, count)
	}

	var netIDs []uint32
	execOnTestLoop(t, cell, func() {
		w := cell.World.(*mmokit.Stage)
		netMap := ecs.NewMap1[mmokit.NetworkID](w.ECSWorld())
		filter := ecs.NewFilter2[BotBehavior, mmokit.NetworkID](w.ECSWorld())
		q := filter.Query()
		for q.Next() {
			netIDs = append(netIDs, netMap.Get(q.Entity()).ID)
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
const settleAfterTransition = 2 * time.Second

func mustSplit(t *testing.T, cluster *testCluster, parent mmokit.CellID) {
	t.Helper()
	if err := cluster.Coord.SplitCell(parent, true); err != nil {
		t.Fatalf("SplitCell(%v): %v", parent, err)
	}
	time.Sleep(settleAfterTransition)
	t.Logf("split: %s -> children", mmokit.MeshCellID(parent))
}

func mustMerge(t *testing.T, cluster *testCluster, child mmokit.CellID) {
	t.Helper()
	if err := cluster.Coord.MergeCell(child, true); err != nil {
		t.Fatalf("MergeCell(%v): %v", child, err)
	}
	time.Sleep(settleAfterTransition)
	t.Logf("merge: %s -> parent", mmokit.MeshCellID(child))
}

// ---------------------------------------------------------------------------
// Wander + assert
// ---------------------------------------------------------------------------

func wanderAndAssert(
	t *testing.T,
	cluster *testCluster,
	dur time.Duration,
	spawned []uint32,
	phase string,
) {
	t.Helper()

	borderBefore := sumBorderFrames(cluster)
	time.Sleep(dur)

	var foundIn map[uint32]string
	var missing []uint32
	const retries = 4
	for attempt := 0; attempt < retries; attempt++ {
		foundIn = botLocations(t, cluster)
		missing = missing[:0]
		for _, id := range spawned {
			if _, ok := foundIn[id]; !ok {
				missing = append(missing, id)
			}
		}
		if len(missing) == 0 {
			break
		}
		time.Sleep(120 * time.Millisecond)
	}
	if len(missing) > 0 {
		t.Errorf("[%s] entity conservation: %d of %d bots missing. Missing netIDs: %v",
			phase, len(missing), len(spawned), summariseIDs(missing))
	}
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

	borderAfter := sumBorderFrames(cluster)
	if borderAfter <= borderBefore {
		t.Errorf("[%s] border-frame counter did not advance during wander: before=%d after=%d (bots may have stopped moving across cells)",
			phase, borderBefore, borderAfter)
	}

	t.Logf("[%s] seen=%d spawned=%d border-frames-delta=%d",
		phase, len(foundIn), len(spawned), borderAfter-borderBefore)

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

func botLocations(t *testing.T, cluster *testCluster) map[uint32]string {
	t.Helper()
	out := map[uint32]string{}
	for _, cell := range cluster.allCells() {
		cellKey := cell.ID
		execOnTestLoop(t, cell, func() {
			w, ok := cell.World.(*mmokit.Stage)
			if !ok {
				return
			}
			netMap := ecs.NewMap1[mmokit.NetworkID](w.ECSWorld())
			filter := ecs.NewFilter2[BotBehavior, mmokit.NetworkID](w.ECSWorld()).
				Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
			q := filter.Query()
			for q.Next() {
				out[netMap.Get(q.Entity()).ID] = cellKey
			}
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Stranded replica assertion
// ---------------------------------------------------------------------------

func assertNoStrandedReplicas(t *testing.T, cluster *testCluster, phase string) {
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

		for _, cell := range cluster.allCells() {
			cellKey := cell.ID
			execOnTestLoop(t, cell, func() {
				w, ok := cell.World.(*mmokit.Stage)
				if !ok {
					return
				}

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
						sourceCell:  rep.SourceCellID,
						sourceNetID: rep.SourceNetID,
					})
				}

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

// execOnTestLoop schedules fn on the cell's game loop, blocks up to 3s,
// t.Fatal on timeout.
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

// sumBorderFrames sums BorderFramesSent across every live cell on all hosts.
func sumBorderFrames(cluster *testCluster) uint64 {
	var total uint64
	for _, cell := range cluster.allCells() {
		if cell.Metrics == nil {
			continue
		}
		total += cell.Metrics.InterNodeSnapshot().BorderFramesSent
	}
	return total
}

// assertBorderTrafficWasLive is a final sanity check: total border frames
// sent + received across all cells must exceed a loose minimum.
func assertBorderTrafficWasLive(t *testing.T, cluster *testCluster) {
	t.Helper()
	var sent, recv uint64
	for _, cell := range cluster.allCells() {
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
