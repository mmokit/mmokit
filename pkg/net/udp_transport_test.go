package net

import (
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUDPTransport_SendReliableClosedDoesNotRetainFrame(t *testing.T) {
	tr := &UDPTransport{closed: true}

	result := tr.SendReliable([]byte("critical"))
	if result.Disposition != SendClosed {
		t.Fatalf("SendReliable disposition = %v, want closed", result.Disposition)
	}
	if tr.sendSeq != 0 {
		t.Fatalf("SendReliable advanced sequence to %d on closed transport", tr.sendSeq)
	}
	for i := range tr.sendBuf {
		if tr.sendBuf[i].used {
			t.Fatalf("closed SendReliable retained retransmit slot %d", i)
		}
	}
}

func TestUDPTransport_MarkAckedRequiresExactSequenceIdentity(t *testing.T) {
	tr := &UDPTransport{}
	const currentSeq uint16 = 300
	idx := currentSeq % reliableBufSize
	tr.sendBuf[idx] = reliableEntry{
		seq:     currentSeq,
		payload: []byte("current"),
		used:    true,
	}

	// Sequence 44 aliases the same ring slot as 300. Its delayed ACK must not
	// clear the current packet occupying that slot.
	tr.markAcked(44)
	if !tr.sendBuf[idx].used || tr.sendBuf[idx].seq != currentSeq {
		t.Fatalf("stale ACK cleared current slot: %+v", tr.sendBuf[idx])
	}

	tr.markAcked(currentSeq)
	if tr.sendBuf[idx].used || tr.sendBuf[idx].payload != nil {
		t.Fatalf("exact ACK did not release slot: %+v", tr.sendBuf[idx])
	}
}

func TestUDPTransport_SendReliableDoesNotOverwriteFullWindow(t *testing.T) {
	tr := &UDPTransport{}
	tr.sendBuf[0] = reliableEntry{
		seq:     0,
		payload: []byte("unacked"),
		used:    true,
	}

	result := tr.SendReliable([]byte("replacement"))
	if result.Disposition != SendBackpressure {
		t.Fatalf("full-window disposition = %v, want backpressure", result.Disposition)
	}
	if tr.sendSeq != 0 {
		t.Fatalf("full-window send advanced sequence to %d", tr.sendSeq)
	}
	if got := string(tr.sendBuf[0].payload); got != "unacked" {
		t.Fatalf("full-window send overwrote payload with %q", got)
	}
}

// A channel-0x01 frame must land in the op queue (channel byte stripped),
// a channel-0x00 frame in the event queue, and the queues must be
// independent. This is the path auth + every typed op rides.
func TestUDPTransport_RoutePayloadDemuxesChannels(t *testing.T) {
	tr := &UDPTransport{}

	tr.routePayload([]byte{ChannelOperation, 0xAA, 0xBB}) // op
	tr.routePayload([]byte{ChannelEvent, 0x11, 0x22})     // event

	ops := tr.DrainOpInput()
	if len(ops) != 1 {
		t.Fatalf("DrainOpInput: got %d msgs, want 1", len(ops))
	}
	if !slices.Equal(ops[0], []byte{0xAA, 0xBB}) {
		t.Fatalf("op payload = %v, want [170 187]", ops[0])
	}

	evs := tr.DrainInput()
	if len(evs) != 1 {
		t.Fatalf("DrainInput: got %d msgs, want 1", len(evs))
	}
	if !slices.Equal(evs[0], []byte{0x11, 0x22}) {
		t.Fatalf("event payload = %v, want [17 34]", evs[0])
	}

	// Draining clears the queue.
	if got := tr.DrainOpInput(); got != nil {
		t.Fatalf("DrainOpInput after drain = %v, want nil", got)
	}
}

// A frame whose leading byte is neither 0x00 nor 0x01 is a legacy
// pre-channel-prefix event — it must go to the event queue with bytes
// intact and never into the op queue.
func TestUDPTransport_RoutePayloadLegacyNoPrefix(t *testing.T) {
	tr := &UDPTransport{}
	tr.routePayload([]byte{0x42, 0x99})

	evs := tr.DrainInput()
	if len(evs) != 1 || !slices.Equal(evs[0], []byte{0x42, 0x99}) {
		t.Fatalf("legacy event routing = %v, want [[66 153]]", evs)
	}
	if got := tr.DrainOpInput(); got != nil {
		t.Fatalf("legacy frame leaked into op queue: %v", got)
	}
}

// The reliable inbound path (handleReliable) is what auth ops use. Verify
// an op frame delivered reliably surfaces via DrainOpInput.
func TestUDPTransport_ReliableOpFrameReachesOpQueue(t *testing.T) {
	tr := &UDPTransport{}
	tr.handleReliable(1, []byte{ChannelOperation, 0x01, 0x02, 0x03})

	ops := tr.DrainOpInput()
	if len(ops) != 1 || !slices.Equal(ops[0], []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("reliable op frame routing = %v, want [[1 2 3]]", ops)
	}
}

func TestUDPTransport_ReliableReceiveDeduplicatesRetransmits(t *testing.T) {
	tr := &UDPTransport{}
	payload := []byte{ChannelEvent, 0xAA}

	tr.handleReliable(7, payload)
	tr.handleReliable(7, payload)
	tr.handleReliable(8, []byte{ChannelEvent, 0xBB})
	tr.handleReliable(7, payload) // older duplicate already represented in recvBits

	events := tr.DrainInput()
	if len(events) != 2 {
		t.Fatalf("reliable retransmits delivered %d events, want 2", len(events))
	}
	if !slices.Equal(events[0], []byte{0xAA}) || !slices.Equal(events[1], []byte{0xBB}) {
		t.Fatalf("reliable event order/content = %v", events)
	}
}

func TestUDPTransport_ReliableReceiveAcceptsUnseenOutOfOrderPacket(t *testing.T) {
	tr := &UDPTransport{}

	tr.handleReliable(10, []byte{ChannelEvent, 10})
	tr.handleReliable(12, []byte{ChannelEvent, 12})
	tr.handleReliable(11, []byte{ChannelEvent, 11})
	tr.handleReliable(11, []byte{ChannelEvent, 11})

	events := tr.DrainInput()
	want := [][]byte{{10}, {12}, {11}}
	if !slices.EqualFunc(events, want, slices.Equal) {
		t.Fatalf("out-of-order reliable events = %v, want %v", events, want)
	}
}

func TestUDPTransport_ConcurrentDuplicateReliableDeliveredOnce(t *testing.T) {
	tr := &UDPTransport{}
	const callers = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			tr.handleReliable(42, []byte{ChannelEvent, 0xCC})
		}()
	}

	close(start)
	wg.Wait()
	events := tr.DrainInput()
	if len(events) != 1 || !slices.Equal(events[0], []byte{0xCC}) {
		t.Fatalf("concurrent duplicate deliveries = %v, want [[204]]", events)
	}
}

