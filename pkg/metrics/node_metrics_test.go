package metrics

import (
	"testing"
	"time"
)

func TestNodeMetrics_RecordTick(t *testing.T) {
	nm := NewNodeMetrics("test_node", 20,
		func() TickStats {
			return TickStats{
				Total: TimingStats{Avg: 3 * time.Millisecond, P95: 8 * time.Millisecond},
			}
		},
		func() (uint64, uint64, int) { return 1000, 500, 10 },
	)

	// Record a normal tick (under budget of 50ms at 20Hz)
	nm.RecordTick(3*time.Millisecond, 100, 20, 5, 10)

	snap := nm.Snapshot()

	if snap.NodeID != "test_node" {
		t.Fatalf("expected test_node, got %s", snap.NodeID)
	}
	if snap.Entities.Real != 100 {
		t.Fatalf("expected 100 real, got %d", snap.Entities.Real)
	}
	if snap.Entities.Replica != 20 {
		t.Fatalf("expected 20 replica, got %d", snap.Entities.Replica)
	}
	if snap.Entities.Ghost != 5 {
		t.Fatalf("expected 5 ghost, got %d", snap.Entities.Ghost)
	}
	if snap.Entities.Connected != 10 {
		t.Fatalf("expected 10 players, got %d", snap.Entities.Connected)
	}
	if snap.Network.BytesSent != 1000 {
		t.Fatalf("expected 1000 bytes sent, got %d", snap.Network.BytesSent)
	}
	if snap.Network.BytesRecv != 500 {
		t.Fatalf("expected 500 bytes recv, got %d", snap.Network.BytesRecv)
	}
	if snap.Network.Connections != 10 {
		t.Fatalf("expected 10 connections, got %d", snap.Network.Connections)
	}
	// Tick stats from callback
	if snap.Tick.AvgDuration != 3*time.Millisecond {
		t.Fatalf("expected 3ms avg, got %v", snap.Tick.AvgDuration)
	}

	// CompositeLoad should be reasonable (3ms/50ms tick + 100/1000 entity)
	// tickLoad = 0.06, entityLoad = 0.1, composite = 0.7*0.06 + 0.3*0.1 = 0.072
	if snap.CompositeLoad < 0.05 || snap.CompositeLoad > 0.15 {
		t.Fatalf("unexpected composite load: %f", snap.CompositeLoad)
	}
}

func TestNodeMetrics_Overbudget(t *testing.T) {
	nm := NewNodeMetrics("test", 20, nil, nil)

	// 20Hz = 50ms budget. Record an 80ms tick.
	nm.RecordTick(80*time.Millisecond, 0, 0, 0, 0)
	snap := nm.Snapshot()

	if snap.Tick.OverbudgetPct != 1.0 {
		t.Fatalf("expected 100%% overbudget, got %f", snap.Tick.OverbudgetPct)
	}

	// Record a normal tick
	nm.RecordTick(3*time.Millisecond, 0, 0, 0, 0)
	snap = nm.Snapshot()

	if snap.Tick.OverbudgetPct != 0.5 {
		t.Fatalf("expected 50%% overbudget, got %f", snap.Tick.OverbudgetPct)
	}
}

func TestNodeMetrics_ByteCounters(t *testing.T) {
	nm := NewNodeMetrics("test", 20, nil, nil)

	nm.AddBytesSent(100)
	nm.AddBytesSent(200)
	nm.AddBytesRecv(50)

	if nm.bytesSent.Load() != 300 {
		t.Fatalf("expected 300, got %d", nm.bytesSent.Load())
	}
	if nm.bytesRecv.Load() != 50 {
		t.Fatalf("expected 50, got %d", nm.bytesRecv.Load())
	}
}

func TestNodeMetrics_InterNodeCounters(t *testing.T) {
	nm := NewNodeMetrics("test", 20, nil, nil)

	// Fresh state: all counters zero.
	snap := nm.InterNodeSnapshot()
	if snap.BytesSent != 0 || snap.BytesRecv != 0 || snap.BorderFramesSent != 0 || snap.BorderFramesRecv != 0 {
		t.Fatalf("fresh InterNodeSnapshot should be zero; got %+v", snap)
	}

	// Record a few sends and receives.
	nm.RecordBorderFrameSent(120)
	nm.RecordBorderFrameSent(80)
	nm.RecordBorderFrameRecv(200)

	snap = nm.InterNodeSnapshot()
	if snap.BytesSent != 200 {
		t.Fatalf("BytesSent = %d, want 200", snap.BytesSent)
	}
	if snap.BorderFramesSent != 2 {
		t.Fatalf("BorderFramesSent = %d, want 2", snap.BorderFramesSent)
	}
	if snap.BytesRecv != 200 {
		t.Fatalf("BytesRecv = %d, want 200", snap.BytesRecv)
	}
	if snap.BorderFramesRecv != 1 {
		t.Fatalf("BorderFramesRecv = %d, want 1", snap.BorderFramesRecv)
	}
}

func TestNodeMetrics_InterNodeCountersNilSafe(t *testing.T) {
	// Calling Record* on a nil receiver must not panic — callers in
	// pkg/universe pass Metrics opportunistically and may have a nil
	// reference in some test setups.
	var nm *NodeMetrics
	nm.RecordBorderFrameSent(100)
	nm.RecordBorderFrameRecv(100)
}

func TestNodeMetrics_EntityCap(t *testing.T) {
	nm := NewNodeMetrics("test", 20, nil, nil, WithEntityCap(500))

	// With 500 cap, 500 entities = 1.0 entity load
	nm.RecordTick(25*time.Millisecond, 500, 0, 0, 0)
	snap := nm.Snapshot()

	// tickLoad = 25/50 = 0.5, entityLoad = 500/500 = 1.0
	// composite = 0.7*0.5 + 0.3*1.0 = 0.65
	if snap.CompositeLoad < 0.6 || snap.CompositeLoad > 0.7 {
		t.Fatalf("expected ~0.65, got %f", snap.CompositeLoad)
	}
}

func TestNodeMetrics_NilCallbacks(t *testing.T) {
	nm := NewNodeMetrics("test", 20, nil, nil)
	nm.RecordTick(5*time.Millisecond, 10, 0, 0, 2)

	// Should not panic
	snap := nm.Snapshot()
	if snap.Entities.Real != 10 {
		t.Fatalf("expected 10, got %d", snap.Entities.Real)
	}
	if snap.Network.BytesSent != 0 {
		t.Fatal("expected 0 bytes sent with nil callback")
	}
}
