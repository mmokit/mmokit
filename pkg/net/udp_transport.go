package net

import (
	"io"
	"log"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zenion/mmoserver/pkg/net/udpproto"
)

const (
	reliableBufSize    = 256
	retransmitInterval = 100 * time.Millisecond
	reliableTimeout    = 5 * time.Second
	keepaliveInterval  = 1 * time.Second
	connectionTimeout  = 10 * time.Second
)

// udpClockEpoch lets the activity timestamps stay both atomic and monotonic.
// Storing Unix timestamps would make timeout handling sensitive to wall-clock
// adjustments, while storing time.Time values would require another mutex on
// every unreliable packet.
var udpClockEpoch = time.Now()

func udpClockStamp(now time.Time) int64 {
	return now.Sub(udpClockEpoch).Nanoseconds()
}

func advanceUDPClock(clock *atomic.Int64, stamp int64) {
	for {
		previous := clock.Load()
		if stamp <= previous || clock.CompareAndSwap(previous, stamp) {
			return
		}
	}
}

type reliableEntry struct {
	seq         uint16
	payload     []byte
	firstSentAt time.Time
	lastSentAt  time.Time
	acked       bool
	used        bool
}

// UDPTransport implements Transport over UDP with reliable and unreliable channels.
type UDPTransport struct {
	server *UDPServer
	// addr is the peer address this session is bound to. It is immutable
	// after construction so UDPServer.routeFor can compare it without a
	// lock. netip.AddrPort is stored raw off the read loop and never
	// round-tripped through *net.UDPAddr: on a dual-stack socket that
	// conversion re-maps a v4 peer into 4-in-6 form, which would compare
	// unequal to the address the packet actually arrived from.
	addr       netip.AddrPort
	token      uint32
	clientSalt uint64
	serverSalt uint64

	// Outbound reliability
	sendMu  sync.Mutex
	sendSeq uint16
	sendBuf [reliableBufSize]reliableEntry

	// Inbound reliability tracking. UDPServer currently dispatches from one
	// reader goroutine, but maintenance and tests are concurrent, and keeping
	// this state synchronized makes dispatch safe if it is parallelized later.
	recvMu   sync.Mutex
	recvInit bool
	recvSeq  uint16
	recvBits uint32
	ackDirty bool // true if we have new ACKs to send

	// Inbound message queues. Payloads carry a leading channel byte
	// matching the WebSocket conn convention: 0x00 → inbound (game
	// events + typed-input mmokit.HandleClient frames), 0x01 →
	// opInbound (typed ops: auth, marketplace, etc.).
	inMu      sync.Mutex
	inbound   [][]byte // channel 0x00 (events + typed client-input)
	opInbound [][]byte // channel 0x01 (operations); drained via DrainOpInput

	lastRecvTick atomic.Int64
	lastSendTick atomic.Int64
	closed       bool
	closeMu      sync.Mutex
	done         chan struct{}

	bytesSent atomic.Uint64
	bytesRecv atomic.Uint64
}

func newUDPTransport(server *UDPServer, addr netip.AddrPort, token uint32, clientSalt, serverSalt uint64) *UDPTransport {
	t := &UDPTransport{
		server:     server,
		addr:       addr,
		token:      token,
		clientSalt: clientSalt,
		serverSalt: serverSalt,
		inbound:    make([][]byte, 0, 32),
		done:       make(chan struct{}),
	}
	now := udpClockStamp(time.Now())
	t.lastRecvTick.Store(now)
	t.lastSendTick.Store(now)
	go t.tickLoop()
	return t
}

