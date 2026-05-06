package system

import (
	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/quantize"
)

// BinaryFrameWriter sends delta-compressed binary frames wrapped via a
// caller-supplied frame builder. This is the standard FrameWriter for the
// binary delta wire format; games that need custom framing can implement
// the FrameWriter interface directly.
//
// The build closure converts the encoded delta body bytes into a complete
// channel-prefixed wire frame (typically a typed-event frame around
// mmokit.WorldDelta). pkg/system can't import pkg/mmokit (cycle), so the
// wiring lives one layer up — see pkg/mmokit/mmokit.go.
type BinaryFrameWriter struct {
	connMgr   net.ConnSender
	encoder   *quantize.FrameEncoder
	makeFrame func(body []byte) []byte
}

// NewBinaryFrameWriter creates a FrameWriter that sends binary delta frames.
// makeFrame wraps the encoded delta body in a wire-ready frame (typically
// a typed-event frame for mmokit.WorldDelta).
func NewBinaryFrameWriter(cm net.ConnSender, makeFrame func(body []byte) []byte) *BinaryFrameWriter {
	return &BinaryFrameWriter{
		connMgr:   cm,
		encoder:   quantize.NewFrameEncoder(8192),
		makeFrame: makeFrame,
	}
}

func (w *BinaryFrameWriter) WriteFrame(frame *ReplicationFrame) {
	// Per-entity ProducedAtMs is stamped by ReplicationSystem at entry-build
	// time: local-authoritative entities get ClusterClock.TickTime from the
	// configured clock (tick-aligned); replicas re-use the cached
	// Replica.ProducedAtMs from the border-frame codec. FrameWriter just
	// passes the value through to the wire encoder.
	full := make([]quantize.FullEntry, len(frame.Full))
	for i := range frame.Full {
		fp := &frame.Full[i]
		full[i] = quantize.FullEntry{
			NetID:        fp.NetID,
			Epoch:        fp.Epoch,
			EntityType:   fp.Type,
			ProducedAtMs: fp.ProducedAtMs,
			Snapshot:     fp.Snapshot,
			InitialData:  fp.InitialData,
		}
	}

	deltas := make([]quantize.DeltaEntry, len(frame.Deltas))
	for i := range frame.Deltas {
		dp := &frame.Deltas[i]
		deltas[i] = quantize.DeltaEntry{
			NetID:        dp.NetID,
			Epoch:        dp.Epoch,
			EntityType:   dp.Type,
			ProducedAtMs: dp.ProducedAtMs,
			Data:         dp.Data,
		}
	}

	binData := w.encoder.Encode(
		frame.Tick, frame.Seq, frame.Flags,
		full, deltas, frame.Removed, frame.Exited,
	)

	// Copy because the encoder reuses its internal buffer.
	wireData := make([]byte, len(binData))
	copy(wireData, binData)

	data := w.makeFrame(wireData)
	if data != nil {
		w.connMgr.Send(frame.Viewer.ConnID, data)
	}
}
