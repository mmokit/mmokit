package udpclient

import (
	"errors"
	"net"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/net/udpproto"
)

func newReliabilityTestClient() *Client {
	c := &Client{
		conn:    &net.UDPConn{},
		token:   0xABCD1234,
		inbound: make([][]byte, 0, 32),
		done:    make(chan struct{}),
	}
	c.inCond = sync.NewCond(&c.inMu)
	now := clientClockStamp(time.Now())
	c.lastRecvTick.Store(now)
	c.lastSendTick.Store(now)
	return c
}

func drainReliabilityTestInbound(c *Client) [][]byte {
	c.inMu.Lock()
	defer c.inMu.Unlock()
	result := c.inbound
	c.inbound = nil
	return result
}

func TestClient_SendReliableDoesNotOverwriteFullWindow(t *testing.T) {
	c := newReliabilityTestClient()
	c.sendBuf[0] = reliableEntry{
		seq:     0,
		payload: []byte("unacked"),
		used:    true,
	}

	err := c.SendReliable([]byte("replacement"))
	if !errors.Is(err, ErrReliableWindowFull) {
		t.Fatalf("SendReliable error = %v, want ErrReliableWindowFull", err)
	}
	if c.sendSeq != 0 {
		t.Fatalf("full-window send advanced sequence to %d", c.sendSeq)
	}
	if got := string(c.sendBuf[0].payload); got != "unacked" {
		t.Fatalf("full-window send overwrote payload with %q", got)
	}
}

func TestClient_SendReliableFailureDoesNotRetainFrame(t *testing.T) {
	c := newReliabilityTestClient()

	err := c.SendReliable([]byte("critical"))
	if err == nil {
		t.Fatal("SendReliable unexpectedly succeeded on zero UDPConn")
	}
	if c.sendBuf[0].used || c.sendBuf[0].payload != nil {
		t.Fatalf("failed SendReliable retained slot: %+v", c.sendBuf[0])
	}
}

func TestClient_ProcessACKRequiresExactSequenceIdentity(t *testing.T) {
	c := newReliabilityTestClient()
	const currentSeq uint16 = 300
	idx := currentSeq % reliableBufSize
	c.sendBuf[idx] = reliableEntry{
		seq:     currentSeq,
		payload: []byte("current"),
		used:    true,
	}

	c.processACK(44, 0) // aliases the same ring slot after one 256-seq wrap
	if !c.sendBuf[idx].used || c.sendBuf[idx].seq != currentSeq {
		t.Fatalf("stale ACK cleared current slot: %+v", c.sendBuf[idx])
	}

	c.processACK(currentSeq, 0)
	if c.sendBuf[idx].used || c.sendBuf[idx].payload != nil {
		t.Fatalf("exact ACK did not release slot: %+v", c.sendBuf[idx])
	}
}

func TestClient_RetransmitUsesExactWrappedSequence(t *testing.T) {
	c := newReliabilityTestClient()
	entry := &reliableEntry{seq: 300, payload: []byte("wrapped"), used: true}

	_, seq, payload, err := udpproto.DecodeReliable(c.encodeReliableEntry(entry))
	if err != nil {
		t.Fatal(err)
	}
	if seq != entry.seq || string(payload) != "wrapped" {
		t.Fatalf("encoded retransmit = (seq=%d payload=%q), want (300, wrapped)", seq, payload)
	}
}

func TestClient_ReliableReceiveDeduplicatesAndAcceptsUnseenOutOfOrder(t *testing.T) {
	c := newReliabilityTestClient()

	c.handlePacket(udpproto.EncodeReliable(c.token, 10, []byte{10}))
	c.handlePacket(udpproto.EncodeReliable(c.token, 12, []byte{12}))
	c.handlePacket(udpproto.EncodeReliable(c.token, 10, []byte{10}))
	c.handlePacket(udpproto.EncodeReliable(c.token, 11, []byte{11}))
	c.handlePacket(udpproto.EncodeReliable(c.token, 11, []byte{11}))

	events := drainReliabilityTestInbound(c)
	want := [][]byte{{10}, {12}, {11}}
	if !slices.EqualFunc(events, want, slices.Equal) {
		t.Fatalf("reliable deliveries = %v, want %v", events, want)
	}
}

func TestClient_ConcurrentDuplicateReliableDeliveredOnce(t *testing.T) {
	c := newReliabilityTestClient()
	packet := udpproto.EncodeReliable(c.token, 0, []byte{0xCC})
	const callers = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			c.handlePacket(packet)
		}()
	}

	close(start)
	wg.Wait()
	events := drainReliabilityTestInbound(c)
	if len(events) != 1 || !slices.Equal(events[0], []byte{0xCC}) {
		t.Fatalf("concurrent duplicate deliveries = %v, want [[204]]", events)
	}
}

