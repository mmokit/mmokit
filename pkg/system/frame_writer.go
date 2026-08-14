package system

import (
	"sync/atomic"

	"github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/quantize"
)

// BinaryFrameWriter sends delta-compressed binary frames wrapped via a
// caller-supplied frame builder. This is the standard FrameWriter for the
// binary delta wire format; games that need custom framing can implement
// the FrameWriter interface directly.
//
// The build closure converts the encoded delta body bytes into a complete
// channel-prefixed wire frame (typically a typed-event frame around
// mmokit.WorldDelta). pkg/system can't import pkg/mmokit (cycle), so the
// wiring lives one layer up — see pkg/mmokit.go.
type BinaryFrameWriter struct {
	connMgr               net.ConnSender
	encoder               *quantize.FrameEncoder
	makeFrame             func(body []byte) []byte
	makeFrameWithMetadata func(frame *ReplicationFrame, body []byte) []byte
	receiptScope          uint64
	fullScratch           []quantize.FullEntry
	deltaScratch          []quantize.DeltaEntry
}

// NewBinaryFrameWriterWithMetadata is like NewBinaryFrameWriter, but gives
// the envelope builder access to frame metadata that intentionally lives
// outside the delta body (for example the session-scoped stream generation).
func NewBinaryFrameWriterWithMetadata(cm net.ConnSender, makeFrame func(frame *ReplicationFrame, body []byte) []byte) *BinaryFrameWriter {
	return &BinaryFrameWriter{
		connMgr:               cm,
		encoder:               quantize.NewFrameEncoder(8192),
		makeFrameWithMetadata: makeFrame,
		receiptScope:          nextBinaryFrameWriterScope.Add(1),
	}
}

var nextBinaryFrameWriterScope atomic.Uint64

// FrameReceiptSource is an optional extension implemented by frame writers
// whose connection boundary can confirm final downstream admission
// asynchronously. ReplicationSystem drains only the connection it currently
// owns so writers backed by a process-wide connection manager cannot consume
// another cell's receipts.
type FrameReceiptSource interface {
	DrainFrameReceipts(connID uint32) []net.ReplicationReceipt
}

var _ FrameReceiptSource = (*BinaryFrameWriter)(nil)

// NewBinaryFrameWriter creates a FrameWriter that sends binary delta frames.
// makeFrame wraps the encoded delta body in a wire-ready frame (typically
// a typed-event frame for mmokit.WorldDelta).
func NewBinaryFrameWriter(cm net.ConnSender, makeFrame func(body []byte) []byte) *BinaryFrameWriter {
	return &BinaryFrameWriter{
		connMgr:      cm,
		encoder:      quantize.NewFrameEncoder(8192),
		makeFrame:    makeFrame,
		receiptScope: nextBinaryFrameWriterScope.Add(1),
	}
}

func (w *BinaryFrameWriter) WriteFrame(frame *ReplicationFrame) net.SendResult {
	// Per-entity ProducedAtMs is stamped by ReplicationSystem at entry-build
	// time: local-authoritative entities get ClusterClock.TickTime from the
	// configured clock (tick-aligned); replicas re-use the cached
	// Replica.ProducedAtMs from the border-frame codec. FrameWriter just
	// passes the value through to the wire encoder.
	if cap(w.fullScratch) < len(frame.Full) {
		w.fullScratch = make([]quantize.FullEntry, len(frame.Full))
	} else {
		w.fullScratch = w.fullScratch[:len(frame.Full)]
	}
	full := w.fullScratch
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

	if cap(w.deltaScratch) < len(frame.Deltas) {
		w.deltaScratch = make([]quantize.DeltaEntry, len(frame.Deltas))
	} else {
		w.deltaScratch = w.deltaScratch[:len(frame.Deltas)]
	}
	deltas := w.deltaScratch
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

	var binData []byte
	if frame.HasInputAck {
		binData = w.encoder.Encode(
			frame.Tick, frame.Seq, frame.Flags,
			full, deltas, frame.Removed, frame.Exited, frame.ProcessedInputSeq,
		)
	} else {
		binData = w.encoder.Encode(
			frame.Tick, frame.Seq, frame.Flags,
			full, deltas, frame.Removed, frame.Exited,
		)
	}
	// The encoder has consumed the entry descriptors synchronously. Clear
	// slice-bearing fields so a quiet connection does not retain entity
	// snapshots solely through reusable scratch capacity.
	clear(full)
	clear(deltas)

	// Copy because the encoder reuses its internal buffer.
	wireData := make([]byte, len(binData))
	copy(wireData, binData)

	var data []byte
	if w.makeFrameWithMetadata != nil {
		data = w.makeFrameWithMetadata(frame, wireData)
	} else {
		data = w.makeFrame(wireData)
	}
	if data == nil {
		return net.SendResult{Disposition: net.SendFailed}
	}
	if tracked, ok := w.connMgr.(net.TrackedReplicationSender); ok {
		return tracked.SendReplication(
			frame.Viewer.ConnID,
			w.receiptScope,
			frame.StreamEpoch,
			frame.Seq,
			data,
		)
	}
	return w.connMgr.Send(frame.Viewer.ConnID, data)
}

// DrainFrameReceipts exposes asynchronous replication receipts when the
// underlying connection sender supports them. Local ConnManager writers keep
// their existing synchronous result path and return no receipts.
func (w *BinaryFrameWriter) DrainFrameReceipts(connID uint32) []net.ReplicationReceipt {
	source, ok := w.connMgr.(net.ReplicationReceiptSource)
	if !ok {
		return nil
	}
	return source.DrainReplicationReceipts(connID, w.receiptScope)
}
