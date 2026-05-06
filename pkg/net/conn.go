package net

import (
	"context"
	"log"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

const (
	outboundBufferSize = 64
	inputBufferSize    = 32

	// Channel bytes prepended to every WebSocket frame.
	ChannelEvent       byte = 0x00 // game events (input, world updates, etc.)
	ChannelOperation   byte = 0x01 // service operations (marketplace, etc.)
	// ChannelClientInput is retired in Plan 1 Phase 5: typed client-input
	// frames now flow on ChannelEvent (0x00) and disambiguate by typeID
	// at the host-side dispatcher. The constant remains as a deprecation
	// marker until Phase 7 deletes it; the read pump and VCM panic on
	// any inbound frame still tagged with this byte.
	ChannelClientInput byte = 0x02
)

// EventInterceptor is called from the read goroutine for each event (channel
// 0x00) frame before it is queued. If it returns true the message is considered
// handled and will NOT be placed in the input queue. The interceptor receives
// the raw event payload (without the channel byte) and may call conn.Send to
// reply immediately.
type EventInterceptor func(conn *Conn, payload []byte) (handled bool)

// Conn wraps a WebSocket connection with read/write pumps.
type Conn struct {
	id               uint32
	ws               *websocket.Conn
	outbound         chan []byte
	mu               sync.Mutex
	input            [][]byte // channel 0x00 frames
	opInput          [][]byte // channel 0x01 frames
	clientInput      [][]byte // channel 0x02 frames (mmokit typed client-input)
	closed           bool
	eventInterceptor EventInterceptor

	bytesSent atomic.Uint64
	bytesRecv atomic.Uint64
}

func newConn(id uint32, ws *websocket.Conn) *Conn {
	c := &Conn{
		id:          id,
		ws:          ws,
		outbound:    make(chan []byte, outboundBufferSize),
		input:       make([][]byte, 0, inputBufferSize),
		opInput:     make([][]byte, 0, 8),
		clientInput: make([][]byte, 0, 8),
	}
	// Start write pump in background
	go c.writePump()
	return c
}

// Send queues a message for sending. Non-blocking; drops if buffer full.
func (c *Conn) Send(data []byte) {
	select {
	case c.outbound <- data:
	default:
		log.Printf("conn %d: outbound buffer full, dropping message", c.id)
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

// DrainClientInput returns all queued typed client-input messages
// (channel 0x02) and clears the queue. Drained per-tick by the gateway
// dispatch path; frames are dispatched to mmokit.HandleClient handlers
// via the typed-message dispatcher.
func (c *Conn) DrainClientInput() [][]byte {
	c.mu.Lock()
	if len(c.clientInput) == 0 {
		c.mu.Unlock()
		return nil
	}
	msgs := c.clientInput
	c.clientInput = make([][]byte, 0, 8)
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
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	close(c.outbound)
	c.ws.CloseNow()
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
		case ChannelClientInput:
			// Plan 1 Phase 5 retired the 0x02 typed-input channel.
			// Typed inputs now flow on ChannelEvent (0x00) — any
			// client still framing on 0x02 is a stale build; fail
			// loudly so the mismatch surfaces instead of being
			// silently routed into a dead queue.
			panic("ChannelClientInput retired in Plan 1 Phase 5; client must send typed inputs on ChannelEvent")
		default:
			// Channel 0x00 (events) or unknown — treat as event
			if c.eventInterceptor != nil && c.eventInterceptor(c, payload) {
				continue
			}
			c.mu.Lock()
			c.input = append(c.input, payload)
			c.mu.Unlock()
		}
	}
}

// writePump writes messages from the outbound channel to the WebSocket.
func (c *Conn) writePump() {
	for data := range c.outbound {
		err := c.ws.Write(context.Background(), websocket.MessageBinary, data)
		if err != nil {
			return
		}
		c.bytesSent.Add(uint64(len(data)))
	}
}

// BytesSent returns cumulative bytes written to this connection.
func (c *Conn) BytesSent() uint64 { return c.bytesSent.Load() }

// BytesRecv returns cumulative bytes read from this connection.
func (c *Conn) BytesRecv() uint64 { return c.bytesRecv.Load() }