func TestUDPTransport_ACKSnapshotDoesNotClearLaterReceive(t *testing.T) {
	tr := &UDPTransport{}
	tr.handleReliable(10, nil)

	ackSeq, ackBits, ok := tr.takePendingACK()
	if !ok || ackSeq != 10 || ackBits != 0 {
		t.Fatalf("first ACK snapshot = (%d, %08x, %v), want (10, 0, true)", ackSeq, ackBits, ok)
	}

	// This receive occurs after the earlier ACK was snapshotted but before its
	// hypothetical socket write completes. It must remain dirty.
	tr.handleReliable(11, nil)
	ackSeq, ackBits, ok = tr.takePendingACK()
	if !ok || ackSeq != 11 || ackBits != 1 {
		t.Fatalf("later ACK snapshot = (%d, %08x, %v), want (11, 1, true)", ackSeq, ackBits, ok)
	}
}

func TestAdvanceUDPClockNeverRegresses(t *testing.T) {
	var clock atomic.Int64
	clock.Store(100)
	advanceUDPClock(&clock, 90)
	if got := clock.Load(); got != 100 {
		t.Fatalf("clock regressed to %d, want 100", got)
	}
	advanceUDPClock(&clock, 110)
	if got := clock.Load(); got != 110 {
		t.Fatalf("clock did not advance: got %d, want 110", got)
	}
}

