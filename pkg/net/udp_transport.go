package net

import (
	"log"
	"net"
	"sync"
	"time"

	"github.com/zenion/gameserver/pkg/net/udpproto"
)

const (
	reliableBufSize    = 256
	retransmitInterval = 100 * time.Millisecond
	reliableTimeout    = 5 * time.Second
	keepaliveInterval  = 1 * time.Second
	connectionTimeout  = 10 * time.Second
	ackInterval        = 50 * time.Millisecond
)

type reliableEntry struct {
	payload []byte
	sentAt  time.Time
	acked   bool
	used    bool
}

// UDPTransport implements Transport over UDP with reliable and unreliable channels.
type UDPTransport struct {
	server     *UDPServer
	addr       *net.UDPAddr
	token      uint32
	clientSalt uint64
	serverSalt uint64

	// Outbound reliability
	sendMu  sync.Mutex
	sendSeq uint16
	sendBuf [reliableBufSize]reliableEntry

	// Inbound reliability tracking
	recvSeq  uint16
	recvBits uint32
	ackDirty bool // true if we have new ACKs to send

	// Inbound message queue
	inMu    sync.Mutex
	inbound [][]byte

	lastRecv time.Time
	lastSend time.Time
	closed   bool
	closeMu  sync.Mutex
	done     chan struct{}
}

func newUDPTransport(server *UDPServer, addr *net.UDPAddr, token uint32, clientSalt, serverSalt uint64) *UDPTransport {
	t := &UDPTransport{
		server:     server,
		addr:       addr,
		token:      token,
		clientSalt: clientSalt,
		serverSalt: serverSalt,
		inbound:    make([][]byte, 0, 32),
		lastRecv:   time.Now(),
		lastSend:   time.Now(),
		done:       make(chan struct{}),
	}
	go t.tickLoop()
	return t
}

// SendReliable sends a message with reliability guarantees.
func (t *UDPTransport) SendReliable(data []byte) {
	t.sendMu.Lock()
	seq := t.sendSeq
	t.sendSeq++
	idx := seq % reliableBufSize
	// Copy payload so caller can reuse buffer
	payload := make([]byte, len(data))
	copy(payload, data)
	t.sendBuf[idx] = reliableEntry{
		payload: payload,
		sentAt:  time.Now(),
		acked:   false,
		used:    true,
	}
	t.sendMu.Unlock()

	pkt := udpproto.EncodeReliable(t.token, seq, payload)
	t.sendRaw(pkt)
}

// SendUnreliable sends a message with no delivery guarantee.
func (t *UDPTransport) SendUnreliable(data []byte) {
	pkt := udpproto.EncodeUnreliable(t.token, data)
	t.sendRaw(pkt)
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

// Close shuts down the transport.
func (t *UDPTransport) Close() {
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return
	}
	t.closed = true
	close(t.done)
	t.closeMu.Unlock()

	// Send disconnect packet (best effort)
	pkt := udpproto.EncodeDisconnect(t.token)
	t.server.conn.WriteToUDP(pkt, t.addr)
}

// handleUnreliable processes an inbound unreliable message.
func (t *UDPTransport) handleUnreliable(payload []byte) {
	t.lastRecv = time.Now()
	if len(payload) == 0 {
		return // keepalive
	}
	msg := make([]byte, len(payload))
	copy(msg, payload)
	t.inMu.Lock()
	t.inbound = append(t.inbound, msg)
	t.inMu.Unlock()
}

// handleReliable processes an inbound reliable message.
func (t *UDPTransport) handleReliable(seq uint16, payload []byte) {
	t.lastRecv = time.Now()

	// Update receive tracking for ACK generation
	if t.recvSeq == 0 && !t.ackDirty {
		// First reliable message received
		t.recvSeq = seq
		t.recvBits = 0
	} else if udpproto.SeqGreaterThan(seq, t.recvSeq) {
		// New highest sequence — shift bits
		diff := seq - t.recvSeq
		if diff <= 32 {
			t.recvBits = (t.recvBits << diff) | (1 << (diff - 1))
		} else {
			t.recvBits = 0
		}
		t.recvSeq = seq
	} else {
		// Older or duplicate — set the appropriate bit
		diff := t.recvSeq - seq
		if diff > 0 && diff <= 32 {
			t.recvBits |= 1 << (diff - 1)
		}
		// diff == 0 is a duplicate of recvSeq, ignore
		// diff > 32 is too old, ignore
	}
	t.ackDirty = true

	if len(payload) > 0 {
		msg := make([]byte, len(payload))
		copy(msg, payload)
		t.inMu.Lock()
		t.inbound = append(t.inbound, msg)
		t.inMu.Unlock()
	}
}

// handleACK processes an inbound ACK packet.
func (t *UDPTransport) handleACK(ackSeq uint16, ackBits uint32) {
	t.lastRecv = time.Now()

	t.sendMu.Lock()
	defer t.sendMu.Unlock()

	// Mark the acked sequence
	t.markAcked(ackSeq)

	// Mark sequences indicated by ack_bits
	for i := uint32(0); i < 32; i++ {
		if ackBits&(1<<i) != 0 {
			t.markAcked(ackSeq - uint16(i) - 1)
		}
	}
}

func (t *UDPTransport) markAcked(seq uint16) {
	idx := seq % reliableBufSize
	entry := &t.sendBuf[idx]
	if entry.used {
		entry.acked = true
	}
}

func (t *UDPTransport) sendRaw(data []byte) {
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return
	}
	t.closeMu.Unlock()

	t.server.conn.WriteToUDP(data, t.addr)
	t.lastSend = time.Now()
}

func (t *UDPTransport) sendACK() {
	pkt := udpproto.EncodeACK(t.token, t.recvSeq, t.recvBits)
	t.sendRaw(pkt)
	t.ackDirty = false
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
			// Connection timeout
			if now.Sub(t.lastRecv) > connectionTimeout {
				log.Printf("udp: connection timeout for token %08x", t.token)
				t.server.removeTransport(t)
				return
			}

			// Retransmit unacked reliable messages
			t.sendMu.Lock()
			for i := range t.sendBuf {
				entry := &t.sendBuf[i]
				if !entry.used || entry.acked {
					continue
				}
				age := now.Sub(entry.sentAt)
				if age > reliableTimeout {
					log.Printf("udp: reliable message seq timed out for token %08x", t.token)
					t.sendMu.Unlock()
					t.server.removeTransport(t)
					return
				}
				if age >= retransmitInterval {
					seq := uint16(i) // approximate — the slot index matches seq%256
					pkt := udpproto.EncodeReliable(t.token, seq, entry.payload)
					t.sendRaw(pkt)
					entry.sentAt = now
				}
			}
			t.sendMu.Unlock()

			// Send standalone ACK if dirty
			if t.ackDirty {
				t.sendACK()
			}

			// Keepalive
			if now.Sub(t.lastSend) > keepaliveInterval {
				pkt := udpproto.EncodeUnreliable(t.token, nil)
				t.sendRaw(pkt)
			}
		}
	}
}
