package net

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type blockingWebSocketConn struct {
	writeDeadline chan time.Time
	closed        chan struct{}
	closeOnce     sync.Once
	closeCalls    atomic.Int32
}

func newBlockingWebSocketConn() *blockingWebSocketConn {
	return &blockingWebSocketConn{
		writeDeadline: make(chan time.Time, 1),
		closed:        make(chan struct{}),
	}
}

func (c *blockingWebSocketConn) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}

func (c *blockingWebSocketConn) Write(ctx context.Context, _ websocket.MessageType, _ []byte) error {
	deadline, _ := ctx.Deadline()
	c.writeDeadline <- deadline
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return context.Canceled
	}
}

func (c *blockingWebSocketConn) CloseNow() error {
	c.closeCalls.Add(1)
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func newQueueOnlyConn(capacity int) *Conn {
	return &Conn{
		id:       1,
		outbound: make(chan outboundEntry, capacity),
		done:     make(chan struct{}),
	}
}

func TestConnSend_Outcomes(t *testing.T) {
	c := newQueueOnlyConn(1)

	result := c.Send([]byte("first"))
	if !result.Supports(DeliveryReliableOrdered) {
		t.Fatalf("first send result = %+v, want reliable ordered enqueue", result)
	}

	result = c.Send([]byte("second"))
	if result.Disposition != SendBackpressure {
		t.Fatalf("second send disposition = %v, want backpressure", result.Disposition)
	}

	c.Close()
	result = c.Send([]byte("after-close"))
	if result.Disposition != SendClosed {
		t.Fatalf("send after close disposition = %v, want closed", result.Disposition)
	}
}

func TestConnSendConcurrentClose(t *testing.T) {
	const senders = 16
	const sendsPerGoroutine = 100
	c := newQueueOnlyConn(senders * sendsPerGoroutine)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(senders + 1)

	for i := 0; i < senders; i++ {
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < sendsPerGoroutine; j++ {
				result := c.Send([]byte{byte(j)})
				switch result.Disposition {
				case SendQueued, SendBackpressure, SendClosed:
				default:
					t.Errorf("unexpected send disposition %v", result.Disposition)
					return
				}
			}
		}()
	}

	go func() {
		defer wg.Done()
		<-start
		c.Close()
	}()

	close(start)
	wg.Wait()

	if result := c.Send([]byte("done")); result.Disposition != SendClosed {
		t.Fatalf("final send disposition = %v, want closed", result.Disposition)
	}
}

func TestConnWritePump_DeadlineClosesWedgedConnectionOnce(t *testing.T) {
	const writeTimeout = 25 * time.Millisecond
	ws := newBlockingWebSocketConn()
	c := &Conn{
		id:           1,
		ws:           ws,
		outbound:     make(chan outboundEntry, 1),
		done:         make(chan struct{}),
		writeTimeout: writeTimeout,
	}
	go c.writePump()

	startedAt := time.Now()
	if result := c.Send([]byte("blocked")); !result.Queued() {
		t.Fatalf("send result = %+v, want queued", result)
	}

	select {
	case deadline := <-ws.writeDeadline:
		if deadline.IsZero() {
			t.Fatal("write context has no deadline")
		}
		if got := deadline.Sub(startedAt); got <= 0 || got > 250*time.Millisecond {
			t.Fatalf("write deadline offset = %v, want approximately %v", got, writeTimeout)
		}
	case <-time.After(time.Second):
		t.Fatal("write did not start")
	}

	select {
	case <-ws.closed:
	case <-time.After(time.Second):
		t.Fatal("timed-out write did not close the WebSocket")
	}
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("timed-out write did not close the connection")
	}

	// Both a repeated explicit close and the write pump's error path must be
	// idempotent at the underlying WebSocket boundary.
	c.Close()
	c.Close()
	if got := ws.closeCalls.Load(); got != 1 {
		t.Fatalf("CloseNow calls = %d, want 1", got)
	}
	if result := c.Send([]byte("after-timeout")); result.Disposition != SendClosed {
		t.Fatalf("send after timeout disposition = %v, want closed", result.Disposition)
	}
	stats := c.Stats()
	if stats.TotalWrites != 1 || stats.BytesSent != 0 {
		t.Fatalf("stats after timeout = %+v, want one failed write and zero sent bytes", stats)
	}
}