// SendReliable submits a message to the experimental UDP reliability layer.
// The current layer does not provide ordered delivery, so its conservative
// delivery classification remains best-effort until that protocol is hardened.
func (t *UDPTransport) SendReliable(data []byte) SendResult {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()

	// Do not consume a sequence or retain caller data when the transport is
	// already closed. Close can still win after this preflight; sendRaw reports
	// that race and the rollback below removes the staged retransmit entry.
	t.closeMu.Lock()
	closed := t.closed
	t.closeMu.Unlock()
	if closed {
		return SendResult{Disposition: SendClosed}
	}

	seq := t.sendSeq
	idx := seq % reliableBufSize
	if entry := &t.sendBuf[idx]; entry.used && !entry.acked {
		// The reliability window is full. Overwriting an unacknowledged slot
		// would make the old packet impossible to retransmit and would let a
		// delayed ACK for it acknowledge the replacement.
		return SendResult{Disposition: SendBackpressure}
	}
	t.sendSeq++
	// Copy payload so caller can reuse buffer
	payload := make([]byte, len(data))
	copy(payload, data)
	now := time.Now()
	t.sendBuf[idx] = reliableEntry{
		seq:         seq,
		payload:     payload,
		firstSentAt: now,
		lastSentAt:  now,
		acked:       false,
		used:        true,
	}

	pkt := udpproto.EncodeReliable(t.token, seq, payload)
	result := t.sendRaw(pkt)
	if !result.Queued() {
		// A failed/closed initial write did not accept ownership. Keep the
		// returned disposition definitive by preventing a later tick from
		// retransmitting the supposedly rejected frame.
		t.sendBuf[idx] = reliableEntry{}
	}
	return result
}

// SendUnreliable sends a message with no delivery guarantee.
func (t *UDPTransport) SendUnreliable(data []byte) SendResult {
	pkt := udpproto.EncodeUnreliable(t.token, data)
	return t.sendRaw(pkt)
}

// DrainOpInput returns all queued operation messages (channel 0x01) and
// clears the queue. Mirrors Conn.DrainOpInput for the WebSocket transport.
func (t *UDPTransport) DrainOpInput() [][]byte {
	t.inMu.Lock()
	if len(t.opInbound) == 0 {
		t.inMu.Unlock()
		return nil
	}
	msgs := t.opInbound
	t.opInbound = make([][]byte, 0, 8)
	t.inMu.Unlock()
	return msgs
}

// InjectInput appends a message directly to the inbound queue.
// Used by the inter-cell forwarding path to replay input on the destination cell.
func (t *UDPTransport) InjectInput(data []byte) {
	msg := make([]byte, len(data))
	copy(msg, data)
	t.inMu.Lock()
	t.inbound = append(t.inbound, msg)
	t.inMu.Unlock()
}

// DrainInput returns all queued inbound messages and clears the queue.
func (t *UDPTransport) DrainInput() [][]byte {
	t.inMu.Lock()
	if len(t.inbound) == 0 {
		t.inMu.Unlock()
		return nil
	}
	msgs := t.inbound
	t.inbound = make([][]byte, 0, 32)
	t.inMu.Unlock()
	return msgs
}

// BytesSent returns cumulative bytes sent on this transport.
func (t *UDPTransport) BytesSent() uint64 { return t.bytesSent.Load() }

// BytesRecv returns cumulative bytes received on this transport.
func (t *UDPTransport) BytesRecv() uint64 { return t.bytesRecv.Load() }

// Close shuts down the transport.
func (t *UDPTransport) Close() {
	// Match the sendMu -> closeMu order used by SendReliable and maintenance.
	// Closing also releases retained retransmit payloads immediately instead of
	// waiting for the transport and its ring to become unreachable.
	t.sendMu.Lock()
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		t.sendMu.Unlock()
		return
	}
	t.closed = true
	close(t.done)
	t.closeMu.Unlock()
	clear(t.sendBuf[:])
	t.sendMu.Unlock()

	// Send disconnect packet (best effort)
	pkt := udpproto.EncodeDisconnect(t.token)
	t.server.conn.WriteToUDPAddrPort(pkt, t.addr)
}

