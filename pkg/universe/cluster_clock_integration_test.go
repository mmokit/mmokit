package universe

import (
	"testing"
	"time"

	"github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/logger"
)

// TestCoordTimeSync_HostObservesClock verifies that a CoordTimeSync
// message dispatched through meshControlClient reaches the host's
// ClusterClock.Observe. Exercises the dispatch path directly without
// the gRPC stack — bypasses Start/runConnection and constructs a bare
// client with only the clusterClock field populated.
func TestCoordTimeSync_HostObservesClock(t *testing.T) {
	cc := NewClusterClock()
	cli := &meshControlClient{clusterClock: cc}

	msg := &meshpb.CoordMessage{Msg: &meshpb.CoordMessage_CoordTimeSync{
		CoordTimeSync: &meshpb.CoordTimeSync{
			CoordTimeMs: uint64(time.Now().UnixMilli()) + 5_000,
			Seq:         1,
		},
	}}
	cli.dispatch(msg)

	if !cc.Observed() {
		t.Fatal("ClusterClock.Observed() must be true after CoordTimeSync dispatch")
	}
}

// TestCoordTimeSync_InitialSyncOnRegister verifies that a newly
// connecting remote host receives a CoordTimeSync on its MeshControl
// stream right after RegisterAck — without waiting for the periodic
// broadcast loop. Uses the distributed fixture so the full gRPC path
// (RegisterHost → RegisterAck → CoordTimeSync) is exercised.
//
// Task C3 closes the window where a remote host could start ticking
// cells before it has a cluster-coherent clock.
func TestCoordTimeSync_InitialSyncOnRegister(t *testing.T) {
	fx := newDistributedFixture(t, FixtureConfig{
		CellsX:  2,
		CellsY:  2,
		HostIDs: []string{"host-a"},
	}).(*distributedFixture)

	hostA := fx.hosts["host-a"]
	if hostA == nil {
		t.Fatal("host-a missing from distributed fixture")
	}
	if hostA.ClusterClock == nil {
		t.Fatal("host-a.ClusterClock is nil")
	}

	// Wait up to 2s for the host's ClusterClock to observe the initial
	// sync. The fixture already waits for RegisterHost to land on the
	// coord's registry, but the CoordTimeSync send races ahead of
	// registration bookkeeping — poll to close that window.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hostA.ClusterClock.Observed() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("host-a.ClusterClock did not Observe within 2s of RegisterHost — initial CoordTimeSync not delivered")
}

// TestCoordTimeSync_PeriodicBroadcastAdvances verifies that after the
// initial observation, subsequent broadcasts continue to update the
// host's ClusterClock. Uses a short ClusterClockSyncInterval so the
// test completes in under a second.
//
// Task C4 adds a goroutine on the coordinator that ticks at
// Config.ClusterClockSyncInterval and pushes a fresh CoordTimeSync to
// every registered remote host. Without it, a host's EMA offset would
// only ever be seeded by the single initial sync and would never track
// the coordinator across transient latency spikes or long-running
// sessions.
func TestCoordTimeSync_PeriodicBroadcastAdvances(t *testing.T) {
	fx := newDistributedFixture(t, FixtureConfig{
		CellsX:                   2,
		CellsY:                   2,
		HostIDs:                  []string{"host-a"},
		ClusterClockSyncInterval: 100 * time.Millisecond,
	}).(*distributedFixture)

	hostA := fx.hosts["host-a"]
	if hostA == nil || hostA.ClusterClock == nil {
		t.Fatal("host-a or host-a.ClusterClock missing from fixture")
	}

	// Wait for the initial observation (sent synchronously from
	// handleHostControl right after RegisterAck).
	obsDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(obsDeadline) {
		if hostA.ClusterClock.Observed() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !hostA.ClusterClock.Observed() {
		t.Fatal("host-a.ClusterClock did not observe initial CoordTimeSync within 2s")
	}

	// Record current coordinator-space Now(); wait across ~3 broadcast
	// ticks (100ms interval). After multiple broadcasts the clock must
	// continue advancing. Primary assertion: the broadcast loop keeps
	// driving Observe, so Now() continues to advance. If the loop is
	// wired correctly, Now() advances roughly with wall-clock time.
	before := hostA.ClusterClock.Now()
	// Sleep for ~5 broadcast cycles (500ms at 100ms cadence) to give
	// loaded CI runners generous slack. The assertions below require
	// only 2 periodic broadcasts to have fired.
	time.Sleep(500 * time.Millisecond)
	after := hostA.ClusterClock.Now()
	if after <= before {
		t.Fatalf("ClusterClock did not advance across periodic broadcasts: before=%d after=%d", before, after)
	}

	// Sanity check: the monotonic seq counter on the coord should have
	// moved past the initial handshake send. Initial + at least 2
	// periodic broadcasts → seq >= 3.
	seq := fx.coord.controlServer.clusterClockSeq.Load()
	if seq < 3 {
		t.Fatalf("expected >=3 CoordTimeSync broadcasts (initial + >=2 periodic); coord seq=%d", seq)
	}
}

// TestCellAssign_RequiresObservedClock verifies that the CellAssign
// dispatch path refuses to accept a cell assignment until the host's
// ClusterClock has observed at least one CoordTimeSync. In production
// the coordinator's RegisterAck path sends CoordTimeSync before any
// CellAssign, so this gate is purely defensive — but we pin the
// behavior so any orchestration regression that reorders the messages
// fails loudly here rather than silently emitting samples with local
// wall-time stamps.
func TestCellAssign_RequiresObservedClock(t *testing.T) {
	cc := NewClusterClock() // deliberately un-observed
	cli := &meshControlClient{
		clusterClock: cc,
		log:          logger.New(),
	}

	// With coord=nil, onCellAssign would panic in a goroutine if the
	// gate failed to short-circuit — the gate's early-return is the
	// only reason this test doesn't flake on a nil deref.
	cli.onCellAssign(&meshpb.CellAssign{CellId: "0_0"})

	if cc.Observed() {
		t.Fatal("sanity: un-observed ClusterClock unexpectedly became Observed")
	}
}
