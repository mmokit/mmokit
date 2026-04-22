package universe

import (
	"testing"
	"time"

	"github.com/zenion/mmoserver/gen/go/meshpb"
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