func TestClient_ACKSnapshotDoesNotClearLaterReceive(t *testing.T) {
	c := newReliabilityTestClient()
	c.updateRecvTracking(10)

	ackSeq, ackBits, ok := c.takePendingACK()
	if !ok || ackSeq != 10 || ackBits != 0 {
		t.Fatalf("first ACK snapshot = (%d, %08x, %v), want (10, 0, true)", ackSeq, ackBits, ok)
	}

	c.updateRecvTracking(11)
	ackSeq, ackBits, ok = c.takePendingACK()
	if !ok || ackSeq != 11 || ackBits != 1 {
		t.Fatalf("later ACK snapshot = (%d, %08x, %v), want (11, 1, true)", ackSeq, ackBits, ok)
	}
}

func TestClient_ReliableLifetimeSurvivesRetransmits(t *testing.T) {
	c := newReliabilityTestClient()
	firstSentAt := time.Now()
	c.lastRecvTick.Store(clientClockStamp(firstSentAt))
	c.lastSendTick.Store(clientClockStamp(firstSentAt))
	c.sendBuf[0] = reliableEntry{
		seq:         0,
		payload:     []byte("never-acked"),
		firstSentAt: firstSentAt,
		lastSentAt:  firstSentAt,
		used:        true,
	}

	for _, elapsed := range []time.Duration{
		retransmitInterval,
		2 * time.Second,
		4*time.Second + 900*time.Millisecond,
	} {
		now := firstSentAt.Add(elapsed)
		advanceClientClock(&c.lastRecvTick, clientClockStamp(now))
		if !c.maintenanceTick(now) {
			t.Fatalf("maintenance stopped at %v before reliable lifetime expired", elapsed)
		}
		if !c.sendBuf[0].used {
			t.Fatalf("reliable slot released early at %v", elapsed)
		}
		if !c.sendBuf[0].firstSentAt.Equal(firstSentAt) {
			t.Fatalf("retransmit moved firstSentAt from %v to %v", firstSentAt, c.sendBuf[0].firstSentAt)
		}
		if !c.sendBuf[0].lastSentAt.Equal(now) {
			t.Fatalf("lastSentAt = %v after maintenance at %v", c.sendBuf[0].lastSentAt, now)
		}
	}

	deadline := firstSentAt.Add(reliableTimeout + time.Nanosecond)
	advanceClientClock(&c.lastRecvTick, clientClockStamp(deadline))
	if c.maintenanceTick(deadline) {
		t.Fatal("maintenance continued after reliable lifetime expired")
	}
	select {
	case <-c.done:
	default:
		t.Fatal("reliable timeout did not close client")
	}
	if c.sendBuf[0].used || c.sendBuf[0].payload != nil {
		t.Fatalf("reliable timeout retained ring slot: %+v", c.sendBuf[0])
	}
}

func TestClient_CloseWakesReceiverAndReleasesRing(t *testing.T) {
	c := newReliabilityTestClient()
	c.sendBuf[0] = reliableEntry{seq: 0, payload: []byte("held"), used: true}
	recvResult := make(chan error, 1)

	// Hold closeMu so Recv stops after acquiring inMu. This lets the test prove
	// that Recv subsequently entered Cond.Wait before Close broadcasts, instead
	// of merely racing Close and observing an already-closed client.
	c.closeMu.Lock()
	go func() {
		_, err := c.Recv()
		recvResult <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if !c.inMu.TryLock() {
			break // Recv owns inMu and is blocked acquiring closeMu.
		}
		c.inMu.Unlock()
		if time.Now().After(deadline) {
			c.closeMu.Unlock()
			t.Fatal("Recv did not enter its close predicate check")
		}
		runtime.Gosched()
	}
	c.closeMu.Unlock()

	// Recv releases inMu only as Cond.Wait begins. Acquiring it here therefore
	// proves the receiver is blocked on the condition before Close runs.
	for {
		if c.inMu.TryLock() {
			c.inMu.Unlock()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Recv did not block in Cond.Wait")
		}
		runtime.Gosched()
	}

	c.Close()
	select {
	case err := <-recvResult:
		if err == nil {
			t.Fatal("Recv returned nil error after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake blocked Recv")
	}
	if c.sendBuf[0].used || c.sendBuf[0].payload != nil {
		t.Fatalf("Close retained reliable slot: %+v", c.sendBuf[0])
	}
}

func TestClient_ConcurrentHandlersMaintenanceAndClose(t *testing.T) {
	c := newReliabilityTestClient()
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

	run(func(i int) {
		c.handlePacket(udpproto.EncodeReliable(c.token, uint16(i), nil))
	})
	run(func(int) {
		c.handlePacket(udpproto.EncodeUnreliable(c.token, nil))
	})
	run(func(i int) { c.processACK(uint16(i), uint32(i)) })
	run(func(int) {
		if !c.maintenanceTick(time.Now()) {
			t.Error("maintenance tick unexpectedly stopped")
		}
	})
	run(func(i int) {
		c.SendUnreliable([]byte{byte(i)})
		c.Close()
	})

	close(start)
	wg.Wait()
}