// handleUnreliable processes an inbound unreliable message. The payload's
// first byte is the channel discriminator (matches the WebSocket conn
// convention): 0x00 → inbound (events + typed client-input), 0x01 →
// opInbound (typed ops). Any other leading byte routes to inbound with the
// bytes left intact (legacy compat for pre-Plan-G senders that omit the
// channel prefix). See routePayload.
func (t *UDPTransport) handleUnreliable(payload []byte) {
	advanceUDPClock(&t.lastRecvTick, udpClockStamp(time.Now()))
	t.bytesRecv.Add(uint64(len(payload)))
	if len(payload) == 0 {
		return // keepalive
	}
	t.routePayload(payload)
}

// routePayload buckets a non-empty inbound payload onto the matching queue
// by its leading channel byte: ChannelEvent (0x00) → event queue,
// ChannelOperation (0x01) → op queue (both stripped of the channel byte).
// An unknown leading byte is a legacy pre-channel-prefix event and routes
// to the event queue with bytes left intact.
func (t *UDPTransport) routePayload(payload []byte) {
	switch payload[0] {
	case ChannelEvent:
		body := make([]byte, len(payload)-1)
		copy(body, payload[1:])
		t.inMu.Lock()
		t.inbound = append(t.inbound, body)
		t.inMu.Unlock()
	case ChannelOperation:
		body := make([]byte, len(payload)-1)
		copy(body, payload[1:])
		t.inMu.Lock()
		t.opInbound = append(t.opInbound, body)
		t.inMu.Unlock()
	default:
		// Unknown leading byte — legacy channel-0x00 event, bytes intact.
		body := make([]byte, len(payload))
		copy(body, payload)
		t.inMu.Lock()
		t.inbound = append(t.inbound, body)
		t.inMu.Unlock()
	}
}

// handleReliable processes an inbound reliable message.
func (t *UDPTransport) handleReliable(seq uint16, payload []byte) {
	advanceUDPClock(&t.lastRecvTick, udpClockStamp(time.Now()))
	t.bytesRecv.Add(uint64(len(payload)))

	// Update receive tracking for ACK generation
	t.recvMu.Lock()
	deliver := false
	if !t.recvInit {
		// First reliable message received
		t.recvInit = true
		t.recvSeq = seq
		t.recvBits = 0
		deliver = true
	} else if udpproto.SeqGreaterThan(seq, t.recvSeq) {
		// New highest sequence — shift bits
		diff := seq - t.recvSeq
		if diff <= 32 {
			t.recvBits = (t.recvBits << diff) | (1 << (diff - 1))
		} else {
			t.recvBits = 0
		}
		t.recvSeq = seq
		deliver = true
	} else {
		// Older or duplicate — deliver an unseen packet within the receive
		// window exactly once, and only update ACK state for repeats.
		diff := t.recvSeq - seq
		if diff > 0 && diff <= 32 {
			bit := uint32(1) << (diff - 1)
			if t.recvBits&bit == 0 {
				t.recvBits |= bit
				deliver = true
			}
		}
		// diff == 0 is a duplicate of recvSeq; diff > 32 is too old to
		// distinguish safely. Neither is re-delivered.
	}
	t.ackDirty = true
	t.recvMu.Unlock()

	if deliver && len(payload) > 0 {
		t.routePayload(payload)
	}
}

// handleACK processes an inbound ACK packet.
func (t *UDPTransport) handleACK(ackSeq uint16, ackBits uint32) {
	advanceUDPClock(&t.lastRecvTick, udpClockStamp(time.Now()))

	t.sendMu.Lock()
	defer t.sendMu.Unlock()

	// Mark the acked sequence
	t.markAcked(ackSeq)

	// Mark sequences indicated by ack_bits
	for i := range uint32(32) {
		if ackBits&(1<<i) != 0 {
			t.markAcked(ackSeq - uint16(i) - 1)
		}
	}
}

func (t *UDPTransport) markAcked(seq uint16) {
	idx := seq % reliableBufSize
	entry := &t.sendBuf[idx]
	if entry.used && entry.seq == seq {
		// Release the payload immediately. Sequence identity prevents a delayed
		// ACK from clearing a newer packet that reused the same ring slot.
		*entry = reliableEntry{}
	}
}

