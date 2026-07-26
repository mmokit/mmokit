package net

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	outboundBufferSize = 64
	inputBufferSize    = 32

	// DefaultWebSocketWriteTimeout bounds how long a single WebSocket write may
	// hold the connection's write pump. A client that stops reading is closed
	// instead of pinning the writer indefinitely and keeping its outbound queue
	// permanently saturated.
	DefaultWebSocketWriteTimeout = 5 * time.Second

	// SlowWriteThreshold is the ws.Write duration above which we log a
	// warning. Above this, the write is almost certainly blocking on
	// TCP back-pressure (client too slow draining) or kernel send-buffer
	// fullness — both are root-cause signals for the stutter
	// investigation.
	SlowWriteThreshold = 5 * time.Millisecond

	// SlowQueueThreshold is the time-in-outbound-channel above which we
	// log a warning. Above this, the writePump goroutine itself was
	// not being scheduled — points at Go runtime / GC issues.
	SlowQueueThreshold = 5 * time.Millisecond

	// Channel bytes prepended to every WebSocket frame.
	ChannelEvent     byte = 0x00 // game events (input, world updates, etc.)
	ChannelOperation byte = 0x01 // service operations (marketplace, etc.)
	// 0x02 (was ChannelClientInput) was retired in Plan 1 Phase 5 and
	// fully deleted in Phase 7: typed client-input now flows on
	// ChannelEvent and disambiguates by typeID at the host-side
	// dispatcher.
)

// outboundEntry pairs a queued payload with the wall-clock time it was
// queued. Lets the write pump measure goroutine-scheduling delay
// (queueDur) separately from ws.Write blocking (writeDur).
type outboundEntry struct {
	data     []byte
	queuedAt time.Time
}

// websocketConn is the subset of websocket.Conn used by Conn. Keeping this
// boundary small also lets the write deadline be tested without relying on
// kernel socket-buffer timing.
type websocketConn interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
	CloseNow() error
}

// Conn wraps a WebSocket connection with read/write pumps.
type Conn struct {
	id       uint32
	ws       websocketConn
	outbound chan outboundEntry
	done     chan struct{}
	// writeTimeout is immutable after construction. A non-positive value uses
	// DefaultWebSocketWriteTimeout, which keeps directly-constructed test
	// connections and future call sites safe by default.
	writeTimeout time.Duration
	sendMu       sync.Mutex // serializes outbound admission with Close
	mu           sync.Mutex // guards inbound queues
	input        [][]byte   // channel 0x00 frames
	opInput      [][]byte   // channel 0x01 frames
	closed       bool       // guarded by sendMu

	bytesSent atomic.Uint64
	bytesRecv atomic.Uint64

	// Diagnostic counters. Read via getters on *Conn for the
	// /debug/conn-stats endpoint. Atomic so the read path is lock-free.
	totalWrites     atomic.Uint64
	slowWrites      atomic.Uint64 // ws.Write > SlowWriteThreshold
	slowQueues      atomic.Uint64 // queueDur > SlowQueueThreshold
	writeDurUsTotal atomic.Uint64 // sum of all write durations (µs)
	queueDurUsTotal atomic.Uint64 // sum of all queue durations (µs)
	writeDurUsMax   atomic.Uint64 // max single write duration (µs)
	queueDurUsMax   atomic.Uint64 // max single queue duration (µs)
}

func newConn(id uint32, ws *websocket.Conn) *Conn {
	c := &Conn{
		id:           id,
		ws:           ws,
		outbound:     make(chan outboundEntry, outboundBufferSize),
		done:         make(chan struct{}),
		input:        make([][]byte, 0, inputBufferSize),
		opInput:      make([][]byte, 0, 8),
		writeTimeout: DefaultWebSocketWriteTimeout,
	}
	// Start write pump in background
	go c.writePump()
	return c
}

// Send queues a message for sending. It is non-blocking and reports queue
// pressure to the caller. Send and Close serialize through sendMu so admission
// cannot race shutdown; outbound itself intentionally remains open and done
// terminates the writer.
func (c *Conn) Send(data []byte) SendResult {
	entry := outboundEntry{data: data, queuedAt: time.Now()}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.closed {
		return SendResult{Disposition: SendClosed}
	}
	select {
	case c.outbound <- entry:
		return queuedSend(DeliveryReliableOrdered)
	default:
		log.Printf("conn %d: outbound buffer full, dropping message", c.id)
		return SendResult{Disposition: SendBackpressure}
	}
}

// DrainInput returns all queued event messages (channel 0x00) and clears the queue.
func (c *Conn) DrainInput() [][]byte {
	c.mu.Lock()
	if len(c.input) == 0 {
		c.mu.Unlock()
		return nil
	}
	msgs := c.input
	c.input = make([][]byte, 0, inputBufferSize)
	c.mu.Unlock()
	return msgs
}

// DrainOpInput returns all queued operation messages (channel 0x01) and clears the queue.
func (c *Conn) DrainOpInput() [][]byte {
	c.mu.Lock()
	if len(c.opInput) == 0 {
		c.mu.Unlock()
		return nil
	}
	msgs := c.opInput
	c.opInput = make([][]byte, 0, 8)
	c.mu.Unlock()
	return msgs
}

// InjectInput appends a message directly to the channel 0x00 input queue.
// Used by the inter-cell forwarding path to inject forwarded input on the
// destination cell without going through the WebSocket read pump.
func (c *Conn) InjectInput(data []byte) {
	msg := make([]byte, len(data))
	copy(msg, data)
	c.mu.Lock()
	c.input = append(c.input, msg)
	c.mu.Unlock()
}

