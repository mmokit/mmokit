package net

import (
	"context"
	"log"
	"sync"

	"github.com/coder/websocket"
)

const (
	outboundBufferSize = 64
	inputBufferSize    = 32
)

// Conn wraps a WebSocket connection with read/write pumps.
type Conn struct {
	id       uint32
	ws       *websocket.Conn
	outbound chan []byte
	mu       sync.Mutex
	input    [][]byte
	closed   bool
}

func newConn(id uint32, ws *websocket.Conn) *Conn {
	c := &Conn{
		id:       id,
		ws:       ws,
		outbound: make(chan []byte, outboundBufferSize),
		input:    make([][]byte, 0, inputBufferSize),
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

// DrainInput returns all queued input messages and clears the queue.
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

// readPump reads messages from the WebSocket and queues them.
func (c *Conn) readPump(ctx context.Context) {
	defer c.Close()
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		c.mu.Lock()
		c.input = append(c.input, data)
		c.mu.Unlock()
	}
}

// writePump writes messages from the outbound channel to the WebSocket.
func (c *Conn) writePump() {
	for data := range c.outbound {
		err := c.ws.Write(context.Background(), websocket.MessageBinary, data)
		if err != nil {
			return
		}
	}
}