func (t *UDPTransport) sendRaw(data []byte) SendResult {
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return SendResult{Disposition: SendClosed}
	}
	n, err := t.server.conn.WriteToUDPAddrPort(data, t.addr)
	if err != nil {
		t.closeMu.Unlock()
		return SendResult{Disposition: SendFailed, Err: err}
	}
	if n != len(data) {
		t.closeMu.Unlock()
		return SendResult{Disposition: SendFailed, Err: io.ErrShortWrite}
	}
	t.lastSendTick.Store(udpClockStamp(time.Now()))
	t.bytesSent.Add(uint64(len(data)))
	t.closeMu.Unlock()
	return queuedSend(DeliveryBestEffort)
}

func (t *UDPTransport) sendACK() {
	ackSeq, ackBits, ok := t.takePendingACK()
	if !ok {
		return
	}

	pkt := udpproto.EncodeACK(t.token, ackSeq, ackBits)
	result := t.sendRaw(pkt)
	if !result.Queued() && result.Disposition != SendClosed {
		// Retry a failed ACK using the latest receive-window snapshot. If a new
		// reliable packet arrived while this write was in flight, ackDirty is
		// already true and this assignment is intentionally idempotent.
		t.recvMu.Lock()
		t.ackDirty = true
		t.recvMu.Unlock()
	}
}

func (t *UDPTransport) takePendingACK() (uint16, uint32, bool) {
	t.recvMu.Lock()
	defer t.recvMu.Unlock()
	if !t.ackDirty {
		return 0, 0, false
	}

	ackSeq, ackBits := t.recvSeq, t.recvBits
	// Clear before the write. A receive concurrent with the write sets this
	// back to true, so completing the older ACK can never erase newer work.
	t.ackDirty = false
	return ackSeq, ackBits, true
}

// tickLoop handles retransmission, ACKs, keepalives, and timeouts at ~10Hz.
func (t *UDPTransport) tickLoop() {
	ticker := time.NewTicker(retransmitInterval)
	defer ticker.Stop()

	for {
		select {
		case <-t.done:
			return
		case now := <-ticker.C:
			if !t.maintenanceTick(now) {
				return
			}
		}
	}
}

// maintenanceTick performs one pass of timeout, retransmit, ACK, and
// keepalive work. Keeping the pass separate from the ticker makes the
// cross-goroutine contract directly testable without timing-dependent sleeps.
func (t *UDPTransport) maintenanceTick(now time.Time) bool {
	nowTick := udpClockStamp(now)

	// Connection timeout
	if time.Duration(nowTick-t.lastRecvTick.Load()) > connectionTimeout {
		log.Printf("udp: connection timeout for token %08x", t.token)
		t.server.removeTransport(t)
		return false
	}

	// Retransmit unacked reliable messages
	t.sendMu.Lock()
	for i := range t.sendBuf {
		entry := &t.sendBuf[i]
		if !entry.used || entry.acked {
			continue
		}
		lifetime := now.Sub(entry.firstSentAt)
		if lifetime > reliableTimeout {
			log.Printf("udp: reliable message seq timed out for token %08x", t.token)
			t.sendMu.Unlock()
			t.server.removeTransport(t)
			return false
		}
		if now.Sub(entry.lastSentAt) >= retransmitInterval {
			pkt := udpproto.EncodeReliable(t.token, entry.seq, entry.payload)
			t.sendRaw(pkt)
			entry.lastSentAt = now
		}
	}
	t.sendMu.Unlock()

	// Atomically snapshots and clears the dirty flag before writing, so a
	// packet arriving during the write remains pending for the next pass.
	t.sendACK()

	// Keepalive
	if time.Duration(nowTick-t.lastSendTick.Load()) > keepaliveInterval {
		pkt := udpproto.EncodeUnreliable(t.token, nil)
		t.sendRaw(pkt)
	}
	return true
}
