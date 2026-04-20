package system

import (
	"time"

	"github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/quantize"
)

// BinaryFrameWriter sends delta-compressed binary frames via a configurable event code.
// This is the standard FrameWriter for the binary wire format. Games that need
// custom framing can implement the FrameWriter interface directly.
type BinaryFrameWriter struct {
	connMgr   net.ConnSender
	encoder   *quantize.FrameEncoder
	eventCode uint32
	makeFrame func(code uint32, data []byte) []byte // MakeEventRaw function
}

// NewBinaryFrameWriter creates a FrameWriter that sends binary delta frames.
// eventCode is the ServerEventCode (e.g., SE_DELTA_WORLD_UPDATE = 13).
// makeFrame wraps raw binary data in a ServerEvent envelope (typically mmokit.MakeEventRaw).
func NewBinaryFrameWriter(cm net.ConnSender, eventCode uint32, makeFrame func(uint32, []byte) []byte) *BinaryFrameWriter {
	return &BinaryFrameWriter{
		connMgr:   cm,
		encoder:   quantize.NewFrameEncoder(8192),
		eventCode: eventCode,
		makeFrame: makeFrame,
	}
}

func (w *BinaryFrameWriter) WriteFrame(frame *ReplicationFrame) {
	full := make([]quantize.FullEntry, len(frame.Full))
	for i := range frame.Full {
		fp := &frame.Full[i]
		full[i] = quantize.FullEntry{
			NetID:       fp.NetID,
			Epoch:       fp.Epoch,
			EntityType:  fp.Type,
			Snapshot:    fp.Snapshot,
			InitialData: fp.InitialData,
		}
	}

	deltas := make([]quantize.DeltaEntry, len(frame.Deltas))
	for i := range frame.Deltas {
		dp := &frame.Deltas[i]
		deltas[i] = quantize.DeltaEntry{
			NetID:      dp.NetID,
			Epoch:      dp.Epoch,
			EntityType: dp.Type,
			Data:       dp.Data,
		}
	}

	serverTimeMs := uint64(time.Now().UnixMilli())
	binData := w.encoder.Encode(
		frame.Tick, frame.Seq, frame.Flags,
		serverTimeMs,
		full, deltas, frame.Removed, frame.Exited,
	)

	// Copy because the encoder reuses its internal buffer.
	wireData := make([]byte, len(binData))
	copy(wireData, binData)

	data := w.makeFrame(w.eventCode, wireData)
	if data != nil {
		w.connMgr.Send(frame.Viewer.ConnID, data)
	}
}
