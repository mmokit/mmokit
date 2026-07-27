package net

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// scriptedWebSocketConn replays a fixed list of inbound frames to readPump and
// then blocks until its context is cancelled, so the pump's queueing behaviour
// can be driven without a real socket.
type scriptedWebSocketConn struct {
	frames [][]byte
	next   int
}

func (c *scriptedWebSocketConn) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	if c.next < len(c.frames) {
		f := c.frames[c.next]
		c.next++
		return websocket.MessageBinary, f, nil
	}
	<-ctx.Done()
	return 0, nil, ctx.Err()
}

func (c *scriptedWebSocketConn) Write(context.Context, websocket.MessageType, []byte) error {
	return nil
}

func (c *scriptedWebSocketConn) CloseNow() error { return nil }

// The per-connection event queue must be bounded. inputBufferSize is an initial
// capacity, not a bound: nothing drains a pre-authentication connection's
// channel-0x00 queue (runSessionPump is not launched on the `all` preset, and
// both Stage.DispatchClientInput and ops.Router.poll only visit connections
// that already have a session), so an unbounded append is an unauthenticated
// memory-exhaustion primitive reachable with entirely well-formed frames.
func TestReadPump_DropsAtInputQueueCap(t *testing.T) {
	const cap = 16
	const overflow = 100

	limits := DefaultWireLimits()
	limits.MaxInputQueueDepth = cap
	// Keep the drain budget above the cap so this test observes the queue
	// bound rather than the per-drain bound.
	limits.MaxFramesPerDrain = cap + overflow

	frames := make([][]byte, 0, cap+overflow)
	for i := range cap + overflow {
		frames = append(frames, []byte{ChannelEvent, byte(i)})
	}

	ws := &scriptedWebSocketConn{frames: frames}
	c := &Conn{
		id:       1,
		ws:       ws,
		outbound: make(chan outboundEntry, 1),
		done:     make(chan struct{}),
		input:    make([][]byte, 0, inputBufferSize),
		limits:   limits,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.readPump(ctx)
		close(done)
	}()

	waitFor(t, "event-queue drops", func() bool { return c.InputDrops() == overflow })
	cancel()
	<-done

	msgs := c.DrainInput()
	if len(msgs) != cap {
		t.Fatalf("DrainInput returned %d frames, want %d (queue cap)", len(msgs), cap)
	}
	if got := c.InputDrops(); got != overflow {
		t.Fatalf("InputDrops = %d, want %d", got, overflow)
	}
	// Drop-newest: the frames retained must be the FIRST cap frames.
	for i, m := range msgs {
		if len(m) != 1 || m[0] != byte(i) {
			t.Fatalf("retained frame %d = %v, want the oldest frames (drop-newest)", i, m)
		}
	}
}

// The op queue has the same exposure and the same bound.
func TestReadPump_DropsAtOpQueueCap(t *testing.T) {
	const cap = 8
	const overflow = 20

	limits := DefaultWireLimits()
	limits.MaxOpQueueDepth = cap
	limits.MaxFramesPerDrain = cap + overflow

	frames := make([][]byte, 0, cap+overflow)
	for i := range cap + overflow {
		frames = append(frames, []byte{ChannelOperation, byte(i)})
	}

	c := &Conn{
		id:       1,
		ws:       &scriptedWebSocketConn{frames: frames},
		outbound: make(chan outboundEntry, 1),
		done:     make(chan struct{}),
		opInput:  make([][]byte, 0, 8),
		limits:   limits,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.readPump(ctx)
		close(done)
	}()

	waitFor(t, "op-queue drops", func() bool { return c.OpInputDrops() == overflow })
	cancel()
	<-done

	if got := len(c.DrainOpInput()); got != cap {
		t.Fatalf("DrainOpInput returned %d frames, want %d (queue cap)", got, cap)
	}
}

// A drain must hand back at most MaxFramesPerDrain frames and leave the
// remainder queued — dropping the overflow here would lose frames the
// connection is already within its depth budget to hold.
func TestDrainInput_StopsAtPerDrainBudget(t *testing.T) {
	limits := DefaultWireLimits()
	limits.MaxFramesPerDrain = 4
	limits.MaxInputQueueDepth = 64

	c := &Conn{id: 1, done: make(chan struct{}), limits: limits}
	for i := range 10 {
		c.InjectInput([]byte{byte(i)})
	}

	first := c.DrainInput()
	if len(first) != 4 {
		t.Fatalf("first drain returned %d frames, want 4", len(first))
	}
	second := c.DrainInput()
	if len(second) != 4 {
		t.Fatalf("second drain returned %d frames, want 4", len(second))
	}
	third := c.DrainInput()
	if len(third) != 2 {
		t.Fatalf("third drain returned %d frames, want 2", len(third))
	}
	if got := c.DrainInput(); got != nil {
		t.Fatalf("fourth drain = %v, want nil", got)
	}
	if first[0][0] != 0 || second[0][0] != 4 || third[0][0] != 8 {
		t.Fatalf("drains returned frames out of order: %v %v %v", first[0], second[0], third[0])
	}
}