// Close closes the connection.
func (c *Conn) Close() {
	c.sendMu.Lock()
	if c.closed {
		c.sendMu.Unlock()
		return
	}
	c.closed = true
	close(c.done)
	c.sendMu.Unlock()
	if c.ws != nil {
		c.ws.CloseNow()
	}
}

// readPump reads messages from the WebSocket and routes by channel byte.
func (c *Conn) readPump(ctx context.Context) {
	defer c.Close()
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		if len(data) == 0 {
			continue
		}
		c.bytesRecv.Add(uint64(len(data)))
		// First byte is the channel
		channel := data[0]
		payload := data[1:]
		switch channel {
		case ChannelOperation:
			c.mu.Lock()
			c.opInput = append(c.opInput, payload)
			c.mu.Unlock()
		default:
			// Channel 0x00 (events) or unknown — treat as event.
			// Typed client-input frames also ride this channel after
			// Plan 1 Phase 5 (the host disambiguates by typeID).
			c.mu.Lock()
			c.input = append(c.input, payload)
			c.mu.Unlock()
		}
	}
}

// writePump writes messages from the outbound channel to the WebSocket.
//
// For diagnostics, records two durations per write:
//
//   - queueDur: time the payload spent in the outbound channel before
//     the pump picked it up. Large values indicate the writePump
//     goroutine was not being scheduled (GC / runtime issue).
//   - writeDur: time spent inside ws.Write itself. Large values indicate
//     TCP back-pressure (the client isn't draining fast enough, so the
//     kernel send buffer is full).
//
// Either kind crossing its threshold gets warn-logged and bumps a
// counter exposed via ConnStats().
func (c *Conn) writePump() {
	writeTimeout := c.writeTimeout
	if writeTimeout <= 0 {
		writeTimeout = DefaultWebSocketWriteTimeout
	}

	for {
		var entry outboundEntry
		select {
		case <-c.done:
			return
		case entry = <-c.outbound:
		}
		queueDur := time.Since(entry.queuedAt)
		writeStart := time.Now()
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), writeTimeout)
		err := c.ws.Write(writeCtx, websocket.MessageBinary, entry.data)
		cancelWrite()
		writeDur := time.Since(writeStart)

		c.totalWrites.Add(1)
		c.queueDurUsTotal.Add(uint64(queueDur.Microseconds()))
		c.writeDurUsTotal.Add(uint64(writeDur.Microseconds()))
		updateMax(&c.queueDurUsMax, uint64(queueDur.Microseconds()))
		updateMax(&c.writeDurUsMax, uint64(writeDur.Microseconds()))

		if queueDur > SlowQueueThreshold {
			c.slowQueues.Add(1)
			log.Printf("conn %d: slow queue %dms (writePump scheduling lag, bytes=%d)",
				c.id, queueDur.Milliseconds(), len(entry.data))
		}
		if writeDur > SlowWriteThreshold {
			c.slowWrites.Add(1)
			log.Printf("conn %d: slow ws.Write %dms (TCP back-pressure, bytes=%d)",
				c.id, writeDur.Milliseconds(), len(entry.data))
		}

		if err != nil {
			c.Close()
			return
		}
		c.bytesSent.Add(uint64(len(entry.data)))
	}
}

// updateMax atomically updates *cur to v if v > *cur. Lock-free via CAS.
func updateMax(cur *atomic.Uint64, v uint64) {
	for {
		prev := cur.Load()
		if v <= prev {
			return
		}
		if cur.CompareAndSwap(prev, v) {
			return
		}
	}
}

// ConnStats is a snapshot of one Conn's write-path timing counters.
// Used by /debug/conn-stats to expose the data to the Bun probe and
// the in-browser diagnostic overlay.
type ConnStats struct {
	ConnID         uint32 `json:"connId"`
	TotalWrites    uint64 `json:"totalWrites"`
	SlowWrites     uint64 `json:"slowWrites"`
	SlowQueues     uint64 `json:"slowQueues"`
	WriteDurUsMean uint64 `json:"writeDurUsMean"`
	QueueDurUsMean uint64 `json:"queueDurUsMean"`
	WriteDurUsMax  uint64 `json:"writeDurUsMax"`
	QueueDurUsMax  uint64 `json:"queueDurUsMax"`
	BytesSent      uint64 `json:"bytesSent"`
}

// Stats returns a snapshot of this connection's write-path counters.
func (c *Conn) Stats() ConnStats {
	total := c.totalWrites.Load()
	var writeMean, queueMean uint64
	if total > 0 {
		writeMean = c.writeDurUsTotal.Load() / total
		queueMean = c.queueDurUsTotal.Load() / total
	}
	return ConnStats{
		ConnID:         c.id,
		TotalWrites:    total,
		SlowWrites:     c.slowWrites.Load(),
		SlowQueues:     c.slowQueues.Load(),
		WriteDurUsMean: writeMean,
		QueueDurUsMean: queueMean,
		WriteDurUsMax:  c.writeDurUsMax.Load(),
		QueueDurUsMax:  c.queueDurUsMax.Load(),
		BytesSent:      c.bytesSent.Load(),
	}
}

// BytesSent returns cumulative bytes written to this connection.
func (c *Conn) BytesSent() uint64 { return c.bytesSent.Load() }

// BytesRecv returns cumulative bytes read from this connection.
func (c *Conn) BytesRecv() uint64 { return c.bytesRecv.Load() }