func TestUDPTransport_ConcurrentHandlersMaintenanceAndClose(t *testing.T) {
	// A zero UDPConn returns a normal write error, which lets this test exercise
	// all synchronization paths without binding a port or depending on sleeps.
	tr := &UDPTransport{
		server: &UDPServer{conn: &net.UDPConn{}},
		addr:   &net.UDPAddr{},
		done:   make(chan struct{}),
	}
	nowTick := udpClockStamp(time.Now())
	tr.lastRecvTick.Store(nowTick)
	tr.lastSendTick.Store(nowTick)

	const iterations = 500
	start := make(chan struct{})
	var wg sync.WaitGroup
	run := func(fn func(int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range iterations {
				fn(i)
			}
		}()
	}

	run(func(i int) { tr.handleReliable(uint16(i), nil) })
	run(func(int) { tr.handleUnreliable(nil) })
	run(func(i int) { tr.handleACK(uint16(i), uint32(i)) })
	run(func(int) {
		if !tr.maintenanceTick(time.Now()) {
			t.Error("maintenance tick unexpectedly stopped")
		}
	})
	run(func(i int) {
		tr.SendUnreliable([]byte{byte(i)})
		tr.Close()
	})

	close(start)
	wg.Wait()
}

func TestUDPTransport_ReliableLifetimeSurvivesRetransmits(t *testing.T) {
	server := &UDPServer{
		conn:    &net.UDPConn{},
		byToken: make(map[uint32]*UDPTransport),
		byAddr:  make(map[string]*UDPTransport),
		connIDs: make(map[*UDPTransport]uint32),
	}
	tr := &UDPTransport{
		server: server,
		addr:   &net.UDPAddr{},
		token:  0xABCDEF01,
		done:   make(chan struct{}),
	}
	server.byToken[tr.token] = tr
	server.byAddr[tr.addr.String()] = tr

	firstSentAt := time.Now()
	tr.lastRecvTick.Store(udpClockStamp(firstSentAt))
	tr.lastSendTick.Store(udpClockStamp(firstSentAt))
	tr.sendBuf[0] = reliableEntry{
		seq:         0,
		payload:     []byte("never-acked"),
		firstSentAt: firstSentAt,
		lastSentAt:  firstSentAt,
		used:        true,
	}

	// Repeated maintenance retransmits the frame and advances only the cadence
	// timestamp. Peer traffic keeps the connection itself alive.
	for _, elapsed := range []time.Duration{
		retransmitInterval,
		2 * time.Second,
		4*time.Second + 900*time.Millisecond,
	} {
		now := firstSentAt.Add(elapsed)
		advanceUDPClock(&tr.lastRecvTick, udpClockStamp(now))
		if !tr.maintenanceTick(now) {
			t.Fatalf("maintenance stopped at %v before reliable lifetime expired", elapsed)
		}
		if !tr.sendBuf[0].used {
			t.Fatalf("reliable slot released early at %v", elapsed)
		}
		if !tr.sendBuf[0].firstSentAt.Equal(firstSentAt) {
			t.Fatalf("retransmit moved firstSentAt from %v to %v", firstSentAt, tr.sendBuf[0].firstSentAt)
		}
		if !tr.sendBuf[0].lastSentAt.Equal(now) {
			t.Fatalf("lastSentAt = %v after maintenance at %v", tr.sendBuf[0].lastSentAt, now)
		}
	}

	deadline := firstSentAt.Add(reliableTimeout + time.Nanosecond)
	advanceUDPClock(&tr.lastRecvTick, udpClockStamp(deadline))
	if tr.maintenanceTick(deadline) {
		t.Fatal("maintenance continued after reliable lifetime expired")
	}
	select {
	case <-tr.done:
	default:
		t.Fatal("reliable timeout did not close transport")
	}
	if tr.sendBuf[0].used || tr.sendBuf[0].payload != nil {
		t.Fatalf("reliable timeout retained ring slot: %+v", tr.sendBuf[0])
	}
	if server.byToken[tr.token] != nil || server.byAddr[tr.addr.String()] != nil {
		t.Fatal("reliable timeout did not remove transport routes")
	}
}
