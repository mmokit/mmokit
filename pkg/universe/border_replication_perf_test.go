package universe

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/replication"
)

// BenchmarkApplyBorderFrame_LargeFrame measures the per-entity cost of
// applying a decoded border frame to create or update replicas. Uses
// a single large frame with 500 entries (create path on first run,
// update path on subsequent iterations) to approximate a busy border
// with steady neighbor traffic.
func BenchmarkApplyBorderFrame_LargeFrame(b *testing.B) {
	base := newTestWorldBase(&testing.T{}, CellID{X: 1, Y: 0})

	const n = 500
	entries := make([]replication.FrameEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = replication.FrameEntry{
			NetID:    replication.NetID{ID: uint32(i + 1), Epoch: 0},
			Kind:     1,
			DeltaBuf: buildWireEntry(1100+float32(i%200), 500+float32(i%100), 10, 50, 25),
		}
	}
	frame := replication.Frame{Entries: entries}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		base.ApplyBorderFrame(frame, "source")
	}
	b.StopTimer()

	// Report the size impact: approximate bytes per entity on the wire.
	entryBytes := 14 + 18 // FrameEntry fixed header + DeltaBuf size
	b.ReportMetric(float64(entryBytes), "bytes/entity")
	b.ReportMetric(float64(n*entryBytes), "bytes/frame")
}

// BenchmarkBorderFrame_EncodeDecodeRoundTrip measures the cost of one
// full Frame encode + decode cycle at a typical per-tick frame size.
// Matches what CellViewer.Send + Cell.processMessage will exercise in
// production.
func BenchmarkBorderFrame_EncodeDecodeRoundTrip(b *testing.B) {
	const n = 100
	entries := make([]replication.FrameEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = replication.FrameEntry{
			NetID:    replication.NetID{ID: uint32(i + 1), Epoch: 0},
			Kind:     1,
			DeltaBuf: buildWireEntry(float32(100+i), float32(200+i), 5, 10, -10),
		}
	}
	frame := replication.Frame{
		ViewerID:   1,
		SenderNode: 42,
		Tick:       0,
		Entries:    entries,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wire := frame.Encode()
		decoded, err := replication.DecodeFrame(wire)
		if err != nil {
			b.Fatal(err)
		}
		if len(decoded.Entries) != n {
			b.Fatalf("round-trip lost entries: got %d", len(decoded.Entries))
		}
	}
	b.StopTimer()

	// Report encoded frame size so regressions are visible in the bench output.
	wire := frame.Encode()
	b.ReportMetric(float64(len(wire)), "bytes/frame")
	b.ReportMetric(float64(len(wire))/float64(n), "bytes/entity")
}
