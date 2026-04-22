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
