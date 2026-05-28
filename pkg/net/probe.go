package net

import (
	"encoding/binary"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// ProbeIntervalMs is how often the heartbeat endpoint emits a frame.
// Matches the production game-loop tick (50ms / 20Hz) so the probe
// produces directly comparable timing to real replication frames.
const ProbeIntervalMs = 50

// ProbeFrameSize is the wire size of one probe frame in bytes. Chosen
// to roughly approximate a small replication frame (header + a couple
// entity entries) without being so large that segmentation kicks in.
const ProbeFrameSize = 32

// HandleProbeWS is a diagnostic WebSocket endpoint that ticks every
// ProbeIntervalMs and emits a small heartbeat frame containing the
// server's sequence number, the tick time (server wall clock in ms),
// and the actual ws.Write completion time.
//
// The frame layout (little-endian):
//
//	[0..8]   uint64 seq             — monotonic from connect
//	[8..16]  uint64 tickUnixMs      — server wall-clock at the tick boundary
//	[16..24] uint64 writeStartUnixMs — server wall-clock just before ws.Write
//	[24..32] uint64 writeEndUnixMs   — server wall-clock just after ws.Write returns
//
// The two extra timestamps let a probe client distinguish three things:
//   - tick → writeStart drift: scheduler / outbound-queue delay on the server
//   - writeStart → writeEnd duration: how long ws.Write itself blocks (back-pressure
//     from the client / TCP send buffer fullness)
//   - writeEnd → client onmessage time: pure transit + browser-event-loop delay
//
// Unlike HandleWebSocket, this path does NOT go through the
// Conn / outbound-channel / writePump indirection — it calls ws.Write
// directly from the per-connection goroutine. That isolates the
// network path from any internal goroutine-scheduling effects.
func HandleProbeWS(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("probe-ws accept error: %v", err)
		return
	}
	defer ws.CloseNow()

	log.Printf("probe-ws: client connected from %s", r.RemoteAddr)
	defer log.Printf("probe-ws: client disconnected from %s", r.RemoteAddr)

	// Track slow-write count so the server log surfaces when ws.Write
	// itself is blocking — that's the clearest signal of TCP back-pressure
	// from the client.
	var slowWrites uint64
	var totalWrites uint64

	ticker := time.NewTicker(ProbeIntervalMs * time.Millisecond)
	defer ticker.Stop()

	ctx := r.Context()
	seq := uint64(0)
	buf := make([]byte, ProbeFrameSize)

	for {
		select {
		case <-ctx.Done():
			log.Printf("probe-ws: closed by ctx (writes=%d slow=%d)", totalWrites, slowWrites)
			return
		case tickTime := <-ticker.C:
			seq++
			tickMs := uint64(tickTime.UnixMilli())

			writeStart := time.Now()
			writeStartMs := uint64(writeStart.UnixMilli())

			binary.LittleEndian.PutUint64(buf[0:8], seq)
			binary.LittleEndian.PutUint64(buf[8:16], tickMs)
			binary.LittleEndian.PutUint64(buf[16:24], writeStartMs)
			// Placeholder; filled below after the write completes. The
			// "endMs" recorded here ends up reflecting the start since we
			// can't write twice — it's the receive-side that measures the
			// after-write delay (writeEnd → client arrival). We store
			// writeStart here too so the wire layout stays fixed.
			binary.LittleEndian.PutUint64(buf[24:32], writeStartMs)

			err := ws.Write(ctx, websocket.MessageBinary, buf)
			writeDur := time.Since(writeStart)
			totalWrites++
			if writeDur > 5*time.Millisecond {
				slowWrites++
				log.Printf("probe-ws: slow write seq=%d dur=%dms (back-pressure?)",
					seq, writeDur.Milliseconds())
			}
			if err != nil {
				return
			}
		}
	}
}