// UDP sessions are source-address bound but still unauthenticated, and their
// queues are drained on exactly the same schedule as the WebSocket ones, so
// every routePayload branch has to be bounded too.
func TestUDPTransport_RoutePayloadCapped(t *testing.T) {
	limits := DefaultWireLimits()
	limits.MaxInputQueueDepth = 4
	limits.MaxOpQueueDepth = 3
	limits.MaxFramesPerDrain = 64
	limits.MaxFrameBytes = 8

	tr := &UDPTransport{limits: limits}

	for range 20 {
		tr.routePayload([]byte{ChannelEvent, 0x01})
		tr.routePayload([]byte{ChannelOperation, 0x02})
		tr.routePayload([]byte{0x42, 0x99}) // legacy, no channel prefix
	}
	if got := len(tr.DrainInput()); got != limits.MaxInputQueueDepth {
		t.Fatalf("event queue held %d frames, want %d", got, limits.MaxInputQueueDepth)
	}
	if got := len(tr.DrainOpInput()); got != limits.MaxOpQueueDepth {
		t.Fatalf("op queue held %d frames, want %d", got, limits.MaxOpQueueDepth)
	}
	if tr.InboundDrops() == 0 || tr.OpInboundDrops() == 0 {
		t.Fatalf("expected non-zero drop counters, got event=%d op=%d",
			tr.InboundDrops(), tr.OpInboundDrops())
	}

	// An oversize payload is refused outright, on its own counter.
	before := tr.OversizeFrameDrops()
	tr.routePayload(append([]byte{ChannelEvent}, make([]byte, limits.MaxFrameBytes)...))
	if tr.OversizeFrameDrops() != before+1 {
		t.Fatalf("OversizeFrameDrops = %d, want %d", tr.OversizeFrameDrops(), before+1)
	}
}

// The WebSocket read limit must be one we set, not coder/websocket's own
// default that happens to be the same number today.
func TestHandleWebSocket_RejectsOversizeFrame(t *testing.T) {
	cm := NewConnManager()
	limits := DefaultWireLimits()
	limits.MaxFrameBytes = 64
	cm.SetWireLimits(limits)

	srv := httptest.NewServer(http.HandlerFunc(cm.HandleWebSocket))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, wsURLFromHTTP(srv.URL), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	// Well under the limit: accepted, connection stays registered.
	if err := c.Write(ctx, websocket.MessageBinary, make([]byte, 16)); err != nil {
		t.Fatalf("small frame write: %v", err)
	}
	waitFor(t, "connection registered", func() bool { return cm.ConnectionCount() == 1 })

	// Over our limit but well under coder/websocket's 32768-byte default, so
	// this only fails if the read limit we set is the one in force.
	oversize := make([]byte, limits.MaxFrameBytes*4)
	if len(oversize) >= DefaultMaxFrameBytes {
		t.Fatalf("test payload %d bytes is not below the library default %d — "+
			"it would prove nothing about our own limit", len(oversize), DefaultMaxFrameBytes)
	}
	_ = c.Write(ctx, websocket.MessageBinary, oversize)

	// The server's read fails, which ends the read pump and unregisters the
	// connection. Asserting on the server's own state rather than on a client
	// read means this cannot pass by timing out.
	waitFor(t, "connection closed after oversize frame", func() bool { return cm.ConnectionCount() == 0 })
}

// RemoteAddrString must report the promoted peer's address for UDP sessions.
// Returning "" collapses every UDP-originated login into a single shared
// IPRateLimiter bucket (ops.ParseClientIP yields the zero netip.Addr) and
// records a null IP on every UDP audit row.
func TestAddTransport_RecordsRemoteAddr(t *testing.T) {
	cm := NewConnManager()
	peer := netip.MustParseAddrPort("203.0.113.7:41234")

	connID := cm.AddTransport(&mockTransport{}, peer.String())
	if got := cm.RemoteAddrString(connID); got != peer.String() {
		t.Fatalf("RemoteAddrString = %q, want %q", got, peer.String())
	}

	// Unregistering must not leave the address behind.
	cm.Unregister(connID)
	if got := cm.RemoteAddrString(connID); got != "" {
		t.Fatalf("RemoteAddrString after Unregister = %q, want \"\"", got)
	}
}

// promotePending is the only place a UDP session's peer address is known, so
// that is where it has to be handed to the ConnManager.
func TestPromotePending_RecordsPeerAddr(t *testing.T) {
	cm := NewConnManager()
	s, err := NewUDPServer("127.0.0.1:0", cm)
	if err != nil {
		t.Fatalf("NewUDPServer: %v", err)
	}
	defer s.conn.Close()

	peer := netip.MustParseAddrPort("198.51.100.9:5000")
	const token uint32 = 0xABCD1234
	s.pending[peer] = pendingHandshake{token: token, createdAt: time.Now()}

	tr := s.promotePending(token, peer)
	if tr == nil {
		t.Fatal("promotePending returned nil for a matching pending entry")
	}
	connID, ok := s.connIDs[tr]
	if !ok {
		t.Fatal("promoted transport has no connID")
	}
	if got := cm.RemoteAddrString(connID); got != peer.String() {
		t.Fatalf("RemoteAddrString = %q, want %q", got, peer.String())
	}
	tr.Close()
}
